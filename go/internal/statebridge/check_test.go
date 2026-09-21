package statebridge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	statev1 "bd2server/gen/state/v1"
	statev2 "bd2server/gen/state/v2"
)

func TestFrameRoundTrip(t *testing.T) {
	for _, payload := range [][]byte{nil, {1}, bytes.Repeat([]byte{0x7f}, 4096)} {
		frame, err := encodeFrame(payload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeFrame(frame)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("round trip len=%d got=%d err=%v", len(payload), len(got), err)
		}
	}
}

func TestLoadCurrentWorkspaceSnapshot(t *testing.T) {
	stateDir := filepath.Join("..", "..", "..", "data", "state")
	if _, err := os.Stat(stateDir); err != nil {
		t.Skipf("workspace state unavailable: %v", err)
	}
	snapshot, hash, err := LoadSnapshot(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FormatVersion != 1 || snapshot.ClientVersion != ClientVersion || snapshot.Progress == nil {
		t.Fatalf("unexpected snapshot header: %+v", snapshot)
	}
	if snapshot.Progress.SourceFormatVersion != 2 || snapshot.Inventory == nil || snapshot.Collection == nil || snapshot.Wallet == nil {
		t.Fatalf("missing or unsupported persisted state: progress=%+v inventory=%v collection=%v wallet=%v", snapshot.Progress, snapshot.Inventory, snapshot.Collection, snapshot.Wallet)
	}
	if hash == ([32]byte{}) {
		t.Fatal("source hash is empty")
	}
}

func TestFrameRejectsTrailingAndOversize(t *testing.T) {
	frame, _ := encodeFrame([]byte{1, 2, 3})
	if _, err := decodeFrame(append(frame, 4)); err == nil {
		t.Fatal("accepted trailing response data")
	}
	if _, err := encodeFrame(make([]byte, MaxFrameBytes+1)); err == nil {
		t.Fatal("accepted oversized request")
	}
}

func TestToolOutputLimit(t *testing.T) {
	output := &boundedOutput{limit: 3}
	if _, err := output.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte{4}); err == nil || !output.overflow || output.Len() != 3 {
		t.Fatalf("unbounded output: err=%v overflow=%v len=%d", err, output.overflow, output.Len())
	}
}

func TestQuestKeysCannotDiscardDamagedSourceIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		progress progressDisk
		want     string
	}{
		{"wrong version", progressDisk{Version: 1}, "expected progress format 2"},
		{"mismatched key", progressDisk{Version: 2, Quests: map[string]struct {
			QuestID uint32   `json:"QuestID"`
			PackID  uint32   `json:"PackID"`
			Values  []uint32 `json:"Values"`
		}{"21:1": {QuestID: 1, PackID: 22}}}, "does not match"},
		{"false cleared marker", progressDisk{Version: 2, Cleared: map[string]bool{"21:1": false}}, "must be true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := progressProto(test.progress); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("progressProto error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestVerifyV2RejectsRewrittenWalletAndCharacter(t *testing.T) {
	source := &statev1.Snapshot{
		FormatVersion: 1, ClientVersion: ClientVersion, GameDataVersion: GameDataVersion,
		Wallet:     &statev1.Wallet{Gold: 100},
		Characters: &statev1.CharacterState{Characters: []*statev1.Character{{InventoryIndex: 123, Id: 1}}},
		Collection: &statev1.Collection{NextCharacterIndex: 1000},
	}
	valid := func() *statev2.Snapshot {
		return &statev2.Snapshot{
			FormatVersion: 2, ClientVersion: ClientVersion, GameDataVersion: GameDataVersion,
			Wallet: &statev1.Wallet{Gold: 100},
			Roster: &statev2.CharacterRoster{NextAcquiredCharacterIndex: 1000, Characters: []*statev2.CharacterRecord{
				{Origin: statev2.CharacterOrigin_CHARACTER_ORIGIN_BASE, Character: &statev1.Character{InventoryIndex: 123, Id: 1}},
			}},
			Collection: &statev2.CollectionLedger{},
		}
	}
	if err := verifyV2(source, valid()); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	alteredWallet := valid()
	alteredWallet.Wallet.Gold++
	if err := verifyV2(source, alteredWallet); err == nil {
		t.Fatal("accepted migrated wallet rewrite")
	}
	alteredCharacter := valid()
	alteredCharacter.Roster.Characters[0].Character.InventoryIndex++
	if err := verifyV2(source, alteredCharacter); err == nil {
		t.Fatal("accepted migrated character index rewrite")
	}
}

func TestRecoverInterruptedInstallRestoresWholeGeneration(t *testing.T) {
	source := filepath.Join("..", "..", "..", "data", "state")
	if complete, err := CompleteStateAvailable(source); err != nil || !complete {
		t.Skipf("workspace state unavailable: complete=%v err=%v", complete, err)
	}
	dir := t.TempDir()
	tx := filepath.Join(dir, repairTransactionDir)
	backup := filepath.Join(tx, "backup")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	original := make(map[string][]byte, len(stateFiles))
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = data
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backup, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, backupHash, err := LoadSnapshot(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tx, "prepared"), backupHash[:], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFiles[0]), []byte("partial generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverInterruptedInstall(dir)
	if err != nil || !recovered {
		t.Fatalf("recover: recovered=%v err=%v", recovered, err)
	}
	for _, name := range stateFiles {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, original[name]) {
			t.Fatalf("restored %s: equal=%v err=%v", name, bytes.Equal(got, original[name]), err)
		}
	}
	if _, err := os.Stat(tx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction directory remains: %v", err)
	}
}

func TestCompleteStateRejectsPartiallyInitializedAccount(t *testing.T) {
	dir := t.TempDir()
	complete, err := CompleteStateAvailable(dir)
	if err != nil || complete {
		t.Fatalf("empty account: complete=%v err=%v", complete, err)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFiles[0]), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteStateAvailable(dir); err == nil {
		t.Fatal("partially initialized account was treated as fresh")
	}
}

func TestProjectionRefusesUnknownJSONFields(t *testing.T) {
	if err := originalKeysRepresented([]byte(`{"wallet":{"gold":2,"future_field":7}}`), []byte(`{"wallet":{"gold":2}}`)); err == nil {
		t.Fatal("projection silently dropped an unknown persisted field")
	}
	if err := originalKeysRepresented([]byte(`{"wallet":{"gold":2}}`), []byte(`{"wallet":{"gold":3,"mileage":0}}`)); err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
}
