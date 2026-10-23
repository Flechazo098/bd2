package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"fmt"
)

func (s *Service) grantFieldMonster(ctx command.Context, pack int, m gamedata.FieldMonsterDesign, identity string) ([]byte, error) {
	if s.researchEconomy == nil {
		return nil, fmt.Errorf("world: monster economy unavailable")
	}
	var rewards []gamedata.Reward
	if m.Reward.Type != 0 && m.Reward.Count > 0 {
		rewards = append(rewards, m.Reward)
	} else if m.BattleDeck != 0 {
		if s.monsterRewards == nil {
			return nil, fmt.Errorf("world: monster reward loader unavailable")
		}
		rows, e := s.monsterRewards(pack, m.BattleDeck)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		}
	}
	bundle, err := s.researchEconomy.Apply(ctx, identity, nil, rewards)
	if err != nil {
		return nil, err
	}
	if err := s.recordMission(ctx, conditions.Event{Type: conditions.FieldRewardMonsterCatch, Count: 1}); err != nil {
		return nil, err
	}
	return bundle, nil
}
