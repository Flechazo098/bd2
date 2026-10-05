package gamedata

func LoadOwnedEventItemDesign(root, version string) (map[uint64]map[uint64]bool, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	out := map[uint64]map[uint64]bool{}
	for typ, table := range map[uint64]string{47: "IdCardItemTable", 49: "AvatarItemTable", 69: "EquipmentRankChangeItemTable"} {
		rows, e := db.Query("SELECT id FROM " + table)
		if e != nil {
			return nil, e
		}
		out[typ] = map[uint64]bool{}
		for rows.Next() {
			var id uint64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, e
			}
			out[typ][id] = true
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return nil, e
		}
		rows.Close()
	}
	return out, nil
}
