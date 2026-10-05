package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

func (s *TalentUseService) charHealing(request []byte) (int, []byte, bool, error) {
	return s.healing(request, "healing")
}

func (s *TalentUseService) healing(request []byte, operation string) (int, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(e error) (int, []byte, bool, error) { return 62, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || s.session == "" {
		return fail(fmt.Errorf("player: invalid healing sequence"))
	}
	index, _, err := wire.Varint(request, 2)
	if err != nil || index > math.MaxInt64 {
		return fail(fmt.Errorf("player: invalid healing caster"))
	}
	food, _, err := wire.Varint(request, 4)
	if err != nil || food > math.MaxInt64 || (index == 0) == (food == 0) {
		return fail(fmt.Errorf("player: healing requires caster or food"))
	}
	state, err := s.load()
	if err != nil {
		return fail(err)
	}
	key := s.session + ":" + operation + ":" + fmt.Sprint(seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	if prior, ok := state.Receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed healing replay"))
		}
		return 62, prior.Body, true, nil
	}
	var ids []uint64
	seen := map[uint64]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("player: invalid healing targets")
		}
		for raw := f.Value; len(raw) > 0; {
			id, n := binary.Uvarint(raw)
			if n <= 0 || id == 0 || id > math.MaxInt64 || seen[id] || len(ids) >= 4096 {
				return fmt.Errorf("player: invalid healing target")
			}
			raw = raw[n:]
			seen[id] = true
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return fail(fmt.Errorf("player: missing healing targets"))
	}
	if s.context == nil {
		return fail(fmt.Errorf("player: healing field context unavailable"))
	}
	pack, _, battle, err := s.context()
	if err != nil {
		return fail(err)
	}
	if battle {
		return fail(fmt.Errorf("player: healing unavailable during battle"))
	}
	var character Character
	var rule gamedata.TalentUseRule
	var item Item
	if food > 0 {
		for _, v := range s.inventory.All() {
			if v.InvenIndex == food && v.Type == 5 {
				item = v
				break
			}
		}
		g, ok := s.design.Foods[item.ID]
		if !ok {
			return fail(fmt.Errorf("player: invalid revival food"))
		}
		rule, ok = s.design.Rules[[2]uint64{g, 1}]
		if !ok {
			return fail(fmt.Errorf("player: missing revival food talent"))
		}
		item.Count = uint64(len(ids))
		if err = s.inventory.CanConsume([]Item{item}); err != nil {
			return fail(err)
		}
	} else {
		var owned bool
		character, owned = s.characters.Find(index)
		meta, ok := s.design.Characters[character.ID]
		if !owned || !ok || meta.BannedPacks[pack] {
			return fail(fmt.Errorf("player: invalid healing caster"))
		}
		rule, ok = s.design.Rules[[2]uint64{meta.Group, character.TalentLevel}]
		if !ok {
			return fail(fmt.Errorf("player: missing recovery level"))
		}
	}
	if rule.Class != 10 || len(rule.Values) == 0 || rule.Values[0] > 100 {
		return fail(fmt.Errorf("player: invalid fatigue recovery skill"))
	}
	var targets []Character
	for _, id := range ids {
		c, owned := s.characters.Find(id)
		if !owned {
			return fail(fmt.Errorf("player: revival target not owned"))
		}
		hp, e := s.characters.CurrentHealth(id)
		if e != nil {
			return fail(e)
		}
		if hp != 0 {
			return fail(fmt.Errorf("player: revival target is not fatigued"))
		}
		maximum, e := s.characters.MaxHealth(id)
		if e != nil {
			return fail(e)
		}
		c.HP = 1
		if rule.Values[0] > 0 {
			c.HP = uint64(float64(maximum) * rule.Values[0] / 100)
			if c.HP == 0 {
				c.HP = 1
			}
		}
		targets = append(targets, c)
	}
	identity := "talent-healing:" + key
	if food > 0 {
		if err = s.inventory.Consume([]Item{item}); err != nil {
			return fail(err)
		}
	} else {
		if rule.Catalyst > math.MaxUint64/uint64(len(ids)) {
			return fail(fmt.Errorf("player: recovery cost overflow"))
		}
		if rule.Catalyst > 0 {
			if rule.Catalyst > 0 {
				if _, err = s.wallet.SpendCatalystOnce(identity, rule.Catalyst*uint64(len(ids))); err != nil {
					return fail(err)
				}
			}
		}
	}
	var out []byte
	for _, c := range targets {
		if err = s.characters.SetCurrentHealth(c.InvenIndex, c.HP); err != nil {
			return fail(err)
		}
		out = wire.AppendBytes(out, 1, CharacterWire(c))
	}
	if food == 0 && rule.Experience > 0 {
		maximum, e := s.experienceMaximum(character)
		if e != nil {
			return fail(e)
		}
		gain := rule.Experience * uint64(len(ids))
		if character.TalentExp >= maximum {
			gain = 0
		} else if gain > maximum-character.TalentExp {
			gain = maximum - character.TalentExp
		}
		if gain > 0 {
			if _, e = s.characters.AddTalentExperience(index, gain, maximum); e != nil {
				return fail(e)
			}
			out = wire.AppendVarint(out, 2, gain)
		}
	}
	state.Receipts[key] = talentUseReceipt{Digest: digest, Body: out}
	b, err := json.Marshal(state)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save("talentuse", b); err != nil {
		return fail(err)
	}
	return 62, out, true, nil
}
