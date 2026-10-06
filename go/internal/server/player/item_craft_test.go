package player

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func craftFixture(t *testing.T, store stateio.Store, class uint64) (*ItemCraftService, *Inventory, *CharacterStore, *Wallet) {
	t.Helper()
	items, e := OpenInventory(store, &Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = items.GrantOnce("materials", []gamedata.BattleReward{{Type: 8, ID: 1, Count: 200}, {Type: 5, ID: 1, Count: 20}, {Type: 8, ID: 2, Count: 2}})
	if e != nil {
		t.Fatal(e)
	}
	chars, e := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 350, Level: 1, TalentLevel: 2}}, items, "", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = chars.EnsurePersisted(); e != nil {
		t.Fatal(e)
	}
	wallet, e := OpenWallet(store, Currency{Catalyst: 100})
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.EnsurePersisted(); e != nil {
		t.Fatal(e)
	}
	talents := &gamedata.TalentUseDesign{Characters: map[uint64]gamedata.TalentUseCharacter{350: {Group: 42, MaxLevel: 2}}, Rules: map[[2]uint64]gamedata.TalentUseRule{{42, 1}: {Class: class, Catalyst: 2, Experience: 3, Values: []float64{100, 100}}, {42, 2}: {Class: class, Catalyst: 4, Experience: 6, Values: []float64{100, 100}}}, Growth: &gamedata.TalentGrowthDesign{Characters: map[uint64]gamedata.CharacterTalent{350: {GrowthGroup: 6, MaxLevel: 2}}, Levels: map[[2]uint64]gamedata.TalentGrowthLevel{{6, 1}: {NeedExp: 10}, {6, 2}: {NeedExp: 10}}}}
	d := &gamedata.ItemCraftDesign{Cooking: map[uint64]gamedata.ItemCraftRecipe{101: {ID: 101, Class: 7, TalentLevel: 1, Result: gamedata.BattleReward{Type: 5, ID: 2, Count: 1}, Costs: []gamedata.PromotionCost{{Type: 5, ID: 1, Count: 2}}}}, Alchemy: map[uint64]gamedata.ItemCraftRecipe{101: {ID: 101, Class: 8, Category: 1, TalentLevel: 1, Result: gamedata.BattleReward{Type: 8, ID: 2, Count: 1}, Costs: []gamedata.PromotionCost{{Type: 8, ID: 1, Count: 5}}}, 102: {ID: 102, Class: 8, Category: 1, TalentLevel: 2, Result: gamedata.BattleReward{Type: 8, ID: 3, Count: 1}, Costs: []gamedata.PromotionCost{{Type: 8, ID: 2, Count: 3}}}, 103: {ID: 103, Class: 8, Category: 2, TalentLevel: 1, Result: gamedata.BattleReward{Type: 8, ID: 4, Count: 10}, Costs: []gamedata.PromotionCost{{Type: 8, ID: 1, Count: 10}}}}}
	s, e := NewItemCraftService(d, talents, store, items, chars, wallet, func(id uint64) bool { return id == 101 })
	if e != nil {
		t.Fatal(e)
	}
	s.BeginSession("craft")
	s.AttachContext(func() (int, bool, error) { return 21, false, nil })
	return s, items, chars, wallet
}

