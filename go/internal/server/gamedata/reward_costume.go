package gamedata

// LoadRewardCostumeCatalog covers every installed costume, including event-only
// rewards that never occur in a regular gacha pool.
func LoadRewardCostumeCatalog(root, version string) (*RegularGachaCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	chars, e := db.Query("SELECT uniqueCharId,ProtoBuf FROM CharTable")
	if e != nil {
		return nil, e
	}
	eligible := map[uint64]uint64{}
	for chars.Next() {
		var unique uint64
		var raw []byte
		if e = chars.Scan(&unique, &raw); e != nil {
			chars.Close()
			return nil, e
		}
		growth, e := optionalScalar(raw, 10)
		if e != nil {
			chars.Close()
			return nil, e
		}
		temporary, e := optionalScalar(raw, 21)
		if e != nil {
			chars.Close()
			return nil, e
		}
		if growth == 1 && temporary == 0 {
			eligible[unique]++
		}
	}
	if e = chars.Err(); e != nil {
		chars.Close()
		return nil, e
	}
	chars.Close()
	rows, err := db.Query("SELECT id,useUniqueCharId FROM CostumeTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for rows.Next() {
		var id, unique uint64
		if err = rows.Scan(&id, &unique); err != nil {
			rows.Close()
			return nil, err
		}
		if eligible[unique] == 1 {
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := &RegularGachaCatalog{characters: map[uint64]CharacterDesign{}}
	for _, id := range ids {
		design, e := loadGachaCharacterDesign(db, id)
		if e != nil {
			// Non-player prototype costumes lack a grantable growth boundary.
			// They remain unavailable to Economy rather than inventing metadata.
			continue
		}
		out.characters[id] = design
	}
	return out, nil
}
