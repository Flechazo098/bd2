package statetx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openFixture(t *testing.T) (*Coordinator, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"wallet.json": "old-wallet", "items.json": "old-items"} {
		if err := atomicTestReplace(filepath.Join(root, name), []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(root, []string{"wallet.json", "items.json"})
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestOpenRejectsUnsafeOrDuplicateTargets(t *testing.T) {
	root := t.TempDir()
	for _, names := range [][]string{{"../wallet.json"}, {"wallet.json", "wallet.json"}, {journalName}} {
		if _, err := Open(root, names); err == nil {
			t.Fatalf("unsafe allow-list accepted: %v", names)
		}
	}
}

func TestRequestOperationCommitsWholeGeneration(t *testing.T) {
	s, root := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"items.json": "new-items", "wallet.json": "new-wallet"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	assertFiles(t, root, map[string]string{"items.json": "new-items", "wallet.json": "new-wallet"})
}

func TestRequestCrashRollsBackEveryFileOnOpen(t *testing.T) {
	s, root := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicTestReplace(filepath.Join(root, "items.json"), []byte("new-items")); err != nil {
		t.Fatal(err)
	}
	op.Abort()
	if _, err := Open(root, []string{"wallet.json", "items.json"}); err != nil {
		t.Fatal(err)
	}
	assertFiles(t, root, map[string]string{"items.json": "old-items", "wallet.json": "old-wallet"})
}

func TestRequestRollbackWithoutMutationKeepsCoordinatorUsable(t *testing.T) {
	s, _ := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err != nil {
		t.Fatal(err)
	}
	second, err := s.Begin()
	if err != nil {
		t.Fatalf("clean rejection poisoned coordinator: %v", err)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRollbackAfterMutationRestoresAndFailsStop(t *testing.T) {
	s, root := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicTestReplace(filepath.Join(root, "wallet.json"), []byte("partial-wallet")); err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err == nil {
		t.Fatal("mutating rollback did not require restart")
	}
	assertFiles(t, root, map[string]string{"wallet.json": "old-wallet"})
	if _, err := s.Begin(); err == nil {
		t.Fatal("coordinator accepted request after memory could diverge")
	}
	if _, err := Open(root, []string{"wallet.json", "items.json"}); err != nil {
		t.Fatalf("clean restart could not reopen state: %v", err)
	}
}

func TestFirstRunMissingFileIsRemovedByCrashRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wallet.json"), []byte("old-wallet"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, []string{"wallet.json", "items.json"})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items.json"), []byte("created-items"), 0o600); err != nil {
		t.Fatal(err)
	}
	op.Abort()
	if _, err := Open(root, []string{"wallet.json", "items.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "items.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first-run file survived rollback: %v", err)
	}
}

func TestRecoveryRejectsTamperedUndoWithoutChangingState(t *testing.T) {
	s, root := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	op.Abort()
	journal := filepath.Join(root, journalName)
	if err := atomicTestReplace(filepath.Join(journal, "before", "wallet.json"), []byte("CORRUPT")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, []string{"wallet.json", "items.json"}); err == nil {
		t.Fatal("tampered undo was accepted")
	}
	assertFiles(t, root, map[string]string{"wallet.json": "old-wallet", "items.json": "old-items"})
}

func TestCommittedRecoveryValidatesFinalGeneration(t *testing.T) {
	s, root := openFixture(t)
	op, err := s.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicTestReplace(filepath.Join(root, "wallet.json"), []byte("new-wallet")); err != nil {
		t.Fatal(err)
	}
	final := finalManifest{Format: requestFormat, Records: []finalRecord{
		{Name: "items.json", Exists: true, SHA256: digest([]byte("old-items"))},
		{Name: "wallet.json", Exists: true, SHA256: digest([]byte("new-wallet"))},
	}}
	encoded, _ := json.Marshal(final)
	if err := writeAtomicSynced(filepath.Join(op.journal, "committed.json"), encoded); err != nil {
		t.Fatal(err)
	}
	op.Abort()
	if _, err := Open(root, []string{"wallet.json", "items.json"}); err != nil {
		t.Fatal(err)
	}
	assertFiles(t, root, map[string]string{"wallet.json": "new-wallet", "items.json": "old-items"})
}

func TestSnapshotBeforeSurvivesAtomicLiveReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "live.json")
	snapshot := filepath.Join(root, "snapshot.json")
	old := []byte("old generation that must remain immutable")
	if err := os.WriteFile(source, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshotBefore(source, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := atomicTestReplace(source, []byte("new generation")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(snapshot)
	if err != nil || string(got) != string(old) {
		t.Fatalf("snapshot changed after live replace: %q err=%v", got, err)
	}
}

func BenchmarkRequestTransactionRealState(b *testing.B) {
	source := filepath.Clean(filepath.Join("..", "..", "..", "data", "state"))
	if _, err := os.Stat(source); err != nil {
		b.Skip("development state fixture is unavailable")
	}
	root := b.TempDir()
	names := []string{"characters.json", "collection.json", "deck.json", "equipment.json", "items.json", "mail.json", "missions.json", "progress.json", "wallet.json"}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	coordinator, err := Open(root, names)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		op, err := coordinator.Begin()
		if err != nil {
			b.Fatal(err)
		}
		if err := op.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

func atomicTestReplace(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".statetx-test-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, path)
}

func assertFiles(t *testing.T, root string, want map[string]string) {
	t.Helper()
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != expected {
			t.Fatalf("%s=%q want=%q err=%v", name, got, expected, err)
		}
	}
}