func TestItemCraftAgainstInstalledGameData(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("set BD2_REAL_GAMEDATA for current GameData integration")
	}
	d, e := gamedata.LoadItemCraftDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	talents, e := gamedata.LoadTalentUseDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	for _, class := range []uint64{7, 8} {
		t.Run(map[uint64]string{7: "Cooking", 8: "Alchemy"}[class], func(t *testing.T) {
			var producers []uint64
			for id, c := range talents.Characters {
				r, ok := talents.Rules[[2]uint64{c.Group, 1}]
				if ok && r.Class == class {
					producers = append(producers, id)
				}
			}
			slices.Sort(producers)
			if len(producers) == 0 {
				t.Fatal("missing actual producers")
			}
			for _, id := range producers {
				store := stateio.NewMemory()
				items, e := OpenInventory(store, &Starter{Version: "2.35.10"})
				if e != nil {
					t.Fatal(e)
				}
				chars, e := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: id, Level: 1, TalentLevel: 1}}, items, root, "20260923193640")
				if e != nil {
					t.Fatal(e)
				}
				wallet, e := OpenWallet(store, Currency{Catalyst: 1000})
				if e != nil {
					t.Fatal(e)
				}
				recipes := d.Cooking
				path := "/Cooking"
				if class == 8 {
					recipes = d.Alchemy
					path = "/Alchemy"
				}
				var ids []uint64
				for rid, r := range recipes {
					if r.TalentLevel == 1 {
						ids = append(ids, rid)
					}
				}
				slices.Sort(ids)
				if len(ids) == 0 {
					t.Fatal("missing level1 recipe")
				}
				r := recipes[ids[0]]
				var materials []gamedata.BattleReward
				for _, c := range r.Costs {
					materials = append(materials, gamedata.BattleReward{Type: c.Type, ID: c.ID, Count: c.Count}) //nolint:staticcheck // S1016
				}
				if _, e = items.GrantOnce("materials", materials); e != nil {
					t.Fatal(e)
				}
				s, e := NewItemCraftService(d, talents, store, items, chars, wallet, func(uint64) bool { return true })
				if e != nil {
					t.Fatal(e)
				}
				s.BeginSession("real")
				s.AttachContext(func() (int, bool, error) { return 1, false, nil })
				_, _, _, e = s.Handle(path, craftRequest(items, r.ID, 1, materials...))
				if e != nil {
					t.Fatalf("actual producer %d recipe %d: %v", id, r.ID, e)
				}
				if craftCount(items, r.Result.Type, r.Result.ID) != r.Result.Count {
					t.Fatal("actual result missing")
				}
			}
		})
	}
}
func craftRequest(items *Inventory, recipe, count uint64, costs ...gamedata.BattleReward) []byte {
	b := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77), 3, recipe), 4, count)
	for _, cost := range costs {
		for _, item := range items.All() {
			if item.Type == cost.Type && item.ID == cost.ID {
				item.Count = cost.Count
				b = wire.AppendBytes(b, 5, ItemWire(item))
				break
			}
		}
	}
	return b
}
func craftCount(items *Inventory, kind, id uint64) uint64 {
	var n uint64
	for _, v := range items.All() {
		if v.Type == kind && v.ID == id {
			n += v.Count
		}
	}
	return n
}

func TestItemCraftChargesRecipeLevelClampsExperienceAndReplaysAfterRestart(t *testing.T) {
	for _, class := range []uint64{7, 8} {
		t.Run(map[uint64]string{7: "Cooking", 8: "Alchemy"}[class], func(t *testing.T) {
			store := stateio.NewMemory()
			s, items, chars, wallet := craftFixture(t, store, class)
			kind := uint64(8)
			path := "/Alchemy"
			if class == 7 {
				kind = 5
				path = "/Cooking"
			}
			input := uint64(10)
			if class == 7 {
				input = 4
			}
			b := craftRequest(items, 101, 2, gamedata.BattleReward{Type: kind, ID: 1, Count: input})
			_, out, _, e := s.Handle(path, b)
			if e != nil {
				t.Fatal(e)
			}
			c, _ := chars.Find(77)
			if wallet.Snapshot().Catalyst != 96 || c.TalentExp != 12 || craftCount(items, kind, 2) != map[uint64]uint64{7: 2, 8: 4}[class] {
				t.Fatalf("bad craft settlement: wallet=%+v character=%+v items=%+v", wallet.Snapshot(), c, items.All())
			}
			next, _, _, againWallet := craftFixture(t, store, class)
			_, again, _, e := next.Handle(path, b)
			if e != nil || !bytes.Equal(out, again) || againWallet.Snapshot().Catalyst != 96 {
				t.Fatalf("craft replay changed: %v", e)
			}
			if _, _, _, e = next.Handle(path, wire.AppendVarint(b, 4, 3)); e == nil {
				t.Fatal("changed/duplicate scalar accepted")
			}
		})
	}
}
func TestAlchemyBatchUsesIntermediateInventoryAndValidatesWholeGraph(t *testing.T) {
	s, items, chars, wallet := craftFixture(t, stateio.NewMemory(), 8)
	b := craftRequest(items, 102, 2, gamedata.BattleReward{Type: 8, ID: 2, Count: 2}, gamedata.BattleReward{Type: 8, ID: 1, Count: 20})
	_, out, _, e := s.Handle("/AlchemyBatch", b)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := chars.Find(77)
	if craftCount(items, 8, 3) != 2 || craftCount(items, 8, 2) != 0 || craftCount(items, 8, 1) != 180 || wallet.Snapshot().Catalyst != 84 || c.TalentExp != 20 {
		t.Fatalf("batch wrong settlement: %+v %+v %+v", items.All(), wallet.Snapshot(), c)
	}
	_, again, _, e := s.Handle("/AlchemyBatch", b)
	if e != nil || !bytes.Equal(out, again) || craftCount(items, 8, 3) != 2 {
		t.Fatal("batch replay changed")
	}
	for _, path := range []string{"/Alchemy", "/AlchemyBatch"} {
		s, items, _, wallet = craftFixture(t, stateio.NewMemory(), 8)
		bad := craftRequest(items, 102, 2, gamedata.BattleReward{Type: 8, ID: 1, Count: 1})
		if _, _, _, e = s.Handle(path, bad); e == nil || wallet.Snapshot().Catalyst != 100 || craftCount(items, 8, 1) != 200 {
			t.Fatalf("underpaid craft accepted by %s", path)
		}
	}
}
func TestAlchemyConversionChargesProducedQuantity(t *testing.T) {
	s, items, _, wallet := craftFixture(t, stateio.NewMemory(), 8)
	b := craftRequest(items, 103, 2, gamedata.BattleReward{Type: 8, ID: 1, Count: 20})
	if _, _, _, e := s.Handle("/Alchemy", b); e != nil {
		t.Fatal(e)
	}
	if craftCount(items, 8, 4) != 20 || wallet.Snapshot().Catalyst != 60 {
		t.Fatal("conversion output/catalyst mismatch")
	}
}

