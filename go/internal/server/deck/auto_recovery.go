package deck

import (
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

func (s *Store) AttachAutoRecovery(f func(uint64, uint64, []uint64) (player.AutoRecoveryResult, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoRecovery = f
}
func (s *Store) AttachAutoRecoveryAllowed(f func() (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoRecoveryAllowed = f
}

type autoRecoveryReceipt struct {
	Digest string
	Body   []byte
}

func (s *Store) handleAutoRecovery(req []byte) (int, []byte, bool, error) {
	fail := func(e error) (int, []byte, bool, error) { return 373, nil, true, e }
	if e := checkSeq(req); e != nil {
		return fail(e)
	}
	caster, _, e := wire.Varint(req, 2)
	if e != nil {
		return fail(e)
	}
	seq, _, _ := wire.Varint(req, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.sessionID + ":" + fmt.Sprint(seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(req))
	if s.storage != nil {
		raw, ok, err := s.storage.LoadEntry("deck", "auto_recovery", key)
		if err != nil {
			return fail(err)
		}
		if ok {
			var r autoRecoveryReceipt
			if err = json.Unmarshal(raw, &r); err != nil {
				return fail(err)
			}
			if r.Digest != digest {
				return fail(fmt.Errorf("deck: changed automatic recovery replay"))
			}
			return 373, r.Body, true, nil
		}
	}
	n := clone(s.state)
	if len(n.Deck) == 0 {
		return fail(fmt.Errorf("deck: automatic recovery has no battle deck"))
	}
	result := player.AutoRecoveryResult{Caster: caster, Catalyst: n.AutoReviveCatalyst}
	if s.wallet != nil {
		result.Catalyst = s.wallet.CatalystBalance()
	}
	mode := uint64(2)
	var settings fieldSettings
	if s.characters != nil {
		settings, e = s.loadFieldSettings()
		if e != nil {
			return fail(e)
		}
		if caster != settings.Caster {
			return fail(fmt.Errorf("deck: automatic recovery caster differs from saved setting"))
		}
		if n.FieldCharControlDeckType == 2 {
			allExhausted := true
			for _, v := range n.Deck {
				hp, err := s.characters.CurrentHealth(v.CharacterInvenIndex)
				if err != nil {
					return fail(err)
				}
				if hp > 0 {
					allExhausted = false
				}
			}
			if allExhausted {
				mode = 3
			}
		}
		field := s.visibleFieldDeckLocked()
		var targets []uint64
		seen := map[uint64]bool{}
		add := func(id uint64) error {
			if seen[id] {
				return nil
			}
			seen[id] = true
			c, ok := s.characters.Find(id)
			if !ok || player.IsStoryCharacter(c) || player.IsCharmCharacter(c) {
				return nil
			}
			hp, err := s.characters.CurrentHealth(id)
			if err != nil {
				return err
			}
			if hp == 0 {
				targets = append(targets, id)
			}
			return nil
		}
		if n.FieldCharControlDeckType == 0 {
			for _, v := range n.Deck {
				if e = add(v.CharacterInvenIndex); e != nil {
					return fail(e)
				}
			}
		} else if n.FieldCharControlDeckType == 1 {
			for _, v := range field {
				if e = add(v.CharacterInvenIndex); e != nil {
					return fail(e)
				}
			}
		}
		allowed := true
		if s.autoRecoveryAllowed != nil {
			allowed, e = s.autoRecoveryAllowed()
			if e != nil {
				return fail(e)
			}
		}
		if settings.AutoRevive && allowed && len(targets) > 0 {
			if s.autoRecovery == nil {
				return fail(fmt.Errorf("deck: automatic recovery executor unavailable"))
			}
			result, e = s.autoRecovery(seq, caster, targets)
			if e != nil {
				return fail(e)
			}
			settings.Caster = result.Caster
			if len(result.Characters) > 0 {
				mode = 1
			}
		}
		// On recovery failure, replace fatigued party members with living,
		// permanent owned characters in inventory order. Keep the fatigued
		// member when no replacement exists so the client can show exhaustion.
		if mode != 1 && len(targets) > 0 {
			all := s.characters.RawAll()
			sort.Slice(all, func(i, j int) bool { return all[i].InvenIndex < all[j].InvenIndex })
			used := map[uint64]bool{}
			for _, v := range n.Deck {
				used[v.CharacterInvenIndex] = true
			}
			for _, v := range field {
				used[v.CharacterInvenIndex] = true
			}
			replacements := map[uint64]player.Character{}
			for _, id := range targets {
				for _, c := range all {
					if used[c.InvenIndex] || player.IsStoryCharacter(c) || player.IsCharmCharacter(c) {
						continue
					}
					hp, err := s.characters.CurrentHealth(c.InvenIndex)
					if err != nil {
						return fail(err)
					}
					if hp == 0 {
						continue
					}
					replacements[id] = c
					used[c.InvenIndex] = true
					break
				}
				if _, ok := replacements[id]; !ok {
					mode = 3
				}
			}
			for i, v := range n.Deck {
				if c, ok := replacements[v.CharacterInvenIndex]; ok {
					n.Deck[i].CharacterInvenIndex = c.InvenIndex
				}
			}
			for i, v := range n.FieldDeck {
				if c, ok := replacements[v.CharacterInvenIndex]; ok {
					n.FieldDeck[i].CharacterInvenIndex = c.InvenIndex
					n.FieldDeck[i].CostumeInvenIndex = c.UseCostume
				}
			}
		}
	} else if caster != 0 {
		return fail(fmt.Errorf("deck: automatic recovery character provider unavailable"))
	}
	var out []byte
	for _, v := range n.Deck {
		b := wire.AppendVarint(nil, 1, v.CharacterInvenIndex)
		b = wire.AppendVarint(b, 2, v.CostumeInvenIndex)
		b = wire.AppendVarint(b, 3, v.Slot)
		out = wire.AppendBytes(out, 1, b)
	}
	for _, v := range n.FieldDeck {
		b := wire.AppendVarint(nil, 1, v.Slot)
		b = wire.AppendVarint(b, 2, v.CharacterInvenIndex)
		b = wire.AppendVarint(b, 3, v.CostumeInvenIndex)
		out = wire.AppendBytes(out, 2, b)
	}
	for _, c := range result.Characters {
		out = wire.AppendVarint(out, 3, c.InvenIndex)
		out = wire.AppendBytes(out, 5, player.CharacterWire(c))
	}
	out = wire.AppendVarint(out, 4, mode)
	if result.Caster != 0 {
		out = wire.AppendVarint(out, 9, result.Caster)
	}
	out = wire.AppendVarint(out, 6, result.Experience)
	out = wire.AppendVarint(out, 7, result.Catalyst)
	out = wire.AppendVarint(out, 8, result.Disabled)
	if s.storage != nil {
		core, err := json.Marshal(n)
		if err != nil {
			return fail(err)
		}
		raw, err := json.Marshal(autoRecoveryReceipt{digest, out})
		if err != nil {
			return fail(err)
		}
		changes := []stateio.EntryMutation{{Bucket: "auto_recovery", Key: key, Payload: raw}}
		if s.characters != nil {
			raw, err = json.Marshal(settings)
			if err != nil {
				return fail(err)
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "field_settings", Key: "state", Payload: raw})
		}
		if err = s.storage.SaveWithEntries("deck", core, changes); err != nil {
			return fail(err)
		}
	}
	s.state = n
	return 373, out, true, nil
}
