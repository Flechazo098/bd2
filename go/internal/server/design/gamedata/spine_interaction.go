package gamedata

import (
	"fmt"

	"bd2server/internal/server/protocol/wire"
)

type InteractionPointKey [3]uint64

type InteractionPoint struct {
	Achievement bool
	Connected   InteractionPointKey
	RewardGroup uint64
	Motions     map[uint64]bool
}

type InteractionAccess struct {
	Provide, Item, Costume, Prestige, Pack uint64
}

type SpineInteractionDesign struct {
	Points                                                map[InteractionPointKey]InteractionPoint
	Rewards                                               map[uint64][]Reward
	Access                                                map[uint64][]InteractionAccess
	RecordGroups                                          map[uint64]bool
	RecordTools                                           map[uint64]bool
	RecordSaveSlots, MaxRecordTime, MaxRecordInteractions uint64
}

func LoadSpineInteractionDesign(root, version string) (*SpineInteractionDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	d := &SpineInteractionDesign{Points: map[InteractionPointKey]InteractionPoint{}, Rewards: map[uint64][]Reward{}, Access: map[uint64][]InteractionAccess{}}
	read := func(table string, visit func([]byte) error) error { return readCashMetadata(db, table, visit) }
	d.RecordGroups = map[uint64]bool{}
	d.RecordTools = map[uint64]bool{}
	if err := read("SpineInteractionTable", func(raw []byte) error {
		id, _, err := wire.Varint(raw, 3)
		if err != nil || id == 0 {
			return fmt.Errorf("gamedata: invalid interaction recording group")
		}
		d.RecordGroups[id] = true
		return nil
	}); err != nil {
		return nil, err
	}
	if err := read("SpineInteractionToolbarTable", func(raw []byte) error {
		id, _, err := wire.Varint(raw, 2)
		if err != nil {
			return err
		}
		d.RecordTools[id] = true
		return nil
	}); err != nil {
		return nil, err
	}
	defaults := 0
	if err := read("SpineInteractionDefaultTable", func(raw []byte) error {
		defaults++
		d.RecordSaveSlots, _, _ = wire.Varint(raw, 12)
		d.MaxRecordTime, _, _ = wire.Varint(raw, 7)
		d.MaxRecordInteractions, _, _ = wire.Varint(raw, 6)
		return nil
	}); err != nil {
		return nil, err
	}
	if defaults != 1 || d.RecordSaveSlots == 0 || d.MaxRecordTime == 0 || d.MaxRecordInteractions == 0 {
		return nil, fmt.Errorf("gamedata: invalid interaction recording limits")
	}
	if err := read("SpineInteractionPointTable", func(raw []byte) error {
		v := func(field int) uint64 { n, _, _ := wire.Varint(raw, field); return n }
		key := InteractionPointKey{v(8), v(5), v(6)}
		if key[0] == 0 || key[1] == 0 || key[2] == 0 {
			return fmt.Errorf("gamedata: invalid interaction point")
		}
		d.Points[key] = InteractionPoint{Achievement: v(1) != 0, Connected: InteractionPointKey{key[0], v(3), v(4)}, RewardGroup: v(21), Motions: map[uint64]bool{}}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := read("InteractionAchievementTable", func(raw []byte) error {
		v := func(field int) uint64 { n, _, _ := wire.Varint(raw, field); return n }
		key := InteractionPointKey{v(3), v(1), v(2)}
		point, exists := d.Points[key]
		if !exists {
			return fmt.Errorf("gamedata: interaction motion has no point %v", key)
		}
		point.Motions[v(4)] = true
		return nil
	}); err != nil {
		return nil, err
	}
	if err := read("InteractionRewardGroupTable", func(raw []byte) error {
		v := func(field int) uint64 { n, _, _ := wire.Varint(raw, field); return n }
		r := Reward{Type: v(5), ID: v(4), Count: v(3)}
		if v(1) == 0 || r.Type == 0 || r.Count == 0 {
			return fmt.Errorf("gamedata: invalid interaction reward")
		}
		d.Rewards[v(1)] = append(d.Rewards[v(1)], r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := read("LobbySettingItemTable", func(raw []byte) error {
		v := func(field int) uint64 { n, _, _ := wire.Varint(raw, field); return n }
		if group := v(4); group != 0 {
			d.Access[group] = append(d.Access[group], InteractionAccess{Provide: v(10), Item: v(3), Costume: v(1), Prestige: v(9), Pack: v(8)})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}
