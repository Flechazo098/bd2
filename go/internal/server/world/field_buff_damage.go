package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"fmt"
	"math"
)

func (s *Service) AttachFieldBuffRuntime(root, version string) error {
	d, e := gamedata.LoadFieldBuffDesign(root, version)
	if e != nil {
		return e
	}
	s.fieldBuffs = d
	s.AttachFieldMonsterDamage(s.applyMonsterFieldDamage)
	return nil
}
func (s *Service) applyMonsterFieldDamage(pack int, id uint64, identity string) ([][]byte, error) {
	r, ok := s.fieldBuffs[id]
	if !ok || (r.Type != 4 && r.Type != 5) || r.TargetType > 2 || math.IsNaN(r.Value) || math.IsInf(r.Value, 0) || r.Value < 0 {
		return nil, fmt.Errorf("world: invalid damaging field buff")
	}
	if s.characters == nil || s.decks == nil {
		return nil, fmt.Errorf("world: field health runtime unavailable")
	}
	entries := s.decks.CurrentFieldDeck()
	indices := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		indices = append(indices, entry.CharacterInvenIndex)
	}
	if r.TargetType == 0 {
		party, e := s.ResolveStoryParty(pack, s.firstUnclearedQuestFor(pack))
		if e != nil {
			return nil, e
		}
		if len(party) > 0 && s.questDifficulty(pack) == 0 {
			indices = []uint64{party[0].InvenIndex}
		} else if s.decks.FieldControlType() == 0 || len(indices) == 0 {
			indices = nil
			for _, entry := range s.decks.CurrentDeck() {
				indices = append(indices, entry.CharacterInvenIndex)
			}
		}
		if len(indices) > 1 {
			indices = indices[:1]
		}
	}
	var response [][]byte
	for _, index := range indices {
		c, found := s.characters.Find(index)
		if !found {
			return nil, fmt.Errorf("world: field character missing")
		}
		hp, e := s.characters.CurrentHealth(c.InvenIndex)
		if e != nil {
			return nil, e
		}
		if hp == 0 && r.TargetType != 0 {
			continue
		}
		damage := r.Value
		if r.Type == 5 {
			max, e := s.characters.MaxHealth(c.InvenIndex)
			if e != nil {
				return nil, e
			}
			damage = float64(max) * r.Value
		}
		remaining := uint64(0)
		if damage < float64(hp) {
			remaining = hp - uint64(damage)
		}
		if e = s.characters.SetCurrentHealth(c.InvenIndex, remaining); e != nil {
			return nil, e
		}
		c.HP = remaining
		response = append(response, player.CharacterWire(c))
		if r.TargetType == 0 {
			break
		}
	}
	return response, nil
}
