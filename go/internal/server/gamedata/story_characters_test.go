package gamedata

import (
	"os"
	"testing"
)

func TestInstalledStoryCharacterCatalogUsesAuthoredRows(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	catalog, err := LoadStoryCharacterCatalog(root, "20260923193640", []int{1, 21})
	if err != nil {
		t.Fatal(err)
	}
	chars, err := catalog.Characters(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chars) != 0 {
		t.Fatalf("pack1 quest1 fabricated temporary characters: %v", chars)
	}
	chars, err = catalog.Characters(21, 29)
	if err != nil {
		t.Fatal(err)
	}
	if len(chars) == 0 {
		t.Fatal("pack21 quest29 omitted authored characters")
	}
	for _, c := range chars {
		if c.CharacterID == 0 || c.Level == 0 || c.HP == 0 || c.CostumeID == 996000 {
			t.Fatalf("invalid story character %v", c)
		}
	}
}

func TestInstalledAllStoryCharacterDesigns(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	story, err := LoadStoryCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	var packs []int
	for id := range story.Packs {
		packs = append(packs, id)
	}
	catalog, err := LoadStoryCharacterCatalog(root, "20260923193640", packs)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, formations := range catalog.formations {
		for _, f := range formations {
			count += len(f.Characters)
		}
	}
	if count == 0 {
		t.Fatal("all story catalog omitted character rows")
	}
	t.Logf("validated %d packs and %d authored character rows", len(packs), count)
}
