package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"fmt"
)

func (s *Service) AttachOverwhelmAuthorization(f func(ctx command.Context, _ string, _ uint64) error) {
	s.overwhelmAuthorize = f
}
func (s *Service) AttachOverwhelmHunting(h interface {
	ValidateBattle(ctx command.Context, _ int, _ uint64, _ uint64, _ uint64) error
	CompleteBattle(ctx command.Context, _ int, _ uint64, _ uint64, _ uint64, _ string) ([]byte, [][]byte, error)
}) {
	s.overwhelmHunting = h
}

type overwhelmedMonster struct {
	ID, Group, Deck, Mode uint64
	Definition            gamedata.FieldMonsterDesign
	Instance              string
	Costs                 []gamedata.Reward
}

func (s *Service) AttachOverwhelmDesign(source *gamedata.Source) error {
	s.overwhelmQuest = func(pack, quest int) (gamedata.OverwhelmQuestRule, error) {
		return source.OverwhelmQuest(pack, quest)
	}
	return nil
}

func (s *Service) validateOverwhelmQuest(ctx command.Context, pack, quest int, values []uint64, targets []overwhelmedMonster) error {
	if s.overwhelmQuest == nil {
		return fmt.Errorf("world: overwhelm quest rules missing")
	}
	r, e := s.overwhelmQuest(pack, quest)
	if e != nil {
		return e
	}
	mapID, e := s.currentFieldMap(ctx, pack)
	if e != nil {
		return e
	}
	gain := uint64(0)
	for _, m := range targets {
		switch r.Type {
		case 3:
			for _, id := range r.Targets {
				if id == m.ID {
					gain++
				}
			}
		case 1:
			for _, enemy := range r.Enemies[m.Deck] {
				for _, id := range r.Targets {
					if enemy == id {
						gain++
					}
				}
			}
		case 8:
			if len(r.Targets) > 0 && r.Targets[0] == uint64(mapID) {
				gain += uint64(len(r.Enemies[m.Deck]))
			}
		}
	}
	old := uint64(0)
	if p, ok := s.state.QuestInPack(quest, pack, s.questDifficultyFor(pack, quest)); ok && len(p.Values) > 0 {
		old = uint64(p.Values[0])
	}
	if gain == 0 || len(values) != 1 || values[0] != old+gain {
		return fmt.Errorf("world: quest does not match overwhelmed monsters")
	}
	return nil
}
