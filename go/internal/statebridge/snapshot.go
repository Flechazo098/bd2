// Package statebridge adapts the legacy JSON persistence files to the frozen
// cross-language protobuf contract. It never mutates the state directory.
package statebridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	statev1 "bd2server/gen/state/v1"
)

const (
	FormatVersion   = 1
	ClientVersion   = "2.34.13"
	GameDataVersion = "20260910162539"
)

var stateFiles = []string{
	"characters.json", "collection.json", "deck.json", "equipment.json",
	"items.json", "mail.json", "missions.json", "progress.json", "wallet.json",
}

// StateFiles returns the complete account-state filename set. The runtime
// transaction coordinator uses the same allow-list as validation/repair, so
// a newly added domain file cannot silently sit outside crash recovery.
func StateFiles() []string {
	return append([]string(nil), stateFiles...)
}

func CompleteStateAvailable(dir string) (bool, error) {
	dir = filepath.Clean(dir)
	found := 0
	for _, name := range stateFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.IsDir() {
			return false, fmt.Errorf("statebridge: %s is a directory", name)
		}
		found++
	}
	if found != 0 && found != len(stateFiles) {
		return false, fmt.Errorf("statebridge: incomplete state: %d of %d required files exist", found, len(stateFiles))
	}
	return found == len(stateFiles), nil
}

type pictorial struct {
	ID      uint64 `json:"id"`
	GroupID uint64 `json:"group_id"`
}

type item struct {
	InvenIndex uint64     `json:"inven_index"`
	ID         uint64     `json:"id"`
	Type       uint64     `json:"type"`
	Count      uint64     `json:"count"`
	KeepFlag   uint64     `json:"keep_flag"`
	TimeValue  uint64     `json:"time_value"`
	Pictorial  *pictorial `json:"pictorialbook"`
	SortID     uint64     `json:"sort_id"`
	UseCount   uint64     `json:"use_count"`
}

type character struct {
	InvenIndex              uint64      `json:"inven_index"`
	ID                      uint64      `json:"id"`
	HP                      uint64      `json:"hp"`
	Level                   uint64      `json:"level"`
	CostumeID               uint64      `json:"costume_id"`
	Exp                     uint64      `json:"exp"`
	UseCostume              uint64      `json:"use_costume"`
	TalentLevel             uint64      `json:"talent_level"`
	TalentExp               uint64      `json:"talent_exp"`
	SolidarityReward        uint64      `json:"solidarity_reward"`
	ExpiryTime              uint64      `json:"expiry_time"`
	ConnectPotentialCostume uint64      `json:"connect_potential_costume"`
	Pictorial               []pictorial `json:"pictorialbook"`
}

type costume struct {
	InvenIndex  uint64      `json:"inven_index"`
	ID          uint64      `json:"id"`
	Level       uint64      `json:"level"`
	UseChar     uint64      `json:"use_char"`
	SortID      uint64      `json:"sort_id"`
	PotentialID uint64      `json:"potential_id"`
	DesignID    uint64      `json:"design_id"`
	BurstLevel  uint64      `json:"burst_level"`
	TimeValue   uint64      `json:"time_value"`
	Pictorial   []pictorial `json:"pictorialbook"`
}

type progressDisk struct {
	Version  uint32 `json:"version"`
	Position struct {
		PackID   uint32 `json:"PackID"`
		Position struct {
			MapID          uint32 `json:"MapId"`
			PlayerPosition struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
				Z float64 `json:"z"`
			} `json:"PlayerPosition"`
			ColleaguePositions json.RawMessage `json:"ColleaguePositions"`
		} `json:"Position"`
		RawJSON string `json:"RawJSON"`
	} `json:"position"`
	Tutorials []uint32 `json:"tutorials"`
	Quests    map[string]struct {
		QuestID uint32   `json:"QuestID"`
		PackID  uint32   `json:"PackID"`
		Values  []uint32 `json:"Values"`
	} `json:"quests"`
	Cleared map[string]bool `json:"cleared_quests"`
}

