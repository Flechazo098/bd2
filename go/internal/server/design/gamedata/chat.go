package gamedata

import (
	"errors"
	"fmt"
)

type ChatDesign struct {
	ReportLimit                          uint64
	ReportIDs                            map[uint64]bool
	GuildHistoryCount, GuildHistoryHours uint64
	Emojis                               map[uint64]bool
}

func LoadChatDesign(root, version string) (result *ChatDesign, resultErr error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RoomChatDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	v, err := cashScalars(raw, 2)
	if err != nil {
		return nil, err
	}
	if v[0] == 0 {
		return nil, fmt.Errorf("gamedata: missing chat report limit")
	}
	d := &ChatDesign{ReportLimit: v[0], ReportIDs: map[uint64]bool{}}
	rows, err := db.Query("SELECT id FROM RoomChatReportTable")
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		d.ReportIDs[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(d.ReportIDs) == 0 {
		return nil, fmt.Errorf("gamedata: missing chat report reasons")
	}
	if err := db.QueryRow("SELECT ProtoBuf FROM GuildDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	history, err := cashScalars(raw, 1, 2)
	if err != nil {
		return nil, err
	}
	d.GuildHistoryCount, d.GuildHistoryHours = history[0], history[1]
	d.Emojis = map[uint64]bool{}
	emojis, err := db.Query("SELECT id FROM ChatResourceTable WHERE chatResourceType=1")
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, emojis.Close()) }()
	for emojis.Next() {
		var id uint64
		if err := emojis.Scan(&id); err != nil {
			return nil, err
		}
		d.Emojis[id] = true
	}
	if err := emojis.Err(); err != nil {
		return nil, err
	}
	return d, nil
}
