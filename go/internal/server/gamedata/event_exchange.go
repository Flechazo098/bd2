package gamedata

import "fmt"

type EventExchangeEntry struct {
	ID, Page, KeyType, Ratio, LimitedRatio, SetCount uint64
	Reward                                           Reward
}
type EventExchangeGroup struct {
	ID, StartPage, EndPage, FreeCount, FreeType, UnlockRatio uint64
	Repeat                                                   bool
	Cost                                                     Reward
	Entries                                                  []EventExchangeEntry
}
type EventExchangeCatalog struct{ Groups map[uint64]EventExchangeGroup }

func LoadEventExchangeCatalog(root, version string) (*EventExchangeCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &EventExchangeCatalog{Groups: map[uint64]EventExchangeGroup{}}
	rows, err := db.Query("SELECT id,ProtoBuf FROM EventCoinExchangeGroupTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		g := EventExchangeGroup{ID: id}
		for n, p := range map[int]*uint64{22: &g.StartPage, 4: &g.EndPage, 7: &g.FreeCount, 8: &g.FreeType, 25: &g.UnlockRatio, 14: &g.Cost.Count, 15: &g.Cost.ID, 16: &g.Cost.Type} {
			*p, err = optionalScalar(raw, n)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		repeat, e := optionalScalar(raw, 12)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
		g.Repeat = repeat != 0
		if g.StartPage == 0 || g.EndPage < g.StartPage || g.Cost.Count == 0 || g.Cost.Type == 0 || g.FreeType > 2 || g.ID > 2147483647 || g.EndPage > 2147483647 || g.Cost.Count > 2147483647 || g.FreeCount > 2147483647 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid exchange group %d", id)
		}
		d.Groups[id] = g
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT groupId,id,pageId,ProtoBuf FROM EventCoinExchangeTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var group, id, page uint64
		var raw []byte
		if err = rows.Scan(&group, &id, &page, &raw); err != nil {
			return nil, err
		}
		g, ok := d.Groups[group]
		if !ok {
			return nil, fmt.Errorf("gamedata: exchange group missing")
		}
		e := EventExchangeEntry{ID: id, Page: page}
		for n, p := range map[int]*uint64{3: &e.KeyType, 4: &e.LimitedRatio, 6: &e.Ratio, 7: &e.Reward.Count, 8: &e.Reward.ID, 9: &e.Reward.Type, 10: &e.SetCount} {
			*p, err = optionalScalar(raw, n)
			if err != nil {
				return nil, err
			}
		}
		if e.ID == 0 || e.Page < g.StartPage || e.Page > g.EndPage || e.SetCount == 0 || e.Ratio == 0 || e.Reward.Type == 0 || e.Reward.Count == 0 || e.SetCount > 2147483647 || e.Reward.Count > 2147483647 || e.ID > 2147483647 {
			return nil, fmt.Errorf("gamedata: invalid exchange entry %d:%d", group, id)
		}
		g.Entries = append(g.Entries, e)
		d.Groups[group] = g
	}
	return d, rows.Err()
}
func (g EventExchangeGroup) Page(page uint64) []EventExchangeEntry {
	if page > g.EndPage {
		page = g.EndPage
	}
	var out []EventExchangeEntry
	for _, e := range g.Entries {
		if e.Page == page {
			out = append(out, e)
		}
	}
	return out
}
