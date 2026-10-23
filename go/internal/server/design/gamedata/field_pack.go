package gamedata

import (
	"database/sql"
	"fmt"
)

// FieldPack describes non-story arena fields. PackType comes from the protocol
// enum; IDs and entry restrictions come from the installed GameData tables.
type FieldPack struct {
	ID, Type, BuyPrice, UseSchedule uint64
	BuyType                         uint64
	BuyRewards                      []Reward
	TicketID, SquadLevel            uint64
	HasOpenRule                     bool
	MapIDs                          map[int]bool
	IntroQuest                      *QuestDesign
}

func LoadFieldPacks(root, version string) (map[int]FieldPack, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadFieldPacks(db)
}

// fieldPackTypes are the Define_PackType values served as field packs: 1 Char,
// 3 PVP, 4 EvilCastle, 5 SkyWay, 6 Event, 7 TotalWar, 8 GuildRaid, 9 Cafeteria, 10 Colosseum,
// 11 Square, 12 Fishing, 13 Life, 14 Ib.
var fieldPackTypes = map[uint64]bool{1: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true, 12: true, 13: true, 14: true}

func loadFieldPacks(db *sql.DB) (map[int]FieldPack, error) {
	result := map[int]FieldPack{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values := map[int]uint64{}
		for _, field := range []int{7, 11, 25, 55, 65} {
			v, e := packedInts(raw, field)
			if e != nil || len(v) > 1 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid arena pack %d field %d", id, field)
			}
			if len(v) == 1 {
				values[field] = v[0]
			}
		}
		if !fieldPackTypes[values[55]] {
			continue
		}
		if id <= 0 || values[25] != uint64(id) {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid field pack identity %d", id)
		}
		pack := FieldPack{ID: uint64(id), Type: values[55], BuyPrice: values[7], BuyType: values[11], UseSchedule: values[65], MapIDs: map[int]bool{}}
		rewards, err := eventGameRewards(raw, 10, 9, 8)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		for _, reward := range rewards {
			pack.BuyRewards = append(pack.BuyRewards, Reward(reward))
		}
		result[id] = pack
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM ContentOpenTable WHERE groupId=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pack, exists := result[id]
		if !exists {
			continue
		}
		pack.HasOpenRule = true
		for field, target := range map[int]*uint64{5: &pack.SquadLevel, 6: &pack.TicketID} {
			v, e := packedInts(raw, field)
			if e != nil || len(v) > 1 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid field pack opening %d", id)
			}
			if len(v) == 1 {
				*target = v[0]
			}
		}
		result[id] = pack
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT id,packId FROM MapTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, packID int
		if err := rows.Scan(&id, &packID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if pack, exists := result[packID]; exists {
			pack.MapIDs[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for id, pack := range result {
		if len(pack.MapIDs) == 0 {
			return nil, fmt.Errorf("gamedata: arena pack %d has no map", id)
		}
		intro, err := loadFieldIntroduction(db, id)
		if err != nil {
			return nil, err
		}
		pack.IntroQuest = intro
		result[id] = pack
	}
	return result, nil
}

// Standalone field introductions have one accept movie and no quest chain,
// completion movie or rewards. Ordinary field quest chains retain their own
// acceptance and settlement rules, even when their first quest has a movie.
func loadFieldIntroduction(db *sql.DB, packID int) (*QuestDesign, error) {
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", fmt.Sprintf("QuestTable%d", packID)).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	quests, err := loadQuestDesignDB(db, packID)
	if err != nil || len(quests) != 1 {
		return nil, err
	}
	for _, quest := range quests {
		if quest.Type != 0 || quest.PriorQuestID != 0 || quest.NextQuestID != 0 || quest.AcceptCinemaName == "" || quest.CompleteCinemaName != "" || len(quest.GiveQuestItemIDs) != 0 || len(quest.CollectionRewards) != 0 {
			return nil, nil
		}
		for _, rewards := range quest.Rewards {
			if len(rewards) != 0 {
				return nil, nil
			}
		}
		return &quest, nil
	}
	return nil, nil
}
