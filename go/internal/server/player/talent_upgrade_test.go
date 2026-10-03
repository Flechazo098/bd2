package player

import (
	"math"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
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
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
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
	// Session dispatch activates SessionAware handlers before every request.
	// Re-activating the same session must retain the sequence replay result.
	characters.BeginSession("login-a")
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
	if err := reloaded.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.AttachTalentGrowth(testTalentGrowthDesign()); err != nil {
		t.Fatal(err)
	}
	reloaded.BeginSession("login-after-restart")
	if code, body, handled, err := reloaded.Handle("/TalentSkillUpgrade", request); err != nil || !handled || code != talentSkillUpgradePacketCode || len(body) != 0 {
		t.Fatalf("restart replay code=%d handled=%v body=%x err=%v", code, handled, body, err)
	}
	if got, _ := reloaded.Find(77); got.TalentLevel != 2 || wallet.Snapshot().Gold != 4000 {
		t.Fatalf("restart replay duplicated upgrade: character=%+v gold=%d", got, wallet.Snapshot().Gold)
	}
}

func TestTalentSkillUpgradeRejectsSameSessionSequenceWithDifferentRequest(t *testing.T) {
	store := testStore(filepath.Join(t.TempDir(), "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("books", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 2}})
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
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	characters.BeginSession("login-a")
	book := books[0]
	book.Count = 1
	request := talentUpgradeRequest(9, 77, book, Item{Type: 4, Count: 1000})
	if _, _, _, err := characters.Handle("/TalentSkillUpgrade", request); err != nil {
		t.Fatal(err)
	}
	different := talentUpgradeRequest(9, 77, book, Item{Type: 4, Count: 999})
	if _, _, handled, err := characters.Handle("/TalentSkillUpgrade", different); err == nil || !handled {
		t.Fatalf("different request replay handled=%v err=%v", handled, err)
	}
	if got, _ := characters.Find(77); got.TalentLevel != 2 || wallet.Snapshot().Gold != 4000 {
		t.Fatalf("different replay mutated state: character=%+v gold=%d", got, wallet.Snapshot().Gold)
	}
}

func TestTalentSkillUpgradeRetainsSequenceDigestAcrossInterleavedSessions(t *testing.T) {
	store := testStore(filepath.Join(t.TempDir(), "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("interleaved-books", []gamedata.BattleReward{
		{Type: 8, ID: 3, Count: 1},
		{Type: 8, ID: 4, Count: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 5000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 42}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	characters.BeginSession("login-a")
	first := talentUpgradeRequest(9, 77, books[0], Item{Type: 4, Count: 1000})
	if _, _, _, err := characters.Handle("/TalentSkillUpgrade", first); err != nil {
		t.Fatal(err)
	}
	characters.BeginSession("login-b")
	characters.BeginSession("login-a")
	second := talentUpgradeRequest(9, 77, books[1], Item{Type: 4, Count: 2000})
	if _, _, handled, err := characters.Handle("/TalentSkillUpgrade", second); err == nil || !handled {
		t.Fatalf("interleaved session forgot sequence digest: handled=%v err=%v", handled, err)
	}
	if got, _ := characters.Find(77); got.TalentLevel != 2 || got.TalentExp != 42 || wallet.Snapshot().Gold != 4000 {
		t.Fatalf("reused sequence mutated state: character=%+v gold=%d", got, wallet.Snapshot().Gold)
	}
	if err := inventory.CanConsume([]Item{books[1]}); err != nil {
		t.Fatalf("reused sequence consumed second-rank material: %v", err)
	}
}

func TestTalentSkillUpgradeRejectsOutOfRangeItemMetadata(t *testing.T) {
	store := testStore(filepath.Join(t.TempDir(), "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("range-book", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 14}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	book := books[0]
	book.Count = 1
	book.ExpiryTime = math.MaxInt64 + 1
	request := talentUpgradeRequest(1, 77, book, Item{Type: 4, Count: 1000})
	if _, _, handled, err := characters.Handle("/TalentSkillUpgrade", request); err == nil || !handled {
		t.Fatalf("out-of-range metadata accepted: handled=%v err=%v", handled, err)
	}
	if got, _ := characters.Find(77); got.TalentLevel != 1 || wallet.Snapshot().Gold != 1000 {
		t.Fatalf("out-of-range request mutated state: character=%+v gold=%d", got, wallet.Snapshot().Gold)
	}
}

func TestTalentSkillUpgradeConsumesOneMaterialAcrossMultipleStacks(t *testing.T) {
	store := testStore(filepath.Join(t.TempDir(), "state.json"))
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("split-books", []gamedata.BattleReward{
		{Type: 8, ID: 3, Count: 1},
		{Type: 8, ID: 3, Count: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Gold: 1000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 14}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	design := testTalentGrowthDesign()
	level := design.Levels[[2]uint64{904, 1}]
	level.Costs = []gamedata.PromotionCost{{Type: 8, ID: 3, Count: 2}, {Type: 4, Count: 1000}}
	design.Levels[[2]uint64{904, 1}] = level
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(design)
	request := talentUpgradeRequest(1, 77, books[0], books[1], Item{Type: 4, Count: 1000})
	if code, body, handled, err := characters.Handle("/TalentSkillUpgrade", request); err != nil || !handled || code != talentSkillUpgradePacketCode || len(body) != 0 {
		t.Fatalf("split-stack upgrade code=%d handled=%v body=%x err=%v", code, handled, body, err)
	}
	if got, _ := characters.Find(77); got.TalentLevel != 2 || got.TalentExp != 14 || wallet.Snapshot().Gold != 0 {
		t.Fatalf("split-stack result character=%+v gold=%d", got, wallet.Snapshot().Gold)
	}
	if err := inventory.CanConsume([]Item{books[0]}); err == nil {
		t.Fatal("first material stack survived consumption")
	}
	if err := inventory.CanConsume([]Item{books[1]}); err == nil {
		t.Fatal("second material stack survived consumption")
	}
}

func TestTalentSkillUpgradeLedgerSurvivesSQLiteCloseAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repository, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := OpenInventory(repository, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	books, err := inventory.GrantOnce("sqlite-talent-book", []gamedata.BattleReward{{Type: 8, ID: 3, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(repository, Currency{Gold: 5000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(repository, []Character{{InvenIndex: 77, ID: 140, Level: 1, TalentLevel: 1, TalentExp: 14}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func() error{inventory.EnsurePersisted, wallet.EnsurePersisted, characters.EnsurePersisted} {
		if err := ensure(); err != nil {
			t.Fatal(err)
		}
	}
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	characters.BeginSession("before-restart")
	request := talentUpgradeRequest(9, 77, books[0], Item{Type: 4, Count: 1000})
	operation, err := repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if code, body, handled, err := characters.Handle("/TalentSkillUpgrade", request); err != nil || !handled || code != talentSkillUpgradePacketCode || len(body) != 0 {
		_ = operation.Rollback()
		t.Fatalf("upgrade code=%d handled=%v body=%x err=%v", code, handled, body, err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	inventory, err = OpenInventory(repository, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err = OpenWallet(repository, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	characters, err = OpenCharacterStore(repository, nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = characters.AttachWallet(wallet)
	_ = characters.AttachTalentGrowth(testTalentGrowthDesign())
	characters.BeginSession("after-restart")
	operation, err = repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if code, body, handled, err := characters.Handle("/TalentSkillUpgrade", request); err != nil || !handled || code != talentSkillUpgradePacketCode || len(body) != 0 {
		_ = operation.Rollback()
		t.Fatalf("replay code=%d handled=%v body=%x err=%v", code, handled, body, err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	if current, found := characters.Find(77); !found || current.TalentLevel != 2 || current.TalentExp != 14 || wallet.Snapshot().Gold != 4000 {
		t.Fatalf("restarted state character=%+v found=%v gold=%d", current, found, wallet.Snapshot().Gold)
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
			inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
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
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
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
