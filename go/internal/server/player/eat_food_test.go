package player

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func foodTestService(t *testing.T, store stateio.Store) (*FoodService, *Inventory, *CharacterStore) {
	t.Helper()
	starter := &Starter{Version: "2.35.10"}
	inventory, err := OpenInventory(store, starter)
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 7}, {InvenIndex: 78, ID: 360, Level: 1, HP: 7}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.AttachMaxHealth(func(Character) (uint64, error) { return 100, nil }); err != nil {
		t.Fatal(err)
	}
	if err = characters.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.FoodDesign{Foods: map[uint64]gamedata.Food{101: {ID: 101, Point: 15}, 102: {ID: 102, Type: 1, Point: 10, FavoritePoint: 25, FavoriteUniqueCharIDs: []uint64{35}}, 103: {ID: 103, Type: 2}, 104: {ID: 104, Point: 100, FoodBuffID: 101}, 105: {ID: 105, Point: 50, RecoveryType: 1}}}
	s, err := OpenFoodService(store, design, inventory, characters)
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("food-session")
	if err = s.AttachContext(func() (int, error) { return 21, nil }, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	return s, inventory, characters
}

func autoFoodRequest(seq uint64, indices []uint64, items []Item) []byte {
	out := wire.AppendVarint(nil, 1, seq)
	for i, index := range indices {
		target := wire.AppendVarint(nil, 1, index)
		target = wire.AppendBytes(target, 2, ItemWire(items[i]))
		out = wire.AppendBytes(out, 2, target)
	}
	return out
}

