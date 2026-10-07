package gamedata

// LoadRewardEquipmentCatalog reads every equipment definition for rewards,
// independently of which gacha banners the server has opened.
func LoadRewardEquipmentCatalog(root, version string) (*EquipmentGachaCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := db.Query("SELECT id FROM EquipmentTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	c := &EquipmentGachaCatalog{equipment: map[uint64]EquipmentDesign{}}
	for _, id := range ids {
		if err = c.loadEquipmentTree(db, WeightedEquipment{ID: id}); err != nil {
			return nil, err
		}
	}
	return c, nil
}
