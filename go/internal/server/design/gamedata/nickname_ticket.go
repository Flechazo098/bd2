package gamedata

import (
	"fmt"
)

func LoadNicknameTicketID(root, version string) (uint64, error) {
	db, release, err := openStatDatabase(root, version)
	if err != nil {
		return 0, err
	}
	defer release()
	rows, err := db.Query("SELECT ProtoBuf FROM UseItemTable")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var ticket uint64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		kind, err := optionalScalar(raw, 11)
		if err != nil {
			return 0, err
		}
		if kind != 1 {
			continue
		}
		id, err := optionalScalar(raw, 3)
		if err != nil || id == 0 || ticket != 0 {
			return 0, fmt.Errorf("gamedata: ambiguous nickname change ticket")
		}
		ticket = id
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if ticket == 0 {
		return 0, fmt.Errorf("gamedata: nickname change ticket missing")
	}
	return ticket, nil
}
