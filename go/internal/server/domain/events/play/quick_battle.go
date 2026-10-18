package eventplay

import (
	"encoding/binary"
	"fmt"
	"math"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) quickBattle(ctx command.Context, req []byte, key string) ([]byte, error) {
	var fields [6]uint64
	seen := map[int]bool{}
	if err := wire.Walk(req, func(f wire.Field) error {
		if f.Number > 6 {
			return nil
		}
		if f.Type != 0 || seen[f.Number] {
			return fmt.Errorf("eventplay: invalid sweep field")
		}
		seen[f.Number] = true
		fields[f.Number-1], _ = binary.Uvarint(f.Value)
		if fields[f.Number-1] > math.MaxInt32 {
			return fmt.Errorf("eventplay: sweep field exceeds int32")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	mode, deck, group, stage, count := fields[1], fields[2], fields[3], fields[4], fields[5]
	if ctx.SessionID == "" || mode != 17 || deck == 0 || group == 0 || stage == 0 || count == 0 || count > 100 {
		return nil, fmt.Errorf("eventplay: invalid event sweep")
	}
	rule, err := s.design.EventBattle(group, stage, deck)
	if err != nil {
		return nil, err
	}
	if !rule.Quick {
		return nil, fmt.Errorf("eventplay: sweep disabled for stage")
	}
	var uid uint64
	for _, schedule := range s.registry.List() {
		if schedule.ID != group || schedule.Type != 8 && schedule.Type != 9 || schedule.UID == 0 || s.now().UnixMilli() < schedule.Start || s.now().UnixMilli() >= schedule.End {
			continue
		}
		if uid != 0 {
			return nil, fmt.Errorf("eventplay: ambiguous active battle schedule")
		}
		uid = schedule.UID
	}
	if uid == 0 {
		return nil, fmt.Errorf("eventplay: event sweep schedule unavailable")
	}
	if !s.state.Stories[fmt.Sprintf("battle:%d:%d:%d", uid, group, stage)] {
		return nil, fmt.Errorf("eventplay: sweep requires completed battle")
	}
	if s.battleChallenges == nil {
		return nil, fmt.Errorf("eventplay: missing battle challenge design")
	}
	definitions := s.battleChallenges[deck]
	for index := range definitions {
		if !s.state.Stories[fmt.Sprintf("challenge:%d:%d:%d:%d", uid, group, stage, index)] {
			return nil, fmt.Errorf("eventplay: sweep requires all battle challenges")
		}
	}
	hub, err := s.battleHub(ctx, uid, true)
	if err != nil {
		return nil, err
	}
	rewards, err := s.battleBonus(ctx, hub, rule.Rewards)
	if err != nil {
		return nil, err
	}
	var grants []gamedata.Reward
	for _, r := range rewards {
		if r.Count > math.MaxInt32/count {
			return nil, fmt.Errorf("eventplay: sweep reward overflow")
		}
		grants = append(grants, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count * count})
	}
	var costs []gamedata.Reward
	if rule.APCost > 0 {
		if rule.APCost > math.MaxInt32/count {
			return nil, fmt.Errorf("eventplay: sweep cost overflow")
		}
		costs = []gamedata.Reward{{Type: 30, Count: rule.APCost * count}}
	}
	bundle, err := s.economy.Apply(ctx, "eventplay:"+key, costs, grants)
	if err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}

func (s *Service) battleHub(ctx command.Context, uid uint64, requireOpen bool) (uint64, error) {
	if s.hubCalendars == nil {
		return 0, nil
	}
	_, body, _, err := s.hubCalendars.Handle(ctx, "/EventHubInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		return 0, err
	}
	var hub uint64
	err = wire.Walk(body, func(f wire.Field) error {
		if f.Number != 1 || f.Type != 2 {
			return nil
		}
		matched := false
		if err := wire.Walk(f.Value, func(slot wire.Field) error {
			if slot.Number != 6 || slot.Type != 2 {
				return nil
			}
			uids, err := list(slot.Value, 3)
			if err != nil {
				return err
			}
			for _, id := range uids {
				matched = matched || id == uid
			}
			return nil
		}); err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if requireOpen && (s.now().UnixMilli() < int64(num(f.Value, 3)) || s.now().UnixMilli() >= int64(num(f.Value, 4))) {
			return fmt.Errorf("eventplay: battle hub play window closed")
		}
		id := num(f.Value, 2)
		if id == 0 || hub != 0 && hub != id {
			return fmt.Errorf("eventplay: ambiguous battle hub")
		}
		hub = id
		return nil
	})
	if err != nil {
		return 0, err
	}
	if hub == 0 {
		return 0, fmt.Errorf("eventplay: battle hub unavailable")
	}
	return hub, nil
}

func (s *Service) battleBonus(ctx command.Context, hub uint64, rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	if hub == 0 || len(s.design.Rows("PickUpCostumeEventTable", 3, hub)) == 0 {
		return rewards, nil
	}
	if s.battleCostumes == nil {
		return nil, fmt.Errorf("eventplay: costume bonus ownership unavailable")
	}
	return s.design.EventBattleBonus(hub, s.battleCostumes(ctx), rewards)
}
