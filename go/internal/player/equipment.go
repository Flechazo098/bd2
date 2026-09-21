package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"bd2server/internal/wire"
)

// Equipment is one server-owned equipment instance. The immutable definition
// (name, icon, slot and base stats) remains in the client's local GameData.
type Equipment struct {
	InvenIndex    uint64            `json:"inven_index"`
	ID            uint64            `json:"id"`
	Level         uint64            `json:"level"`
	UseChar       uint64            `json:"use_char,omitempty"`
	KeepFlag      uint64            `json:"keep_flag,omitempty"`
	LockFlag      uint64            `json:"lock_flag,omitempty"`
	SortID        uint64            `json:"sort_id,omitempty"`
	MainOption    []EquipmentOption `json:"main_option,omitempty"`
	SubOption     []EquipmentOption `json:"sub_option,omitempty"`
	PrivateOption *EquipmentOption  `json:"private_option,omitempty"`
	Rank          []uint64          `json:"rank,omitempty"`
}

// EquipmentOption is the exact EquipmentOptionTable composite key used by an
// equipment instance.  The client resolves the displayed stat curve locally.
type EquipmentOption struct {
	GroupID uint64 `json:"group_id"`
	ID      uint64 `json:"id"`
}

type equipmentSnapshot struct {
	Version   string            `json:"version"`
	NextIndex uint64            `json:"next_index"`
	Equipment []Equipment       `json:"equipment"`
	Granted   map[string]uint64 `json:"granted"`
}

// EquipmentInventory persists non-stackable equipment independently from
// ItemDBInfo inventory because the wire protocols are different types.
type EquipmentInventory struct {
	mu         sync.Mutex
	path       string
	owned      equipmentSnapshot
	characters *CharacterStore
}

