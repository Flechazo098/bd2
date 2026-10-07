package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

// LoadFieldMonsterMaps resolves actual scene placements, not optional map ids
// guessed from the monster definition or arithmetic on its id.
func LoadFieldMonsterMaps(root, version string, pack int) (map[int][]int, error) {
	common, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	maps := map[string]int{}
	rows, e := common.Query("SELECT id,mapScenePath FROM MapTable WHERE packId=?", pack)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id int
		var scene string
		if e = rows.Scan(&id, &scene); e != nil {
			_ = rows.Close()
			return nil, e
		}
		maps[scene] = id
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	scenes, closeScenes, e := openLogicalReadDB(root, version, "FieldObjectSceneData")
	if e != nil {
		return nil, e
	}
	defer closeScenes()
	rows, e = scenes.Query(fmt.Sprintf("SELECT ProtoBuf FROM FieldObjectSceneData%d WHERE objectType=8", pack))
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := map[int][]int{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		id, e := optionalScalar(b, 8)
		if e != nil {
			return nil, e
		}
		scene, _, e := wire.Bytes(b, 7)
		if e != nil {
			return nil, e
		}
		mapID, ok := maps[string(scene)]
		if !ok {
			return nil, fmt.Errorf("gamedata: monster scene without map")
		}
		out[int(id)] = append(out[int(id)], mapID)
	}
	return out, rows.Err()
}
