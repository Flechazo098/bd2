package progress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/wire"
)

func TestStorePersistsProgressAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "player", "progress.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected initial save: %v", err)
	}
	position := wire.AppendVarint(nil, 2, 21)
	position = wire.AppendString(position, 3, `{"MapId":211,"PlayerPosition":{"x":1,"y":2,"z":3}}`)
	if err := store.SaveUserPosition(position); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearTutorial(wire.AppendVarint(nil, 2, 2001)); err != nil {
		t.Fatal(err)
	}
	quest := wire.AppendVarint(nil, 2, 12)
	quest = wire.AppendVarint(quest, 3, 21)
	quest = wire.AppendBytes(quest, 4, []byte{121})
	if _, err := store.UpdateQuest(quest); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pos, found := reopened.Position()
	if !found || pos.Position.MapID != 211 || pos.PackID != 21 || !reopened.TutorialCleared(2001) {
		t.Fatalf("lost persisted progress: %+v", pos)
	}
	if saved, found := reopened.Quest(12); !found || saved.PackID != 21 || len(saved.Values) != 1 || saved.Values[0] != 121 {
		t.Fatalf("lost quest progress: %+v/%v", saved, found)
	}
}

func TestStoreMigratesLegacyQuestKeysAndKeepsPackOverlap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	legacy := `{"quests":{"1":{"QuestID":1,"PackID":21,"Values":[7]}},"cleared_quests":{"1":21}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !store.QuestCleared(1, 21) || store.QuestCleared(1, 22) {
		t.Fatal("legacy pack21 clear was not migrated with its pack identity")
	}
	request := wire.AppendVarint(nil, 2, 1)
	request = wire.AppendVarint(request, 3, 22)
	request = wire.AppendVarint(request, 4, 9)
	if _, err := store.UpdateQuest(request); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearQuest(1, 22); err != nil {
		t.Fatal(err)
	}
	if !store.QuestCleared(1, 21) || !store.QuestCleared(1, 22) {
		t.Fatal("same quest id did not remain independently cleared in both packs")
	}
	if _, found := store.Quest(1); found {
		t.Fatal("pack-less lookup accepted an ambiguous quest id")
	}
	for _, packID := range []int{21, 22} {
		if quest, found := store.QuestInPack(1, packID); !found || quest.PackID != packID {
			t.Fatalf("pack%d quest missing: %+v found=%v", packID, quest, found)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := json.Unmarshal(saved["version"], &version); err != nil || version != snapshotVersion {
		t.Fatalf("migrated version=%d err=%v", version, err)
	}
	var cleared map[string]bool
	if err := json.Unmarshal(saved["cleared_quests"], &cleared); err != nil || !cleared["21:1"] || !cleared["22:1"] {
		t.Fatalf("migrated clears=%v err=%v", cleared, err)
	}
}

func TestStoreRejectsBrokenSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte(`{"quests":{"12":{"QuestID":5,"PackID":21}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("accepted inconsistent save")
	}
}
