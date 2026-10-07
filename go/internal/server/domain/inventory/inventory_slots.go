package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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
	store        stateio.ScopedEntryStore
	design       *gamedata.InventorySlotDesign
	wallet       *Wallet
	state        inventorySlotSnapshot
	settingsPath string

	replies map[string]inventorySlotReply
}

func OpenInventorySlots(ctx command.Context, store stateio.Store, design *gamedata.InventorySlotDesign, initial InventorySlotCounts, wallet *Wallet) (*InventorySlots, error) {
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok || design == nil || wallet == nil {
		return nil, errors.New("player: incomplete inventory slot configuration")
	}
	s := &InventorySlots{store: entries, design: design, wallet: wallet, state: inventorySlotSnapshot{
		Version: versionconfig.State(), InventorySlotCounts: initial,
	}, replies: map[string]inventorySlotReply{}}
	data, found, err := entries.LoadEntry(ctx.State, "items", "slots", "capacity")
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
	if s.state.Version != versionconfig.State() ||
		counts.Items < s.design.Items.Default || counts.Items > s.design.Items.Maximum ||
		counts.Storage < s.design.Storage.Default || counts.Storage > s.design.Storage.Maximum ||
		counts.Equipment < s.design.Equipment.Default || counts.Equipment > s.design.Equipment.Maximum ||
		counts.EquipmentStorage < s.design.EquipmentStorage.Default || counts.EquipmentStorage > s.design.EquipmentStorage.Maximum {
		return errors.New("player: invalid saved inventory slot counts")
	}
	return nil
}

func (s *InventorySlots) AttachDevelopmentSettings(path string) {

	s.settingsPath = path
}

func (s *InventorySlots) BeginLogin(ctx command.Context) {
	id := ctx.SessionID

	if id == "" {
		return
	}
	s.replies = map[string]inventorySlotReply{}
}

func (s *InventorySlots) EnsurePersisted(ctx command.Context) error {

	_, found, err := s.store.LoadEntry(ctx.State, "items", "slots", "capacity")
	if err != nil || found {
		return err
	}
	return s.persistLocked(ctx, s.state)
}

func (s *InventorySlots) InventorySlotCounts(ctx command.Context) (InventorySlotCounts, error) {

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

func (s *InventorySlots) UserInventorySlots(ctx command.Context) (items, storage, equipment, equipmentStorage uint64, err error) {
	counts, err := s.InventorySlotCounts(ctx)
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

func inventorySlotPrice(rule gamedata.InventorySlotRule, current, count uint64) (uint64, error) {
	if (rule.PriceType != 2 && rule.PriceType != 3 && rule.PriceType != 4) || count == 0 || current < rule.Default || current > rule.Maximum || count > rule.Maximum-current {
		return 0, errors.New("player: invalid inventory slot price request")
	}
	var total uint64
	step := rule.BasePrice / 3
	for i := range count {
		offset := current + i + 1 - rule.Default
		if step != 0 && offset > (math.MaxUint64-rule.BasePrice)/step {
			return 0, errors.New("player: inventory slot price overflow")
		}
		price := min(rule.BasePrice+step*offset, rule.MaxPrice)
		if math.MaxUint64-total < price {
			return 0, errors.New("player: inventory slot total price overflow")
		}
		total += price
	}
	return total, nil
}

func (s *InventorySlots) persistLocked(ctx command.Context, next inventorySlotSnapshot) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.store.SaveWithEntries(ctx.State, "items", nil, []stateio.EntryMutation{{Bucket: "slots", Key: "capacity", Payload: data}}); err != nil {
		return fmt.Errorf("player: persist inventory slots: %w", err)
	}
	s.state = next
	return nil
}
