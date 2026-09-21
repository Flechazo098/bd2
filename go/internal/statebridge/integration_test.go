package statebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	controlv1 "bd2server/gen/state/control/v1"
	controlv2 "bd2server/gen/state/control/v2"
	statev1 "bd2server/gen/state/v1"
	statev2 "bd2server/gen/state/v2"
	"bd2server/internal/gacha"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"google.golang.org/protobuf/proto"
)

// BD2_STATE_TOOL enables an explicit cross-language test without making
// ordinary Go tests depend on a locally compiled Haskell executable.
func TestHaskellCheckRejectsCorruptCopyWithoutEditingSource(t *testing.T) {
	tool := os.Getenv("BD2_STATE_TOOL")
	if tool == "" {
		t.Skip("set BD2_STATE_TOOL to the cabal-built bd2-state.exe")
	}
	source := filepath.Join("..", "..", "..", "data", "state")
	_, originalHash, err := LoadSnapshot(source)
	if err != nil {
		t.Skipf("workspace save not available: %v", err)
	}
	copyDir := t.TempDir()
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(copyDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check, err := Check(context.Background(), copyDir, tool)
	if err != nil || len(check.Violations) != 0 || check.SourceSHA256 != originalHash {
		t.Fatalf("valid cross-language snapshot: violations=%+v err=%v hash=%x", check.Violations, err, check.SourceSHA256)
	}
	path := filepath.Join(copyDir, "items.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var inventory map[string]json.RawMessage
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	inventory["next_index"] = json.RawMessage("1")
	corrupt, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	check, err = Check(context.Background(), copyDir, tool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, problem := range check.Violations {
		if problem.Code == "inventory.next_index" && problem.Severity == controlv1.Severity_SEVERITY_ERROR {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt copy did not report inventory.next_index: %+v", check.Violations)
	}
	// Equipment grants have stronger retry semantics than consumed item grants:
	// a recorded equipment index must still resolve to an owned instance.
	if err := os.WriteFile(path, mustReadStateFile(t, source, "items.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	equipmentPath := filepath.Join(copyDir, "equipment.json")
	var equipment map[string]json.RawMessage
	if err := json.Unmarshal(mustReadStateFile(t, equipmentPath, ""), &equipment); err != nil {
		t.Fatal(err)
	}
	equipment["granted"] = json.RawMessage(`{"test:missing":999999999}`)
	corrupt, err = json.Marshal(equipment)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(equipmentPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	check, err = Check(context.Background(), copyDir, tool)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, problem := range check.Violations {
		if problem.Code == "equipment.grant_missing_equipment" && problem.Severity == controlv1.Severity_SEVERITY_ERROR {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt copy did not report missing equipment grant: %+v", check.Violations)
	}
	_, afterHash, err := LoadSnapshot(source)
	if err != nil || afterHash != originalHash {
		t.Fatalf("check modified the actual state: before=%x after=%x err=%v", originalHash, afterHash, err)
	}
}

func TestHaskellMigrationPreservesSourceAndRejectsDuplicateCharacter(t *testing.T) {
	tool := os.Getenv("BD2_STATE_TOOL")
	if tool == "" {
		t.Skip("set BD2_STATE_TOOL to the cabal-built bd2-state.exe")
	}
	source := filepath.Join("..", "..", "..", "data", "state")
	originalSnapshot, originalHash, err := LoadSnapshot(source)
	if err != nil {
		t.Skipf("workspace save not available: %v", err)
	}
	copyDir := t.TempDir()
	for _, name := range stateFiles {
		if err := os.WriteFile(filepath.Join(copyDir, name), mustReadStateFile(t, source, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := MigrateV1ToV2(context.Background(), copyDir, tool)
	if err != nil || result.Target == nil || hasErrorViolation(result.Violations) {
		t.Fatalf("valid V1 migration: target=%v violations=%+v err=%v", result.Target != nil, result.Violations, err)
	}
	if result.SourceSHA256 != originalHash || result.Target.FormatVersion != 2 || len(result.Target.Roster.Characters) == 0 {
		t.Fatalf("unexpected migrated snapshot: source=%x target=%+v", result.SourceSHA256, result.Target)
	}
	projected, err := projectV2JSON(result.Target)
	if err != nil {
		t.Fatal(err)
	}
	projectionDir := t.TempDir()
	for _, name := range stateFiles {
		if err := os.WriteFile(filepath.Join(projectionDir, name), projected[name], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectedSnapshot, _, err := LoadSnapshot(projectionDir)
	if err != nil || !proto.Equal(originalSnapshot, projectedSnapshot) {
		t.Fatalf("V2 JSON projection changed V1 semantics: err=%v", err)
	}
	repeated, err := MigrateV1ToV2(context.Background(), copyDir, tool)
	if err != nil || repeated.Target == nil || repeated.TargetSHA256 != result.TargetSHA256 {
		t.Fatalf("migration is not deterministic: first=%x repeated=%x err=%v", result.TargetSHA256, repeated.TargetSHA256, err)
	}
	_, copyHash, err := LoadSnapshot(copyDir)
	if err != nil || copyHash != originalHash {
		t.Fatalf("successful migration changed its input: before=%x after=%x err=%v", originalHash, copyHash, err)
	}

	charactersPath := filepath.Join(copyDir, "characters.json")
	var characters struct {
		Version    string            `json:"version"`
		Characters []json.RawMessage `json:"characters"`
	}
	if err := json.Unmarshal(mustReadStateFile(t, charactersPath, ""), &characters); err != nil {
		t.Fatal(err)
	}
	if len(characters.Characters) == 0 {
		t.Fatal("workspace fixture has no base character")
	}
	characters.Characters = append(characters.Characters, characters.Characters[0])
	damaged, err := json.Marshal(characters)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(charactersPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = MigrateV1ToV2(context.Background(), copyDir, tool)
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != nil || !hasViolation(result.Violations, "characters.duplicate_index") {
		t.Fatalf("duplicate character migration was not rejected: target=%v violations=%+v", result.Target != nil, result.Violations)
	}
	_, afterHash, err := LoadSnapshot(source)
	if err != nil || afterHash != originalHash {
		t.Fatalf("migration modified actual state: before=%x after=%x err=%v", originalHash, afterHash, err)
	}
}

func hasViolation(violations []*controlv1.Violation, code string) bool {
	for _, violation := range violations {
		if violation.Code == code && violation.Severity == controlv1.Severity_SEVERITY_ERROR {
			return true
		}
	}
	return false
}

// A small, hand-checked cross-language golden fixture independent of the
// workspace account. Go encodes V1 and compares the decoded Haskell V2 result
// against a separately constructed expected semantic snapshot.
func TestV1ToV2GoldenSemanticCorpus(t *testing.T) {
	tool := os.Getenv("BD2_STATE_TOOL")
	if tool == "" {
		t.Skip("set BD2_STATE_TOOL to the cabal-built bd2-state.exe")
	}
	base := &statev1.Character{InventoryIndex: 535604118, Id: 6490, Level: 1}
	acquired := &statev1.Character{InventoryIndex: 920000001, Id: 50, Level: 20}
	costume := &statev1.Costume{InventoryIndex: 930000001, Id: 501, Level: 2, UseChar: acquired.InventoryIndex}
	grant := &statev1.CollectionGrant{Identity: "golden:gacha", CharacterIndices: []uint64{acquired.InventoryIndex}, CostumeIndices: []uint64{costume.InventoryIndex}}
	source := &statev1.Snapshot{
		FormatVersion: 1, ClientVersion: ClientVersion, GameDataVersion: GameDataVersion,
		Progress: &statev1.Progress{SourceFormatVersion: 2}, Deck: &statev1.Deck{ClientVersion: ClientVersion},
		Inventory:  &statev1.Inventory{ClientVersion: ClientVersion, NextIndex: 900000001},
		Equipment:  &statev1.EquipmentInventory{ClientVersion: ClientVersion, NextIndex: 910000001},
		Characters: &statev1.CharacterState{ClientVersion: ClientVersion, Characters: []*statev1.Character{base}},
		Collection: &statev1.Collection{ClientVersion: ClientVersion, NextCharacterIndex: acquired.InventoryIndex + 1,
			NextCostumeIndex: costume.InventoryIndex + 1, Characters: []*statev1.Character{acquired},
			Costumes: []*statev1.Costume{costume}, Grants: []*statev1.CollectionGrant{grant}},
		Wallet: &statev1.Wallet{ClientVersion: ClientVersion, Gold: 120, FreeJewelry: 42},
		Mail:   &statev1.MailState{ClientVersion: ClientVersion}, Missions: &statev1.MissionState{ClientVersion: ClientVersion},
	}
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i)
	}
	requestBytes, err := proto.Marshal(&controlv2.MigrateV1ToV2Request{BridgeApiVersion: MigrationBridgeAPIVersion, SourceSha256: hash[:], Source: source})
	if err != nil {
		t.Fatal(err)
	}
	responseBytes, err := runTool(context.Background(), tool, []string{"migrate-v1-v2"}, requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	var response controlv2.MigrateV1ToV2Response
	if err := proto.Unmarshal(responseBytes, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Migrated || !bytes.Equal(response.SourceSha256, hash[:]) || len(response.Violations) != 0 {
		t.Fatalf("golden migration rejected: %+v", &response)
	}
	expected := &statev2.Snapshot{
		FormatVersion: 2, ClientVersion: ClientVersion, GameDataVersion: GameDataVersion,
		Progress: source.Progress, Deck: source.Deck, Inventory: source.Inventory, Equipment: source.Equipment,
		Roster: &statev2.CharacterRoster{NextAcquiredCharacterIndex: acquired.InventoryIndex + 1,
			NextCostumeIndex: costume.InventoryIndex + 1,
			Characters: []*statev2.CharacterRecord{
				{Character: base, Origin: statev2.CharacterOrigin_CHARACTER_ORIGIN_BASE},
				{Character: acquired, Origin: statev2.CharacterOrigin_CHARACTER_ORIGIN_ACQUIRED},
			}, Costumes: []*statev1.Costume{costume}},
		Collection: &statev2.CollectionLedger{Grants: []*statev1.CollectionGrant{grant}},
		Wallet:     source.Wallet, Mail: source.Mail, Missions: source.Missions,
	}
	if !proto.Equal(response.Target, expected) {
		t.Fatalf("golden V2 semantic mismatch:\nreceived=%v\nexpected=%v", response.Target, expected)
	}
	if err := verifyV2(source, response.Target); err != nil {
		t.Fatalf("golden target failed Go safety check: %v", err)
	}
}

func TestHaskellRepairInstallsStaleCursorAndBecomesIdempotent(t *testing.T) {
	tool := os.Getenv("BD2_STATE_TOOL")
	if tool == "" {
		t.Skip("set BD2_STATE_TOOL to the cabal-built bd2-state.exe")
	}
	source := filepath.Join("..", "..", "..", "data", "state")
	_, originalHash, err := LoadSnapshot(source)
	if err != nil {
		t.Skipf("workspace save unavailable: %v", err)
	}
	gameDataRoot := os.Getenv("BD2_TEST_GAMEDATA_DIR")
	if gameDataRoot == "" {
		t.Skip("set BD2_TEST_GAMEDATA_DIR to enable GameData-aware repair integration test")
	}
	const gameDataVersion = "20260910162539"
	catalog, err := gamedata.LoadRegularCostumeGacha(gameDataRoot, gameDataVersion)
	if err != nil {
		t.Skipf("GameData unavailable: %v", err)
	}
	starter, err := player.Load(filepath.Join("..", "..", "seed", "v2_34_13", "starter_player.json"))
	if err != nil {
		t.Fatal(err)
	}
	groupID, gachaIDs := gacha.StepUpMigrationFact()
	validation, err := BuildValidationContext(gameDataVersion, catalog, starter, groupID, gachaIDs)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range stateFiles {
		if err := os.WriteFile(filepath.Join(dir, name), mustReadStateFile(t, source, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	itemsPath := filepath.Join(dir, "items.json")
	var inventory map[string]json.RawMessage
	if err := json.Unmarshal(mustReadStateFile(t, itemsPath, ""), &inventory); err != nil {
		t.Fatal(err)
	}
	inventory["next_index"] = json.RawMessage("1")
	damaged, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(itemsPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Repair(context.Background(), dir, tool, validation)
	if err != nil || result.Target == nil || !result.Changed {
		t.Fatalf("stale cursor repair: changed=%v target=%v violations=%+v err=%v", result.Changed, result.Target != nil, result.Violations, err)
	}
	// A concurrent request could write a newer save after the Haskell snapshot.
	// Reject that stale result without changing any of the nine files.
	if err := os.WriteFile(itemsPath, append(append([]byte(nil), damaged...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallRepair(dir, result); err == nil {
		t.Fatal("accepted a repair calculated from stale source bytes")
	}
	if err := os.WriteFile(itemsPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallRepair(dir, result); err != nil {
		t.Fatal(err)
	}
	verified, err := Repair(context.Background(), dir, tool, validation)
	if err != nil || verified.Target == nil || verified.Changed {
		t.Fatalf("installed repair not idempotent: changed=%v target=%v violations=%+v err=%v", verified.Changed, verified.Target != nil, verified.Violations, err)
	}
	if _, err := os.Stat(filepath.Join(dir, repairTransactionDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repair transaction remains: %v", err)
	}
	_, afterHash, err := LoadSnapshot(source)
	if err != nil || afterHash != originalHash {
		t.Fatalf("repair test changed actual state: before=%x after=%x err=%v", originalHash, afterHash, err)
	}
}

func mustReadStateFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	path := dir
	if name != "" {
		path = filepath.Join(dir, name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
