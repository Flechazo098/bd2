package deck

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

type fieldSettings struct {
	TalentIDs  []uint64 `json:"talent_ids"`
	AutoRevive bool     `json:"auto_revive"`
	Caster     uint64   `json:"caster"`
}

// A new formal entry owns quick slots and automatic recovery preferences; the
// existing deck core and historical protobuf schema keep their current layout.
func (s *Store) AttachFieldSettings(d *gamedata.FieldSettingsDesign) error {
	if d == nil || d.TalentSlots <= 0 {
		return fmt.Errorf("deck: invalid field settings design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fieldSettingsDesign = d
	_, e := s.loadFieldSettings()
	return e
}
func (s *Store) AttachFieldSettingsPack(source func() (int, error)) error {
	if source == nil {
		return fmt.Errorf("deck: nil field pack provider")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fieldSettingsPack = source
	return nil
}
func (s *Store) temporaryAllowed(c player.Character) bool {
	if s.fieldSettingsDesign == nil || s.fieldSettingsPack == nil {
		return false
	}
	p, e := s.fieldSettingsPack()
	return e == nil && s.fieldSettingsDesign.CharacterTemporaryPack[c.ID] == p && p != 0
}
func (s *Store) loadFieldSettings() (fieldSettings, error) {
	d := s.fieldSettingsDesign
	if d == nil {
		return fieldSettings{}, fmt.Errorf("deck: field settings unavailable")
	}
	v := fieldSettings{TalentIDs: make([]uint64, d.TalentSlots)}
	raw, ok, e := s.storage.LoadEntry("deck", "field_settings", "state")
	if e != nil || !ok {
		return v, e
	}
	if e = stateExactFieldSettings(raw, &v); e != nil {
		return v, e
	}
	if e = s.validateFieldSettings(v, false); e != nil {
		return v, e
	}
	return v, nil
}
func stateExactFieldSettings(raw []byte, v *fieldSettings) error {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	if len(fields) != 3 || fields["talent_ids"] == nil || fields["auto_revive"] == nil || fields["caster"] == nil {
		return fmt.Errorf("deck: invalid field settings layout")
	}
	return json.Unmarshal(raw, v)
}
func (s *Store) validateFieldSettings(v fieldSettings, live bool) error {
	if len(v.TalentIDs) != s.fieldSettingsDesign.TalentSlots {
		return fmt.Errorf("deck: talent slot count")
	}
	owned := map[uint64]bool{}
	if !live {
		for id := range s.fieldSettingsDesign.CharacterTalentClass {
			owned[id] = true
		}
	}
	if s.characters != nil {
		for _, c := range s.characters.RawAll() {
			if live && player.CharacterExpired(c, time.Now()) {
				continue
			}
			if !player.IsStoryCharacter(c) || s.temporaryAllowed(c) || !live && s.fieldSettingsDesign.CharacterTemporaryPack[c.ID] != 0 {
				owned[c.ID] = true
			}
		}
	}
	seen := map[uint64]bool{}
	for _, id := range v.TalentIDs {
		if id == 0 {
			continue
		}
		if id > 2147483647 || seen[id] || !owned[id] {
			return fmt.Errorf("deck: talent slot character unavailable %d", id)
		}
		if _, ok := s.fieldSettingsDesign.CharacterTalentClass[id]; !ok {
			return fmt.Errorf("deck: character has no talent")
		}
		seen[id] = true
	}
	if v.Caster != 0 {
		if s.characters == nil {
			return fmt.Errorf("deck: character provider unavailable")
		}
		c, ok := s.characters.Find(v.Caster)
		if !ok || player.IsStoryCharacter(c) || player.IsCharmCharacter(c) || s.fieldSettingsDesign.CharacterTalentClass[c.ID] != 10 {
			return fmt.Errorf("deck: invalid automatic recovery caster")
		}
	}
	if v.AutoRevive && v.Caster == 0 {
		return fmt.Errorf("deck: automatic recovery needs caster")
	}
	return nil
}
func (s *Store) TalentCharacterIDs() ([]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, e := s.loadFieldSettings()
	return s.projectTalentSlots(v.TalentIDs), e
}
func (s *Store) projectTalentSlots(ids []uint64) []uint64 {
	out := append([]uint64(nil), ids...)
	owned := map[uint64]bool{}
	charm := map[uint64]bool{}
	if s.characters != nil {
		for _, c := range s.characters.RawAll() {
			if player.CharacterExpired(c, time.Now()) {
				continue
			}
			if player.IsCharmCharacter(c) {
				owned[c.ID] = true
				charm[c.ID] = true
				continue
			}
			if !player.IsStoryCharacter(c) || s.temporaryAllowed(c) {
				owned[c.ID] = true
			}
		}
	}
	for i, id := range out {
		if id != 0 && !owned[id] {
			out[i] = 0
			continue
		}
		if s.fieldSettingsDesign.CharacterTemporaryPack[id] != 0 && !charm[id] {
			p := 0
			if s.fieldSettingsPack != nil {
				p, _ = s.fieldSettingsPack()
			}
			if p != s.fieldSettingsDesign.CharacterTemporaryPack[id] {
				out[i] = 0
			}
		}
	}
	return out
}
func (s *Store) AutoReviveSettings() (bool, uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, e := s.loadFieldSettings()
	return v.AutoRevive, v.Caster, e
}
func (s *Store) FieldControlType() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.FieldCharControlDeckType
}
func (s *Store) handleFieldSettings(path string, req []byte) (int, []byte, bool, error) {
	if e := checkSeq(req); e != nil {
		return 0, nil, true, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.loadFieldSettings()
	if e != nil {
		return 0, nil, true, e
	}
	code := 71
	var body []byte
	if path == "/TalentSlotSave" {
		var ids []uint64
		e = wire.Walk(req, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 0 && f.Type != 2 {
				return fmt.Errorf("deck: invalid talent slots")
			}
			b := f.Value
			for len(b) > 0 {
				n, k := binary.Uvarint(b)
				if k <= 0 {
					return fmt.Errorf("deck: invalid packed talent slots")
				}
				ids = append(ids, n)
				if len(ids) > s.fieldSettingsDesign.TalentSlots {
					return fmt.Errorf("deck: too many talent slots")
				}
				b = b[k:]
			}
			return nil
		})
		if e != nil {
			return 0, nil, true, e
		}
		v.TalentIDs = ids
	} else {
		code = 372
		on, _, e := wire.Varint(req, 2)
		if e != nil || on > 1 {
			return 0, nil, true, fmt.Errorf("deck: invalid auto-recovery switch")
		}
		caster, _, e := wire.Varint(req, 3)
		if e != nil {
			return 0, nil, true, e
		}
		v.AutoRevive = on != 0
		v.Caster = caster
		body = wire.AppendVarint(nil, 1, on)
		body = wire.AppendVarint(body, 2, caster)
	}
	if e = s.validateFieldSettings(v, path == "/TalentSlotSave"); e != nil {
		return 0, nil, true, e
	}
	raw, e := json.Marshal(v)
	if e == nil {
		core, err := json.Marshal(s.state)
		if err != nil {
			return 0, nil, true, err
		}
		e = s.storage.SaveWithEntries("deck", core, []stateio.EntryMutation{{Bucket: "field_settings", Key: "state", Payload: raw}})
	}
	return code, body, true, e
}
