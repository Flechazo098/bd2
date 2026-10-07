package gamedata

func LoadEventAPDesign(root, version string) (map[uint64]uint64, HuntingAPDesign, error) {
	d, e := LoadHuntingAPDesign(root, version)
	if e != nil {
		return nil, d, e
	}
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, d, e
	}
	defer done()
	var raw []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); e != nil {
		return nil, d, e
	}
	caps := map[uint64]uint64{}
	for typ, field := range map[uint64]int{30: 43, 32: 113} {
		n, e := optionalScalar(raw, field)
		if e != nil {
			return nil, d, e
		}
		caps[typ] = n
	}
	return caps, d, nil
}
