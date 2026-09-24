// Package deck owns local deck, field-party, waypoint, and selected-costume
// state.  It stores typed JSON, never captured protobuf/base64 envelopes.
package deck

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
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
	Version                  string            `json:"version"`
	Deck                     []DeckEntry       `json:"deck"`
	FieldDeck                []FieldEntry      `json:"field_deck"`
	FieldCharControlDeckType uint64            `json:"field_char_control_deck_type"`
	Waypoints                map[uint64]uint64 `json:"waypoints"`
	Costumes                 map[uint64]uint64 `json:"costumes"`
	Packs                    map[uint64]uint64 `json:"packs"`
	HighestTotalBattlePower  uint64            `json:"highest_total_battle_power"`
	PortraitCostumeID        uint64            `json:"portrait_costume_id"`
	AutoReviveCatalyst       uint64            `json:"auto_revive_catalyst"`
}
type Store struct {
	mu              sync.RWMutex
	storage         stateio.AtomicEntryStore
	state           state
	presets         map[uint64]Preset
	presetSlots     uint64
	costumeSettings map[uint64]CostumeSetting
	wallet          *player.Wallet
	characters      *player.CharacterStore
	equipment       *player.EquipmentInventory
	collection      *player.CollectionStore
	sessionID       string
	replies         map[string]deckReply
}

type deckReply struct {
	code int
	body []byte
}

