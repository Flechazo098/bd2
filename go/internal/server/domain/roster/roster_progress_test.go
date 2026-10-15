//go:build integration

package roster_test

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/stateio"
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

type progressFixture struct {
	repo       *accountstate.Repository
	tx         *accountstate.CommandStore
	ctx        command.Context
	service    *roster.RosterProgressService
	collection *roster.CollectionStore
	characters *roster.CharacterStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet
}

func openProgress(t *testing.T, path string, d *gamedata.RosterProgressDesign, p *gamedata.CostumePotentialDesign, chars []roster.Character, costumes []roster.Costume) *progressFixture {
	t.Helper()
	r, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := r.BeginCommand(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback(); _ = r.Close() })
	ctx := command.Context{Identity: command.Identity{AccountID: "roster-contract", SessionID: "session"}, State: tx}
	ctx.Emit = func(event command.Event) error { return outbox.Publish(tx, ctx.AccountID, event) }
	scope := stateio.RootStore{}
	i, e := assets.OpenInventory(ctx, scope, nil)
	if e != nil {
		t.Fatal(e)
	}
	stack, e := gamedata.LoadItemStackDesign(filepath.Join("..", "..", "..", "..", "..", "data", "resources", "GameData"), versionconfig.GameData())
	if e != nil {
		t.Fatal(e)
	}
	if e = i.AttachItemStacks(stack); e != nil {
		t.Fatal(e)
	}
	w, e := assets.OpenWallet(ctx, scope, assets.Currency{Gold: 100000, FreeJewelry: 100})
	if e != nil {
		t.Fatal(e)
	}
	col, e := roster.OpenCollectionStore(ctx, scope, costumes)
	if e != nil {
		t.Fatal(e)
	}
	c, e := roster.OpenCharacterStore(ctx, scope, chars, i, filepath.Join("..", "..", "..", "..", "..", "data", "resources", "GameData"), versionconfig.GameData())
	if e != nil {
		t.Fatal(e)
	}
	if e = c.AttachCollection(ctx, col); e != nil {
		t.Fatal(e)
	}
	if e = col.BindBaseCharacters(ctx, chars); e != nil {
		t.Fatal(e)
	}
	s, e := roster.NewRosterProgressService(d, p, c, col, i, w)
	if e != nil {
		t.Fatal(e)
	}
	for _, persist := range []func(command.Context) error{i.EnsurePersisted, w.EnsurePersisted, col.EnsurePersisted, c.EnsurePersisted} {
		if e = persist(ctx); e != nil {
			t.Fatal(e)
		}
	}
	return &progressFixture{r, tx, ctx, s, col, c, i, w}
}
func currentRosterDesign(t *testing.T) (*gamedata.RosterProgressDesign, *gamedata.CostumePotentialDesign) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "..", "data", "resources", "GameData")
	d, e := gamedata.LoadRosterProgressDesign(root, versionconfig.GameData())
	if e != nil {
		t.Fatal(e)
	}
	p, e := gamedata.LoadCostumePotentialDesign(root, versionconfig.GameData())
	if e != nil {
		t.Fatal(e)
	}
	return d, p
}
func rosterRequest(seq, index, third uint64) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, index)
	return wire.AppendVarint(b, 3, third)
}
func callProgress(t *testing.T, f *progressFixture, path string, b []byte) {
	t.Helper()
	_, _, handled, e := f.service.Handle(f.ctx, path, b)
	if !handled || e != nil {
		t.Fatalf("%s: handled=%v error=%v", path, handled, e)
	}
}

