package player

import (
	"bd2server/internal/server/stateio"
	"testing"
)

func TestEnsureStoryCharactersPersistsWithoutCollectionGrant(t *testing.T) {
	storage := stateio.NewMemory()
	inventory, err := OpenInventory(storage, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	owned := Character{InvenIndex: 3, ID: 30, Level: 1}
	store, err := OpenCharacterStore(storage, []Character{owned}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	story := Character{InvenIndex: StoryCharacterIndexBase + 1, ID: 10383, HP: 777, Level: 15, CostumeID: 3801, UseCostume: 2}
	if err := store.EnsureStoryCharacters([]Character{story}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentHealth(story.InvenIndex, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureStoryCharacters([]Character{story}); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenCharacterStore(storage, []Character{owned}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	current, ok := loaded.Find(story.InvenIndex)
	if !ok || current.HP != 7 || current.Level != 15 {
		t.Fatalf("reloaded story %+v/%v", current, ok)
	}
	collision := story
	collision.ID++
	if err := loaded.EnsureStoryCharacters([]Character{collision}); err == nil {
		t.Fatal("accepted conflicting story instance")
	}
	if err := loaded.EnsureStoryCharacters([]Character{owned}); err == nil {
		t.Fatal("accepted owned instance as story")
	}
}
