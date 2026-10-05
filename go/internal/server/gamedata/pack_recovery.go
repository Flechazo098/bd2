package gamedata

// PackRecoveryPolicy keeps the actual PackTable type, including packs outside
// the currently playable story catalog.
type PackRecoveryPolicy struct{ Types map[int]uint64 }

func LoadPackRecoveryPolicy(root, version string) (*PackRecoveryPolicy, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := &PackRecoveryPolicy{Types: map[int]uint64{}}
	for rows.Next() {
		var id int
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		typ, e := optionalScalar(raw, 55)
		if e != nil {
			return nil, e
		}
		p.Types[id] = typ
	}
	return p, rows.Err()
}
func (p *PackRecoveryPolicy) Allowed(pack int, complete bool) bool {
	typ, ok := p.Types[pack]
	if !ok {
		return false
	}
	switch typ {
	case 4:
		return false
	case 1, 6:
		return complete
	default:
		return true
	}
}
