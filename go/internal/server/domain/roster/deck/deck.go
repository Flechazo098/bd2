// Package deck owns local deck, field-party, waypoint, and selected-costume
// state.  It stores typed JSON, never captured protobuf/base64 envelopes.
package deck

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
)

type DeckEntry struct {
	CharacterInvenIndex uint64 `json:"character_inven_index"`
	// CostumeInvenIndex is retained as the persisted Go/JSON name for the
	// development save format. On the wire DeckDBInfo field 2 is Position: a
	// zero-based battle-grid cell (or -1 while unassigned), not a costume
	// inventory index.
	CostumeInvenIndex uint64 `json:"costume_inven_index"`
	Slot              uint64 `json:"slot"`
}
type FieldEntry struct {
	Slot                uint64 `json:"slot"`
	CharacterInvenIndex uint64 `json:"character_inven_index"`
	CostumeInvenIndex   uint64 `json:"costume_inven_index"`
}
type Seed struct {
	Version                  string       `json:"version"`
	FieldDeck                []FieldEntry `json:"field_deck"`
	FieldCharControlDeckType uint64       `json:"field_char_control_deck_type"`
	AutoReviveCatalyst       uint64       `json:"auto_revive_catalyst,omitempty"`
}
type state struct {
	Version                  string              `json:"version"`
	Deck                     []DeckEntry         `json:"deck"`
	FieldDeck                []FieldEntry        `json:"field_deck"`
	FieldCharControlDeckType uint64              `json:"field_char_control_deck_type"`
	Waypoints                map[uint64][]uint64 `json:"waypoints"`
	Costumes                 map[uint64]uint64   `json:"costumes"`
	Packs                    map[uint64]uint64   `json:"packs"`
	HighestTotalBattlePower  uint64              `json:"highest_total_battle_power"`
	PortraitCostumeID        uint64              `json:"portrait_costume_id"`
	AutoReviveCatalyst       uint64              `json:"auto_revive_catalyst"`
}

// PortraitCostume exposes the current portrait without changing the frozen deck schema.
func (s *Store) PortraitCostume() uint64 {

	return s.state.PortraitCostumeID
}

type Store struct {
	autoRecovery        func(ctx command.Context, _ uint64, _ uint64, _ []uint64) (roster.AutoRecoveryResult, error)
	autoRecoveryAllowed func(ctx command.Context) (bool, error)
	fieldSettingsDesign *gamedata.FieldSettingsDesign
	fieldSettingsPack   func(command.Context) (int, error)

	storage         stateio.ScopedEntryStore
	state           state
	presets         map[uint64]Preset
	presetSlots     uint64
	presetDesign    gamedata.PresetDesign
	costumeSettings map[uint64]CostumeSetting
	wallet          *assets.Wallet
	characters      *roster.CharacterStore
	equipment       *assets.EquipmentInventory
	collection      *roster.CollectionStore

	replies        map[string]deckReply
	waypointDesign func(uint64) (gamedata.WaypointPack, error)
	waypointPack   func(ctx command.Context, _ uint64, _ bool) error
}

type deckReply struct {
	code int
	body []byte
}

func (s *Store) CurrentDeck() []DeckEntry {

	return append([]DeckEntry(nil), s.state.Deck...)
}

func LoadSeed(path string) (Seed, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Seed{}, fmt.Errorf("deck: read seed: %w", e)
	}
	var s Seed
	if e = json.Unmarshal(b, &s); e != nil {
		return Seed{}, fmt.Errorf("deck: decode seed: %w", e)
	}
	if e = s.validate(); e != nil {
		return Seed{}, e
	}
	return s, nil
}
func (s Seed) validate() error {
	if s.Version != versionconfig.State() {
		return errors.New("deck: wrong seed version")
	}
	return validField(s.FieldDeck)
}
func validField(entries []FieldEntry) error {
	if len(entries) == 0 || len(entries) > 5 {
		return errors.New("deck: invalid field deck size")
	}
	characters := map[uint64]bool{}
	costumes := map[uint64]bool{}
	sequences := map[uint64]bool{}
	for _, e := range entries {
		if e.Slot == 0 || e.Slot > 5 || e.CharacterInvenIndex == 0 ||
			characters[e.CharacterInvenIndex] || sequences[e.Slot] ||
			(e.CostumeInvenIndex != 0 && costumes[e.CostumeInvenIndex]) {
			return errors.New("deck: invalid field deck")
		}
		characters[e.CharacterInvenIndex] = true
		sequences[e.Slot] = true
		if e.CostumeInvenIndex != 0 {
			costumes[e.CostumeInvenIndex] = true
		}
	}
	for slot := uint64(1); slot <= uint64(len(entries)); slot++ {
		if !sequences[slot] {
			return errors.New("deck: field deck has a missing sequence")
		}
	}
	return nil
}

