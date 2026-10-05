package gamedata

import (
	"fmt"
	"sort"
)

type PrestigeSkinSelling struct {
	DesignID, GroupID, ProductID, MagicGroupID, MagicID uint64
}

type PrestigeSkinCatalog struct {
	Skins   map[uint64]uint64
	Selling []PrestigeSkinSelling
}

// LoadPrestigeSkinCatalog loads the skin ownership mapping and the separate
// selling-page bindings. PrestigeSkinTable has no "limited" flag; availability
// is therefore determined from the selling binding and the live shop window.
func LoadPrestigeSkinCatalog(root, version string) (*PrestigeSkinCatalog, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT id,ProtoBuf FROM PrestigeSkinTable")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	catalog := &PrestigeSkinCatalog{Skins: map[uint64]uint64{}}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		costume, e := optionalScalar(raw, 1)
		if e != nil {
			return nil, e
		}
		if id == 0 || costume == 0 || catalog.Skins[id] != 0 {
			return nil, fmt.Errorf("gamedata: invalid prestige skin %d", id)
		}
		catalog.Skins[id] = costume
	}
	if e := rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	rows, e = db.Query("SELECT ProtoBuf FROM SkinSellingTable ORDER BY groupId,id")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		values := make([]uint64, 6)
		for i, field := range []int{1, 2, 3, 5, 6} {
			values[i], e = optionalScalar(raw, field)
			if e != nil {
				return nil, e
			}
		}
		if values[0] == 0 || values[1] == 0 || values[2] == 0 {
			return nil, fmt.Errorf("gamedata: invalid prestige selling row")
		}
		catalog.Selling = append(catalog.Selling, PrestigeSkinSelling{DesignID: values[0], GroupID: values[1], ProductID: values[2], MagicGroupID: values[3], MagicID: values[4]})
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	sort.Slice(catalog.Selling, func(i, j int) bool { return catalog.Selling[i].DesignID < catalog.Selling[j].DesignID })
	return catalog, nil
}

// Giftable returns stable design IDs which are not currently represented by a
// cash purchase page. Event-task sources are intentionally not treated as a
// purchase page; their rewards are not purchasable shop goods.
func (c *PrestigeSkinCatalog) Giftable(cashAvailable func(CashProductKey) bool) []uint64 {
	if c == nil {
		return nil
	}
	current := map[uint64]bool{}
	for _, row := range c.Selling {
		if row.MagicGroupID != 0 && cashAvailable != nil && cashAvailable(CashProductKey{GroupID: row.MagicGroupID, ProductID: row.MagicID}) {
			current[row.DesignID] = true
		}
	}
	result := make([]uint64, 0, len(c.Skins))
	for design := range c.Skins {
		if !current[design] {
			result = append(result, design)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func LoadPrestigeSkins(root, version string) (map[uint64]uint64, error) {
	catalog, err := LoadPrestigeSkinCatalog(root, version)
	if err != nil {
		return nil, err
	}
	return catalog.Skins, nil
}
