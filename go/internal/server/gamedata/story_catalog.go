package gamedata

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
)

// ContentOpenRule uses group 1 (ContentUnlockPack). The client grants access
// when the ticket exists and squad level is sufficient. TutorialID announces
// the opening; it is not an additional authorization condition.
type ContentOpenRule struct{ SquadLevel, TicketID, TutorialID uint64 }

type StoryPack struct {
	ID, Type, PriorPackID, NextPackID int
	BuyType, BuyPrice                 uint64
	BuyRewards                        []Reward
	MapIDs                            []int
	Open                              *ContentOpenRule
	Quests                            map[int]QuestDesign
	MainQuestIDs                      []int
}

type StoryTicketSource struct {
	PackID, QuestID, Difficulty int
	Count                       uint64
}
type StoryCatalog struct {
	Packs         map[int]StoryPack
	TicketSources map[uint64][]StoryTicketSource
}

// LoadStoryCatalog enumerates all normal story and master packs independently
// of NextPackId. Terminal master packs retain their actual zero next edge.
func LoadStoryCatalog(root, version string) (*StoryCatalog, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return loadStoryCatalog(db)
}

func loadStoryCatalog(db *sql.DB) (*StoryCatalog, error) {
	d := &StoryCatalog{Packs: map[int]StoryPack{}, TicketSources: map[uint64][]StoryTicketSource{}}
	rows, err := db.Query("SELECT id,ProtoBuf FROM PackTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		read := func(field int) (int, error) {
			values, err := packedInts(raw, field)
			if err != nil || len(values) > 1 || (len(values) == 1 && values[0] > math.MaxInt32) {
				return 0, fmt.Errorf("gamedata: invalid story pack%d field%d", id, field)
			}
			if len(values) == 0 {
				return 0, nil
			}
			return int(values[0]), nil
		}
		kind, err := read(55)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if kind != 0 && kind != 1000 {
			continue
		}
		protoID, err := read(25)
		if err != nil || protoID != id || id <= 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid story pack%d identity", id)
		}
		pack := StoryPack{ID: id, Type: kind}
		buyType, err := read(11)
		if err != nil {
			rows.Close()
			return nil, err
		}
		pack.BuyType = uint64(buyType)
		buyPrice, err := read(7)
		if err != nil {
			rows.Close()
			return nil, err
		}
		pack.BuyPrice = uint64(buyPrice)
		counts, err := packedInts(raw, 8)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ids, err := packedInts(raw, 9)
		if err != nil {
			rows.Close()
			return nil, err
		}
		types, err := packedInts(raw, 10)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if len(counts) != len(ids) || len(ids) != len(types) {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid story pack%d buy rewards", id)
		}
		for i, count := range counts {
			// Costume/equipment are instance rewards: their count is zero,
			// just as in QuestTable. Only zero-count stackable slots are empty.
			if count == 0 && types[i] != 10 && types[i] != 11 {
				continue
			}
			if types[i] == 0 || count > math.MaxInt32 || types[i] > math.MaxInt32 || ids[i] > math.MaxInt32 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid story pack%d buy reward", id)
			}
			pack.BuyRewards = append(pack.BuyRewards, Reward{Type: types[i], ID: ids[i], Count: count})
		}
		if pack.PriorPackID, err = read(57); err != nil {
			rows.Close()
			return nil, err
		}
		if pack.NextPackID, err = read(45); err != nil {
			rows.Close()
			return nil, err
		}
		maps, err := packedInts(raw, 21)
		if err != nil {
			rows.Close()
			return nil, err
		}
		for _, mapID := range maps {
			if mapID == 0 || mapID > math.MaxInt32 {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid story pack%d map", id)
			}
			pack.MapIDs = append(pack.MapIDs, int(mapID))
		}
		d.Packs[id] = pack
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = db.Query("SELECT id,ProtoBuf FROM ContentOpenTable WHERE groupId=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		pack, found := d.Packs[id]
		if !found {
			continue
		}
		var group, protoID uint64
		open := &ContentOpenRule{}
		for field, dst := range map[int]*uint64{1: &group, 2: &protoID, 4: &open.TutorialID, 5: &open.SquadLevel, 6: &open.TicketID} {
			values, err := packedInts(raw, field)
			if err != nil || len(values) > 1 || (len(values) == 1 && values[0] > math.MaxInt32) {
				rows.Close()
				return nil, fmt.Errorf("gamedata: invalid story contentopen%d", id)
			}
			if len(values) == 1 {
				*dst = values[0]
			}
		}
		if group != 1 || protoID != uint64(id) || open.TicketID == 0 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: invalid story contentopen%d key/ticket", id)
		}
		pack.Open = open
		d.Packs[id] = pack
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for id, pack := range d.Packs {
		pack.Quests, err = loadQuestDesignDB(db, id)
		if err != nil {
			return nil, err
		}
		for questID, quest := range pack.Quests {
			if quest.Type == 0 {
				pack.MainQuestIDs = append(pack.MainQuestIDs, questID)
			}
			for slot, rewards := range quest.Rewards {
				for _, reward := range rewards {
					if reward.Type == 19 {
						d.TicketSources[reward.ID] = append(d.TicketSources[reward.ID], StoryTicketSource{PackID: id, QuestID: questID, Difficulty: slot, Count: reward.Count})
					}
				}
			}
		}
		sort.Ints(pack.MainQuestIDs)
		if len(pack.MainQuestIDs) == 0 {
			return nil, fmt.Errorf("gamedata: story pack%d has no main quests", id)
		}
		d.Packs[id] = pack
	}
	if len(d.Packs) == 0 {
		return nil, fmt.Errorf("gamedata: empty story catalog")
	}
	for ticket, sources := range d.TicketSources {
		sort.Slice(sources, func(i, j int) bool {
			if sources[i].PackID != sources[j].PackID {
				return sources[i].PackID < sources[j].PackID
			}
			if sources[i].QuestID != sources[j].QuestID {
				return sources[i].QuestID < sources[j].QuestID
			}
			return sources[i].Difficulty < sources[j].Difficulty
		})
		d.TicketSources[ticket] = sources
	}
	return d, nil
}
