package statebridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	statev1 "bd2server/gen/state/v1"
	statev2 "bd2server/gen/state/v2"
)

const repairTransactionDir = ".statebridge-repair"

// RecoverInterruptedInstall restores all nine files from a prepared backup.
// It is safe to call before every load. A successful install removes the
// transaction directory, so the common path is read-only.
func RecoverInterruptedInstall(stateDir string) (bool, error) {
	stateDir = filepath.Clean(stateDir)
	tx := filepath.Join(stateDir, repairTransactionDir)
	if _, err := os.Stat(tx); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	backup := filepath.Join(tx, "backup")
	expectedHash, err := os.ReadFile(filepath.Join(tx, "prepared"))
	if err != nil {
		return false, fmt.Errorf("statebridge: incomplete repair transaction without prepared marker: %w", err)
	}
	_, backupHash, err := LoadSnapshot(backup)
	if err != nil {
		return false, fmt.Errorf("statebridge: repair backup unreadable: %w", err)
	}
	if !bytes.Equal(expectedHash, backupHash[:]) {
		return false, errors.New("statebridge: repair backup hash mismatch; refusing restore")
	}
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(backup, name))
		if err != nil {
			return false, fmt.Errorf("statebridge: recover backup %s: %w", name, err)
		}
		if err := writeAtomic(filepath.Join(stateDir, name), data); err != nil {
			return false, fmt.Errorf("statebridge: restore %s: %w", name, err)
		}
	}
	if err := os.RemoveAll(tx); err != nil {
		return false, err
	}
	return true, nil
}

// InstallRepair is the only write boundary for Haskell output. It prepares
// and fsyncs a complete generation before replacing any live file. If the
// process dies during replacement, RecoverInterruptedInstall rolls all files
// back on the next start.
func InstallRepair(stateDir string, result RepairResult) error {
	if !result.Changed {
		return nil
	}
	if result.Target == nil {
		return errors.New("statebridge: cannot install an empty repair target")
	}
	stateDir = filepath.Clean(stateDir)
	if recovered, err := RecoverInterruptedInstall(stateDir); err != nil {
		return err
	} else if recovered {
		return errors.New("statebridge: recovered an interrupted repair; rerun validation before installing")
	}
	_, currentHash, err := LoadSnapshot(stateDir)
	if err != nil {
		return err
	}
	if currentHash != result.SourceSHA256 {
		return errors.New("statebridge: state changed after repair calculation; refusing stale install")
	}
	files, err := projectV2JSON(result.Target)
	if err != nil {
		return err
	}
	tx := filepath.Join(stateDir, repairTransactionDir)
	backup, candidate := filepath.Join(tx, "backup"), filepath.Join(tx, "candidate")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tx)
		}
	}()
	for _, name := range stateFiles {
		original, err := os.ReadFile(filepath.Join(stateDir, name))
		if err != nil {
			return err
		}
		if err := originalKeysRepresented(original, files[name]); err != nil {
			return fmt.Errorf("statebridge: cannot safely project %s: %w", name, err)
		}
		if err := writeSynced(filepath.Join(backup, name), original); err != nil {
			return err
		}
		if err := writeSynced(filepath.Join(candidate, name), files[name]); err != nil {
			return err
		}
	}
	if _, _, err := LoadSnapshot(candidate); err != nil {
		return fmt.Errorf("statebridge: generated candidate is not loadable: %w", err)
	}
	if err := writeSynced(filepath.Join(tx, "prepared"), []byte(result.SourceSHA256[:])); err != nil {
		return err
	}
	cleanup = false // from this point recovery must retain the backups on error
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(candidate, name))
		if err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(stateDir, name), data); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(tx); err != nil {
		return err
	}
	return nil
}

// Reject an install if the JSON compatibility adapter would silently drop
// an unrecognized field. Arrays can intentionally shrink during repair; the
// overlapping entries still must preserve their object field structure.
func originalKeysRepresented(original, projected []byte) error {
	var oldValue, newValue any
	if err := json.Unmarshal(original, &oldValue); err != nil {
		return err
	}
	if err := json.Unmarshal(projected, &newValue); err != nil {
		return err
	}
	return requireObjectKeys(oldValue, newValue, "$")
}

