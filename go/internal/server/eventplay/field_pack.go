package eventplay

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
)

// ListEventFieldPacks authorizes hidden fields through installed pack rules
// and the live project calendar. A hub field and a minigame field are separate
// routes: some public hubs have no PackEventMiniGameTable row at all.
func (s *Service) ListEventFieldPacks() ([]gamedata.EventFieldPack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventFieldPacks()
}

func (s *Service) ResolveEventFieldPack(id int) (gamedata.EventFieldPack, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	packs, err := s.eventFieldPacks()
	if err != nil {
		return gamedata.EventFieldPack{}, false, err
	}
	for _, p := range packs {
		if p.ID == id {
			return p, true, nil
		}
	}
	return gamedata.EventFieldPack{}, false, nil
}

func (s *Service) eventFieldPacks() ([]gamedata.EventFieldPack, error) {
	now := s.now().UnixMilli()
	result := map[int]gamedata.EventFieldPack{}
	// Bound minigames use the hub play window even when their global schedule
	// has a longer archive window. Zero means a known, currently closed binding.
	boundEnds := map[uint64]int64{}
	add := func(id int, uid, game, hub, mapID, point uint64, end int64) error {
		base, ok := s.design.FieldPacks[id]
		if !ok {
			return nil
		}
		if len(base.MapIDs) == 0 {
			return fmt.Errorf("eventplay: hidden pack %d has no installed map", id)
		}
		if mapID != 0 {
			found := false
			for _, m := range base.MapIDs {
				if uint64(m) == mapID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("eventplay: pack %d calendar map %d mismatch", id, mapID)
			}
		}
		if old, exists := result[id]; exists && old.End >= end {
			return nil
		}
		base.MapIDs = append([]int(nil), base.MapIDs...)
		base.BuyRewards = append([]gamedata.Reward(nil), base.BuyRewards...)
		base.ScheduleUID, base.GameID, base.HubID, base.End = uid, game, hub, end
		base.InitialMapID, base.PointPositionID = mapID, point
		if base.InitialMapID == 0 {
			base.InitialMapID = uint64(base.MapIDs[0])
		}
		result[id] = base
		return nil
	}
	if s.hubCalendars != nil {
		_, body, handled, err := s.hubCalendars.Handle("/EventHubInfo", wire.AppendVarint(nil, 1, 1))
		if err != nil {
			return nil, err
		}
		if handled {
			err = wire.Walk(body, func(f wire.Field) error {
				if f.Number != 1 || f.Type != 2 {
					return nil
				}
				start, end := int64(num(f.Value, 3)), int64(num(f.Value, 4))
				if err := wire.Walk(f.Value, func(setting wire.Field) error {
					if setting.Number != 6 || setting.Type != 2 || num(setting.Value, 2) != 6 {
						return nil
					}
					uids, err := list(setting.Value, 3)
					if err != nil {
						return err
					}
					for _, uid := range uids {
						if _, exists := boundEnds[uid]; !exists {
							boundEnds[uid] = 0
						}
						if now >= start && now < end && boundEnds[uid] < end {
							boundEnds[uid] = end
						}
					}
					return nil
				}); err != nil {
					return err
				}
				if now < start || now >= end {
					return nil
				}
				hubID := num(f.Value, 2)
				hub, e := s.design.Row("PackEventHubTable", 14, hubID)
				if e != nil {
					return e
				}
				return add(int(num(hub, 20)), num(f.Value, 1), 0, hubID, 0, 0, end)
			})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, c := range s.registry.List() {
		for _, binding := range s.fieldBindings {
			if c.Type == binding.EventType && now >= c.Start && now < c.End {
				if err := add(binding.PackID, c.UID, c.ID, 0, 0, 0, c.End); err != nil {
					return nil, err
				}
				p := result[binding.PackID]
				p.ContentOpenType = binding.ContentOpenType
				result[binding.PackID] = p
			}
		}
		if c.Type != 11 || now < c.Start || now >= c.End {
			continue
		}
		end := c.End
		if hubEnd, bound := boundEnds[c.UID]; bound {
			if hubEnd == 0 {
				continue
			}
			if hubEnd < end {
				end = hubEnd
			}
		}
		game, err := s.design.Row("PackEventMiniGameTable", 8, c.ID)
		if err != nil {
			return nil, err
		}
		if err = add(int(num(game, 12)), c.UID, c.ID, 0, num(game, 9), num(game, 13), end); err != nil {
			return nil, err
		}
	}
	var packs []gamedata.EventFieldPack
	for _, p := range result {
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	return packs, nil
}
