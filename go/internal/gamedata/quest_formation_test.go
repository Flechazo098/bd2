package gamedata

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bd2server/internal/wire"
)

func TestLoadQuestFormationsDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quest.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE QuestTable21 (id INTEGER PRIMARY KEY, ProtoBuf BLOB NOT NULL)",
		"CREATE TABLE CharGroupTable (id INTEGER NOT NULL, GroupId INTEGER NOT NULL, ProtoBuf BLOB NOT NULL)",
		"CREATE TABLE StoryCharGroupTable (id INTEGER NOT NULL, GroupId INTEGER NOT NULL, ProtoBuf BLOB NOT NULL)",
		"CREATE TABLE CostumeTable (id INTEGER PRIMARY KEY, ProtoBuf BLOB NOT NULL)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	quest := wire.AppendVarint(nil, 4, 2102)
	quest = wire.AppendVarint(quest, 12, 9)
	quest = wire.AppendVarint(quest, 12, 7)
	quest = wire.AppendVarint(quest, 61, 2103)
	if _, err := db.Exec("INSERT INTO QuestTable21(id,ProtoBuf) VALUES(29,?)", quest); err != nil {
		t.Fatal(err)
	}
	character := wire.AppendVarint(nil, 1, 10383)
	character = wire.AppendVarint(character, 2, 2102)
	character = wire.AppendVarint(character, 3, 1)
	character = wire.AppendVarint(character, 5, 15)
	if _, err := db.Exec("INSERT INTO CharGroupTable(id,GroupId,ProtoBuf) VALUES(1,2102,?)", character); err != nil {
		t.Fatal(err)
	}
	story := wire.AppendVarint(nil, 1, 3801)
	story = wire.AppendVarint(story, 2, 2103)
	story = wire.AppendVarint(story, 3, 3)
	if _, err := db.Exec("INSERT INTO StoryCharGroupTable(id,GroupId,ProtoBuf) VALUES(3,2103,?)", story); err != nil {
		t.Fatal(err)
	}
	costume := wire.AppendVarint(nil, 11, 3801)
	costume = wire.AppendVarint(costume, 27, 38)
	if _, err := db.Exec("INSERT INTO CostumeTable(id,ProtoBuf) VALUES(3801,?)", costume); err != nil {
		t.Fatal(err)
	}

	formations, err := loadQuestFormationsDB(db, 21)
	if err != nil {
		t.Fatal(err)
	}
	want := QuestFormation{
		QuestID:          29,
		CharGroupID:      2102,
		StoryCharGroupID: 2103,
		DeckList:         []uint64{9, 7},
		Characters:       []QuestCharacterDesign{{Order: 1, CharacterID: 10383, Level: 15}},
		StoryCostumes:    []QuestCostumeDesign{{Order: 3, CostumeID: 3801, UniqueCharacterID: 38}},
	}
	if got := formations[29]; !reflect.DeepEqual(got, want) {
		t.Fatalf("formation mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRealQuest21Formations(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	formations, err := LoadQuestFormations(root, "20260910162539", 21)
	if err != nil {
		t.Fatal(err)
	}
	quest29 := formations[29]
	if quest29.CharGroupID != 2102 || quest29.StoryCharGroupID != 2103 || len(quest29.DeckList) != 0 {
		t.Fatalf("unexpected quest 29 groups: %#v", quest29)
	}
	wantCharacters := []QuestCharacterDesign{
		{Order: 1, CharacterID: 10383, Level: 15},
		{Order: 2, CharacterID: 10372, Level: 15},
		{Order: 3, CharacterID: 10412, Level: 15},
	}
	if !reflect.DeepEqual(quest29.Characters, wantCharacters) {
		t.Fatalf("quest 29 characters = %#v", quest29.Characters)
	}
	wantCostumes := []QuestCostumeDesign{
		{Order: 1, CostumeID: 996000, UniqueCharacterID: 9960},
		{Order: 2, CostumeID: 3501, UniqueCharacterID: 35},
		{Order: 3, CostumeID: 3801, UniqueCharacterID: 38},
		{Order: 4, CostumeID: 3701, UniqueCharacterID: 37},
		{Order: 5, CostumeID: 4101, UniqueCharacterID: 41},
	}
	if !reflect.DeepEqual(quest29.StoryCostumes, wantCostumes) {
		t.Fatalf("quest 29 story costumes = %#v", quest29.StoryCostumes)
	}
	quest31 := formations[31]
	if quest31.StoryCharGroupID != 2107 || len(quest31.StoryCostumes) != 3 {
		t.Fatalf("unexpected quest 31 formation: %#v", quest31)
	}
}