func validDeck(entries []DeckEntry) error {
	if len(entries) == 0 || len(entries) > 5 {
		return errors.New("deck: invalid battle deck size")
	}
	characters := map[uint64]bool{}
	positions := map[uint64]bool{}
	sequences := map[uint64]bool{}
	for _, entry := range entries {
		position := entry.CostumeInvenIndex
		unassigned := position == ^uint64(0) // int32 -1 sign-extends in protobuf varints.
		if entry.CharacterInvenIndex == 0 || (!unassigned && position > 11) || entry.Slot == 0 || entry.Slot > 5 ||
			characters[entry.CharacterInvenIndex] || (!unassigned && positions[position]) || sequences[entry.Slot] {
			return errors.New("deck: invalid battle deck")
		}
		characters[entry.CharacterInvenIndex] = true
		if !unassigned {
			positions[position] = true
		}
		sequences[entry.Slot] = true
	}
	return nil
}
func NewStore(seed Seed, designs ...gamedata.PresetDesign) (*Store, error) {
	if e := seed.validate(); e != nil {
		return nil, e
	}
	var design gamedata.PresetDesign
	if len(designs) > 1 {
		return nil, errors.New("deck: multiple preset designs")
	}
	if len(designs) == 1 {
		design = designs[0]
		if err := design.Validate(); err != nil {
			return nil, err
		}
	}
	return &Store{presetDesign: design, state: state{Version: versionconfig.State(), FieldDeck: append([]FieldEntry(nil), seed.FieldDeck...), FieldCharControlDeckType: seed.FieldCharControlDeckType, AutoReviveCatalyst: seed.AutoReviveCatalyst, Waypoints: map[uint64][]uint64{}, Costumes: map[uint64]uint64{}, Packs: map[uint64]uint64{}}, presets: map[uint64]Preset{}, presetSlots: design.BaseCount, costumeSettings: map[uint64]CostumeSetting{}, replies: map[string]deckReply{}}, nil
}
func OpenStore(ctx command.Context, storage stateio.Store, seed Seed, designs ...gamedata.PresetDesign) (*Store, error) {
	s, e := NewStore(seed, designs...)
	if e != nil {
		return nil, e
	}
	entries, ok := storage.(stateio.ScopedEntryStore)
	if storage == nil || !ok {
		return nil, errors.New("deck: nil storage")
	}
	s.storage = entries
	b, e := storage.Load(ctx.State, "deck")
	if e != nil {
		return nil, fmt.Errorf("deck: load state: %w", e)
	}
	if b == nil {
		if e = stateio.RequireNoEntries(entries, ctx.State, "deck", "presets", "preset_config", "costume_settings", "field_settings"); e != nil {
			return nil, fmt.Errorf("deck: invalid entry storage: %w", e)
		}
		return s, nil
	}
	if e = stateio.RequireExactJSONObject(b, "version", "deck", "field_deck", "field_char_control_deck_type", "waypoints", "costumes", "packs", "highest_total_battle_power", "portrait_costume_id", "auto_revive_catalyst"); e != nil {
		return nil, fmt.Errorf("deck: incompatible state layout: %w", e)
	}
	var loaded state
	if e = json.Unmarshal(b, &loaded); e != nil {
		return nil, fmt.Errorf("deck: malformed state: %w", e)
	}
	if loaded.Version != versionconfig.State() || (len(loaded.Deck) != 0 && validDeck(loaded.Deck) != nil) || validField(loaded.FieldDeck) != nil || loaded.Waypoints == nil || loaded.Costumes == nil || loaded.Packs == nil {
		return nil, errors.New("deck: invalid saved state")
	}
	if e = validWaypointState(loaded.Waypoints); e != nil {
		return nil, e
	}
	s.state = loaded
	if e = s.loadPresetEntries(ctx); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Store) EnsurePersisted(ctx command.Context) error {

	b, e := s.storage.Load(ctx.State, "deck")
	if e != nil {
		return e
	}
	if b != nil {
		return nil
	}
	return s.commit(ctx, clone(s.state))
}
func (s *Store) commit(ctx command.Context, next state) error {
	if s.storage != nil {
		b, e := json.MarshalIndent(next, "", "  ")
		if e != nil {
			return e
		}
		if e = s.storage.Save(ctx.State, "deck", append(b, '\n')); e != nil {
			return e
		}
	}
	s.state = next
	return nil
}
func clone(x state) state {
	y := x
	y.Deck = append([]DeckEntry(nil), x.Deck...)
	y.FieldDeck = append([]FieldEntry(nil), x.FieldDeck...)
	y.Waypoints = map[uint64][]uint64{}
	for k, v := range x.Waypoints {
		y.Waypoints[k] = append([]uint64(nil), v...)
	}
	y.Costumes = map[uint64]uint64{}
	maps.Copy(y.Costumes, x.Costumes)
	y.Packs = map[uint64]uint64{}
	maps.Copy(y.Packs, x.Packs)
	return y
}

func (s *Store) validateOwnedDeckLocked(ctx command.Context, entries []DeckEntry) error {
	if s.characters == nil {
		return nil
	}
	for _, entry := range entries {
		if _, found := s.characters.Find(ctx, entry.CharacterInvenIndex); !found {
			return fmt.Errorf("deck: battle deck references unknown character %d", entry.CharacterInvenIndex)
		}
	}
	return nil
}

func (s *Store) validateOwnedFieldDeckLocked(ctx command.Context, entries []FieldEntry) error {
	if s.characters == nil || s.collection == nil {
		return nil
	}
	for _, entry := range entries {
		character, found := s.characters.Find(ctx, entry.CharacterInvenIndex)
		if !found {
			return fmt.Errorf("deck: field deck references unknown character %d", entry.CharacterInvenIndex)
		}
		if roster.IsStoryCharacter(character) && !s.temporaryAllowed(ctx, character) {
			return fmt.Errorf("deck: field character unavailable in this pack")
		}
		if entry.CostumeInvenIndex == 0 {
			continue
		}
		if (roster.IsStoryCharacter(character) || roster.IsCharmCharacter(character)) && character.UseCostume == entry.CostumeInvenIndex {
			continue
		}
		costume, found := s.collection.CostumeByIndex(entry.CostumeInvenIndex)
		if !found {
			return fmt.Errorf("deck: field deck references unknown costume %d", entry.CostumeInvenIndex)
		}
		if costume.UseChar != entry.CharacterInvenIndex {
			return fmt.Errorf("deck: costume %d does not belong to character %d", entry.CostumeInvenIndex, entry.CharacterInvenIndex)
		}
	}
	return nil
}
