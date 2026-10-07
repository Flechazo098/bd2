package deck

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
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
func (s *Store) AttachFieldSettings(ctx command.Context, d *gamedata.FieldSettingsDesign) error {
	if d == nil || d.TalentSlots <= 0 {
		return fmt.Errorf("deck: invalid field settings design")
	}

	s.fieldSettingsDesign = d
	_, e := s.loadFieldSettings(ctx)
	return e
}
func (s *Store) AttachFieldSettingsPack(source func(command.Context) (int, error)) error {
	if source == nil {
		return fmt.Errorf("deck: nil field pack provider")
	}

	s.fieldSettingsPack = source
	return nil
}
func (s *Store) temporaryAllowed(ctx command.Context, c roster.Character) bool {
	if s.fieldSettingsDesign == nil || s.fieldSettingsPack == nil {
		return false
	}
	p, e := s.fieldSettingsPack(ctx)
	return e == nil && s.fieldSettingsDesign.CharacterTemporaryPack[c.ID] == p && p != 0
}
func (s *Store) loadFieldSettings(ctx command.Context) (fieldSettings, error) {
	d := s.fieldSettingsDesign
	if d == nil {
		return fieldSettings{}, fmt.Errorf("deck: field settings unavailable")
	}
	v := fieldSettings{TalentIDs: make([]uint64, d.TalentSlots)}
	raw, ok, e := s.storage.LoadEntry(ctx.State, "deck", "field_settings", "state")
	if e != nil || !ok {
		return v, e
	}
	if e = stateExactFieldSettings(raw, &v); e != nil {
		return v, e
	}
	if e = s.validateFieldSettings(ctx, v, false); e != nil {
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
func (s *Store) validateFieldSettings(ctx command.Context, v fieldSettings, live bool) error {
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
			if live && roster.CharacterExpired(c, time.Now()) {
				continue
			}
			if !roster.IsStoryCharacter(c) || s.temporaryAllowed(ctx, c) || !live && s.fieldSettingsDesign.CharacterTemporaryPack[c.ID] != 0 {
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
		c, ok := s.characters.Find(ctx, v.Caster)
		if !ok || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) || s.fieldSettingsDesign.CharacterTalentClass[c.ID] != 10 {
			return fmt.Errorf("deck: invalid automatic recovery caster")
		}
	}
	if v.AutoRevive && v.Caster == 0 {
		return fmt.Errorf("deck: automatic recovery needs caster")
	}
	return nil
}
func (s *Store) TalentCharacterIDs(ctx command.Context) ([]uint64, error) {

	v, e := s.loadFieldSettings(ctx)
	return s.projectTalentSlots(ctx, v.TalentIDs), e
}
func (s *Store) projectTalentSlots(ctx command.Context, ids []uint64) []uint64 {
	out := append([]uint64(nil), ids...)
	owned := map[uint64]bool{}
	charm := map[uint64]bool{}
	if s.characters != nil {
		for _, c := range s.characters.RawAll() {
			if roster.CharacterExpired(c, time.Now()) {
				continue
			}
			if roster.IsCharmCharacter(c) {
				owned[c.ID] = true
				charm[c.ID] = true
				continue
			}
			if !roster.IsStoryCharacter(c) || s.temporaryAllowed(ctx, c) {
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
				p, _ = s.fieldSettingsPack(ctx)
			}
			if p != s.fieldSettingsDesign.CharacterTemporaryPack[id] {
				out[i] = 0
			}
		}
	}
	return out
}
func (s *Store) AutoReviveSettings(ctx command.Context) (bool, uint64, error) {

	v, e := s.loadFieldSettings(ctx)
	return v.AutoRevive, v.Caster, e
}
func (s *Store) FieldControlType() uint64 {

	return s.state.FieldCharControlDeckType
}