func (s *Store) CurrentDeck() []DeckEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	if s.Version != versionconfig.Protocol() {
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
func NewStore(seed Seed) (*Store, error) {
	if e := seed.validate(); e != nil {
		return nil, e
	}
	return &Store{state: state{Version: versionconfig.Protocol(), FieldDeck: append([]FieldEntry(nil), seed.FieldDeck...), FieldCharControlDeckType: seed.FieldCharControlDeckType, AutoReviveCatalyst: seed.AutoReviveCatalyst, Waypoints: map[uint64]uint64{}, Costumes: map[uint64]uint64{}, Packs: map[uint64]uint64{}}, presets: map[uint64]Preset{}, presetSlots: presetBaseCount, costumeSettings: map[uint64]CostumeSetting{}, replies: map[string]deckReply{}}, nil
}
func OpenStore(storage stateio.Store, seed Seed) (*Store, error) {
	s, e := NewStore(seed)
	if e != nil {
		return nil, e
	}
	entries, ok := storage.(stateio.AtomicEntryStore)
	if storage == nil || !ok {
		return nil, errors.New("deck: nil storage")
	}
	s.storage = entries
	b, e := storage.Load("deck")
	if e != nil {
		return nil, fmt.Errorf("deck: load state: %w", e)
	}
	if b == nil {
		if e = stateio.RequireNoEntries(entries, "deck", "presets", "preset_config", "costume_settings"); e != nil {
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
	if loaded.Version != versionconfig.Protocol() || (len(loaded.Deck) != 0 && validDeck(loaded.Deck) != nil) || validField(loaded.FieldDeck) != nil || loaded.Waypoints == nil || loaded.Costumes == nil || loaded.Packs == nil {
		return nil, errors.New("deck: invalid saved state")
	}
	s.state = loaded
	if e = s.loadPresetEntries(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Store) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, e := s.storage.Load("deck")
	if e != nil {
		return e
	}
	if b != nil {
		return nil
	}
	return s.commit(clone(s.state))
}
func (s *Store) commit(next state) error {
	if s.storage != nil {
		b, e := json.MarshalIndent(next, "", "  ")
		if e != nil {
			return e
		}
		if e = s.storage.Save("deck", append(b, '\n')); e != nil {
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
	y.Waypoints = map[uint64]uint64{}
	for k, v := range x.Waypoints {
		y.Waypoints[k] = v
	}
	y.Costumes = map[uint64]uint64{}
	for k, v := range x.Costumes {
		y.Costumes[k] = v
	}
	y.Packs = map[uint64]uint64{}
	for k, v := range x.Packs {
		y.Packs[k] = v
	}
	return y
}
func checkSeq(req []byte) error {
	v, ok, e := wire.Varint(req, 1)
	if e != nil || !ok || v == 0 {
		return errors.New("deck: invalid request sequence")
	}
	return nil
}
func triples(req []byte) ([]DeckEntry, error) {
	var out []DeckEntry
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("deck: deck field")
		}
		a, aok, e := wire.Varint(f.Value, 1)
		if e != nil || !aok || a == 0 {
			return errors.New("deck: deck character")
		}
		position, _, e := wire.Varint(f.Value, 2)
		if e != nil || (position > 11 && position != ^uint64(0)) {
			return errors.New("deck: invalid deck position")
		}
		sequence, sequenceOK, e := wire.Varint(f.Value, 3)
		if e != nil || !sequenceOK || sequence == 0 || sequence > 5 {
			return errors.New("deck: invalid deck sequence")
		}
		out = append(out, DeckEntry{CharacterInvenIndex: a, CostumeInvenIndex: position, Slot: sequence})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, errors.New("deck: empty deck")
	}
	if e := validDeck(out); e != nil {
		return nil, e
	}
	return out, nil
}
func fieldEntries(req []byte) ([]FieldEntry, error) {
	var out []FieldEntry
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return errors.New("deck: field deck entry is not a message")
		}
		slot, slotOK, err := wire.Varint(f.Value, 1)
		if err != nil || !slotOK || slot == 0 {
			return errors.New("deck: invalid field deck slot")
		}
		character, characterOK, err := wire.Varint(f.Value, 2)
		if err != nil || !characterOK || character == 0 {
			return errors.New("deck: invalid field deck character")
		}
		costume, _, err := wire.Varint(f.Value, 3)
		if err != nil {
			return errors.New("deck: invalid field deck costume")
		}
		out = append(out, FieldEntry{Slot: slot, CharacterInvenIndex: character, CostumeInvenIndex: costume})
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(out) == 0 {
		return nil, errors.New("deck: empty field deck")
	}
	return out, validField(out)
}

func (s *Store) validateOwnedDeckLocked(entries []DeckEntry) error {
	if s.characters == nil {
		return nil
	}
	for _, entry := range entries {
		if _, found := s.characters.Find(entry.CharacterInvenIndex); !found {
			return fmt.Errorf("deck: battle deck references unknown character %d", entry.CharacterInvenIndex)
		}
	}
	return nil
}

func (s *Store) validateOwnedFieldDeckLocked(entries []FieldEntry) error {
	if s.characters == nil || s.collection == nil {
		return nil
	}
	for _, entry := range entries {
		if _, found := s.characters.Find(entry.CharacterInvenIndex); !found {
			return fmt.Errorf("deck: field deck references unknown character %d", entry.CharacterInvenIndex)
		}
		if entry.CostumeInvenIndex == 0 {
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

// Handle implements session.Handler. Every mutation validates its complete
// typed request before committing a replacement JSON state.
func (s *Store) Handle(path string, req []byte) (int, []byte, bool, error) {
	switch path {
	case "/PresetInfo":
		return s.handlePresetInfo(req)
	case "/PresetSave":
		return s.handlePresetSave(req)
	case "/PresetAddSlot":
		return s.handlePresetAddSlot(req)
	case "/PresetInfoChange":
		return s.handlePresetInfoChange(req)
	case "/PresetDelete":
		return s.handlePresetDelete(req)
	case "/PresetUse":
		return s.handlePresetUse(req)
	case "/DeckCostumeSettingInfo":
		return s.handleCostumeSettingInfo(req)
	case "/DeckCostumeSettingSave":
		return s.handleCostumeSettingSave(req)
	case "/DeckInfo":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		slog.Info("team trace: deliver saved battle deck", "deck", s.state.Deck)
		return 8, encodeDeck(s.state.Deck), true, nil
	case "/FieldDeckInfo":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return 273, encodeField(s.state.FieldDeck), true, nil
	case "/DeckCharAutoRevive":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		caster, _, e := wire.Varint(req, 2)
		if e != nil {
			return 0, nil, true, fmt.Errorf("deck: invalid auto-revive caster: %w", e)
		}
		if caster != 0 {
			// The observed story flow has no caster and only refreshes the
			// formation. A nonzero caster would require authoritative revive
			// targets, talent experience, and catalyst consumption.
			return 0, nil, true, fmt.Errorf("deck: auto-revive caster %d is not verified", caster)
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		if len(s.state.Deck) == 0 {
			return 0, nil, true, errors.New("deck: auto-revive has no current battle deck")
		}
		var response []byte
		for _, entry := range s.state.Deck {
			deck := wire.AppendVarint(nil, 1, entry.CharacterInvenIndex)
			if entry.CostumeInvenIndex != 0 {
				deck = wire.AppendVarint(deck, 2, entry.CostumeInvenIndex)
			}
			deck = wire.AppendVarint(deck, 3, entry.Slot)
			response = wire.AppendBytes(response, 1, deck)
		}
		for _, entry := range s.state.FieldDeck {
			field := wire.AppendVarint(nil, 1, entry.Slot)
			field = wire.AppendVarint(field, 2, entry.CharacterInvenIndex)
			if entry.CostumeInvenIndex != 0 {
				field = wire.AppendVarint(field, 3, entry.CostumeInvenIndex)
			}
			response = wire.AppendBytes(response, 2, field)
		}
		response = wire.AppendVarint(response, 4, 2) // Define_AutoReviveCharType.CHANGE.
		if s.state.AutoReviveCatalyst != 0 {
			response = wire.AppendVarint(response, 7, s.state.AutoReviveCatalyst)
		}
		return 373, response, true, nil
	case "/WaypointInfo":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return 31, encodeWaypoints(s.state.Waypoints), true, nil
	case "/DeckSave":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		x, e := triples(req)
		if e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e = s.validateOwnedDeckLocked(x); e != nil {
			return 0, nil, true, e
		}
		seq, _, _ := wire.Varint(req, 1)
		slog.Info("team trace: client requested battle deck replacement", "seq", seq, "before", s.state.Deck, "after", x)
		n := clone(s.state)
		n.Deck = x
		e = s.commit(n)
		if e != nil {
			slog.Error("team trace: deck replacement failed", "seq", seq, "error", e)
		}
		return 10, nil, true, e
	case "/FieldDeckSave":
		if e := checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		x, e := fieldEntries(req)
		if e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e = s.validateOwnedFieldDeckLocked(x); e != nil {
			return 0, nil, true, e
		}
		n := clone(s.state)
		n.FieldDeck = x
		e = s.commit(n)
		return 274, nil, true, e
	case "/SaveFieldCharControlDeckType":
		// Define_FieldCharControllDeckType is a proto3 enum whose valid values
		// are BATTLE=0, FIELD=1 and STORY=2. BATTLE is the protobuf default, so
		// the generated client deliberately omits field 2 when it switches out
		// of story mode after the final quest. An absent field is therefore a
		// real value 0, not a malformed request.
		v, _, e := wire.Varint(req, 2)
		if e != nil || v > 2 {
			return 0, nil, true, errors.New("deck: invalid field control type")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		n.FieldCharControlDeckType = v
		e = s.commit(n)
		return 288, nil, true, e
	case "/WaypointSave":
		pack, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || pack == 0 {
			return 0, nil, true, errors.New("deck: invalid waypoint pack")
		}
		way, ok, e := wire.Varint(req, 3)
		if e != nil || !ok || way == 0 {
			return 0, nil, true, errors.New("deck: invalid waypoint")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		n.Waypoints[pack] = way
		e = s.commit(n)
		return 32, nil, true, e
	case "/CostumeUse":
		raw, ok, e := wire.Bytes(req, 2)
		if e != nil || !ok {
			return 0, nil, true, errors.New("deck: invalid costume use")
		}
		cost, ok, e := wire.Varint(raw, 1)
		if e != nil || !ok || cost == 0 {
			return 0, nil, true, errors.New("deck: invalid costume")
		}
		char, ok, e := wire.Varint(raw, 2)
		if e != nil || !ok || char == 0 {
			return 0, nil, true, errors.New("deck: invalid costume character")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		n.Costumes[char] = cost
		e = s.commit(n)
		return 41, nil, true, e
	case "/PackBuy":
		pack, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || pack == 0 {
			return 0, nil, true, errors.New("deck: invalid pack")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		// Purchasing the story pack is idempotent. Login/bootstrap may repeat
		// the request after a client restart before all local state is restored.
		n.Packs[pack] = 1
		e = s.commit(n)
		if e != nil {
			return 0, nil, true, e
		}
		packInfo := wire.AppendVarint(nil, 1, pack)
		packInfo = wire.AppendVarint(packInfo, 8, n.Packs[pack])
		response := wire.AppendBytes(nil, 1, packInfo)
		// Starter pack purchase grants the local story-pack entitlement and
		// starter currency. These are semantic RewardDBInfoBundle fields, not
		// recorded response bytes.
		entitlement := wire.AppendVarint(nil, 2, 1)
		entitlement = wire.AppendVarint(entitlement, 3, 19)
		entitlement = wire.AppendVarint(entitlement, 4, 1)
		entitlement = wire.AppendVarint(entitlement, 8, ^uint64(0)-32400000+1)
		currency := wire.AppendVarint(nil, 3, 12)
		currency = wire.AppendVarint(currency, 4, 200)
		tracking := wire.AppendVarint(nil, 2, 1)
		tracking = wire.AppendVarint(tracking, 3, 19)
		tracking = wire.AppendVarint(tracking, 4, 1)
		bundle := wire.AppendBytes(nil, 1, entitlement)
		bundle = wire.AppendBytes(bundle, 1, currency)
		bundle = wire.AppendBytes(bundle, 6, tracking)
		return 6, wire.AppendBytes(response, 2, bundle), true, nil
	case "/SaveTotalBattlePower":
		power, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || power == 0 {
			return 0, nil, true, errors.New("deck: invalid total battle power")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		if power > n.HighestTotalBattlePower {
			n.HighestTotalBattlePower = power
		}
		if e = s.commit(n); e != nil {
			return 0, nil, true, e
		}
		return 258, wire.AppendVarint(nil, 1, n.HighestTotalBattlePower), true, nil
	case "/UserPortraitChange":
		costumeID, ok, e := wire.Varint(req, 2)
		if e != nil || !ok || costumeID == 0 {
			return 0, nil, true, errors.New("deck: invalid portrait costume")
		}
		if e = checkSeq(req); e != nil {
			return 0, nil, true, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n := clone(s.state)
		n.PortraitCostumeID = costumeID
		if e = s.commit(n); e != nil {
			return 0, nil, true, e
		}
		return 75, wire.AppendVarint(nil, 1, costumeID), true, nil
	}
	return 0, nil, false, nil
}
func encodeDeck(xs []DeckEntry) []byte {
	var b []byte
	for _, x := range xs {
		v := wire.AppendVarint(nil, 1, x.CharacterInvenIndex)
		v = wire.AppendVarint(v, 2, x.CostumeInvenIndex)
		v = wire.AppendVarint(v, 3, x.Slot)
		b = wire.AppendBytes(b, 1, v)
	}
	return b
}
func encodeField(xs []FieldEntry) []byte {
	var b []byte
	for _, x := range xs {
		v := wire.AppendVarint(nil, 1, x.Slot)
		v = wire.AppendVarint(v, 2, x.CharacterInvenIndex)
		v = wire.AppendVarint(v, 3, x.CostumeInvenIndex)
		b = wire.AppendBytes(b, 1, v)
	}
	return b
}
func encodeWaypoints(xs map[uint64]uint64) []byte {
	keys := make([]uint64, 0, len(xs))
	for k := range xs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var b []byte
	for _, k := range keys {
		v := wire.AppendVarint(nil, 1, k)
		v = wire.AppendVarint(v, 2, xs[k])
		b = wire.AppendBytes(b, 1, v)
	}
	return b
}
