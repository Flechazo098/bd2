package gamedata

func LoadPrestigeSkins(root, version string) (map[uint64]uint64, error) {
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
	out := map[uint64]uint64{}
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
		out[id] = costume
	}
	return out, rows.Err()
}
