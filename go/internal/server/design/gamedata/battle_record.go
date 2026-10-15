package gamedata

import "fmt"

func BattleRecordContent(root, version string, mode, monster uint64) error {
	var table string
	switch mode {
	case 8, 24:
		table = "MonsterHuntTable"
	case 23, 26:
		table = "GuildRaidBossTable"
	default:
		return fmt.Errorf("gamedata: battle mode has no recorded actions")
	}
	if monster == 0 {
		return fmt.Errorf("gamedata: missing battle record monster")
	}
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return err
	}
	defer done()
	field := 17
	if mode == 23 || mode == 26 {
		field = 12
	}
	rows, err := db.Query("SELECT ProtoBuf FROM " + table)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		id, e := optionalScalar(raw, field)
		if e != nil {
			return e
		}
		if id == monster {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return fmt.Errorf("gamedata: unknown battle record content %d", monster)
}
