package pictorial

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

type ownedState struct {
	characters []player.Character
	costumes   []player.Costume
	items      []player.Item
	equipment  []player.Equipment
	discovered []player.Pictorial
}

func (s *ownedState) PictorialCharacters() []player.Character { return s.characters }
func (s *ownedState) PictorialCostumes() []player.Costume     { return s.costumes }
func (s *ownedState) PictorialItems() []player.Item           { return s.items }
func (s *ownedState) PictorialEquipment() []player.Equipment  { return s.equipment }
func (s *ownedState) PictorialDiscovered() []player.Pictorial { return s.discovered }

func TestOfficialPictorialProgression(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA to verify installed 2.34.13 GameData")
	}
	design, err := gamedata.LoadPictorialDesign(root, "20260910162539")
	if err != nil {
		t.Fatal(err)
	}
	starter, err := player.Load(filepath.Join("..", "..", "seed", "v2_34_13", "starter_player.json"))
	if err != nil {
		t.Fatal(err)
	}
	var world struct {
		RewardCharacter player.Character   `json:"reward_character"`
		RewardCostume   player.Costume     `json:"reward_costume"`
		StoryCharacters []player.Character `json:"story_characters"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "seed", "v2_34_13", "world.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &world); err != nil {
		t.Fatal(err)
	}
	owned := &ownedState{
		characters: append(append([]player.Character(nil), starter.Characters...), world.RewardCharacter),
		costumes:   append(append([]player.Costume(nil), starter.Costumes...), world.RewardCostume),
		items:      append([]player.Item(nil), starter.Items...),
		discovered: append([]player.Pictorial(nil), starter.Pictorialbook...),
	}
	owned.items = append(owned.items, player.Item{ID: 2101, Type: 17, Count: 1}, player.Item{ID: 2102, Type: 17, Count: 1})
	service := &Service{Design: design, Owned: owned}
	assertSnapshot := func(health, attack float64, equip bool, officialHex string) {
		t.Helper()
		entries, buffs, err := service.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !equip && len(entries) != 18 {
			t.Fatalf("initial account pictorial entries=%+v, want 18 official entries", entries)
		}
		if equip && len(entries) != 20 {
			t.Fatalf("quest-28 pictorial entries=%+v, want 20", entries)
		}
		got := map[uint64]float64{}
		for _, buff := range buffs {
			got[buff.StatType] = buff.Value
		}
		if math.Abs(got[2]-health) > 1e-9 || math.Abs(got[4]-attack) > 1e-9 {
			t.Fatalf("entries=%+v buffs=%+v, want health=%v attack=%v", entries, buffs, health, attack)
		}
		foundEquip := false
		for _, entry := range entries {
			if entry.GroupID == gamedata.PictorialEquipment && entry.ID == 1 {
				foundEquip = true
			}
		}
		if foundEquip != equip {
			t.Fatalf("equipment pictorial=%v, want=%v", foundEquip, equip)
		}
		if !equip {
			_, bookResponse, handled, err := service.Handle("/PictorialBookInfo", wire.AppendVarint(nil, 1, 41))
			if err != nil || !handled {
				t.Fatalf("encode pictorial entries: handled=%v err=%v", handled, err)
			}
			officialBook, err := hex.DecodeString("0a040801101f0a04080110200a04080110210a04080110220a04080110230a040801103c" +
				"0a04080210010a04080210090a0408041001" +
				"0a06080510c5ea010a05080510b5100a05080510b610" +
				"0a040807101c0a040807103d0a040807103e0a040807103f0a04080710400a0408071074")
			if err != nil || !bytes.Equal(bookResponse, officialBook) {
				t.Fatalf("pictorial response=%x official=%x err=%v", bookResponse, officialBook, err)
			}
		}
		_, actual, ok, err := service.Handle("/AllCharRefresh", wire.AppendVarint(nil, 1, 42))
		if err != nil || !ok {
			t.Fatalf("encode account buffs: handled=%v err=%v", ok, err)
		}
		official, err := hex.DecodeString(officialHex)
		if err != nil || !bytes.Equal(actual, official) {
			t.Fatalf("account buffs proto=%x, official=%x err=%v", actual, official, err)
		}
	}
	assertSnapshot(.015, .0096, false, "0a0b080211b81e85eb51b88e3f0a0b080411613255302aa9833f")
	owned.equipment = append(owned.equipment, player.Equipment{InvenIndex: 910000001, ID: 10010})
	owned.items = append(owned.items, player.Item{ID: 2103, Type: 17, Count: 1})
	assertSnapshot(.0175, .01, true, "0a0b080211ec51b81e85eb913f0a0b0804117b14ae47e17a843f")
	owned.equipment = append(owned.equipment, player.Equipment{InvenIndex: 910000002, ID: 10010})
	assertSnapshot(.0175, .01, true, "0a0b080211ec51b81e85eb913f0a0b0804117b14ae47e17a843f") // same entry does not stack
	owned.characters[5].Level = 20
	maxHP, err := service.MaxHealth(owned.characters[5])
	if err != nil || maxHP != 513 {
		t.Fatalf("official level-20 character HP=%d err=%v, want 513", maxHP, err)
	}
	code, response, ok, err := service.Handle("/AllCharRefresh", wire.AppendVarint(nil, 1, 42))
	if err != nil || !ok || code != 165 {
		t.Fatalf("AllCharRefresh: code=%d handled=%v err=%v", code, ok, err)
	}
	var count int
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			count++
		}
		return nil
	}); err != nil || count != 2 {
		t.Fatalf("buff proto: count=%d err=%v", count, err)
	}
}

func TestSharedBuffIDCountsDistinctEntriesButNotDuplicateInventory(t *testing.T) {
	owned := &ownedState{items: []player.Item{{ID: 2101, Type: 17, Count: 1}, {ID: 2101, Type: 17, Count: 1}, {ID: 2102, Type: 17, Count: 1}, {ID: 2103, Type: 8, Count: 99}}}
	design := &gamedata.PictorialDesign{
		Items: []gamedata.ItemPictorial{{ID: 2101, ItemID: 2101, BuffID: 2002}, {ID: 2102, ItemID: 2102, BuffID: 2002}},
		Buffs: map[uint64]gamedata.PictorialBuff{2002: {ID: 2002, StatType: 2, Value: .0025}},
	}
	entries, buffs, err := (&Service{Design: design, Owned: owned}).Snapshot()
	if err != nil || len(entries) != 2 || len(buffs) != 1 || buffs[0].Value != .005 {
		t.Fatalf("distinct-entries aggregation entries=%v buffs=%v err=%v", entries, buffs, err)
	}
}

func TestGrowthResourceIDCannotMasqueradeAsCollectionItem(t *testing.T) {
	owned := &ownedState{items: []player.Item{{ID: 8, Type: 8, Count: 99}}}
	design := &gamedata.PictorialDesign{
		Items: []gamedata.ItemPictorial{{ID: 8, ItemID: 8, BuffID: 2002, Category: 0}},
		Buffs: map[uint64]gamedata.PictorialBuff{2002: {ID: 2002, StatType: 2, Value: .0025}},
	}
	entries, buffs, err := (&Service{Design: design, Owned: owned}).Snapshot()
	if err != nil || len(entries) != 0 || len(buffs) != 0 {
		t.Fatalf("resource incorrectly unlocked pictorial entries=%v buffs=%v err=%v", entries, buffs, err)
	}
}
