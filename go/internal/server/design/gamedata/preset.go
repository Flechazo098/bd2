package gamedata

import (
	"database/sql"
	"fmt"
)

// PresetDesign is the normal party preset configuration in GameDefaultTable.
type PresetDesign struct {
	BaseCount, Maximum, PriceType, PriceID, Price uint64
	Icons                                         map[uint64]bool
}

func (d PresetDesign) Validate() error {
	if d.BaseCount == 0 || d.Maximum < d.BaseCount || d.Price == 0 || d.PriceID != 0 {
		return fmt.Errorf("gamedata: invalid preset configuration")
	}
	switch d.PriceType {
	case 2, 3, 4, 12:
	default:
		return fmt.Errorf("gamedata: unsupported preset currency %d", d.PriceType)
	}
	return nil
}

func LoadPresetDesign(root, version string) (*PresetDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	return loadPresetDesign(db)
}

func loadPresetDesign(db *sql.DB) (*PresetDesign, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d := &PresetDesign{}
	for field, target := range map[int]*uint64{91: &d.BaseCount, 92: &d.Price, 93: &d.PriceID, 94: &d.PriceType, 95: &d.Maximum} {
		v, err := optionalScalar(raw, field)
		if err != nil {
			return nil, err
		}
		*target = v
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id FROM PresetTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	d.Icons = map[uint64]bool{}
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		d.Icons[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return d, nil
}
