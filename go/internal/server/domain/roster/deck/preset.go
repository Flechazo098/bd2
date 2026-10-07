package deck

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Preset struct {
	Name          string        `json:"name"`
	ResourceID    uint64        `json:"resource_id"`
	ResourceColor uint64        `json:"resource_color"`
	Slot          uint64        `json:"slot"`
	Decks         []PresetDeck  `json:"decks"`
	Blesses       []PresetBless `json:"blesses"`
}

type PresetDeck struct {
	Deck         DeckEntry             `json:"deck"`
	CostumeIndex uint64                `json:"costume_index"`
	Equipment    []PresetEquipmentItem `json:"equipment"`
	Team         uint64                `json:"team"`
}

type PresetEquipmentItem struct {
	Type  uint64 `json:"type"`
	Index uint64 `json:"index"`
}

type PresetBless struct {
	DeckType uint64   `json:"deck_type"`
	IDs      []uint64 `json:"ids"`
}

type CostumeSetting struct {
	CharacterIndex uint64               `json:"character_index"`
	Sequence       []CostumeSettingItem `json:"sequence"`
	BattleMode     uint64               `json:"battle_mode"`
	MonsterID      uint64               `json:"monster_id"`
}

type CostumeSettingItem struct {
	CostumeIndex int64  `json:"costume_index"`
	BurstLevel   uint64 `json:"burst_level"`
}

func (s *Store) AttachPresetRuntime(ctx command.Context, wallet *assets.Wallet, characters *roster.CharacterStore, equipment *assets.EquipmentInventory, collection *roster.CollectionStore) error {
	if wallet == nil || characters == nil || equipment == nil || collection == nil {
		return errors.New("deck: incomplete preset runtime")
	}

	s.wallet, s.characters, s.equipment, s.collection = wallet, characters, equipment, collection
	return s.validatePresetOwnershipLocked(ctx)
}

func (s *Store) BeginLogin(ctx command.Context) {
	id := ctx.SessionID

	if id == "" {
		return
	}
	s.replies = map[string]deckReply{}
}

func (s *Store) PresetSlotCount() uint64 {

	return s.presetSlots
}

func (s *Store) loadPresetEntries(ctx command.Context) error {
	rawConfig, found, err := s.storage.LoadEntry(ctx.State, "deck", "preset_config", "slots")
	if err != nil {
		return err
	}
	if found {
		if err := json.Unmarshal(rawConfig, &s.presetSlots); err != nil || s.presetSlots < s.presetDesign.BaseCount || s.presetSlots > s.presetDesign.Maximum {
			return errors.New("deck: invalid preset slot configuration")
		}
	}
	raw, err := s.storage.ListEntries(ctx.State, "deck", "presets")
	if err != nil {
		return err
	}
	for key, payload := range raw {
		slot, err := strconv.ParseUint(key, 10, 64)
		if err != nil || key != strconv.FormatUint(slot, 10) {
			return fmt.Errorf("deck: invalid preset key %q", key)
		}
		var preset Preset
		if err := json.Unmarshal(payload, &preset); err != nil || preset.Slot != slot {
			return fmt.Errorf("deck: invalid preset %q", key)
		}
		if err := s.validatePresetShape(preset, s.presetSlots); err != nil {
			return fmt.Errorf("deck: invalid preset %q: %w", key, err)
		}
		s.presets[slot] = preset
	}
	rawSettings, err := s.storage.ListEntries(ctx.State, "deck", "costume_settings")
	if err != nil {
		return err
	}
	for key, payload := range rawSettings {
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) {
			return fmt.Errorf("deck: invalid costume setting key %q", key)
		}
		var setting CostumeSetting
		if err := json.Unmarshal(payload, &setting); err != nil || setting.CharacterIndex != index {
			return fmt.Errorf("deck: invalid costume setting %q", key)
		}
		s.costumeSettings[index] = setting
	}
	return nil
}

func (s *Store) validatePresetOwnershipLocked(ctx command.Context) error {
	if s.characters == nil || s.collection == nil || s.equipment == nil {
		return nil
	}
	for _, preset := range s.presets {
		if err := s.validatePresetOwnedLocked(ctx, preset); err != nil {
			return fmt.Errorf("deck: saved preset %d: %w", preset.Slot, err)
		}
	}
	for _, setting := range s.costumeSettings {
		if _, found := s.characters.Find(ctx, setting.CharacterIndex); !found {
			return fmt.Errorf("deck: costume setting references unknown character %d", setting.CharacterIndex)
		}
		for _, item := range setting.Sequence {
			if item.CostumeIndex > 0 {
				if _, found := s.collection.CostumeByIndex(uint64(item.CostumeIndex)); !found {
					return fmt.Errorf("deck: costume setting references unknown costume %d", item.CostumeIndex)
				}
			}
		}
	}
	return nil
}

