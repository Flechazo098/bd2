package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"fmt"
	"math"
)

func (s *Service) AttachFieldBuffRuntime(design map[uint64]gamedata.FieldBuffDesign) error {
	s.fieldBuffs = design
	s.AttachFieldMonsterDamage(s.applyMonsterFieldDamage)
	return nil
}
func (s *Service) applyMonsterFieldDamage(ctx command.Context, pack int, id uint64, identity string) ([][]byte, error) {
	r, ok := s.fieldBuffs[id]
	if !ok || (r.Type != 4 && r.Type != 5) || r.TargetType > 2 || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 {
		return nil, fmt.Errorf("world: invalid damaging field buff")
	}
	if s.characters == nil || s.decks == nil {
		return nil, fmt.Errorf("world: field health runtime unavailable")
	}
	indices, e := s.fieldBuffTargets(ctx, pack, r.TargetType)
	if e != nil {
		return nil, e
	}
	var response [][]byte
	for _, index := range indices {
		c, found := s.characters.Find(ctx, index)
		if !found {
			return nil, fmt.Errorf("world: field character missing")
		}
		hp, e := s.characters.CurrentHealth(ctx, c.InvenIndex)
		if e != nil {
			return nil, e
		}
		if hp == 0 && r.TargetType != 0 {
			continue
		}
		damage := r.Value
		if r.Type == 5 {
			max, e := s.characters.MaxHealth(ctx, c.InvenIndex)
			if e != nil {
				return nil, e
			}
			damage = float64(max) * r.Value
		}
		remaining := uint64(0)
		if damage < float64(hp) {
			remaining = hp - uint64(damage)
		}
		if e = s.characters.SetCurrentHealth(ctx, c.InvenIndex, remaining); e != nil {
			return nil, e
		}
		c.HP = remaining
		response = append(response, roster.CharacterWire(c))
		if r.TargetType == 0 {
			break
		}
	}
	return response, nil
}

func (s *Service) fieldBuffTargets(ctx command.Context, pack int, target uint64) ([]uint64, error) {
	if target > 2 || s.decks == nil {
		return nil, fmt.Errorf("world: invalid field buff target")
	}
	var indices []uint64
	if s.decks.FieldControlType() == 2 && s.questDifficulty(pack) == 0 && s.storyRoster != nil {
		if quest := s.firstUnclearedQuestFor(pack); quest != 0 {
			party, err := s.currentBattleParty(ctx)
			if err != nil {
				return nil, err
			}
			for _, c := range party {
				indices = append(indices, c.InvenIndex)
			}
		}
	}
	if len(indices) == 0 {
		if s.decks.FieldControlType() == 0 || len(s.decks.CurrentFieldDeck(ctx)) == 0 {
			for _, entry := range s.decks.CurrentDeck() {
				indices = append(indices, entry.CharacterInvenIndex)
			}
		} else {
			for _, entry := range s.decks.CurrentFieldDeck(ctx) {
				indices = append(indices, entry.CharacterInvenIndex)
			}
		}
	}
	// The client animates either party target through the active field train;
	// LEADER applies only to its first member, never all owned characters.
	if target == 0 && len(indices) > 1 {
		indices = indices[:1]
	}
	seen := map[uint64]bool{}
	var unique []uint64
	for _, id := range indices {
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return unique, nil
}