// Equipment making requests missing intermediate resources in one batch; the
// amount can exceed the ordinary alchemy slider limit. Check settlement and
// replay using the same SQLite transaction boundary as the request dispatcher.
func TestAlchemyBatchSQLiteCanExceedOrdinaryCraftLimit(t *testing.T) {
	repo, err := accountstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	s, items, chars, wallet := craftFixture(t, repo, 8)
	rule := s.talents.Rules[[2]uint64{42, 2}]
	rule.Values[0] = 1
	s.talents.Rules[[2]uint64{42, 2}] = rule
	request := craftRequest(items, 102, 2, gamedata.BattleReward{Type: 8, ID: 2, Count: 2}, gamedata.BattleReward{Type: 8, ID: 1, Count: 20})
	if _, _, _, err = s.Handle("/Alchemy", request); err == nil {
		t.Fatal("ordinary alchemy exceeded its slider limit")
	}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, response, _, err := s.Handle("/AlchemyBatch", request)
	if err != nil {
		_ = op.Rollback()
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
	c, _ := chars.Find(77)
	if craftCount(items, 8, 3) != 2 || wallet.Snapshot().Catalyst != 84 || c.TalentExp != 20 {
		t.Fatalf("batch settlement items=%+v currency=%+v producer=%+v", items.All(), wallet.Snapshot(), c)
	}
	next, reloadedItems, reloadedChars, reloadedWallet := craftFixture(t, repo, 8)
	_, replay, _, err := next.Handle("/AlchemyBatch", request)
	if err != nil || !bytes.Equal(response, replay) {
		t.Fatalf("persisted replay response=%x err=%v", replay, err)
	}
	c, _ = reloadedChars.Find(77)
	if craftCount(reloadedItems, 8, 3) != 2 || reloadedWallet.Snapshot().Catalyst != 84 || c.TalentExp != 20 {
		t.Fatal("persisted replay changed settlement")
	}
}

type failCraftReceipt struct{ stateio.Store }

func (s failCraftReceipt) Save(name string, b []byte) error {
	if name == "itemcraft" {
		return errors.New("receipt unavailable")
	}
	return s.Store.Save(name, b)
}
func TestItemCraftFinalReceiptFailureRollsBackEveryDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s, items, _, _ := craftFixture(t, repo, 8)
	s.store = failCraftReceipt{repo}
	b := craftRequest(items, 101, 2, gamedata.BattleReward{Type: 8, ID: 1, Count: 10})
	op, e := repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.Handle("/Alchemy", b); e == nil {
		t.Fatal("failed receipt accepted")
	}
	if e = op.Rollback(); e != nil && !errors.Is(e, stateio.ErrStateRecoveryRequired) {
		t.Fatal(e)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, e = accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	next, items, chars, wallet := craftFixture(t, repo, 8)
	c, _ := chars.Find(77)
	if c.TalentExp != 0 || wallet.Snapshot().Catalyst != 100 || craftCount(items, 8, 1) != 200 || craftCount(items, 8, 2) != 2 {
		t.Fatal("partial craft survived rollback")
	}
	op, e = repo.BeginOperation()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = next.Handle("/Alchemy", b); e != nil {
		t.Fatal(e)
	}
	if e = op.Commit(); e != nil {
		t.Fatal(e)
	}
}