func (s *Store) validatePresetShape(p Preset, slotCount uint64) error {
	if p.Slot >= slotCount || (p.ResourceID != 0 && !s.presetDesign.Icons[p.ResourceID]) || p.ResourceColor > 5 || !validPresetName(p.Name) || len(p.Decks) > 5 {
		return errors.New("invalid metadata or deck count")
	}
	characters, positions, sequences := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
	for _, deck := range p.Decks {
		if deck.Deck.CharacterInvenIndex == 0 || deck.Deck.CostumeInvenIndex > 11 || deck.Deck.Slot == 0 || deck.Deck.Slot > 5 || deck.Team != 0 ||
			characters[deck.Deck.CharacterInvenIndex] || positions[deck.Deck.CostumeInvenIndex] || sequences[deck.Deck.Slot] {
			return errors.New("invalid deck entry")
		}
		characters[deck.Deck.CharacterInvenIndex] = true
		positions[deck.Deck.CostumeInvenIndex] = true
		sequences[deck.Deck.Slot] = true
		if len(deck.Equipment) != 5 {
			return errors.New("preset deck requires five equipment slots")
		}
		seen := map[uint64]bool{}
		for _, item := range deck.Equipment {
			if item.Type >= 5 || seen[item.Type] {
				return errors.New("invalid preset equipment slot")
			}
			seen[item.Type] = true
		}
	}
	return nil
}

func validPresetName(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 16 || strings.Contains(value, "<") || strings.Contains(value, ">") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Store) validatePresetOwnedLocked(ctx command.Context, p Preset) error {
	if err := s.validatePresetShape(p, s.presetSlots); err != nil {
		return err
	}
	ownedEquipment := make(map[uint64]assets.Equipment)
	for _, item := range s.equipment.All(ctx) {
		ownedEquipment[item.InvenIndex] = item
	}
	seenEquipment := map[uint64]bool{}
	for _, deck := range p.Decks {
		if _, found := s.characters.Find(ctx, deck.Deck.CharacterInvenIndex); !found {
			return fmt.Errorf("unknown character %d", deck.Deck.CharacterInvenIndex)
		}
		if deck.CostumeIndex != 0 {
			costume, found := s.collection.CostumeByIndex(deck.CostumeIndex)
			if !found || costume.UseChar != deck.Deck.CharacterInvenIndex {
				return fmt.Errorf("unknown costume %d", deck.CostumeIndex)
			}
		}
		for _, reference := range deck.Equipment {
			if reference.Index == 0 {
				continue
			}
			if seenEquipment[reference.Index] || ownedEquipment[reference.Index].InvenIndex == 0 {
				return fmt.Errorf("invalid or repeated equipment %d", reference.Index)
			}
			seenEquipment[reference.Index] = true
		}
		binding := assets.PresetEquipmentBinding{CharacterIndex: deck.Deck.CharacterInvenIndex, Equipment: make([]uint64, 5)}
		for _, reference := range deck.Equipment {
			binding.Equipment[reference.Type] = reference.Index
		}
		if err := s.equipment.ValidatePresetEquipment(ctx, []assets.PresetEquipmentBinding{binding}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) presetCacheKey(ctx command.Context, kind string, seq uint64) string {
	return kind + ":" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
}

func (s *Store) persistPresetLocked(ctx command.Context, p Preset) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	core, err := s.corePayloadLocked()
	if err != nil {
		return err
	}
	return s.storage.SaveWithEntries(ctx.State, "deck", core, []stateio.EntryMutation{{Bucket: "presets", Key: strconv.FormatUint(p.Slot, 10), Payload: payload}})
}

func (s *Store) corePayloadLocked() ([]byte, error) {
	payload, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func (s *Store) cachedReplyLocked(ctx command.Context, kind string, seq uint64) (deckReply, bool) {
	reply, found := s.replies[s.presetCacheKey(ctx, kind, seq)]
	if found {
		reply.body = append([]byte(nil), reply.body...)
	}
	return reply, found
}

func (s *Store) rememberReplyLocked(ctx command.Context, kind string, seq uint64, code int, body []byte) {
	s.replies[s.presetCacheKey(ctx, kind, seq)] = deckReply{code: code, body: append([]byte(nil), body...)}
}

func (s *Store) validateCostumeSettingLocked(ctx command.Context, setting CostumeSetting) error {
	if s.characters == nil || s.collection == nil {
		return errors.New("deck: costume setting runtime unavailable")
	}
	if setting.BattleMode != 0 || setting.MonsterID != 0 {
		return errors.New("deck: ordinary costume setting requires normal battle mode")
	}
	if _, found := s.characters.Find(ctx, setting.CharacterIndex); !found {
		return fmt.Errorf("deck: unknown costume setting character %d", setting.CharacterIndex)
	}
	for _, item := range setting.Sequence {
		if item.CostumeIndex <= 0 {
			if item.CostumeIndex != 0 && item.CostumeIndex != -1 {
				return fmt.Errorf("deck: invalid costume setting sentinel %d", item.CostumeIndex)
			}
			continue
		}
		costume, found := s.collection.CostumeByIndex(uint64(item.CostumeIndex))
		if !found || costume.UseChar != setting.CharacterIndex {
			return fmt.Errorf("deck: costume %d does not belong to character %d", item.CostumeIndex, setting.CharacterIndex)
		}
		if item.BurstLevel > costume.BurstLevel {
			return fmt.Errorf("deck: costume %d burst level exceeds owned level", item.CostumeIndex)
		}
	}
	return nil
}