func TestEatFoodAutoSharesStacksValidatesWholeRequestAndReplaysSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, inventory, characters := foodTestService(t, repo)
	stacks, err := inventory.GrantOnce("auto-food", []gamedata.BattleReward{{Type: 5, ID: 102, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []uint64{77, 78} {
		if err = characters.SetCurrentHealth(index, 0); err != nil {
			t.Fatal(err)
		}
	}
	stack := stacks[0]
	stack.Count = 2
	overspend := autoFoodRequest(1, []uint64{77, 78}, []Item{stack, stack})
	if _, _, _, err = s.Handle("/EatFoodAuto", overspend); err == nil {
		t.Fatal("accepted aggregate stack overspend")
	}
	if hp, _ := characters.CurrentHealth(77); hp != 0 {
		t.Fatal("partially healed rejected request")
	}
	if inventory.All()[0].Count != 3 {
		t.Fatal("partially consumed rejected request")
	}
	stack.Count = 1
	request := autoFoodRequest(2, []uint64{77, 78}, []Item{stack, stack})
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	code, body, _, err := s.Handle("/EatFoodAuto", request)
	if err != nil || code != 27 {
		t.Fatalf("auto code=%d err=%v", code, err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 25 {
		t.Fatalf("favorite hp=%d", hp)
	}
	if hp, _ := characters.CurrentHealth(78); hp != 10 {
		t.Fatalf("ordinary hp=%d", hp)
	}
	var resultIndices []uint64
	if err = wire.Walk(body, func(field wire.Field) error {
		index, _, err := wire.Varint(field.Value, 1)
		resultIndices = append(resultIndices, index)
		return err
	}); err != nil || len(resultIndices) != 2 || resultIndices[0] != 77 || resultIndices[1] != 78 {
		t.Fatalf("auto response=%v err=%v", resultIndices, err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	s, inventory, _ = foodTestService(t, repo)
	code, replay, _, err := s.Handle("/EatFoodAuto", request)
	if err != nil || code != 27 || !bytes.Equal(replay, body) || inventory.All()[0].Count != 1 {
		t.Fatalf("auto replay code=%d err=%v", code, err)
	}
	// Equal sequences on the two endpoints refer to separate requests.
	if _, _, _, err = s.Handle("/EatFood", foodRequest(2, 77, 0, stack)); err != nil {
		t.Fatalf("normal/auto sequence collision: %v", err)
	}
}

func TestEatFoodAutoRejectsEmptyTargetWithoutConsumingValidTarget(t *testing.T) {
	s, inventory, characters := foodTestService(t, stateio.NewMemory())
	stacks, err := inventory.GrantOnce("auto-empty", []gamedata.BattleReward{{Type: 5, ID: 101, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	request := autoFoodRequest(1, []uint64{77}, stacks)
	request = wire.AppendBytes(request, 2, wire.AppendVarint(nil, 1, 78))
	if _, _, _, err = s.Handle("/EatFoodAuto", request); err == nil {
		t.Fatal("accepted empty recovery target")
	}
	if inventory.All()[0].Count != 1 {
		t.Fatal("empty target consumed valid target food")
	}
	if hp, _ := characters.CurrentHealth(77); hp != 0 {
		t.Fatal("empty target partially healed valid target")
	}
}

func foodRequest(seq, index, pack uint64, items ...Item) []byte {
	out := wire.AppendVarint(nil, 1, seq)
	if pack != 0 {
		out = wire.AppendVarint(out, 2, pack)
	}
	out = wire.AppendVarint(out, 3, index)
	for _, item := range items {
		out = wire.AppendBytes(out, 4, ItemWire(item))
	}
	return out
}

func TestEatFoodSQLitePersistsRecoveryAndSequenceReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, inventory, characters := foodTestService(t, repo)
	stacks, err := inventory.GrantOnce("food", []gamedata.BattleReward{{Type: 5, ID: 101, Count: 4}, {Type: 5, ID: 102, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(77, 20); err != nil {
		t.Fatal(err)
	}
	stacks[0].Count = 2
	stacks[1].Count = 1
	request := foodRequest(10, 77, 0, stacks...)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	code, body, handled, err := s.Handle("/EatFood", request)
	if err != nil || code != 22 || !handled {
		t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if hp, err := characters.CurrentHealth(77); err != nil || hp != 75 {
		t.Fatalf("hp=%d err=%v", hp, err)
	}
	if c, _ := characters.Find(77); c.HP != 75 || characters.All()[0].HP != 75 {
		t.Fatal("current HP overwritten by maximum")
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	s, inventory, characters = foodTestService(t, repo)
	code, replayed, _, err := s.Handle("/EatFood", request)
	if err != nil || code != 22 || !bytes.Equal(body, replayed) {
		t.Fatalf("replay err=%v", err)
	}
	if items := inventory.All(); len(items) != 2 || items[0].Count != 2 || items[1].Count != 1 {
		t.Fatalf("retry consumed items %+v", items)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 75 {
		t.Fatalf("reopened hp=%d", hp)
	}
	request = foodRequest(10, 77, 0, stacks[0])
	if _, _, _, err = s.Handle("/EatFood", request); err == nil {
		t.Fatal("accepted changed request with reused sequence")
	}
}

func TestEatFoodRejectsInvalidStacksAndContextWithoutMutation(t *testing.T) {
	s, inventory, characters := foodTestService(t, stateio.NewMemory())
	stacks, err := inventory.GrantOnce("food", []gamedata.BattleReward{{Type: 5, ID: 101, Count: 2}, {Type: 5, ID: 103, Count: 2}, {Type: 5, ID: 104, Count: 2}, {Type: 5, ID: 999, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	wrongID := stacks[0]
	wrongID.ID = 102
	wrongType := stacks[0]
	wrongType.Type = 8
	over := stacks[0]
	over.Count = 3
	missing := stacks[0]
	missing.InvenIndex++
	requests := [][]byte{foodRequest(1, 999, 0, stacks[0]), foodRequest(2, 77, 22, stacks[0]), foodRequest(3, 77, 0, wrongID), foodRequest(4, 77, 0, wrongType), foodRequest(5, 77, 0, over), foodRequest(6, 77, 0, missing), foodRequest(7, 77, 0, stacks[1]), foodRequest(8, 77, 0, stacks[2]), foodRequest(9, 77, 0, stacks[3]), foodRequest(10, 77, 0, stacks[0], stacks[0]), foodRequest(11, 77, 0)}
	for i, request := range requests {
		if _, _, _, err := s.Handle("/EatFood", request); err == nil {
			t.Fatalf("invalid request %d accepted", i)
		}
	}
	if err := s.AttachContext(func() (int, error) { return 21, nil }, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/EatFood", foodRequest(12, 77, 0, stacks[0])); err == nil {
		t.Fatal("accepted food during battle")
	}
	if hp, _ := characters.CurrentHealth(77); hp != 0 {
		t.Fatalf("rejected request changed HP=%d", hp)
	}
	if got := inventory.All(); len(got) != 4 || got[0].Count != 2 {
		t.Fatalf("rejected request consumed inventory %+v", got)
	}
}

func TestEatFoodPercentageRecoversDeadCharacterAndClampsToMaximum(t *testing.T) {
	s, inventory, characters := foodTestService(t, stateio.NewMemory())
	stacks, err := inventory.GrantOnce("food", []gamedata.BattleReward{{Type: 5, ID: 105, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	stack := stacks[0]
	stack.Count = 1
	if _, _, _, err = s.Handle("/EatFood", foodRequest(1, 77, 21, stack)); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 50 {
		t.Fatalf("percentage hp=%d", hp)
	}
	stack.Count = 2
	if _, _, _, err = s.Handle("/EatFood", foodRequest(2, 77, 21, stack)); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 100 {
		t.Fatalf("clamped hp=%d", hp)
	}
	if len(inventory.All()) != 0 {
		t.Fatal("empty food stack remains")
	}
	if err = characters.SetCurrentHealth(77, 101); err == nil {
		t.Fatal("accepted health above maximum")
	}
}

func TestCurrentHealthGrowthAndImmortalClearPersistedInjury(t *testing.T) {
	_, inventory, characters := foodTestService(t, stateio.NewMemory())
	stacks, err := inventory.GrantOnce("growth-food-health", []gamedata.BattleReward{{Type: 8, ID: 1, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	characters.grow = func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return 2, 0, nil, nil
	}
	if err = characters.SetCurrentHealth(77, 3); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77)
	request = wire.AppendBytes(request, 3, ItemWire(stacks[0]))
	if _, _, _, err = characters.Handle("/CharGrowth", request); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 100 {
		t.Fatalf("grown current HP=%d", hp)
	}
	if err = characters.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	request = wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 77)
	target, _, _ := wire.Varint(request, 2)
	candidate, _ := characters.Find(target)
	if err := characters.AttachImmortalDesign(&gamedata.ImmortalDesign{Characters: map[uint64]uint64{candidate.ID: 42}, FullRestore: map[[2]uint64]bool{{42, candidate.TalentLevel}: true}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = characters.Handle("/CharImmortal", request); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 100 {
		t.Fatalf("revived current HP=%d", hp)
	}
}

func TestCurrentHealthRetainsSavedCharacterHPWithoutSeparateEntryAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := []Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 17}, {InvenIndex: 78, ID: 360, Level: 1, HP: 0}}
	open := func(store stateio.Store) *CharacterStore {
		inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		characters, err := OpenCharacterStore(store, seed, inventory, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = characters.AttachMaxHealth(func(Character) (uint64, error) { return 500, nil }); err != nil {
			t.Fatal(err)
		}
		if err = characters.EnsurePersisted(); err != nil {
			t.Fatal(err)
		}
		return characters
	}
	check := func(characters *CharacterStore) {
		for _, saved := range seed {
			hp, err := characters.CurrentHealth(saved.InvenIndex)
			if err != nil || hp != saved.HP {
				t.Fatalf("current %d=%d want=%d err=%v", saved.InvenIndex, hp, saved.HP, err)
			}
			character, found := characters.Find(saved.InvenIndex)
			if !found || character.HP != saved.HP {
				t.Fatalf("Find=%+v found=%v", character, found)
			}
		}
		all := characters.All()
		if len(all) != 2 || all[0].HP != 17 || all[1].HP != 0 {
			t.Fatalf("All=%+v", all)
		}
		maximum, err := characters.MaxHealth(77)
		if err != nil || maximum != 500 {
			t.Fatalf("maximum=%d err=%v", maximum, err)
		}
	}
	characters := open(repo)
	check(characters)
	rows, err := repo.ListEntries("characters", "current_hp")
	if err != nil || len(rows) != 0 {
		t.Fatalf("reads created entries=%v err=%v", rows, err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	characters = open(repo)
	check(characters)
	if err = characters.SetCurrentHealth(77, 300); err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(78, 0); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 300 {
		t.Fatalf("explicit health entry lost=%d", hp)
	}
	// Revival must write the restored value, since deleting the entry alone
	// would expose the zero HP in the owned character record again.
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 78)
	target, _, _ := wire.Varint(request, 2)
	candidate, _ := characters.Find(target)
	if err := characters.AttachImmortalDesign(&gamedata.ImmortalDesign{Characters: map[uint64]uint64{candidate.ID: 42}, FullRestore: map[[2]uint64]bool{{42, candidate.TalentLevel}: true}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = characters.Handle("/CharImmortal", request); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(78); hp != 500 {
		t.Fatalf("revived health=%d", hp)
	}
}

func TestEatFoodRecoversSavedHPWithoutSeparateCurrentHealthEntry(t *testing.T) {
	food, inventory, characters := foodTestService(t, stateio.NewMemory())
	// The seed has current HP 7 and maximum 100, with no current_hp entry.
	stacks, err := inventory.GrantOnce("saved-health-food", []gamedata.BattleReward{{Type: 5, ID: 101, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = food.Handle("/EatFood", foodRequest(1, 77, 21, stacks[0])); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 22 {
		t.Fatalf("recovered saved health=%d want22", hp)
	}
	if len(inventory.All()) != 0 {
		t.Fatal("food was not consumed")
	}
}

type failFoodStore struct {
	stateio.AtomicEntryStore
	fail bool
}

func (s *failFoodStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail && domain == "characters" {
		return errors.New("injected food save failure")
	}
	return s.AtomicEntryStore.SaveWithEntries(domain, core, changes)
}

func TestEatFoodSQLiteRollbackRestoresInventoryHealthAndReplayLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &failFoodStore{AtomicEntryStore: repo}
	s, inventory, characters := foodTestService(t, store)
	stacks, err := inventory.GrantOnce("food", []gamedata.BattleReward{{Type: 5, ID: 101, Count: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err = characters.SetCurrentHealth(77, 1); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	request := foodRequest(1, 77, 21, stacks[0])
	if _, _, _, err = s.Handle("/EatFood", request); err == nil {
		t.Fatal("injected failure was ignored")
	}
	if err = op.Rollback(); !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatalf("expected recovery fencing, got %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	s, inventory, characters = foodTestService(t, repo)
	if inventory.All()[0].Count != 1 {
		t.Fatal("rollback lost inventory")
	}
	if hp, _ := characters.CurrentHealth(77); hp != 1 {
		t.Fatalf("rollback HP=%d", hp)
	}
	ledger, err := repo.ListEntries("characters", "food_requests")
	if err != nil || len(ledger) != 0 {
		t.Fatalf("rollback ledger=%v err=%v", ledger, err)
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/EatFood", request); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	if hp, _ := characters.CurrentHealth(77); hp != 16 {
		t.Fatalf("recovery after rollback HP=%d", hp)
	}
}