type deckSlot struct {
	Character uint64 `json:"character_inven_index"`
	Costume   uint64 `json:"costume_inven_index"`
	Slot      uint64 `json:"slot"`
}

type deckDisk struct {
	Version         string            `json:"version"`
	Deck            []deckSlot        `json:"deck"`
	FieldDeck       []deckSlot        `json:"field_deck"`
	FieldControl    uint64            `json:"field_char_control_deck_type"`
	Waypoints       map[uint64]uint64 `json:"waypoints"`
	Costumes        map[uint64]uint64 `json:"costumes"`
	Packs           map[uint64]uint64 `json:"packs"`
	HighestPower    uint64            `json:"highest_total_battle_power"`
	PortraitCostume uint64            `json:"portrait_costume_id"`
	AutoRevive      uint64            `json:"auto_revive_catalyst"`
}

type inventoryDisk struct {
	Version    string              `json:"version"`
	NextIndex  uint64              `json:"next_index"`
	Items      []item              `json:"items"`
	Granted    map[string]bool     `json:"granted"`
	GrantItems map[string][]uint64 `json:"grant_items"`
}

type equipmentOption struct {
	GroupID uint64 `json:"group_id"`
	ID      uint64 `json:"id"`
}
type equipment struct {
	InvenIndex      uint64            `json:"inven_index"`
	ID              uint64            `json:"id"`
	Level           uint64            `json:"level"`
	UseChar         uint64            `json:"use_char"`
	KeepFlag        uint64            `json:"keep_flag"`
	LockFlag        uint64            `json:"lock_flag"`
	SortID          uint64            `json:"sort_id"`
	MainOption      []equipmentOption `json:"main_option"`
	SubOption       []equipmentOption `json:"sub_option"`
	PrivateOption   *equipmentOption  `json:"private_option"`
	Rank            []uint64          `json:"rank"`
	UpgradeAttempts uint64            `json:"upgrade_attempts"`
}
type equipmentDisk struct {
	Version   string            `json:"version"`
	NextIndex uint64            `json:"next_index"`
	Equipment []equipment       `json:"equipment"`
	Granted   map[string]uint64 `json:"granted"`
}
type charactersDisk struct {
	Version    string      `json:"version"`
	Characters []character `json:"characters"`
}