func (s *EquipmentInventory) AttachCharacters(characters *CharacterStore) error {
	if characters == nil {
		return errors.New("player: nil character store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.characters = characters
	return nil
}

func OpenEquipmentInventory(path string) (*EquipmentInventory, error) {
	s := &EquipmentInventory{path: filepath.Clean(path), owned: equipmentSnapshot{
		Version: "2.34.13", NextIndex: 910000001, Granted: map[string]uint64{},
	}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.owned); err != nil {
		return nil, fmt.Errorf("player: decode equipment: %w", err)
	}
	if s.owned.Version != "2.34.13" || s.owned.NextIndex < 910000001 || s.owned.Granted == nil {
		return nil, errors.New("player: invalid saved equipment")
	}
	return s, nil
}

// GrantOnce returns the same instance on a retry, allowing QuestClear response
// retries without duplicating ownership.
func (s *EquipmentInventory) GrantOnce(identity string, equipmentID uint64) (Equipment, error) {
	if identity == "" || equipmentID == 0 {
		return Equipment{}, errors.New("player: invalid equipment grant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if index := s.owned.Granted[identity]; index != 0 {
		for _, current := range s.owned.Equipment {
			if current.InvenIndex == index {
				return current, nil
			}
		}
		return Equipment{}, errors.New("player: equipment grant index is missing")
	}
	return s.grantLocked(identity, Equipment{ID: equipmentID})
}

// GrantGeneratedOnce saves an independently generated gacha instance. Retry
// calls return the original rolls rather than creating a second copy.
func (s *EquipmentInventory) GrantGeneratedOnce(identity string, entry Equipment) (Equipment, error) {
	if identity == "" || entry.ID == 0 {
		return Equipment{}, errors.New("player: invalid generated equipment")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grantLocked(identity, entry)
}

func (s *EquipmentInventory) grantLocked(identity string, entry Equipment) (Equipment, error) {
	if index := s.owned.Granted[identity]; index != 0 {
		for _, current := range s.owned.Equipment {
			if current.InvenIndex == index {
				return current, nil
			}
		}
		return Equipment{}, errors.New("player: equipment grant index is missing")
	}
	entry.InvenIndex = s.owned.NextIndex
	next := equipmentSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex + 1,
		Equipment: append([]Equipment(nil), s.owned.Equipment...), Granted: make(map[string]uint64, len(s.owned.Granted)+1)}
	next.Equipment = append(next.Equipment, entry)
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	next.Granted[identity] = entry.InvenIndex
	data, err := json.Marshal(next)
	if err != nil {
		return Equipment{}, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Equipment{}, err
	}
	f, err := os.CreateTemp(dir, ".equipment-*.tmp")
	if err != nil {
		return Equipment{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return Equipment{}, fmt.Errorf("player: persist equipment: %w", err)
	}
	s.owned = next
	return entry, nil
}

func EquipmentWire(entry Equipment) []byte {
	base := wire.AppendVarint(nil, 1, entry.ID)
	if entry.Level != 0 {
		base = wire.AppendVarint(base, 2, entry.Level)
	}
	for _, option := range entry.MainOption {
		base = wire.AppendBytes(base, 3, equipmentOptionWire(option))
	}
	for _, option := range entry.SubOption {
		base = wire.AppendBytes(base, 4, equipmentOptionWire(option))
	}
	if entry.PrivateOption != nil {
		base = wire.AppendBytes(base, 5, equipmentOptionWire(*entry.PrivateOption))
	}
	for _, rank := range entry.Rank {
		if rank != 0 {
			base = wire.AppendVarint(base, 6, rank)
		}
	}
	var out []byte
	out = wire.AppendVarint(out, 1, entry.InvenIndex)
	if entry.UseChar != 0 {
		out = wire.AppendVarint(out, 2, entry.UseChar)
	}
	if entry.KeepFlag != 0 {
		out = wire.AppendVarint(out, 3, entry.KeepFlag)
	}
	if entry.LockFlag != 0 {
		out = wire.AppendVarint(out, 4, entry.LockFlag)
	}
	out = wire.AppendBytes(out, 5, base)
	if entry.SortID != 0 {
		out = wire.AppendVarint(out, 7, entry.SortID)
	}
	return out
}

func equipmentOptionWire(option EquipmentOption) []byte {
	out := wire.AppendVarint(nil, 1, option.GroupID)
	return wire.AppendVarint(out, 2, option.ID)
}

func (s *EquipmentInventory) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/EquipInfo" && path != "/EquipUse" {
		return 0, nil, false, nil
	}
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	if path == "/EquipUse" {
		return s.use(request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var response []byte
	for _, entry := range s.owned.Equipment {
		response = wire.AppendBytes(response, 1, EquipmentWire(entry))
	}
	return 34, response, true, nil
}

func (s *EquipmentInventory) All() []Equipment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Equipment(nil), s.owned.Equipment...)
}

// Granted returns a prior idempotent grant without creating an item.
func (s *EquipmentInventory) Granted(identity string) (Equipment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.owned.Granted[identity]
	if index == 0 {
		return Equipment{}, false
	}
	for _, entry := range s.owned.Equipment {
		if entry.InvenIndex == index {
			return entry, true
		}
	}
	return Equipment{}, false
}

func (s *EquipmentInventory) use(request []byte) (int, []byte, bool, error) {
	equipmentIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || equipmentIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing equipment")
	}
	characterIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: EquipUse missing character")
	}
	if s.characters == nil {
		return 0, nil, true, errors.New("player: EquipUse character store unavailable")
	}
	// Find computes account buffs and may query EquipmentInventory.All(). Do
	// not hold the equipment mutex while resolving the character's stats.
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown character %d", characterIndex)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := equipmentSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex,
		Equipment: append([]Equipment(nil), s.owned.Equipment...), Granted: make(map[string]uint64, len(s.owned.Granted))}
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	position := -1
	for i, current := range next.Equipment {
		if current.InvenIndex == equipmentIndex {
			position = i
			break
		}
	}
	if position < 0 {
		return 0, nil, true, fmt.Errorf("player: EquipUse unknown equipment %d", equipmentIndex)
	}
	next.Equipment[position].UseChar = characterIndex
	data, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".equipment-*.tmp")
	if err != nil {
		return 0, nil, true, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: persist equipment use: %w", err)
	}
	s.owned = next
	return 35, wire.AppendBytes(nil, 1, CharacterWire(character)), true, nil
}
