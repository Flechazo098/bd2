package progress

import (
	"encoding/json"
	"testing"

	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func TestStorePersistsProgressAcrossRestart(t *testing.T) {
	storage := stateio.NewMemory()
	store, err := OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := storage.Load("progress"); data != nil {
		t.Fatalf("unexpected initial save: %s", data)
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
	reopened, err := OpenStore(storage)
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

func TestStoreKeepsPackOverlap(t *testing.T) {
	storage := stateio.NewMemory()
	initial := `{"version":2,"position":{"PackID":0,"Position":{"MapId":0,"PlayerPosition":{"x":0,"y":0,"z":0},"ColleaguePositions":null},"RawJSON":""},"tutorials":[],"quests":{"21:1":{"QuestID":1,"PackID":21,"Values":[7]}},"cleared_quests":{"21:1":true}}`
	if err := storage.Save("progress", []byte(initial)); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	if !store.QuestCleared(1, 21) || store.QuestCleared(1, 22) {
		t.Fatal("pack21 clear was not loaded with its pack identity")
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
	data, err := storage.Load("progress")
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := json.Unmarshal(saved["version"], &version); err != nil || version != snapshotVersion {
		t.Fatalf("saved version=%d err=%v", version, err)
	}
	var cleared map[string]bool
	if err := json.Unmarshal(saved["cleared_quests"], &cleared); err != nil || !cleared["21:1"] || !cleared["22:1"] {
		t.Fatalf("saved clears=%v err=%v", cleared, err)
	}
}

func TestStoreRejectsBrokenSave(t *testing.T) {
	storage := stateio.NewMemory()
	if err := storage.Save("progress", []byte(`{"version":2,"quests":{"12":{"QuestID":5,"PackID":21}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(storage); err == nil {
		t.Fatal("accepted inconsistent save")
	}
}

func TestStoreRejectsLegacySave(t *testing.T) {
	storage := stateio.NewMemory()
	_ = storage.Save("progress", []byte(`{"quests":{"1":{"QuestID":1,"PackID":21}}}`))
	if _, err := OpenStore(storage); err == nil {
		t.Fatal("accepted unversioned legacy state")
	}
}