type gachaSelection struct {
	GroupID uint64 `json:"group_id"`
	Slot    uint64 `json:"slot"`
	ItemID  uint64 `json:"item_id"`
}
type gachaUser struct {
	GroupID              uint64 `json:"group_id"`
	Point                uint64 `json:"point"`
	TotalBuyCount        uint64 `json:"total_buy_count"`
	OneFreePickCount     uint64 `json:"one_free_pick_count"`
	OneCashPickCount     uint64 `json:"one_cash_pick_count"`
	TenFreePickCount     uint64 `json:"ten_free_pick_count"`
	TenCashPickCount     uint64 `json:"ten_cash_pick_count"`
	ExchangeItemCount    uint64 `json:"exchange_item_count"`
	ExchangeMileageCount uint64 `json:"exchange_mileage_count"`
}
type gachaFixed struct {
	FixedID   uint64 `json:"fixed_id"`
	Type      uint64 `json:"type"`
	Count     uint64 `json:"count"`
	ApplySort int32  `json:"apply_sort_id"`
}
type pointExchange struct {
	GroupID uint64 `json:"group_id"`
	Count   uint64 `json:"count"`
}
type costumeUpgrade struct {
	InvenIndex uint64 `json:"inven_index"`
	CostumeID  uint64 `json:"costume_id"`
	Before     uint64 `json:"before"`
	After      uint64 `json:"after"`
	SortID     uint64 `json:"sort_id"`
}
type costumeExchange struct {
	InvenIndex       uint64 `json:"inven_index"`
	OriginalItemType uint64 `json:"original_item_type"`
	OriginalItemID   uint64 `json:"original_item_id"`
	OriginalCount    uint64 `json:"original_count"`
	ExchangeItemType uint64 `json:"exchange_item_type"`
	ExchangeItemID   uint64 `json:"exchange_item_id"`
	ExchangeCount    uint64 `json:"exchange_count"`
	SortID           uint64 `json:"sort_id"`
}
type collectionGrant struct {
	CharacterIndices      []uint64          `json:"character_indices"`
	CostumeIndices        []uint64          `json:"costume_indices"`
	Upgrades              []costumeUpgrade  `json:"upgrades"`
	Exchanges             []costumeExchange `json:"exchanges"`
	ViewCostumeIDs        []uint64          `json:"view_costume_ids"`
	GachaGroupID          uint64            `json:"gacha_group_id"`
	GachaPoint            uint64            `json:"gacha_point"`
	GachaFixed            []gachaFixed      `json:"gacha_fixed"`
	SelectionApplySortIDs []uint64          `json:"selection_apply_sort_ids"`
}
type collectionDisk struct {
	Version             string                      `json:"version"`
	NextCharacterIndex  uint64                      `json:"next_character_index"`
	NextCostumeIndex    uint64                      `json:"next_costume_index"`
	LatestPreview       []uint64                    `json:"latest_preview"`
	PreviewEventIndex   uint64                      `json:"preview_event_index"`
	PreviewLocked       bool                        `json:"preview_locked"`
	Characters          []character                 `json:"characters"`
	Costumes            []costume                   `json:"costumes"`
	BaseCostumeLevels   map[string]uint64           `json:"base_costume_levels"`
	GachaSelections     map[string][]gachaSelection `json:"gacha_selections"`
	StepUpProgress      map[string]uint64           `json:"step_up_progress"`
	GachaUsers          map[string]gachaUser        `json:"gacha_users"`
	GachaFixed          map[string]gachaFixed       `json:"gacha_fixed"`
	GachaApplied        map[string]bool             `json:"gacha_applied"`
	GachaPointExchanges map[string]pointExchange    `json:"gacha_point_exchanges"`
	GachaCountCorrected bool                        `json:"gacha_count_corrected"`
	Grants              map[string]collectionGrant  `json:"grants"`
}

type walletDisk struct {
	Version                  string          `json:"version"`
	Gold                     uint64          `json:"gold"`
	FreeJewelry              uint64          `json:"free_jewelry"`
	Jewelry                  uint64          `json:"jewelry"`
	Mileage                  uint64          `json:"mileage"`
	HopePowder               uint64          `json:"hope_powder"`
	EquipMileage             uint64          `json:"equip_mileage"`
	EquipMileageExchangeGage uint64          `json:"equip_mileage_exchange_gage"`
	Granted                  map[string]bool `json:"granted"`
	Spent                    map[string]bool `json:"spent"`
}
type mailDisk struct {
	Version string   `json:"version"`
	Opened  []uint64 `json:"opened"`
}
type missionsDisk struct {
	Version   string            `json:"version"`
	Completed []string          `json:"completed"`
	Claimed   []string          `json:"claimed"`
	Progress  map[string]uint64 `json:"progress"`
}

