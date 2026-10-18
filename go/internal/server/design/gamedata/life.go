package gamedata

import (
	"fmt"
	"slices"
	"sort"

	"bd2server/internal/server/protocol/wire"
)

// LifeCatalog is loaded once at assembly and shared read-only by player actors.
type LifeCatalog struct {
	Tables            map[string]map[[3]uint64][]byte
	WorldDefaults     map[uint64][][]byte
	Gathering         map[uint64]map[uint64][][]byte
	ItemsByUnique     map[uint64][][]byte
	ToolGroups        map[uint64][]uint64
	HelperUniques     map[uint64][]uint64
	CollectionsByItem map[[2]uint64][]uint64
	MaxChunks         uint64
}

var lifeKeys = map[string][]int{
	"LifeDefaultTable": {21}, "LifeWorldTable": {4}, "LifeDefaultObjectGroupTable": {3},
	"LifeObjectGroupTable": {5}, "LifeBuildingObjectTable": {4}, "LifeCraftingObjectTable": {1},
	"LifeDecoObjectTable": {1}, "LifeChunkExpandTable": {4}, "LifeCharGrowthTable": {2, 3},
	"LifeGatheringObjectTable": {4, 6, 10}, "LifeItemTable": {5}, "LifeToolTable": {5, 7},
	"LifeCookTable": {1}, "LifeCropSeedTable": {2}, "LifeCropGradeTable": {2},
	"LifeBuyItemTable": {1, 2}, "LifeSellItemTable": {1, 2}, "LifeBuffTable": {8},
	"LifeHelperSpeciesTable": {9}, "LifeCollectionTable": {4},
}

func LoadLifeCatalog(root, version string) (*LifeCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	tables := map[string][][]byte{}
	for table := range lifeKeys {
		if err := readCashMetadata(db, table, func(raw []byte) error { tables[table] = append(tables[table], append([]byte(nil), raw...)); return nil }); err != nil {
			return nil, err
		}
	}
	return NewLifeCatalog(tables)
}

func NewLifeCatalog(tables map[string][][]byte) (*LifeCatalog, error) {
	c := &LifeCatalog{Tables: map[string]map[[3]uint64][]byte{}, WorldDefaults: map[uint64][][]byte{}, Gathering: map[uint64]map[uint64][][]byte{}, ItemsByUnique: map[uint64][][]byte{}, ToolGroups: map[uint64][]uint64{}, HelperUniques: map[uint64][]uint64{}, CollectionsByItem: map[[2]uint64][]uint64{}}
	for table, keys := range lifeKeys {
		c.Tables[table] = map[[3]uint64][]byte{}
		for _, raw := range tables[table] {
			if err := wire.Walk(raw, func(wire.Field) error { return nil }); err != nil {
				return nil, err
			}
			var key [3]uint64
			for i, field := range keys {
				key[i], _, _ = wire.Varint(raw, field)
			}
			if _, exists := c.Tables[table][key]; exists {
				return nil, fmt.Errorf("life: duplicate %s key %v", table, key)
			}
			c.Tables[table][key] = append([]byte(nil), raw...)
			n := func(f int) uint64 { v, _, _ := wire.Varint(raw, f); return v }
			switch table {
			case "LifeDefaultObjectGroupTable":
				c.WorldDefaults[n(8)] = append(c.WorldDefaults[n(8)], raw)
			case "LifeGatheringObjectTable":
				if c.Gathering[n(4)] == nil {
					c.Gathering[n(4)] = map[uint64][][]byte{}
				}
				c.Gathering[n(4)][n(6)] = append(c.Gathering[n(4)][n(6)], raw)
			case "LifeItemTable":
				c.ItemsByUnique[n(10)] = append(c.ItemsByUnique[n(10)], raw)
			case "LifeToolTable":
				c.ToolGroups[n(5)] = append(c.ToolGroups[n(5)], n(7))
			case "LifeCollectionTable":
				c.CollectionsByItem[[2]uint64{n(2), n(1)}] = append(c.CollectionsByItem[[2]uint64{n(2), n(1)}], n(4))
			case "LifeChunkExpandTable":
				c.MaxChunks = max(c.MaxChunks, n(4))
			}
		}
	}
	for _, rows := range c.WorldDefaults {
		sort.Slice(rows, func(i, j int) bool {
			a, _, _ := wire.Varint(rows[i], 3)
			b, _, _ := wire.Varint(rows[j], 3)
			return a < b
		})
	}
	for _, variants := range c.Gathering {
		for _, rows := range variants {
			sort.Slice(rows, func(i, j int) bool {
				a, _, _ := wire.Varint(rows[i], 10)
				b, _, _ := wire.Varint(rows[j], 10)
				return a < b
			})
		}
	}
	for _, ids := range c.ToolGroups {
		slices.Sort(ids)
	}
	for _, rows := range c.ItemsByUnique {
		sort.Slice(rows, func(i, j int) bool {
			a, _, _ := wire.Varint(rows[i], 3)
			b, _, _ := wire.Varint(rows[j], 3)
			return a < b
		})
	}
	seen := map[uint64]map[uint64]bool{}
	for _, variants := range c.Gathering {
		for _, rows := range variants {
			for _, r := range rows {
				f, _, _ := wire.Varint(r, 2)
				if seen[f] == nil {
					seen[f] = map[uint64]bool{}
				}
				items, err := packedInts(r, 9)
				if err != nil {
					return nil, err
				}
				for _, u := range items {
					seen[f][u] = true
				}
			}
		}
	}
	for f, items := range seen {
		for u := range items {
			c.HelperUniques[f] = append(c.HelperUniques[f], u)
		}
		slices.Sort(c.HelperUniques[f])
	}
	if c.Row("LifeDefaultTable", 0) == nil || len(c.Tables["LifeWorldTable"]) == 0 {
		return nil, fmt.Errorf("life: missing defaults or worlds")
	}
	return c, nil
}

func (c *LifeCatalog) Row(table string, key ...uint64) []byte {
	var k [3]uint64
	copy(k[:], key)
	return c.Tables[table][k]
}
