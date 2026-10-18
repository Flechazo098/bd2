package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

type RogueRule struct {
	Values   map[int][]uint64
	Decimals map[int]float64
}

func (r RogueRule) V(f int) uint64 {
	if a := r.Values[f]; len(a) > 0 {
		return a[0]
	}
	return 0
}
func (r RogueRule) A(f int) []uint64 { return r.Values[f] }
func (r RogueRule) D(f int) float64  { return r.Decimals[f] }

type RogueTable struct {
	Rows   map[[2]uint64]RogueRule
	Groups map[uint64][]uint64
}

func (t RogueTable) Row(g, id uint64) RogueRule { return t.Rows[[2]uint64{g, id}] }

type RogueEnemy struct{ ID, Level, Position uint64 }
type RogueCostume struct {
	ID, Character, Unique, MaxLevel, MaxBurst uint64
	Health                                    uint64
	RequiresOwnership                         bool
}
type RogueLikeDesign struct {
	PackOpen                                                                                                        ContentOpenRule
	Open                                                                                                            ContentOpenRule
	Default                                                                                                         RogueRule
	Buffs                                                                                                           RogueTable
	Floors, Rooms, Levels, Growth, Relics, RelicChoices, Mix, Events, EventChoices, Effects, Scores, Shops, Rewards RogueTable
	Pack                                                                                                            uint64
	Enemies                                                                                                         map[uint64][]RogueEnemy
	Costumes                                                                                                        map[uint64]RogueCostume
	CostumeIDs                                                                                                      []uint64
}