// LoadSnapshot reads every state file before decoding any semantic DTO. Hash
// covers filenames, lengths, and original bytes in a fixed order.
func LoadSnapshot(dir string) (*statev1.Snapshot, [32]byte, error) {
	dir = filepath.Clean(dir)
	raw := make(map[string][]byte, len(stateFiles))
	hasher := sha256.New()
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, [32]byte{}, fmt.Errorf("statebridge: read %s: %w", name, err)
		}
		raw[name] = data
		_ = binary.Write(hasher, binary.LittleEndian, uint32(len(name)))
		_, _ = hasher.Write([]byte(name))
		_ = binary.Write(hasher, binary.LittleEndian, uint64(len(data)))
		_, _ = hasher.Write(data)
	}
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))

	var progress progressDisk
	var deck deckDisk
	var inventory inventoryDisk
	var equipment equipmentDisk
	var characters charactersDisk
	var collection collectionDisk
	var wallet walletDisk
	var mail mailDisk
	var missions missionsDisk
	for name, target := range map[string]any{
		"progress.json": &progress, "deck.json": &deck, "items.json": &inventory,
		"equipment.json": &equipment, "characters.json": &characters,
		"collection.json": &collection, "wallet.json": &wallet,
		"mail.json": &mail, "missions.json": &missions,
	} {
		decoder := json.NewDecoder(bytes.NewReader(raw[name]))
		if err := decoder.Decode(target); err != nil {
			return nil, [32]byte{}, fmt.Errorf("statebridge: decode %s: %w", name, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, [32]byte{}, fmt.Errorf("statebridge: trailing data in %s", name)
		}
	}
	snapshot, err := buildSnapshot(progress, deck, inventory, equipment, characters, collection, wallet, mail, missions)
	return snapshot, sum, err
}

func buildSnapshot(progress progressDisk, deck deckDisk, inventory inventoryDisk, equipment equipmentDisk, characters charactersDisk, collection collectionDisk, wallet walletDisk, mail mailDisk, missions missionsDisk) (*statev1.Snapshot, error) {
	versions := []string{deck.Version, inventory.Version, equipment.Version, characters.Version, collection.Version, wallet.Version, mail.Version, missions.Version}
	for _, version := range versions {
		if version != ClientVersion {
			return nil, fmt.Errorf("statebridge: incompatible client version %q", version)
		}
	}
	progressPB, err := progressProto(progress)
	if err != nil {
		return nil, err
	}
	return &statev1.Snapshot{
		FormatVersion: FormatVersion, ClientVersion: ClientVersion, GameDataVersion: GameDataVersion,
		Progress: progressPB, Deck: deckProto(deck), Inventory: inventoryProto(inventory),
		Equipment: equipmentProto(equipment), Characters: &statev1.CharacterState{ClientVersion: characters.Version, Characters: charactersProto(characters.Characters)},
		Collection: collectionProto(collection), Wallet: walletProto(wallet),
		Mail:     &statev1.MailState{ClientVersion: mail.Version, OpenedMailIds: append([]uint64(nil), mail.Opened...)},
		Missions: &statev1.MissionState{ClientVersion: missions.Version, Completed: append([]string(nil), missions.Completed...), Claimed: append([]string(nil), missions.Claimed...), Progress: namedCounts(missions.Progress)},
	}, nil
}

func progressProto(in progressDisk) (*statev1.Progress, error) {
	if in.Version != 2 {
		return nil, fmt.Errorf("statebridge: expected progress format 2, got %d", in.Version)
	}
	out := &statev1.Progress{SourceFormatVersion: in.Version, ClearedTutorialIds: append([]uint32(nil), in.Tutorials...)}
	out.Position = &statev1.Position{PackId: in.Position.PackID, MapId: in.Position.Position.MapID,
		Player:       &statev1.Vector3{X: in.Position.Position.PlayerPosition.X, Y: in.Position.Position.PlayerPosition.Y, Z: in.Position.Position.PlayerPosition.Z},
		OriginalJson: []byte(in.Position.RawJSON), ColleaguePositionsJson: append([]byte(nil), in.Position.Position.ColleaguePositions...)}
	for _, key := range sortedKeys(in.Quests) {
		quest := in.Quests[key]
		packID, questID, err := parseQuestKey(key)
		if err != nil {
			return nil, err
		}
		if packID != quest.PackID || questID != quest.QuestID {
			return nil, fmt.Errorf("statebridge: quest key %q does not match value %d:%d", key, quest.PackID, quest.QuestID)
		}
		out.Quests = append(out.Quests, &statev1.QuestProgress{Key: &statev1.QuestKey{PackId: quest.PackID, QuestId: quest.QuestID}, Values: append([]uint32(nil), quest.Values...)})
	}
	for _, key := range sortedKeys(in.Cleared) {
		if !in.Cleared[key] {
			return nil, fmt.Errorf("statebridge: cleared quest %q must be true", key)
		}
		pack, quest, err := parseQuestKey(key)
		if err != nil {
			return nil, err
		}
		out.ClearedQuests = append(out.ClearedQuests, &statev1.QuestKey{PackId: pack, QuestId: quest})
	}
	return out, nil
}