func TestPartnerBattleGrowthClaimsAndDecreasesSurviveReopen(t *testing.T) {
	d, p := currentRosterDesign(t)
	chars := []roster.Character{{InvenIndex: 101, ID: 10, Level: 1, HP: 100, SolidarityReward: 1}, {InvenIndex: 102, ID: 20, Level: 1, HP: 100, SolidarityReward: 1}}
	path := filepath.Join(t.TempDir(), "state.db")
	f := openProgress(t, path, d, p, chars, nil)
	before := f.wallet.Snapshot(f.ctx)
	_, _, _, e := f.service.Handle(f.ctx, "/CharPartnerStoryReward", rosterRequest(1, 101, 102))
	if e == nil {
		t.Fatal("unearned story reward accepted")
	}
	if f.wallet.Snapshot(f.ctx) != before {
		t.Fatal("locked story changed wallet")
	}
	for battle := range 91 {
		if _, e = f.service.ApplyBattlePartners(f.ctx, fmt.Sprintf("clear-%d", battle), []uint64{101, 102}, true); e != nil {
			t.Fatal(e)
		}
	}
	story := rosterRequest(2, 101, 102)
	callProgress(t, f, "/CharPartnerStoryReward", story)
	if got := f.wallet.Snapshot(f.ctx).Gold; got != before.Gold+2000 {
		t.Fatalf("story gold %d, want %d from current Lathel partner reward", got, before.Gold+2000)
	}
	callProgress(t, f, "/CharPartnerReward", rosterRequest(3, 101, 2))
	if got := f.wallet.Snapshot(f.ctx).FreeJewelry; got != before.FreeJewelry+2 {
		t.Fatalf("solidarity step2 jewelry %d, want %d", got, before.FreeJewelry+2)
	}
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, chars, nil)
	callProgress(t, f, "/CharPartnerStoryReward", story)
	if f.wallet.Snapshot(f.ctx).Gold != before.Gold+2000 {
		t.Fatal("reopened story retry duplicated gold")
	}
	if _, e = f.service.ApplyBattlePartners(f.ctx, "clear-0", []uint64{101, 102}, true); e != nil {
		t.Fatal(e)
	}
	rule := d.Partners[1]
	rule.Weight = -20
	d.Partners[1] = rule
	changed, e := f.service.ApplyBattlePartners(f.ctx, "lost-signed-weight", []uint64{101, 102}, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(changed) != 1 || changed[0].Point != 995 || changed[0].Reward != 1 {
		t.Fatalf("loss did not decrease affinity or retain story claim: %+v", changed)
	}
	if _, e = f.service.ApplyBattlePartners(f.ctx, "second-loss", []uint64{101, 102}, false); e != nil {
		t.Fatal(e)
	}
	changed, e = f.service.ApplyBattlePartners(f.ctx, "negative-weight-win", []uint64{101, 102}, true)
	if e != nil || len(changed) != 1 || changed[0].Point != 998 {
		t.Fatalf("negative weight must reduce win gain to8: %+v %v", changed, e)
	}
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, chars, nil)
	assertCurrentPartner := func(seq uint64) {
		t.Helper()
		_, info, handled, err := f.service.Handle(f.ctx, "/CharPartnerInfo", rosterRequest(seq, 0, 0))
		if err != nil || !handled {
			t.Fatalf("partner query: %v", err)
		}
		var found bool
		_ = wire.Walk(info, func(field wire.Field) error {
			if field.Number == 1 && field.Type == 2 {
				main, _, _ := wire.Varint(field.Value, 1)
				sub, _, _ := wire.Varint(field.Value, 2)
				if main == 1 && sub == 2 {
					point, _, _ := wire.Varint(field.Value, 3)
					claimed, _, _ := wire.Varint(field.Value, 4)
					if point != 998 || claimed != 1 {
						t.Fatal("current partner progress or story claim lost on reopen")
					}
					found = true
				}
			}
			return nil
		})
		if !found {
			t.Fatal("current partner relationship absent after reopen")
		}
	}
	assertCurrentPartner(20)
	changed, e = f.service.ApplyBattlePartners(f.ctx, "lost-signed-weight", []uint64{101, 102}, false)
	if e != nil || len(changed) != 1 || changed[0].Point != 995 {
		t.Fatalf("decrease receipt failed after reopen: %+v %v", changed, e)
	}
	assertCurrentPartner(21)
	c, ok := f.characters.Find(f.ctx, 101)
	if !ok || c.SolidarityReward != 2 {
		t.Fatalf("claimed step did not persist: %+v", c)
	}
}

