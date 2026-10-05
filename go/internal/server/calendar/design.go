package calendar

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"fmt"
)

// ValidateDesign checks playable identities against their domain's installed
// GameData. Announcement-only types 3/6/14/15/16/18 have no local gameplay
// design resolver and receive only the manifest's protocol/format validation.
func (s *Set) ValidateDesign(root, version string) error {
	cash, e := gamedata.LoadCashCatalog(root, version)
	if e != nil {
		return e
	}
	keys := map[gamedata.CashProductKey]bool{}
	for _, v := range cash.Products {
		keys[v.Key] = true
	}
	for _, v := range s.CashProducts {
		if !keys[gamedata.CashProductKey{GroupID: v.GroupID, ProductID: v.ProductID, SaleGroup: v.SaleGroup}] {
			return fmt.Errorf("calendar: cash product %d/%d/%d missing GameData", v.GroupID, v.ProductID, v.SaleGroup)
		}
	}
	if s.MonsterHunt != nil {
		ids, e := gamedata.LoadMonsterHuntIDs(root, version)
		if e != nil {
			return e
		}
		for _, v := range s.MonsterHunt.Seasons {
			if !ids[v.HuntID] {
				return fmt.Errorf("calendar: active/playable hunt %d missing GameData", v.HuntID)
			}
		}
	}
	play, e := gamedata.LoadEventPlayCatalog(root, version)
	if e != nil {
		return e
	}
	tasks, e := gamedata.LoadEventTasksDesign(root, version)
	if e != nil {
		return e
	}
	exchange, e := gamedata.LoadEventExchangeCatalog(root, version)
	if e != nil {
		return e
	}
	actions, e := gamedata.LoadEventActionsDesign(root, version)
	if e != nil {
		return e
	}
	packs, e := gamedata.LoadCalendarPackIDs(root, version)
	if e != nil {
		return e
	}
	for _, h := range s.EventHubs {
		if _, e = play.Row("PackEventHubTable", 14, h.HubID); e != nil {
			return fmt.Errorf("calendar: hub %d: %w", h.HubID, e)
		}
	}
	uid := map[uint64]events.Schedule{}
	for _, v := range s.Events {
		if v.UID != 0 {
			uid[v.UID] = v
		}
	}
	for _, h := range s.MiniGameHubs {
		v, ok := uid[h.EventUID]
		if !ok || v.Type != 11 {
			return fmt.Errorf("calendar: mini game slot %d references non-minigame event %d", h.Slot, h.EventUID)
		}
		if _, e := play.Row("PackEventMiniGameTable", 8, v.ID); e != nil {
			return e
		}

	}
	for _, v := range s.Events {
		valid := true
		var err error
		switch v.Type {
		case 0:
			_, valid = tasks.Attendance[v.ID]
		case 1:
			valid = false
			for k := range tasks.LimitRewards {
				if k[0] == v.ID {
					valid = true
					break
				}
			}
		case 4:
			_, valid = tasks.MissionGroups[v.ID]
		case 5:
			_, valid = tasks.Passes[v.ID]
		case 7:
			_, valid = exchange.Groups[v.ID]
		case 8:
			valid = packs[v.ID]
		case 9:
			_, err = play.Row("PackEventBattleGroupTable", 3, v.ID)
		case 10:
			_, err = play.Row("PackEventStoryGroupTable", 1, v.ID)
		case 11:
			_, err = play.Row("PackEventMiniGameTable", 8, v.ID)
		case 12, 13, 17, 19:
			_, err = gamedata.LoadEventGame(root, version, v.Type, v.ID)
		case 20:
			_, valid = actions.Row("TacticsBingoGroupTable", 3, v.ID)
		case 21:
			_, valid = actions.Row("FieldSpawnEventTable", 5, v.ID)
		case 22:
			_, valid = actions.Row("FireworksTable", 4, v.ID)
		case 23:
			_, valid = actions.Row("VotingEventTable", 6, v.ID)
		case 24:
			_, valid = actions.Row("FriendshipSpecialEpisodeTable", 5, v.ID)
		case 25:
			id := v.SubID
			if id == 0 {
				id = v.ID
			}
			_, valid = actions.Row("CafeteriaEventTable", 5, id)
		}
		if !valid || err != nil {
			return fmt.Errorf("calendar: event uid %d type %d id %d missing GameData: %v", v.UID, v.Type, v.ID, err)
		}
	}
	return nil
}
