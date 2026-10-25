package gamedata

import "fmt"

type RoomLimits struct{ Objects, Carpets, Characters uint64 }
type RoomItem struct{ Kind, ObjectType, Location, MaxCount, PackID, QuestLevel uint64 }
type RoomDesign struct {
	Defaults                          []uint64
	Limits                            map[uint64]RoomLimits
	Items                             map[uint64]RoomItem
	ListCount, MyPresets, UserPresets uint64
	HiddenWallItemID                  uint64
}

func LoadRoomDesign(root, version string) (*RoomDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &RoomDesign{Limits: map[uint64]RoomLimits{}, Items: map[uint64]RoomItem{}}
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM MyRoomDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d.MyPresets, err = optionalScalar(raw, 6)
	if err != nil {
		return nil, err
	}
	d.HiddenWallItemID, err = optionalScalar(raw, 11)
	if err != nil {
		return nil, err
	}
	d.UserPresets, err = optionalScalar(raw, 12)
	if err != nil {
		return nil, err
	}
	d.ListCount, err = optionalScalar(raw, 7)
	if err != nil || d.ListCount == 0 || d.ListCount > 256 {
		return nil, fmt.Errorf("gamedata: invalid room recommendation count")
	}
	if err := readCashMetadata(db, "MyRoomExpandTable", func(raw []byte) error {
		v, err := cashScalars(raw, 4, 3, 5, 1, 2)
		if err != nil {
			return err
		}
		d.Limits[v[0]] = RoomLimits{Objects: v[2], Carpets: v[3], Characters: v[4]}
		if v[1] != 0 {
			d.Defaults = append(d.Defaults, v[0])
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(d.Defaults) == 0 {
		return nil, fmt.Errorf("gamedata: no default rooms")
	}
	if err := readCashMetadata(db, "MyRoomItemTable", func(raw []byte) error {
		v, err := cashScalars(raw, 7, 23, 19, 15, 16, 20, 22)
		if err != nil {
			return err
		}
		d.Items[v[0]] = RoomItem{Kind: v[1], ObjectType: v[2], Location: v[3], MaxCount: v[4], PackID: v[5], QuestLevel: v[6]}
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}