func TestCostumeUpgradeConsumesExactAssetsAndLockReopens(t *testing.T) {
	d, p := currentRosterDesign(t)
	var id, uintID, charID uint64
	for candidate, r := range d.Costumes {
		if r.Maximum < 2 || len(d.Growth[[2]uint64{r.Group, 1}]) == 0 || len(p.Nodes[candidate]) == 0 {
			continue
		}
		if id == 0 || candidate < id {
			id = candidate
			uintID = r.Unique
		}
	}
	for candidate, u := range p.CharacterUnique {
		if u == uintID && p.CharacterGrade[candidate] == 1 && (charID == 0 || candidate < charID) {
			charID = candidate
		}
	}
	if id == 0 || charID == 0 {
		t.Fatal("current GameData lacks permanent upgrade fixture")
	}
	chars := []roster.Character{{InvenIndex: 101, ID: charID, Level: 1, HP: 100, CostumeID: id, UseCostume: 201}}
	costumes := []roster.Costume{{InvenIndex: 201, ID: id, Level: 1, UseChar: 101}}
	path := filepath.Join(t.TempDir(), "state.db")
	f := openProgress(t, path, d, p, chars, costumes)
	costs := d.Growth[[2]uint64{d.Costumes[id].Group, 1}]
	var items []gamedata.BattleReward
	var gold uint64
	for _, r := range costs {
		if r.Type == 4 {
			gold += r.Count
		} else {
			items = append(items, gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count + 1})
		}
	}
	granted, e := f.inventory.GrantOnce(f.ctx, "upgrade-fixture", items)
	if e != nil {
		t.Fatal(e)
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 201)
	for _, r := range costs {
		item := assets.Item{Type: r.Type, ID: r.ID, Count: r.Count}
		for _, g := range granted {
			if g.Type == r.Type && g.ID == r.ID {
				item.InvenIndex = g.InvenIndex
			}
		}
		req = wire.AppendBytes(req, 3, assets.ItemWire(item))
	}
	before := f.wallet.Snapshot(f.ctx).Gold
	callProgress(t, f, "/CostumeUpgrade", req)
	if f.wallet.Snapshot(f.ctx).Gold != before-gold {
		t.Fatal("upgrade did not debit exact GameData gold")
	}
	for _, r := range items {
		var count uint64
		for _, v := range f.inventory.All(f.ctx) {
			if v.Type == r.Type && v.ID == r.ID {
				count += v.Count
			}
		}
		if count != 1 {
			t.Fatalf("upgrade resource remainder %d, want1", count)
		}
	}
	var nodes []uint64
	var skill uint64
	for n, v := range p.Nodes[id] {
		nodes = append(nodes, n)
		if v.NodeType >= 3 && (skill == 0 || n < skill) {
			skill = n
		}
	}
	if skill == 0 {
		t.Fatal("fixture has no skill potential")
	}
	if e = f.collection.ActivateCostumePotential(f.ctx, 201, nodes); e != nil {
		t.Fatal(e)
	}
	lock := wire.AppendVarint(rosterRequest(2, 201, skill), 4, 1)
	callProgress(t, f, "/CostumeSkillPotentialLock", lock)
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, chars, costumes)
	callProgress(t, f, "/CostumeUpgrade", req)
	c, ok := f.collection.CostumeByIndex(201)
	if !ok || c.Level != 2 || len(c.PotentialLockIDs) != 1 || c.PotentialLockIDs[0] != skill {
		t.Fatalf("upgrade/skill lock did not reopen: %+v", c)
	}
	if f.wallet.Snapshot(f.ctx).Gold != before-gold {
		t.Fatal("reopened upgrade retry charged gold twice")
	}
}

