package player

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
)

const developmentSettingsVersion = 1

type InventorySlotCounts struct {
	Items            uint64 `json:"items"`
	Storage          uint64 `json:"storage"`
	Equipment        uint64 `json:"equipment"`
	EquipmentStorage uint64 `json:"equipment_storage"`
}

type inventorySlotSnapshot struct {
	Version string `json:"version"`
	InventorySlotCounts
}

type developmentSettings struct {
	Version   int `json:"version"`
	Inventory *struct {
		Unlimited bool `json:"unlimited"`
	} `json:"inventory"`
}

type inventorySlotReply struct {
	code int
	body []byte
}

// InventorySlots owns the four ordinary UserDBInfo capacity fields and the
// four matching expansion endpoints. The optional development settings file
// changes only the two backpack values advertised at login; purchased values
// remain durable and become visible again when the switch is disabled.
type InventorySlots struct {
	mu           sync.Mutex
	store        stateio.AtomicEntryStore
	design       *gamedata.InventorySlotDesign
	wallet       *Wallet
	state        inventorySlotSnapshot
	settingsPath string
	sessionID    string
	replies      map[string]inventorySlotReply
}

func OpenInventorySlots(store stateio.Store, design *gamedata.InventorySlotDesign, initial InventorySlotCounts, wallet *Wallet) (*InventorySlots, error) {
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok || design == nil || wallet == nil {
		return nil, errors.New("player: incomplete inventory slot configuration")
	}
	s := &InventorySlots{store: entries, design: design, wallet: wallet, state: inventorySlotSnapshot{
		Version: versionconfig.Protocol(), InventorySlotCounts: initial,
	}, replies: map[string]inventorySlotReply{}}
	data, found, err := entries.LoadEntry("items", "slots", "capacity")
	if err != nil {
		return nil, err
	}
	if found {
		if err := stateio.RequireExactJSONObject(data, "version", "items", "storage", "equipment", "equipment_storage"); err != nil {
			return nil, fmt.Errorf("player: incompatible inventory slot layout: %w", err)
		}
		if err := json.Unmarshal(data, &s.state); err != nil {
			return nil, fmt.Errorf("player: decode inventory slots: %w", err)
		}
	}
	if err := s.validateCounts(s.state.InventorySlotCounts); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *InventorySlots) validateCounts(counts InventorySlotCounts) error {
	if s.state.Version != versionconfig.Protocol() ||
		counts.Items < s.design.Items.Default || counts.Items > s.design.Items.Maximum ||
		counts.Storage < s.design.Storage.Default || counts.Storage > s.design.Storage.Maximum ||
		counts.Equipment < s.design.Equipment.Default || counts.Equipment > s.design.Equipment.Maximum ||
		counts.EquipmentStorage < s.design.EquipmentStorage.Default || counts.EquipmentStorage > s.design.EquipmentStorage.Maximum {
		return errors.New("player: invalid saved inventory slot counts")
	}
	return nil
}

func (s *InventorySlots) AttachDevelopmentSettings(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsPath = path
}

func (s *InventorySlots) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
	s.replies = map[string]inventorySlotReply{}
}

func (s *InventorySlots) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found, err := s.store.LoadEntry("items", "slots", "capacity")
	if err != nil || found {
		return err
	}
	return s.persistLocked(s.state)
}

func (s *InventorySlots) InventorySlotCounts() (InventorySlotCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := s.state.InventorySlotCounts
	unlimited, err := loadUnlimitedInventory(s.settingsPath)
	if err != nil {
		return InventorySlotCounts{}, err
	}
	if unlimited {
		counts.Items = s.design.Items.Maximum
		counts.Equipment = s.design.Equipment.Maximum
	}
	return counts, nil
}

func (s *InventorySlots) UserInventorySlots() (items, storage, equipment, equipmentStorage uint64, err error) {
	counts, err := s.InventorySlotCounts()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return counts.Items, counts.Storage, counts.Equipment, counts.EquipmentStorage, nil
}

func loadUnlimitedInventory(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("player: read development settings: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var settings developmentSettings
	if err := decoder.Decode(&settings); err != nil {
		return false, errors.New("player: malformed development settings")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false, errors.New("player: malformed development settings")
	}
	if settings.Version != developmentSettingsVersion || settings.Inventory == nil {
		return false, errors.New("player: unsupported development settings version")
	}
	return settings.Inventory.Unlimited, nil
}

func (s *InventorySlots) Handle(path string, request []byte) (int, []byte, bool, error) {
	var code int
	var current *uint64
	var rule gamedata.InventorySlotRule
	switch path {
	case "/InvenAddSlot":
		code, current, rule = 24, &s.state.Items, s.design.Items
	case "/StorageAddSlot":
		code, current, rule = 25, &s.state.Storage, s.design.Storage
	case "/EquipAddSlot":
		code, current, rule = 39, &s.state.Equipment, s.design.Equipment
	case "/EquipStorageAddSlot":
		code, current, rule = 82, &s.state.EquipmentStorage, s.design.EquipmentStorage
	default:
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("player: inventory slot request missing sequence")
	}
	count, found, err := wire.Varint(request, 2)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("player: inventory slot request invalid count")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%d", path, seq)
	if prior, ok := s.replies[key]; ok {
		return prior.code, append([]byte(nil), prior.body...), true, nil
	}
	if *current > rule.Maximum || count > rule.Maximum-*current {
		return 0, nil, true, errors.New("player: inventory slot expansion exceeds GameData maximum")
	}
	cost, err := inventorySlotPrice(rule, *current, count)
	if err != nil {
		return 0, nil, true, err
	}
	identity := fmt.Sprintf("inventory-slot:%s:%s:%d", path, s.sessionID, seq)
	if _, err := s.wallet.SpendGoldOnce(identity, cost); err != nil {
		return 0, nil, true, fmt.Errorf("player: inventory slot price: %w", err)
	}
	next := s.state
	switch path {
	case "/InvenAddSlot":
		next.Items += count
	case "/StorageAddSlot":
		next.Storage += count
	case "/EquipAddSlot":
		next.Equipment += count
	case "/EquipStorageAddSlot":
		next.EquipmentStorage += count
	}
	if err := s.persistLocked(next); err != nil {
		return 0, nil, true, err
	}
	s.replies[key] = inventorySlotReply{code: code}
	return code, nil, true, nil
}

func inventorySlotPrice(rule gamedata.InventorySlotRule, current, count uint64) (uint64, error) {
	if rule.PriceType != 4 || count == 0 || current < rule.Default || current > rule.Maximum || count > rule.Maximum-current {
		return 0, errors.New("player: invalid inventory slot price request")
	}
	var total uint64
	step := rule.BasePrice / 3
	for i := uint64(0); i < count; i++ {
		offset := current + i + 1 - rule.Default
		if step != 0 && offset > (math.MaxUint64-rule.BasePrice)/step {
			return 0, errors.New("player: inventory slot price overflow")
		}
		price := rule.BasePrice + step*offset
		if price > rule.MaxPrice {
			price = rule.MaxPrice
		}
		if math.MaxUint64-total < price {
			return 0, errors.New("player: inventory slot total price overflow")
		}
		total += price
	}
	return total, nil
}

func (s *InventorySlots) persistLocked(next inventorySlotSnapshot) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.store.SaveWithEntries("items", nil, []stateio.EntryMutation{{Bucket: "slots", Key: "capacity", Payload: data}}); err != nil {
		return fmt.Errorf("player: persist inventory slots: %w", err)
	}
	s.state = next
	return nil
}
