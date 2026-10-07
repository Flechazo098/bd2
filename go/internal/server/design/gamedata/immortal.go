package gamedata

import "fmt"

// ImmortalDesign resolves exactly the current character talent and skill level.
// Full restoration is evidenced for value 10000; other values require official
// server evidence of their rounding/scaling semantics before being enabled.
type ImmortalDesign struct {
	Characters  map[uint64]uint64
	FullRestore map[[2]uint64]bool
}

func LoadImmortalDesign(root, version string) (*ImmortalDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	d := &ImmortalDesign{Characters: map[uint64]uint64{}, FullRestore: map[[2]uint64]bool{}}
	talents := map[uint64]uint64{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM TalentTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		group, err := optionalScalar(raw, 18)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		talents[id] = group
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM CharTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		talent, err := optionalScalar(raw, 18)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		if group := talents[talent]; group != 0 {
			d.Characters[id] = group
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT groupId,id,ProtoBuf FROM TalentSkillTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var group, level uint64
		var raw []byte
		if err := rows.Scan(&group, &level, &raw); err != nil {
			return nil, err
		}
		class, err := optionalScalar(raw, 2)
		if err != nil {
			return nil, err
		}
		if class != 14 {
			continue
		}
		values, err := fixed32Floats(raw, 14)
		if err != nil || len(values) == 0 {
			return nil, fmt.Errorf("gamedata: immortal skill missing value")
		}
		d.FullRestore[[2]uint64{group, level}] = values[0] == 10000
	}
	return d, rows.Err()
}

func (d *ImmortalDesign) CanRestore(characterID, level uint64) bool {
	return d != nil && d.FullRestore[[2]uint64{d.Characters[characterID], level}]
}
