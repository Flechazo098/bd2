package gamedata

import (
	"encoding/binary"
	"fmt"
	"slices"

	"bd2server/internal/server/protocol/wire"
)

type IBPoint struct{ X, Y int }
type IBItem struct {
	ID, Type, Grade, Price, SellType, TypeID, CostumeID uint64
	Shape                                               []IBPoint
}
type IBPlacement struct{ ItemID, Position, Rotation uint64 }
type IBStage struct {
	ID, EnemyDeck, Credit uint64
	Ratios                [7]uint64
	Slots                 [4]uint64
}
type IBDungeon struct {
	ID, Life, Unlock uint64
	Reward           Reward
	Stages           []IBStage
}
type IBDesign struct {
	StartDeck, RetryCredit, RefreshPriceUp, DiscountRate uint64
	Seasons                                              map[uint64][]uint64
	Dungeons                                             map[uint64]IBDungeon
	Items                                                map[uint64]IBItem
	Decks                                                map[uint64][]IBPlacement
	UpgradeCosts                                         map[[3]uint64]uint64
	Sockets                                              map[[2]uint64][]IBPoint
}

func ibNumbers(raw []byte, packed ...int) (map[int][]uint64, error) {
	out := map[int][]uint64{}
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Type == 0 {
			v, n := binary.Uvarint(f.Value)
			if n <= 0 {
				return wire.ErrMalformed
			}
			out[f.Number] = append(out[f.Number], v)
		} else if f.Type == 2 && slices.Contains(packed, f.Number) {
			for data := f.Value; len(data) > 0; {
				v, n := binary.Uvarint(data)
				if n <= 0 {
					return wire.ErrMalformed
				}
				out[f.Number] = append(out[f.Number], v)
				data = data[n:]
			}
		}
		return nil
	})
	return out, err
}

func LoadIBDesign(root, version string) (*IBDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &IBDesign{Seasons: map[uint64][]uint64{}, Dungeons: map[uint64]IBDungeon{}, Items: map[uint64]IBItem{}, Decks: map[uint64][]IBPlacement{}, UpgradeCosts: map[[3]uint64]uint64{}, Sockets: map[[2]uint64][]IBPoint{}}
	stages := map[uint64][]IBStage{}
	for _, spec := range []struct {
		name   string
		packed []int
	}{
		{"IBDefaultTable", nil}, {"IBSeasonTable", []int{1}}, {"IBItemTable", []int{13, 14}}, {"IBDeckTable", []int{2, 3, 4}}, {"IBStageTable", nil}, {"IBDungeonTable", nil}, {"IBUnitItemTable", []int{21, 22}}, {"IBEquipItemTable", nil},
	} {
		rows, e := db.Query("SELECT ProtoBuf FROM " + spec.name)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				break
			}
			var f map[int][]uint64
			f, e = ibNumbers(raw, spec.packed...)
			if e != nil {
				break
			}
			v := func(n int) uint64 {
				if len(f[n]) > 0 {
					return f[n][0]
				}
				return 0
			}
			switch spec.name {
			case "IBDefaultTable":
				d.StartDeck = v(22)
				d.RetryCredit = v(18)
				d.RefreshPriceUp = v(20)
				d.DiscountRate = v(19)
			case "IBSeasonTable":
				d.Seasons[v(2)] = f[1]
			case "IBItemTable":
				i := IBItem{ID: v(3), Type: v(11), Grade: v(5), Price: v(8), SellType: v(12), TypeID: v(15), CostumeID: v(1)}
				if len(f[13]) != len(f[14]) {
					e = fmt.Errorf("ib: item shape mismatch %d", i.ID)
					break
				}
				for n, x := range f[13] {
					i.Shape = append(i.Shape, IBPoint{int(int32(x)), int(int32(f[14][n]))})
				}
				d.Items[i.ID] = i
			case "IBDeckTable":
				if len(f[2]) != len(f[4]) || len(f[3]) != len(f[4]) {
					e = fmt.Errorf("ib: malformed deck %d", v(1))
					break
				}
				for n, id := range f[4] {
					d.Decks[v(1)] = append(d.Decks[v(1)], IBPlacement{id, f[2][n], f[3][n]})
				}
			case "IBStageTable":
				stages[v(5)] = append(stages[v(5)], IBStage{ID: v(8), EnemyDeck: v(2), Credit: v(4), Ratios: [7]uint64{v(11), v(12), v(1), v(7), v(9), v(10), v(17)}, Slots: [4]uint64{v(13), v(14), v(15), v(16)}})
			case "IBDungeonTable":
				d.Dungeons[v(6)] = IBDungeon{ID: v(6), Life: v(4), Unlock: v(9), Reward: Reward{v(3), v(2), v(1)}, Stages: []IBStage{{ID: v(8)}}}
			case "IBUnitItemTable":
				d.UpgradeCosts[[3]uint64{1, v(5), v(7)}] = v(23)
				if len(f[21]) != len(f[22]) {
					e = fmt.Errorf("ib: socket shape mismatch")
					break
				}
				for n, x := range f[21] {
					d.Sockets[[2]uint64{v(5), v(7)}] = append(d.Sockets[[2]uint64{v(5), v(7)}], IBPoint{int(int32(x)), int(int32(f[22][n]))})
				}
			case "IBEquipItemTable":
				d.UpgradeCosts[[3]uint64{2, v(2), v(3)}] = v(7)
			}
			if e != nil {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
	}
	for id, dungeon := range d.Dungeons {
		group := dungeon.Stages[0].ID
		dungeon.Stages = stages[group]
		slices.SortFunc(dungeon.Stages, func(a, b IBStage) int {
			if a.ID < b.ID {
				return -1
			}
			if a.ID > b.ID {
				return 1
			}
			return 0
		})
		if dungeon.Life == 0 || len(dungeon.Stages) == 0 || dungeon.Reward.Type == 0 || dungeon.Reward.Count == 0 {
			return nil, fmt.Errorf("ib: invalid dungeon %d", id)
		}
		for _, s := range dungeon.Stages {
			if len(d.Decks[s.EnemyDeck]) == 0 {
				return nil, fmt.Errorf("ib: missing enemy deck %d", s.EnemyDeck)
			}
		}
		d.Dungeons[id] = dungeon
	}
	if len(d.Decks[d.StartDeck]) == 0 || len(d.Seasons) == 0 {
		return nil, fmt.Errorf("ib: invalid defaults")
	}
	for _, ids := range d.Seasons {
		for _, id := range ids {
			if _, ok := d.Dungeons[id]; !ok {
				return nil, fmt.Errorf("ib: missing season dungeon %d", id)
			}
		}
	}
	for _, deck := range d.Decks {
		for _, p := range deck {
			if _, ok := d.Items[p.ItemID]; !ok {
				return nil, fmt.Errorf("ib: missing item %d", p.ItemID)
			}
		}
	}
	return d, nil
}
