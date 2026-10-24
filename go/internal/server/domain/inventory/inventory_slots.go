package inventory

import (
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

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

// DevelopmentSettings is the per-account development overrides file. Each
// account switches its own entry from the console; grants read it as they happen.
type DevelopmentSettings struct {
	Version  int                           `json:"version"`
	Accounts map[string]DevelopmentAccount `json:"accounts"`
}

type DevelopmentAccount struct {
	Items DevelopmentItems `json:"items"`
}

type DevelopmentItems struct {
	// UnlimitedStacks lifts every item stack limit. When it is switched off,
	// stacks above their limit are cut back to it at the next login.
	UnlimitedStacks bool `json:"unlimited_stacks"`
}

var DevelopmentSettingsDocument = commonconfig.Document[DevelopmentSettings]{
	Name:     "dev-tools",
	FileName: "dev-tools.json",
	Defaults: func() DevelopmentSettings {
		return DevelopmentSettings{Version: 3, Accounts: map[string]DevelopmentAccount{}}
	},
	Base:   func() DevelopmentSettings { return DevelopmentSettings{} },
	Decode: commonconfig.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, RejectNull: true},
	Schema: commonconfig.Schema{Title: "开发覆盖", Sections: []commonconfig.Section{{ID: "format", Title: "格式", Fields: []commonconfig.Field{
		{Path: "version", Kind: commonconfig.KindInteger, Label: "格式版本", Required: true, Options: []commonconfig.Option{{Value: 3, Label: "3"}}},
		{Path: "accounts", Kind: commonconfig.KindMap, Label: "账号", Required: true},
		{Path: "accounts.*.items", Kind: commonconfig.KindMap, Label: "物品", Required: true},
		{Path: "accounts.*.items.unlimited_stacks", Kind: commonconfig.KindBool, Label: "不限物品堆叠", Required: true},
	}}}},
	Rules: func(settings DevelopmentSettings) commonconfig.Problems {
		var problems commonconfig.Problems
		if settings.Accounts == nil {
			problems.Add("accounts", commonconfig.RuleRequired, "is required")
		}
		if _, ok := settings.Accounts[""]; ok {
			problems.Add("accounts", commonconfig.RuleFormat, "account IDs must be nonempty")
		}
		return problems
	},
}

type inventorySlotReply struct {
	code int
	body []byte
}

// InventorySlots owns the four ordinary UserDBInfo capacity fields and the
// four matching expansion endpoints.
type InventorySlots struct {
	store  stateio.ScopedEntryStore
	design *gamedata.InventorySlotDesign
	wallet *Wallet
	state  inventorySlotSnapshot

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

func (s *InventorySlots) InventorySlotCounts(command.Context) (InventorySlotCounts, error) {
	return s.state.InventorySlotCounts, nil
}

func (s *InventorySlots) UserInventorySlots(ctx command.Context) (items, storage, equipment, equipmentStorage uint64, err error) {
	counts, err := s.InventorySlotCounts(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return counts.Items, counts.Storage, counts.Equipment, counts.EquipmentStorage, nil
}

// UnlimitedItemStacks reports the account's switch in the development
// settings file at path; a missing path or file means ordinary limits.
func UnlimitedItemStacks(path, accountID string) (bool, error) {
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
	settings, err := DevelopmentSettingsDocument.Parse(data)
	if err != nil {
		return false, fmt.Errorf("player: malformed development settings: %w", err)
	}
	return settings.Accounts[accountID].Items.UnlimitedStacks, nil
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