func parseQuestKey(key string) (uint32, uint32, error) {
	left, right, ok := strings.Cut(key, ":")
	if !ok {
		return 0, 0, fmt.Errorf("statebridge: malformed quest key %q", key)
	}
	pack, err1 := strconv.ParseUint(left, 10, 32)
	quest, err2 := strconv.ParseUint(right, 10, 32)
	if err1 != nil || err2 != nil || pack == 0 || quest == 0 {
		return 0, 0, fmt.Errorf("statebridge: malformed quest key %q", key)
	}
	return uint32(pack), uint32(quest), nil
}

func deckProto(in deckDisk) *statev1.Deck {
	return &statev1.Deck{ClientVersion: in.Version, Battle: deckSlots(in.Deck), Field: deckSlots(in.FieldDeck), FieldControlType: in.FieldControl,
		Waypoints: pairs(in.Waypoints), SelectedCostumes: pairs(in.Costumes), BoughtPacks: pairs(in.Packs), HighestBattlePower: in.HighestPower,
		PortraitCostumeId: in.PortraitCostume, AutoReviveCatalyst: in.AutoRevive}
}
func deckSlots(in []deckSlot) []*statev1.DeckSlot {
	out := make([]*statev1.DeckSlot, 0, len(in))
	for _, v := range in {
		out = append(out, &statev1.DeckSlot{CharacterIndex: v.Character, CostumeIndex: v.Costume, Slot: v.Slot})
	}
	return out
}
func pairs(in map[uint64]uint64) []*statev1.Pair {
	keys := make([]uint64, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]*statev1.Pair, 0, len(keys))
	for _, k := range keys {
		out = append(out, &statev1.Pair{Key: k, Value: in[k]})
	}
	return out
}

func inventoryProto(in inventoryDisk) *statev1.Inventory {
	out := &statev1.Inventory{ClientVersion: in.Version, NextIndex: in.NextIndex}
	for _, v := range in.Items {
		out.Items = append(out.Items, itemProto(v))
	}
	out.GrantedIdentities = sortedTrueKeys(in.Granted)
	for _, identity := range sortedKeys(in.GrantItems) {
		out.GrantItems = append(out.GrantItems, &statev1.IndexedGrant{Identity: identity, InventoryIndices: append([]uint64(nil), in.GrantItems[identity]...)})
	}
	return out
}
func itemProto(v item) *statev1.Item {
	out := &statev1.Item{InventoryIndex: v.InvenIndex, Id: v.ID, Type: v.Type, Count: v.Count, KeepFlag: v.KeepFlag, TimeValue: v.TimeValue, SortId: v.SortID, UseCount: v.UseCount}
	if v.Pictorial != nil {
		out.Pictorial = &statev1.Pictorial{Id: v.Pictorial.ID, GroupId: v.Pictorial.GroupID}
	}
	return out
}

