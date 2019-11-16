package mbtiles

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/slippy"
	"github.com/go-spatial/tegola/cache"
)

var (
	ErrMissingBasepath        = errors.New("mbtilescache: missing required param 'basepath'")
	ErrLayerCacheNotSupported = errors.New("mbtilescache: cache by layer is not supported")
)

//TODO attribution form maps definition (if possible)
//TODO set generic description
//TODO filter Set by bounds

const CacheType = "mbtiles"

const (
	ConfigKeyBasepath = "basepath"
	ConfigKeyMaxZoom  = "max_zoom"
	ConfigKeyMinZoom  = "min_zoom"
	ConfigKeyBounds   = "bounds"
)

var (
	EarthBounds Bounds = [4]float64{-180, -85.0511, 180, 85.0511}
)

func init() {
	cache.Register(CacheType, New)
}

//Cache hold the cache configuration
type Cache struct {
	Basepath string
	Bounds   Bounds
	// MinZoom determines the min zoom the cache to persist. Before this
	// zoom, cache Set() calls will be ignored.
	MinZoom uint
	// MaxZoom determines the max zoom the cache to persist. Beyond this
	// zoom, cache Set() calls will be ignored. This is useful if the cache
	// should not be leveraged for higher zooms when data changes often.
	MaxZoom uint
	// reference to the database connections
	DBList map[string]*sql.DB
}

//Bounds alias of [4]float64
type Bounds [4]float64

//String return a string representation of cache bounds
func (b Bounds) String() string {
	return fmt.Sprintf("%f,%f,%f,%f", b[0], b[1], b[2], b[3])
}

//IsEarth return true if bound to full earth
func (b Bounds) IsEarth() bool {
	return EarthBounds[0] == b[0] && EarthBounds[1] == b[1] && EarthBounds[2] == b[2] && EarthBounds[3] == b[3]
}

//Get reads a z,x,y entry from the cache and returns the contents
// if there is a hit. the second argument denotes a hit or miss
// so the consumer does not need to sniff errors for cache read misses
func (fc *Cache) Get(key *cache.Key) ([]byte, bool, error) {
	db, err := fc.openOrCreateDB(key.MapName, key.LayerName)
	if err != nil {
		return nil, false, err
	}
	yCorr := (1 << key.Z) - 1 - key.Y
	var data []byte
	err = db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?", key.Z, key.X, yCorr).Scan(&data)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

//Set save a z,x,y entry in the cache
func (fc *Cache) Set(key *cache.Key, val []byte) error {
	// check for maxzoom and minzoom
	if key.Z > fc.MaxZoom || key.Z < fc.MinZoom {
		return nil
	}
	if !fc.Bounds.IsEarth() {
		//Check if match bounds
		t := slippy.NewTile(key.Z, key.X, key.Y)
		b := geom.Extent(fc.Bounds)
		if _, doesIntersect := t.Extent3857().Intersect(&b); !doesIntersect {
			return nil
		}
	}

	db, err := fc.openOrCreateDB(key.MapName, key.LayerName)
	if err != nil {
		return err
	}
	yCorr := (1 << key.Z) - 1 - key.Y
	_, err = db.Exec("INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)", key.Z, key.X, yCorr, val)
	return err
}

//Purge clear a z,x,y entry from the cache
func (fc *Cache) Purge(key *cache.Key) error {
	db, err := fc.openOrCreateDB(key.MapName, key.LayerName)
	if err != nil {
		return err
	}
	yCorr := (1 << key.Z) - 1 - key.Y
	_, err = db.Exec("DELETE FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?", key.Z, key.X, yCorr)
	return err
}

func (fc *Cache) openOrCreateDB(mapName, layerName string) (*sql.DB, error) {
	if mapName == "" {
		mapName = "default"
	}
	fileName := mapName
	if layerName != "" {
		fileName += "-" + layerName
	}
	//Look for open connection in DBList
	db, ok := fc.DBList[fileName]
	if ok {
		return db, nil
	}

	//Connection is not already opend we need one
	file := filepath.Join(fc.Basepath, fileName+".mbtiles")

	//Check if file exist prior to init
	_, err := os.Stat(file)
	dbNeedInit := os.IsNotExist(err)

	db, err = sql.Open("sqlite3", file)
	if err != nil {
		return nil, err
	}
	if dbNeedInit {
		for _, initSt := range []string{
			"CREATE TABLE metadata (name text, value text)",
			"CREATE TABLE tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob)",
			"CREATE UNIQUE INDEX tile_index on tiles (zoom_level, tile_column, tile_row)",
			//"CREATE TABLE grids (zoom_level integer, tile_column integer, tile_row integer, grid blob)",
			//"CREATE TABLE grid_data (zoom_level integer, tile_column integer, tile_row integer, key_name text, key_json text)",
		} {
			_, err := db.Exec(initSt)
			if err != nil {
				return nil, err
			}
		}

		json := `{"vector_layers":[]}` //TODO populate layers
		for metaName, metaValue := range map[string]string{
			"name":    "Tegola Cache Tiles",
			"format":  "pbf",
			"bounds":  fc.Bounds.String(),
			"minzoom": fmt.Sprintf("%d", fc.MinZoom),
			"maxzoom": fmt.Sprintf("%d", fc.MaxZoom),
			"json":    json,
			//Not mandatory but could be implemented
			//center
			//attribution
			//description
			//type
			//version
		} {

			_, err = db.Exec("INSERT INTO metadata (name, value) VALUES (?, ?)", metaName, metaValue)
			if err != nil {
				return nil, err
			}
		}

		//TODO generate metadata
		//TODO generate json vector defs

		//TODO find better storage in sqlite + use views
	}
	//TODO find if needed to update an already set mbtiles but with others metadata

	//Store connection
	fc.DBList[fileName] = db
	return db, err
}