func TestClassUpConsumesPromotionMaterialsAndPersistsStage(t *testing.T) {
	d, p := currentRosterDesign(t)
	chars := []roster.Character{{InvenIndex: 101, ID: 10, Level: 20, HP: 100}}
	path := filepath.Join(t.TempDir(), "state.db")
	f := openProgress(t, path, d, p, chars, nil)
	next, costs, e := gamedata.CharacterPromotion(filepath.Join("..", "..", "..", "..", "..", "data", "resources", "GameData"), versionconfig.GameData(), 10, 20, 0)
	if e != nil {
		t.Fatal(e)
	}
	var items []gamedata.BattleReward
	var gold uint64
	for _, r := range costs {
		if r.Type == 4 {
			gold += r.Count
		} else {
			items = append(items, gamedata.BattleReward{Type: r.Type, ID: r.ID, Count: r.Count + 1})
		}
	}
	granted, e := f.inventory.GrantOnce(f.ctx, "promotion-fixture", items)
	if e != nil {
		t.Fatal(e)
	}
	req := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 101)
	for _, r := range costs {
		item := assets.Item{Type: r.Type, ID: r.ID, Count: r.Count}
		for _, g := range granted {
			if g.Type == r.Type && g.ID == r.ID {
				item.InvenIndex = g.InvenIndex
			}
		}
		req = wire.AppendBytes(req, 3, assets.ItemWire(item))
	}
	before := f.wallet.Snapshot(f.ctx).Gold
	callProgress(t, f, "/CharClassUp", req)
	if f.wallet.Snapshot(f.ctx).Gold != before-gold {
		t.Fatal("class-up charged incorrect gold")
	}
	for _, r := range items {
		var count uint64
		for _, v := range f.inventory.All(f.ctx) {
			if v.Type == r.Type && v.ID == r.ID {
				count += v.Count
			}
		}
		if count != 1 {
			t.Fatalf("promotion resource remainder %d, want1", count)
		}
	}
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, chars, nil)
	callProgress(t, f, "/CharClassUp", req)
	c, ok := f.characters.Find(f.ctx, 101)
	if !ok || c.ID != next || c.Level != 20 || c.Exp != 0 {
		t.Fatalf("class-up stage or retained progress wrong: %+v", c)
	}
	if f.wallet.Snapshot(f.ctx).Gold != before-gold {
		t.Fatal("class-up replay charged twice")
	}
}
func TestRecommendationsUseCompletedOwnedSnapshotAndPersistOptions(t *testing.T) {
	d, p := currentRosterDesign(t)
	chars := []roster.Character{{InvenIndex: 101, ID: 10, Level: 1, HP: 100, UseCostume: 201, CostumeID: 101}, {InvenIndex: 102, ID: 20, Level: 1, HP: 100, UseCostume: 202, CostumeID: 201}}
	costumes := []roster.Costume{{InvenIndex: 201, ID: 101, Level: 1, UseChar: 101}, {InvenIndex: 202, ID: 201, Level: 1, UseChar: 102}}
	path := filepath.Join(t.TempDir(), "state.db")
	f := openProgress(t, path, d, p, chars, costumes)
	seed := deck.Seed{Version: versionconfig.State(), FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: 101, CostumeInvenIndex: 201}}}
	openDeck := func(f *progressFixture) (*deck.Store, *assets.EquipmentInventory) {
		s, e := deck.OpenStore(f.ctx, stateio.RootStore{}, seed)
		if e != nil {
			t.Fatal(e)
		}
		equipment, e := assets.OpenEquipmentInventory(f.ctx, stateio.RootStore{})
		if e != nil {
			t.Fatal(e)
		}
		if e = equipment.EnsurePersisted(f.ctx); e != nil {
			t.Fatal(e)
		}
		if e = s.AttachPresetRuntime(f.ctx, f.wallet, f.characters, equipment, f.collection); e != nil {
			t.Fatal(e)
		}
		if e = s.EnsurePersisted(f.ctx); e != nil {
			t.Fatal(e)
		}
		if e = s.AttachRecommendation(func(command.Context) (deck.RecommendIdentity, error) {
			return deck.RecommendIdentity{OwnerIndex: 123, UserID: "actual-owner"}, nil
		}, func(ctx command.Context, c []roster.Character) (uint64, error) {
			return f.service.TeamBattlePower(ctx, c, equipment.All(ctx))
		}); e != nil {
			t.Fatal(e)
		}
		return s, equipment
	}
	ds, _ := openDeck(f)
	scenario := deck.RecommendContext{BattleMode: 1, PackID: 1, BattleIndex: 1, Level: 1, TeamCountType: 1, DeckType: 1}
	query := wire.AppendVarint(nil, 1, 1)
	for i, v := range []uint64{1, 1, 1, 1, 1, 1, 1} {
		query = wire.AppendVarint(query, i+2, v)
	}
	_, body, _, e := ds.Handle(f.ctx, "/RecommendDeckInfo", query)
	if e != nil || len(body) != 0 {
		t.Fatalf("empty real history fabricated recommendations: %x %v", body, e)
	}
	entries := []deck.DeckEntry{{CharacterInvenIndex: 101, CostumeInvenIndex: 0, Slot: 1}, {CharacterInvenIndex: 102, CostumeInvenIndex: 1, Slot: 2}}
	if e = ds.PrepareRecommendation(f.ctx, "verified-clear", scenario, entries); e != nil {
		t.Fatal(e)
	}
	if e = ds.CompleteRecommendation(f.ctx, "verified-clear", true); e != nil {
		t.Fatal(e)
	}
	beforeDeck := ds.CurrentDeck()
	_, body, _, e = ds.Handle(f.ctx, "/RecommendDeckInfo", query)
	if e != nil {
		t.Fatal(e)
	}
	var ownerRow, base []byte
	if e = wire.Walk(body, func(field wire.Field) error {
		if field.Number == 1 {
			ownerRow = field.Value
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	owner, _, e := wire.Varint(ownerRow, 1)
	if e != nil || owner != 123 {
		t.Fatalf("recommendation owner wrong: %d %v", owner, e)
	}
	if e = wire.Walk(ownerRow, func(field wire.Field) error {
		if field.Number == 9 {
			var err error
			base, err = base64.StdEncoding.DecodeString(string(field.Value))
			return err
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	power, _, e := wire.Varint(base, 1)
	if e != nil || power != 820 {
		t.Fatalf("client formula level1+one level1 costume per participant should give820, got%d %v", power, e)
	}
	if !reflect.DeepEqual(beforeDeck, ds.CurrentDeck()) {
		t.Fatal("recommendation query changed active deck")
	}
	options := wire.AppendBytes(wire.AppendVarint(nil, 1, 2), 2, wire.AppendVarint(nil, 2, 1))
	if _, _, _, e = ds.Handle(f.ctx, "/RecommendDeckUserOptionSave", options); e != nil {
		t.Fatal(e)
	}
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, chars, costumes)
	ds, _ = openDeck(f)
	saved, e := ds.RecommendationOptions(f.ctx)
	if e != nil || saved.IsSave || !saved.OnlyOwned {
		t.Fatalf("recommendation options did not reopen: %+v %v", saved, e)
	}
	_, body, _, e = ds.Handle(f.ctx, "/RecommendDeckInfo", query)
	if e != nil || len(body) == 0 {
		t.Fatalf("completed recommendation did not reopen: %x %v", body, e)
	}
}

func TestExpiredCharmRemovesOwnedInstanceAndReplacesEmptyFieldParty(t *testing.T) {
	d, p := currentRosterDesign(t)
	temporary := roster.CharmCharacterIndexBase + 1
	permanent := roster.Character{InvenIndex: 101, ID: 10, Level: 1, HP: 100, UseCostume: 201, CostumeID: 101}
	chars := []roster.Character{permanent, {InvenIndex: temporary, ID: 30, Level: 1, HP: 100, ExpiryTime: 1}}
	costumes := []roster.Costume{{InvenIndex: 201, ID: 101, Level: 1, UseChar: 101}}
	path := filepath.Join(t.TempDir(), "state.db")
	f := openProgress(t, path, d, p, chars, costumes)
	seed := deck.Seed{Version: versionconfig.State(), FieldDeck: []deck.FieldEntry{{Slot: 1, CharacterInvenIndex: temporary}}}
	ds, e := deck.OpenStore(f.ctx, stateio.RootStore{}, seed)
	if e != nil {
		t.Fatal(e)
	}
	equipment, e := assets.OpenEquipmentInventory(f.ctx, stateio.RootStore{})
	if e != nil {
		t.Fatal(e)
	}
	if e = ds.AttachPresetRuntime(f.ctx, f.wallet, f.characters, equipment, f.collection); e != nil {
		t.Fatal(e)
	}
	if e = ds.EnsurePersisted(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.service.AttachExpiry(ds.ExpireCharacter)
	_, _, _, e = f.service.Handle(f.ctx, "/CharExpiry", rosterRequest(1, 101, 0))
	if e == nil {
		t.Fatal("expiry accepted permanent owned character")
	}
	request := rosterRequest(2, temporary, 0)
	callProgress(t, f, "/CharExpiry", request)
	if all := f.characters.RawAll(); len(all) != 1 || all[0].InvenIndex != 101 {
		t.Fatalf("expired instance remained in roster: %+v", all)
	}
	if field := ds.CurrentFieldDeck(f.ctx); len(field) != 1 || field[0].CharacterInvenIndex != 101 || field[0].CostumeInvenIndex != 201 {
		t.Fatalf("empty field party did not choose permanent replacement: %+v", field)
	}
	if e = f.tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	f = openProgress(t, path, d, p, []roster.Character{permanent}, costumes)
	callProgress(t, f, "/CharExpiry", request)
	if all := f.characters.RawAll(); len(all) != 1 || all[0].InvenIndex != 101 {
		t.Fatal("expired charm deletion did not survive reopen/replay")
	}
}