func equipmentProto(in equipmentDisk) *statev1.EquipmentInventory {
	out := &statev1.EquipmentInventory{ClientVersion: in.Version, NextIndex: in.NextIndex}
	for _, v := range in.Equipment {
		out.Equipment = append(out.Equipment, equipmentItemProto(v))
	}
	for _, k := range sortedKeys(in.Granted) {
		out.Grants = append(out.Grants, &statev1.NamedIndex{Identity: k, InventoryIndex: in.Granted[k]})
	}
	return out
}
func equipmentItemProto(v equipment) *statev1.Equipment {
	out := &statev1.Equipment{InventoryIndex: v.InvenIndex, Id: v.ID, Level: v.Level, UseChar: v.UseChar, KeepFlag: v.KeepFlag, LockFlag: v.LockFlag, SortId: v.SortID, Ranks: append([]uint64(nil), v.Rank...), UpgradeAttempts: v.UpgradeAttempts}
	for _, o := range v.MainOption {
		out.MainOptions = append(out.MainOptions, &statev1.EquipmentOption{GroupId: o.GroupID, Id: o.ID})
	}
	for _, o := range v.SubOption {
		out.SubOptions = append(out.SubOptions, &statev1.EquipmentOption{GroupId: o.GroupID, Id: o.ID})
	}
	if v.PrivateOption != nil {
		out.PrivateOption = &statev1.EquipmentOption{GroupId: v.PrivateOption.GroupID, Id: v.PrivateOption.ID}
	}
	return out
}

func charactersProto(in []character) []*statev1.Character {
	out := make([]*statev1.Character, 0, len(in))
	for _, v := range in {
		p := &statev1.Character{InventoryIndex: v.InvenIndex, Id: v.ID, Hp: v.HP, Level: v.Level, CostumeId: v.CostumeID, Exp: v.Exp, UseCostume: v.UseCostume, TalentLevel: v.TalentLevel, TalentExp: v.TalentExp, SolidarityReward: v.SolidarityReward, ExpiryTime: v.ExpiryTime, ConnectPotentialCostume: v.ConnectPotentialCostume}
		for _, book := range v.Pictorial {
			p.Pictorial = append(p.Pictorial, &statev1.Pictorial{Id: book.ID, GroupId: book.GroupID})
		}
		out = append(out, p)
	}
	return out
}
func costumesProto(in []costume) []*statev1.Costume {
	out := make([]*statev1.Costume, 0, len(in))
	for _, v := range in {
		p := &statev1.Costume{InventoryIndex: v.InvenIndex, Id: v.ID, Level: v.Level, UseChar: v.UseChar, SortId: v.SortID, PotentialId: v.PotentialID, DesignId: v.DesignID, BurstLevel: v.BurstLevel, TimeValue: v.TimeValue}
		for _, book := range v.Pictorial {
			p.Pictorial = append(p.Pictorial, &statev1.Pictorial{Id: book.ID, GroupId: book.GroupID})
		}
		out = append(out, p)
	}
	return out
}

