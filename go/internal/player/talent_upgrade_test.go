package player

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func testTalentGrowthDesign() *gamedata.TalentGrowthDesign {
	return &gamedata.TalentGrowthDesign{
		Characters: map[uint64]gamedata.CharacterTalent{
			140: {TalentID: 904, GrowthGroup: 904, MaxLevel: 5},
		},
		Levels: map[[2]uint64]gamedata.TalentGrowthLevel{
			{904, 1}: {Level: 1, NeedExp: 14, Costs: []gamedata.PromotionCost{{Type: 8, ID: 3, Count: 1}, {Type: 4, Count: 1000}}},
			{904, 2}: {Level: 2, NeedExp: 28, Costs: []gamedata.PromotionCost{{Type: 8, ID: 4, Count: 1}, {Type: 4, Count: 2000}}},
			{904, 3}: {Level: 3, NeedExp: 404, Costs: []gamedata.PromotionCost{{Type: 8, ID: 5, Count: 1}, {Type: 4, Count: 4000}}},
			{904, 4}: {Level: 4, NeedExp: 1008, Costs: []gamedata.PromotionCost{{Type: 8, ID: 6, Count: 1}, {Type: 4, Count: 10000}}},
		},
	}
}

func talentUpgradeRequest(seq, character uint64, materials ...Item) []byte {
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, character)
	for _, material := range materials {
		request = wire.AppendBytes(request, 3, ItemWire(material))
	}
	return request
}

func TestTalentSkillUpgradeConsumesExactCostsPersistsAndReplays(t *testing.T) {
	dir := t.TempDir()
	store := testStore(filepath.Join(dir, "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("talent-books", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 5000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 14}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachTalentGrowth(testTalentGrowthDesign()); err != nil {
		t.Fatal(err)
	}
	characters.BeginSession("login-a")
	book := books[0]
	book.Count = 1
	request := talentUpgradeRequest(9, 77, book, Item{Type: 4, Count: 1000})
	code, response, handled, err := characters.Handle("/TalentSkillUpgrade", request)
	if err != nil || !handled || code != talentSkillUpgradePacketCode || len(response) != 0 {
		t.Fatalf("upgrade code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	got, found := characters.Find(77)
	if !found || got.TalentLevel != 2 || got.TalentExp != 14 {
		t.Fatalf("upgraded character=%+v found=%v", got, found)
	}
	if wallet.Snapshot().Gold != 4000 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	if err := inventory.CanConsume([]Item{book}); err != nil {
		t.Fatalf("one talent book should remain: %v", err)
	}
	code, response, handled, err = characters.Handle("/TalentSkillUpgrade", request)
	if err != nil || !handled || code != talentSkillUpgradePacketCode || len(response) != 0 {
		t.Fatalf("replay code=%d handled=%v response=%x err=%v", code, handled, response, err)
	}
	if wallet.Snapshot().Gold != 4000 {
		t.Fatal("replay charged gold again")
	}
	if err := inventory.CanConsume([]Item{book}); err != nil {
		t.Fatal("replay consumed the remaining talent book")
	}
	reloaded, err := OpenCharacterStore(store, nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, found := reloaded.Find(77); !found || got.TalentLevel != 2 || got.TalentExp != 14 {
		t.Fatalf("reloaded character=%+v found=%v", got, found)
	}
}

func TestTalentSkillUpgradeRejectsInvalidStateWithoutCharging(t *testing.T) {
	tests := []struct {
		name       string
		level      uint64
		experience uint64
		materialID uint64
		gold       uint64
	}{
		{name: "experience", level: 1, experience: 13, materialID: 3, gold: 1000},
		{name: "wrong-book", level: 1, experience: 14, materialID: 4, gold: 1000},
		{name: "wrong-gold", level: 1, experience: 14, materialID: 3, gold: 999},
		{name: "maximum", level: 5, experience: 1454, materialID: 6, gold: 10000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := testStore(filepath.Join(dir, "state.json"))
			inventory, err := OpenInventory(store, &Starter{Version: "2.34.13"})
			if err != nil {
				t.Fatal(err)
			}
			books, err := inventory.GrantOnce("books", []gamedata.BattleReward{{Type: 8, ID: test.materialID, Count: 2}})
			if err != nil {
				t.Fatal(err)
			}
			wallet, err := OpenWallet(store, Currency{Gold: 20000})
			if err != nil {
				t.Fatal(err)
			}
			characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: test.level, TalentExp: test.experience}}, inventory, "", "")
			if err != nil {
				t.Fatal(err)
			}
			_ = characters.AttachWallet(wallet)
			_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
			book := books[0]
			book.Count = 1
			request := talentUpgradeRequest(1, 77, book, Item{Type: 4, Count: test.gold})
			if _, _, handled, err := characters.Handle("/TalentSkillUpgrade", request); err == nil || !handled {
				t.Fatalf("invalid upgrade accepted: handled=%v err=%v", handled, err)
			}
			if wallet.Snapshot().Gold != 20000 {
				t.Fatal("invalid upgrade charged gold")
			}
			if err := inventory.CanConsume([]Item{books[0]}); err != nil {
				t.Fatalf("invalid upgrade consumed a book: %v", err)
			}
			if current, _ := characters.Find(77); current.TalentLevel != test.level || current.TalentExp != test.experience {
				t.Fatalf("invalid upgrade changed character: %+v", current)
			}
		})
	}
}

func TestTalentSkillUpgradePersistsCollectionCharacter(t *testing.T) {
	dir := t.TempDir()
	store := testStore(filepath.Join(dir, "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("book", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := OpenCollectionStore(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneCollection(collection.data)
	next.Characters = []Character{{InvenIndex: 920000021, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 14}}
	if err := collection.commit(next); err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 350, Level: 1, TalentLevel: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = characters.AttachCollection(collection)
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	request := talentUpgradeRequest(4, 920000021, books[0], Item{Type: 4, Count: 1000})
	if code, _, handled, err := characters.Handle("/TalentSkillUpgrade", request); err != nil || !handled || code != talentSkillUpgradePacketCode {
		t.Fatalf("collection upgrade code=%d handled=%v err=%v", code, handled, err)
	}
	reloaded, err := OpenCollectionStore(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, found := reloaded.FindCharacter(920000021); !found || got.TalentLevel != 2 || got.TalentExp != 14 {
		t.Fatalf("reloaded collection character=%+v found=%v", got, found)
	}
}