func requireObjectKeys(oldValue, newValue any, path string) error {
	switch old := oldValue.(type) {
	case map[string]any:
		current, ok := newValue.(map[string]any)
		if !ok {
			return fmt.Errorf("%s changed object type", path)
		}
		for key, prior := range old {
			next, found := current[key]
			if !found {
				return fmt.Errorf("%s.%s is missing from projection", path, key)
			}
			if err := requireObjectKeys(prior, next, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		current, ok := newValue.([]any)
		if !ok {
			return fmt.Errorf("%s changed array type", path)
		}
		for index := 0; index < len(old) && index < len(current); index++ {
			if err := requireObjectKeys(old[index], current[index], fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".statebridge-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(temp, path)
}

func projectV2JSON(snapshot *statev2.Snapshot) (map[string][]byte, error) {
	if snapshot == nil || snapshot.Roster == nil || snapshot.Collection == nil {
		return nil, errors.New("statebridge: incomplete V2 snapshot")
	}
	base, acquired := splitCharacters(snapshot.Roster.Characters)
	files := map[string]any{
		"progress.json":   progressDiskFromProto(snapshot.Progress),
		"deck.json":       deckDiskFromProto(snapshot.Deck),
		"items.json":      inventoryDiskFromProto(snapshot.Inventory),
		"equipment.json":  equipmentDiskFromProto(snapshot.Equipment),
		"characters.json": charactersDisk{Version: snapshot.ClientVersion, Characters: base},
		"collection.json": collectionDiskFromProto(snapshot, acquired),
		"wallet.json":     walletDiskFromProto(snapshot.Wallet),
		"mail.json":       mailDisk{Version: snapshot.ClientVersion, Opened: append([]uint64(nil), snapshot.Mail.OpenedMailIds...)},
		"missions.json":   missionsDiskFromProto(snapshot.Missions),
	}
	encoded := make(map[string][]byte, len(files))
	for _, name := range stateFiles {
		data, err := json.MarshalIndent(files[name], "", "  ")
		if err != nil {
			return nil, fmt.Errorf("statebridge: encode %s: %w", name, err)
		}
		encoded[name] = append(data, '\n')
	}
	return encoded, nil
}

func progressDiskFromProto(in *statev1.Progress) progressDisk {
	var out progressDisk
	out.Version = in.SourceFormatVersion
	if in.Position != nil {
		out.Position.PackID, out.Position.Position.MapID = in.Position.PackId, in.Position.MapId
		if in.Position.Player != nil {
			out.Position.Position.PlayerPosition.X, out.Position.Position.PlayerPosition.Y, out.Position.Position.PlayerPosition.Z = in.Position.Player.X, in.Position.Player.Y, in.Position.Player.Z
		}
		out.Position.RawJSON = string(in.Position.OriginalJson)
		out.Position.Position.ColleaguePositions = append(json.RawMessage(nil), in.Position.ColleaguePositionsJson...)
	}
	out.Tutorials = append([]uint32(nil), in.ClearedTutorialIds...)
	out.Quests = make(map[string]struct {
		QuestID uint32   `json:"QuestID"`
		PackID  uint32   `json:"PackID"`
		Values  []uint32 `json:"Values"`
	}, len(in.Quests))
	for _, quest := range in.Quests {
		key := fmt.Sprintf("%d:%d", quest.Key.PackId, quest.Key.QuestId)
		out.Quests[key] = struct {
			QuestID uint32   `json:"QuestID"`
			PackID  uint32   `json:"PackID"`
			Values  []uint32 `json:"Values"`
		}{quest.Key.QuestId, quest.Key.PackId, append([]uint32(nil), quest.Values...)}
	}
	out.Cleared = make(map[string]bool, len(in.ClearedQuests))
	for _, key := range in.ClearedQuests {
		out.Cleared[fmt.Sprintf("%d:%d", key.PackId, key.QuestId)] = true
	}
	return out
}

func deckDiskFromProto(in *statev1.Deck) deckDisk {
	return deckDisk{Version: in.ClientVersion, Deck: slotsFromProto(in.Battle), FieldDeck: slotsFromProto(in.Field), FieldControl: in.FieldControlType,
		Waypoints: pairsFromProto(in.Waypoints), Costumes: pairsFromProto(in.SelectedCostumes), Packs: pairsFromProto(in.BoughtPacks), HighestPower: in.HighestBattlePower,
		PortraitCostume: in.PortraitCostumeId, AutoRevive: in.AutoReviveCatalyst}
}

func slotsFromProto(in []*statev1.DeckSlot) []deckSlot {
	out := make([]deckSlot, 0, len(in))
	for _, slot := range in {
		out = append(out, deckSlot{Character: slot.CharacterIndex, Costume: slot.CostumeIndex, Slot: slot.Slot})
	}
	return out
}

func pairsFromProto(in []*statev1.Pair) map[uint64]uint64 {
	out := make(map[uint64]uint64, len(in))
	for _, pair := range in {
		out[pair.Key] = pair.Value
	}
	return out
}

func inventoryDiskFromProto(in *statev1.Inventory) inventoryDisk {
	out := inventoryDisk{Version: in.ClientVersion, NextIndex: in.NextIndex, Granted: make(map[string]bool), GrantItems: make(map[string][]uint64)}
	for _, entry := range in.Items {
		out.Items = append(out.Items, itemFromProto(entry))
	}
	for _, identity := range in.GrantedIdentities {
		out.Granted[identity] = true
	}
	for _, grant := range in.GrantItems {
		out.GrantItems[grant.Identity] = append([]uint64(nil), grant.InventoryIndices...)
	}
	return out
}

func itemFromProto(in *statev1.Item) item {
	out := item{InvenIndex: in.InventoryIndex, ID: in.Id, Type: in.Type, Count: in.Count, KeepFlag: in.KeepFlag, TimeValue: in.TimeValue, SortID: in.SortId, UseCount: in.UseCount}
	if in.Pictorial != nil {
		out.Pictorial = &pictorial{ID: in.Pictorial.Id, GroupID: in.Pictorial.GroupId}
	}
	return out
}

func equipmentDiskFromProto(in *statev1.EquipmentInventory) equipmentDisk {
	out := equipmentDisk{Version: in.ClientVersion, NextIndex: in.NextIndex, Granted: make(map[string]uint64)}
	for _, entry := range in.Equipment {
		out.Equipment = append(out.Equipment, equipmentFromProto(entry))
	}
	for _, grant := range in.Grants {
		out.Granted[grant.Identity] = grant.InventoryIndex
	}
	return out
}

func equipmentFromProto(in *statev1.Equipment) equipment {
	out := equipment{InvenIndex: in.InventoryIndex, ID: in.Id, Level: in.Level, UseChar: in.UseChar, KeepFlag: in.KeepFlag, LockFlag: in.LockFlag, SortID: in.SortId, Rank: append([]uint64(nil), in.Ranks...)}
	for _, option := range in.MainOptions {
		out.MainOption = append(out.MainOption, equipmentOption{GroupID: option.GroupId, ID: option.Id})
	}
	for _, option := range in.SubOptions {
		out.SubOption = append(out.SubOption, equipmentOption{GroupID: option.GroupId, ID: option.Id})
	}
	if in.PrivateOption != nil {
		out.PrivateOption = &equipmentOption{GroupID: in.PrivateOption.GroupId, ID: in.PrivateOption.Id}
	}
	return out
}

func splitCharacters(records []*statev2.CharacterRecord) ([]character, []character) {
	var base, acquired []character
	for _, record := range records {
		entry := characterFromProto(record.Character)
		if record.Origin == statev2.CharacterOrigin_CHARACTER_ORIGIN_BASE {
			base = append(base, entry)
		} else {
			acquired = append(acquired, entry)
		}
	}
	return base, acquired
}

func characterFromProto(in *statev1.Character) character {
	out := character{InvenIndex: in.InventoryIndex, ID: in.Id, HP: in.Hp, Level: in.Level, CostumeID: in.CostumeId, Exp: in.Exp, UseCostume: in.UseCostume,
		TalentLevel: in.TalentLevel, TalentExp: in.TalentExp, SolidarityReward: in.SolidarityReward, ExpiryTime: in.ExpiryTime, ConnectPotentialCostume: in.ConnectPotentialCostume}
	for _, book := range in.Pictorial {
		out.Pictorial = append(out.Pictorial, pictorial{ID: book.Id, GroupID: book.GroupId})
	}
	return out
}

func costumeFromProto(in *statev1.Costume) costume {
	out := costume{InvenIndex: in.InventoryIndex, ID: in.Id, Level: in.Level, UseChar: in.UseChar, SortID: in.SortId, PotentialID: in.PotentialId, DesignID: in.DesignId, BurstLevel: in.BurstLevel, TimeValue: in.TimeValue}
	for _, book := range in.Pictorial {
		out.Pictorial = append(out.Pictorial, pictorial{ID: book.Id, GroupID: book.GroupId})
	}
	return out
}

func collectionDiskFromProto(snapshot *statev2.Snapshot, acquired []character) collectionDisk {
	in, roster := snapshot.Collection, snapshot.Roster
	out := collectionDisk{Version: snapshot.ClientVersion, NextCharacterIndex: roster.NextAcquiredCharacterIndex, NextCostumeIndex: roster.NextCostumeIndex,
		LatestPreview: append([]uint64(nil), in.LatestPreview...), PreviewEventIndex: in.PreviewEventIndex, PreviewLocked: in.PreviewLocked, Characters: acquired,
		BaseCostumeLevels: map[string]uint64{}, GachaSelections: map[string][]gachaSelection{}, StepUpProgress: map[string]uint64{}, GachaUsers: map[string]gachaUser{},
		GachaFixed: map[string]gachaFixed{}, GachaApplied: map[string]bool{}, GachaPointExchanges: map[string]pointExchange{}, Grants: map[string]collectionGrant{}, GachaCountCorrected: in.GachaCountCorrected}
	for _, entry := range roster.Costumes {
		out.Costumes = append(out.Costumes, costumeFromProto(entry))
	}
	for _, entry := range in.BaseCostumeLevels {
		out.BaseCostumeLevels[entry.Identity] = entry.Value
	}
	for _, entry := range in.StepUpProgress {
		out.StepUpProgress[entry.Identity] = entry.Value
	}
	for _, entry := range in.GachaApplied {
		out.GachaApplied[entry] = true
	}
	for _, entry := range in.GachaSelections {
		for _, selection := range entry.Selections {
			out.GachaSelections[entry.Identity] = append(out.GachaSelections[entry.Identity], gachaSelection{GroupID: selection.GroupId, Slot: selection.Slot, ItemID: selection.ItemId})
		}
	}
	for _, entry := range in.GachaUsers {
		user := entry.User
		out.GachaUsers[entry.Identity] = gachaUser{GroupID: user.GroupId, Point: user.Point, TotalBuyCount: user.TotalBuyCount, OneFreePickCount: user.OneFreePickCount, OneCashPickCount: user.OneCashPickCount, TenFreePickCount: user.TenFreePickCount, TenCashPickCount: user.TenCashPickCount, ExchangeItemCount: user.ExchangeItemCount, ExchangeMileageCount: user.ExchangeMileageCount}
	}
	for _, entry := range in.GachaFixed {
		fixed := entry.Fixed
		out.GachaFixed[entry.Identity] = gachaFixed{FixedID: fixed.FixedId, Type: fixed.Type, Count: fixed.Count, ApplySort: fixed.ApplySortId}
	}
	for _, entry := range in.GachaPointExchanges {
		exchange := entry.Exchange
		out.GachaPointExchanges[entry.Identity] = pointExchange{GroupID: exchange.GroupId, Count: exchange.Count}
	}
	for _, entry := range in.Grants {
		out.Grants[entry.Identity] = collectionGrantFromProto(entry)
	}
	return out
}

func collectionGrantFromProto(in *statev1.CollectionGrant) collectionGrant {
	out := collectionGrant{CharacterIndices: append([]uint64(nil), in.CharacterIndices...), CostumeIndices: append([]uint64(nil), in.CostumeIndices...), ViewCostumeIDs: append([]uint64(nil), in.ViewCostumeIds...), GachaGroupID: in.GachaGroupId, GachaPoint: in.GachaPoint, SelectionApplySortIDs: append([]uint64(nil), in.SelectionApplySortIds...)}
	for _, entry := range in.Upgrades {
		out.Upgrades = append(out.Upgrades, costumeUpgrade{InvenIndex: entry.InventoryIndex, CostumeID: entry.CostumeId, Before: entry.Before, After: entry.After, SortID: entry.SortId})
	}
	for _, entry := range in.Exchanges {
		out.Exchanges = append(out.Exchanges, costumeExchange{InvenIndex: entry.InventoryIndex, OriginalItemType: entry.OriginalItemType, OriginalItemID: entry.OriginalItemId, OriginalCount: entry.OriginalCount, ExchangeItemType: entry.ExchangeItemType, ExchangeItemID: entry.ExchangeItemId, ExchangeCount: entry.ExchangeCount, SortID: entry.SortId})
	}
	for _, entry := range in.GachaFixed {
		out.GachaFixed = append(out.GachaFixed, gachaFixed{FixedID: entry.FixedId, Type: entry.Type, Count: entry.Count, ApplySort: entry.ApplySortId})
	}
	return out
}

func walletDiskFromProto(in *statev1.Wallet) walletDisk {
	out := walletDisk{Version: in.ClientVersion, Gold: in.Gold, FreeJewelry: in.FreeJewelry, Jewelry: in.Jewelry, Mileage: in.Mileage, HopePowder: in.HopePowder, Granted: map[string]bool{}, Spent: map[string]bool{}}
	for _, value := range in.GrantedIdentities {
		out.Granted[value] = true
	}
	for _, value := range in.SpentIdentities {
		out.Spent[value] = true
	}
	return out
}

func missionsDiskFromProto(in *statev1.MissionState) missionsDisk {
	out := missionsDisk{Version: in.ClientVersion, Completed: append([]string(nil), in.Completed...), Claimed: append([]string(nil), in.Claimed...), Progress: map[string]uint64{}}
	for _, entry := range in.Progress {
		out.Progress[entry.Identity] = entry.Value
	}
	return out
}

// stableJSONKeys is retained as an assertion helper for tests and documents
// that all map-backed JSON domains are emitted deterministically by encoding/json.
func stableJSONKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