func collectionProto(in collectionDisk) *statev1.Collection {
	out := &statev1.Collection{ClientVersion: in.Version, NextCharacterIndex: in.NextCharacterIndex, NextCostumeIndex: in.NextCostumeIndex, LatestPreview: append([]uint64(nil), in.LatestPreview...), PreviewEventIndex: in.PreviewEventIndex, PreviewLocked: in.PreviewLocked, Characters: charactersProto(in.Characters), Costumes: costumesProto(in.Costumes), BaseCostumeLevels: namedCounts(in.BaseCostumeLevels), StepUpProgress: namedCounts(in.StepUpProgress), GachaApplied: sortedTrueKeys(in.GachaApplied), GachaCountCorrected: in.GachaCountCorrected}
	for _, k := range sortedKeys(in.GachaSelections) {
		entry := &statev1.NamedSelections{Identity: k}
		for _, v := range in.GachaSelections[k] {
			entry.Selections = append(entry.Selections, &statev1.GachaSelection{GroupId: v.GroupID, Slot: v.Slot, ItemId: v.ItemID})
		}
		out.GachaSelections = append(out.GachaSelections, entry)
	}
	for _, k := range sortedKeys(in.GachaUsers) {
		v := in.GachaUsers[k]
		out.GachaUsers = append(out.GachaUsers, &statev1.NamedGachaUser{Identity: k, User: &statev1.GachaUser{GroupId: v.GroupID, Point: v.Point, TotalBuyCount: v.TotalBuyCount, OneFreePickCount: v.OneFreePickCount, OneCashPickCount: v.OneCashPickCount, TenFreePickCount: v.TenFreePickCount, TenCashPickCount: v.TenCashPickCount, ExchangeItemCount: v.ExchangeItemCount, ExchangeMileageCount: v.ExchangeMileageCount}})
	}
	for _, k := range sortedKeys(in.GachaFixed) {
		out.GachaFixed = append(out.GachaFixed, &statev1.NamedGachaFixed{Identity: k, Fixed: fixedProto(in.GachaFixed[k])})
	}
	for _, k := range sortedKeys(in.GachaPointExchanges) {
		v := in.GachaPointExchanges[k]
		out.GachaPointExchanges = append(out.GachaPointExchanges, &statev1.NamedGachaPointExchange{Identity: k, Exchange: &statev1.GachaPointExchange{GroupId: v.GroupID, Count: v.Count}})
	}
	for _, k := range sortedKeys(in.Grants) {
		v := in.Grants[k]
		grant := &statev1.CollectionGrant{Identity: k, CharacterIndices: append([]uint64(nil), v.CharacterIndices...), CostumeIndices: append([]uint64(nil), v.CostumeIndices...), ViewCostumeIds: append([]uint64(nil), v.ViewCostumeIDs...), GachaGroupId: v.GachaGroupID, GachaPoint: v.GachaPoint, SelectionApplySortIds: append([]uint64(nil), v.SelectionApplySortIDs...)}
		for _, u := range v.Upgrades {
			grant.Upgrades = append(grant.Upgrades, &statev1.CostumeUpgrade{InventoryIndex: u.InvenIndex, CostumeId: u.CostumeID, Before: u.Before, After: u.After, SortId: u.SortID})
		}
		for _, e := range v.Exchanges {
			grant.Exchanges = append(grant.Exchanges, &statev1.CostumeExchange{InventoryIndex: e.InvenIndex, OriginalItemType: e.OriginalItemType, OriginalItemId: e.OriginalItemID, OriginalCount: e.OriginalCount, ExchangeItemType: e.ExchangeItemType, ExchangeItemId: e.ExchangeItemID, ExchangeCount: e.ExchangeCount, SortId: e.SortID})
		}
		for _, f := range v.GachaFixed {
			grant.GachaFixed = append(grant.GachaFixed, fixedProto(f))
		}
		out.Grants = append(out.Grants, grant)
	}
	return out
}
func fixedProto(v gachaFixed) *statev1.GachaFixed {
	return &statev1.GachaFixed{FixedId: v.FixedID, Type: v.Type, Count: v.Count, ApplySortId: v.ApplySort}
}
func walletProto(in walletDisk) *statev1.Wallet {
	return &statev1.Wallet{ClientVersion: in.Version, Gold: in.Gold, FreeJewelry: in.FreeJewelry, Jewelry: in.Jewelry, Mileage: in.Mileage, HopePowder: in.HopePowder, GrantedIdentities: sortedTrueKeys(in.Granted), SpentIdentities: sortedTrueKeys(in.Spent), EquipMileage: in.EquipMileage, EquipMileageExchangeGage: in.EquipMileageExchangeGage}
}
func namedCounts(in map[string]uint64) []*statev1.NamedCount {
	out := make([]*statev1.NamedCount, 0, len(in))
	for _, k := range sortedKeys(in) {
		out = append(out, &statev1.NamedCount{Identity: k, Value: in[k]})
	}
	return out
}

func sortedTrueKeys(in map[string]bool) []string {
	keys := make([]string, 0, len(in))
	for k, v := range in {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
func sortedKeys[V any](in map[string]V) []string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
