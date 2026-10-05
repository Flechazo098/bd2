package gamedata

import "fmt"

// LoadMonsterHuntIDs reads only supported hunt identities. Historical calendar
// entries remain display metadata even after their gameplay design is removed.
func LoadMonsterHuntIDs(root, version string) (map[uint64]bool, error) {
	return loadCalendarIDs(root, version, "MonsterHuntTable")
}

// LoadCalendarPackIDs includes story and event packs, rather than only the
// arena subset returned by LoadFieldPacks.
func LoadCalendarPackIDs(root, version string) (map[uint64]bool, error) {
	return loadCalendarIDs(root, version, "PackTable")
}
func loadCalendarIDs(root, version, table string) (map[uint64]bool, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT id FROM " + table)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := map[uint64]bool{}
	for rows.Next() {
		var id uint64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		if id == 0 {
			return nil, fmt.Errorf("gamedata: invalid monster hunt identity")
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
