package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"slices"
)

// SocialRow contains scalar and repeated integer fields from the current client schema.
type SocialRow struct{ Fields map[int][]uint64 }

func (r SocialRow) V(n int) uint64 {
	if v := r.Fields[n]; len(v) > 0 {
		return v[0]
	}
	return 0
}
func (r SocialRow) A(n int) []uint64 { return r.Fields[n] }

type SocialTable struct {
	Rows   map[[2]uint64]SocialRow
	Groups map[uint64][]uint64
}

func (t SocialTable) Row(group, id uint64) (SocialRow, bool) {
	r, ok := t.Rows[[2]uint64{group, id}]
	return r, ok
}
func loadSocialTables(root, version string, specs map[string][2]int) (map[string]SocialTable, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	out := map[string]SocialTable{}
	for name, fields := range specs {
		rows, e := db.Query("SELECT ProtoBuf FROM " + name)
		if e != nil {
			return nil, e
		}
		table := SocialTable{Rows: map[[2]uint64]SocialRow{}, Groups: map[uint64][]uint64{}}
		for rows.Next() {
			var p []byte
			if e = rows.Scan(&p); e != nil {
				_ = rows.Close()
				return nil, e
			}
			r := SocialRow{Fields: map[int][]uint64{}}
			e = wire.Walk(p, func(f wire.Field) error {
				if f.Type == 0 {
					v, _ := binary.Uvarint(f.Value)
					r.Fields[f.Number] = append(r.Fields[f.Number], v)
				} else if f.Type == 2 && socialPacked(name, f.Number) {
					p := f.Value
					for len(p) > 0 {
						v, n := binary.Uvarint(p)
						if n <= 0 {
							return fmt.Errorf("%w: %s field %d packed varint is truncated", wire.ErrMalformed, name, f.Number)
						}
						r.Fields[f.Number] = append(r.Fields[f.Number], v)
						p = p[n:]
					}
				}
				return nil
			})
			if e != nil {
				_ = rows.Close()
				return nil, e
			}
			g, id := r.V(fields[0]), r.V(fields[1])
			if fields[0] == 0 {
				g = 0
			}
			k := [2]uint64{g, id}
			if _, ok := table.Rows[k]; ok {
				_ = rows.Close()
				return nil, fmt.Errorf("%s: duplicate row", name)
			}
			table.Rows[k] = r
			table.Groups[g] = append(table.Groups[g], id)
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
		for _, ids := range table.Groups {
			slices.Sort(ids)
		}
		out[name] = table
	}
	return out, nil
}
func socialPacked(table string, f int) bool {
	switch table {
	case "CafeteriaLevelTable":
		return f >= 8 && f <= 12
	case "DatingDefaultTable":
		return f >= 2 && f <= 4
	case "DatingMessageTable":
		return f >= 6 && f <= 8
	case "CafeteriaUniqueNpcSpawnTable":
		return f == 2 || f == 5
	}
	return false
}

type CafeteriaDesign struct {
	Default                                                         SocialRow
	Levels, Manage, Costumes, NPCs, Events, Connections, UniqueNPCs SocialTable
}

func LoadCafeteriaDesign(root, version string) (*CafeteriaDesign, error) {
	t, e := loadSocialTables(root, version, map[string][2]int{"CafeteriaDefaultTable": {0, 13}, "CafeteriaLevelTable": {0, 5}, "CafeteriaManageTable": {5, 6}, "CafeteriaCostumeTable": {0, 1}, "CafeteriaNpcTable": {0, 2}, "CafeteriaEventTable": {5, 6}, "DatingCostumeTable": {0, 1}, "CafeteriaUniqueNpcSpawnTable": {0, 1}})
	if e != nil {
		return nil, e
	}
	d := &CafeteriaDesign{Levels: t["CafeteriaLevelTable"], Manage: t["CafeteriaManageTable"], Costumes: t["CafeteriaCostumeTable"], NPCs: t["CafeteriaNpcTable"], Events: t["CafeteriaEventTable"], Connections: t["DatingCostumeTable"], UniqueNPCs: t["CafeteriaUniqueNpcSpawnTable"]}
	d.Default, _ = t["CafeteriaDefaultTable"].Row(0, 0)
	if d.Default.V(16) == 0 || len(d.Levels.Rows) == 0 {
		return nil, fmt.Errorf("cafeteria: missing design")
	}
	return d, nil
}

type DatingDesign struct {
	Default                      SocialRow
	Episodes, Messages, Costumes SocialTable
	MessageEpisode               map[uint64][2]uint64
	CostumeGroup                 map[uint64]SocialRow
}

func LoadDatingDesign(root, version string) (*DatingDesign, error) {
	t, e := loadSocialTables(root, version, map[string][2]int{"DatingDefaultTable": {0, 6}, "DatingEpisodeTable": {6, 7}, "DatingMessageTable": {1, 2}, "DatingCostumeTable": {0, 4}})
	if e != nil {
		return nil, e
	}
	d := &DatingDesign{Episodes: t["DatingEpisodeTable"], Messages: t["DatingMessageTable"], Costumes: t["DatingCostumeTable"], MessageEpisode: map[uint64][2]uint64{}, CostumeGroup: map[uint64]SocialRow{}}
	d.Default, _ = t["DatingDefaultTable"].Row(0, 0)
	for k, r := range d.Episodes.Rows {
		for _, f := range []int{2, 3} {
			if id := r.V(f); id > 0 {
				d.MessageEpisode[id] = k
			}
		}
	}
	for _, r := range d.Costumes.Rows {
		d.CostumeGroup[r.V(2)] = r
	}
	if d.Default.V(5) == 0 || len(d.Episodes.Rows) == 0 {
		return nil, fmt.Errorf("dating: missing design")
	}
	return d, nil
}
