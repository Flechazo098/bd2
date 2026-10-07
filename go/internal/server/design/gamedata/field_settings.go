package gamedata

import "fmt"

type FieldSettingsDesign struct {
	TalentSlots            int
	CharacterTalentClass   map[uint64]uint64
	CharacterTemporaryPack map[uint64]int
}

func LoadFieldSettingsDesign(root, version string) (*FieldSettingsDesign, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	var raw []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); e != nil {
		return nil, e
	}
	n, e := optionalScalar(raw, 109)
	if e != nil || n == 0 || n > 2147483647 {
		return nil, fmt.Errorf("gamedata: invalid talent slot capacity")
	}
	d := &FieldSettingsDesign{TalentSlots: int(n), CharacterTalentClass: map[uint64]uint64{}, CharacterTemporaryPack: map[uint64]int{}}
	classes := map[uint64]uint64{}
	rows, e := db.Query("SELECT id,ProtoBuf FROM TalentTable")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id uint64
		if e = rows.Scan(&id, &raw); e != nil {
			_ = rows.Close()
			return nil, e
		}
		if id == 0 || id > 2147483647 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid talent identity")
		}
		classes[id], e = optionalScalar(raw, 4)
		if e != nil {
			_ = rows.Close()
			return nil, e
		}
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		return nil, e
	}
	_ = rows.Close()
	rows, e = db.Query("SELECT id,ProtoBuf FROM CharTable")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		if id == 0 || id > 2147483647 {
			return nil, fmt.Errorf("gamedata: invalid character identity")
		}
		talent, e := optionalScalar(raw, 18)
		if e != nil {
			return nil, e
		}
		if class, ok := classes[talent]; ok {
			d.CharacterTalentClass[id] = class
		}
		pack, e := optionalScalar(raw, 21)
		if e != nil {
			return nil, e
		}
		if pack > 2147483647 {
			return nil, fmt.Errorf("gamedata: temporary pack overflows protocol")
		}
		if pack != 0 {
			d.CharacterTemporaryPack[id] = int(pack)
		}
	}
	return d, rows.Err()
}
