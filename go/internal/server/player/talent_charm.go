package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const CharmCharacterIndexBase uint64 = 1 << 59

func IsCharmCharacter(c Character) bool {
	return c.InvenIndex >= CharmCharacterIndexBase && c.InvenIndex < StoryCharacterIndexBase
}
func CharacterExpired(c Character, now time.Time) bool {
	return c.ExpiryTime > 0 && c.ExpiryTime <= uint64(now.UnixMilli())
}

// Charm instances are field companions. They live only in the character domain
// with an expiry, never grant a permanent collection character or costume.
func (s *TalentUseService) applyCharm(_ string, _ Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error) {
	pack, _, _, err := s.context()
	if err != nil {
		return nil, err
	}
	npcs, err := s.design.NPCs(pack)
	if err != nil {
		return nil, err
	}
	npc := npcs[targets[0]]
	if s.design.CharmCharacter == nil || len(rule.Values) < 2 {
		return nil, fmt.Errorf("player: charm character design unavailable")
	}
	var chars []Character
	expiry := uint64(s.now().UnixMilli() + int64(rule.Values[1])*1000)
	for _, id := range npc.CharmCharacters {
		d, e := s.design.CharmCharacter(id)
		if e != nil {
			return nil, e
		}
		if d.CharacterID == 0 || d.CostumeID == 0 || d.HP == 0 {
			return nil, fmt.Errorf("player: invalid charm companion design")
		}
		if pack <= 0 || pack >= 65536 || d.CharacterID >= 1<<32 {
			return nil, fmt.Errorf("player: charm instance namespace overflow")
		}
		index := CharmCharacterIndexBase | uint64(pack)<<32 | d.CharacterID
		chars = append(chars, Character{InvenIndex: index, ID: d.CharacterID, Level: d.Level, HP: d.HP, CostumeID: d.CostumeID, TalentLevel: 1, ExpiryTime: expiry})
	}
	if err = s.characters.ensureCharmCharacters(chars); err != nil {
		return nil, err
	}
	var out []byte
	for _, c := range chars {
		out = wire.AppendBytes(out, 5, CharacterWire(c))
	}
	return out, nil
}
func (s *CharacterStore) ensureCharmCharacters(chars []Character) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := append([]Character(nil), s.characters...)
	for _, c := range chars {
		if !IsCharmCharacter(c) || c.ExpiryTime == 0 {
			return fmt.Errorf("player: invalid charm instance")
		}
		found := false
		for i, old := range next {
			if old.InvenIndex == c.InvenIndex {
				if old.ID != c.ID {
					return fmt.Errorf("player: charm instance collision")
				}
				next[i] = c
				found = true
				break
			}
		}
		if !found {
			next = append(next, c)
		}
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.characters = next
	for _, c := range chars {
		raw, err := json.Marshal(c.HP)
		if err != nil {
			return err
		}
		if err = s.store.PutEntry("characters", "current_hp", strconv.FormatUint(c.InvenIndex, 10), raw); err != nil {
			return err
		}
	}
	return nil
}
