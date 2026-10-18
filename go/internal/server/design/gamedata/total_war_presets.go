package gamedata

import "fmt"

func loadTotalWarPresetDesign(root, version string) (*PresetDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	d, err := loadPresetDesign(db)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	if d.BaseCount, err = optionalScalar(raw, 114); err != nil {
		return nil, err
	}
	if d.Maximum, err = optionalScalar(raw, 115); err != nil {
		return nil, err
	}
	if err = d.Validate(); err != nil {
		return nil, fmt.Errorf("gamedata: total war presets: %w", err)
	}
	return d, nil
}