func LoadRogueLikeDesign(root, version string) (*RogueLikeDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	d := &RogueLikeDesign{Costumes: map[uint64]RogueCostume{}}
	var access []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM ContentOpenTable WHERE groupId=18 AND id=1").Scan(&access); e != nil {
		return nil, e
	}
	d.Open.SquadLevel, e = optionalScalar(access, 5)
	if e != nil {
		return nil, e
	}
	d.Open.TicketID, e = optionalScalar(access, 6)
	if e != nil {
		return nil, e
	}

	for _, spec := range []struct {
		name   string
		g, id  int
		table  *RogueTable
		packed []int
	}{
		{"RLDefaultTable", 0, 26, nil, []int{2}},
		{"RLBuffTable", 0, 8, &d.Buffs, []int{5}},
		{"RLFloorTable", 7, 8, &d.Floors, []int{11, 12}}, {"RLRoomTable", 6, 7, &d.Rooms, []int{8}},
		{"RLLevelTable", 0, 6, &d.Levels, []int{1, 8, 9, 10}}, {"RLGrowthTable", 1, 7, &d.Growth, nil},
		{"RLRelicTable", 0, 2, &d.Relics, []int{3}}, {"RLRelicChoiceTable", 0, 1, &d.RelicChoices, []int{2}},
		{"RLRelicMixTable", 0, 1, &d.Mix, nil}, {"RLEventTable", 13, 14, &d.Events, []int{1, 2, 3, 4, 5, 6, 7, 10}},
		{"RLEventChoiceTable", 0, 2, &d.EventChoices, []int{1}}, {"RLEventEffectTypeTable", 0, 5, &d.Effects, nil},
		{"RLScoreTable", 0, 1, &d.Scores, nil}, {"RLShopTable", 0, 2, &d.Shops, []int{1}}, {"RLRewardTable", 0, 1, &d.Rewards, []int{2, 3, 4}},
	} {
		table := RogueTable{Rows: map[[2]uint64]RogueRule{}, Groups: map[uint64][]uint64{}}
		rows, e := db.Query("SELECT ProtoBuf FROM " + spec.name)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				break
			}
			r := RogueRule{Values: map[int][]uint64{}, Decimals: map[int]float64{}}
			e = wire.Walk(raw, func(f wire.Field) error {
				switch f.Type {
				case 0:
					v, n := binary.Uvarint(f.Value)
					if n <= 0 {
						return wire.ErrMalformed
					}
					r.Values[f.Number] = append(r.Values[f.Number], v)
				case 1:
					if len(f.Value) != 8 {
						return wire.ErrMalformed
					}
					v := math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
					if math.IsNaN(v) || math.IsInf(v, 0) {
						return fmt.Errorf("roguelike: nonfinite design")
					}
					r.Decimals[f.Number] = v
				case 2:
					if slices.Contains(spec.packed, f.Number) {
						for b := f.Value; len(b) > 0; {
							v, n := binary.Uvarint(b)
							if n <= 0 {
								return wire.ErrMalformed
							}
							r.Values[f.Number] = append(r.Values[f.Number], v)
							b = b[n:]
						}
					}
				}
				return nil
			})
			if e != nil {
				break
			}
			g, id := r.V(spec.g), r.V(spec.id)
			if spec.g == 0 {
				g = 0
			}
			table.Rows[[2]uint64{g, id}] = r
			table.Groups[g] = append(table.Groups[g], id)
			if spec.table == nil {
				d.Default = r
			}
		}
		if e == nil {
			e = rows.Err()
		}
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
		for _, ids := range table.Groups {
			slices.Sort(ids)
		}
		if spec.table != nil {
			*spec.table = table
		}
	}
	stats, e := loadCharacterStatDesign(db)
	if e != nil {
		return nil, e
	}
	chars := map[uint64]uint64{}
	rows, e := db.Query("SELECT ProtoBuf FROM CharTable")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			break
		}
		unique, _ := optionalScalar(raw, 20)
		grade, _ := optionalScalar(raw, 10)
		temporary, _ := optionalScalar(raw, 21)
		typ, _ := optionalScalar(raw, 19)
		id, _ := optionalScalar(raw, 12)
		if grade == d.Default.V(6) && temporary == 0 && typ == 0 {
			chars[unique] = id
		}
	}
	if e == nil {
		e = rows.Err()
	}
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	bursts, e := loadCostumeBurstDesign(db)
	if e != nil {
		return nil, e
	}
	visibleDesigns := map[uint64]bool{}
	collaborationDesigns := map[uint64]bool{}
	designRows, err := db.Query("SELECT id, ProtoBuf FROM CostumeDesignTable")
	if err != nil {
		return nil, err
	}
	for designRows.Next() {
		var id uint64
		var raw []byte
		if e = designRows.Scan(&id, &raw); e != nil {
			break
		}
		visibleDesigns[id] = true
		collaboration, err := optionalScalar(raw, 12)
		if err != nil {
			e = err
			break
		}
		collaborationDesigns[id] = collaboration != 0
	}
	if e == nil {
		e = designRows.Err()
	}
	_ = designRows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.Query("SELECT ProtoBuf FROM CostumeTable")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		usable, _ := optionalScalar(raw, 26)
		if usable == 0 {
			continue
		}
		id, _ := optionalScalar(raw, 11)
		designs, err := packedInts(raw, 6)
		if err != nil {
			return nil, err
		}
		if len(designs) == 0 {
			return nil, fmt.Errorf("roguelike: costume lacks default design")
		}
		if !visibleDesigns[designs[0]] {
			continue
		}
		unique, _ := optionalScalar(raw, 27)
		maximum, _ := optionalScalar(raw, 15)
		char := chars[unique]
		if char == 0 {
			return nil, fmt.Errorf("roguelike costume %d has no growth character", id)
		}
		base, e := stats.BaseStats(char, d.Default.V(7))
		if e != nil {
			return nil, e
		}
		burst := uint64(len(bursts.Levels[id]))
		d.Costumes[id] = RogueCostume{ID: id, Unique: unique, Character: char, MaxLevel: maximum, MaxBurst: burst, Health: uint64(base.Health), RequiresOwnership: usable == 2 || collaborationDesigns[designs[0]]}
		d.CostumeIDs = append(d.CostumeIDs, id)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	slices.Sort(d.CostumeIDs)
	if e = d.loadEnemies(root, version); e != nil {
		return nil, e
	}
	if len(d.CostumeIDs) < int(d.Default.V(42)) || len(d.Levels.Rows) == 0 {
		return nil, fmt.Errorf("roguelike: incomplete design")
	}
	return d, nil
}
