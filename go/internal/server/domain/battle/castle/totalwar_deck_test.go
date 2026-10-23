package castle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/instanceid"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

type totalWarFixture struct {
	repo       *accountstate.Repository
	design     *gamedata.CastleRewardDesign
	path       string
	costumes   []roster.Costume
	characters []roster.Character
	completion func(command.Context, string, uint64, []uint64) error
}
type totalWarRuntime struct {
	service    *Service
	wallet     *assets.Wallet
	equipment  *assets.EquipmentInventory
	characters *roster.CharacterStore
	collection *roster.CollectionStore
}

func newTotalWarFixture(t *testing.T, characters ...roster.Character) *totalWarFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	r, e := accountstate.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	})
	d := testDesign()
	d.TotalWarPresets = &gamedata.PresetDesign{BaseCount: 5, Maximum: 5, PriceType: 4, Price: 2000, Icons: map[uint64]bool{11: true}}
	f := &totalWarFixture{repo: r, design: d, path: path, characters: characters}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if e := rt.wallet.EnsurePersisted(ctx); e != nil {
			return e
		}
		if e := rt.characters.EnsurePersisted(ctx); e != nil {
			return e
		}
		if e := rt.collection.EnsurePersisted(ctx); e != nil {
			return e
		}
		weapon, e := rt.equipment.GrantOnce(ctx, "weapon", 700)
		if e != nil {
			return e
		}
		shield, e := rt.equipment.GrantOnce(ctx, "shield", 701)
		if e != nil {
			return e
		}
		_, e = rt.equipment.ApplyPresetEquipment(ctx, []assets.PresetEquipmentBinding{{CharacterIndex: 81, Equipment: []uint64{weapon.InvenIndex, 0, 0, 0, 0}}, {CharacterIndex: 82, Equipment: []uint64{0, shield.InvenIndex, 0, 0, 0}}})
		return e
	}, false)
	return f
}
func (f *totalWarFixture) open(t *testing.T, ctx command.Context) totalWarRuntime {
	t.Helper()
	scope := stateio.RootStore{}
	i, e := assets.OpenInventory(ctx, scope, nil)
	if e != nil {
		t.Fatal(e)
	}
	w, e := assets.OpenWallet(ctx, scope, assets.Currency{Gold: 10000})
	if e != nil {
		t.Fatal(e)
	}
	chars := []roster.Character{{InvenIndex: 81, ID: 71, Level: 1, ExpiryTime: ^uint64(0) - 32399999}, {InvenIndex: 82, ID: 72, Level: 1}}
	if len(f.characters) > 0 {
		chars = f.characters
	}
	costumes := []roster.Costume{{InvenIndex: 101, ID: 91, UseChar: 81}, {InvenIndex: 102, ID: 92, UseChar: 81}, {InvenIndex: 103, ID: 93, UseChar: 82}}
	if f.costumes != nil {
		costumes = f.costumes
	}
	col, e := roster.OpenCollectionStore(ctx, scope, costumes)
	if e != nil {
		t.Fatal(e)
	}
	c, e := roster.OpenCharacterStore(ctx, scope, chars, i, "", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.AttachCollection(ctx, col); e != nil {
		t.Fatal(e)
	}
	if e = col.BindBaseCharacters(ctx, chars); e != nil {
		t.Fatal(e)
	}
	equipment, e := assets.OpenEquipmentInventory(ctx, scope)
	if e != nil {
		t.Fatal(e)
	}
	if e = equipment.AttachCharacters(ctx, c); e != nil {
		t.Fatal(e)
	}
	if e = equipment.AttachSlots(ctx, map[uint64]uint64{700: 0, 701: 1}); e != nil {
		t.Fatal(e)
	}
	s := NewService(f.design)
	if e = s.AttachTotalWarRuntime(c, col, equipment, w); e != nil {
		t.Fatal(e)
	}
	s.AttachTotalWarValidation(func(_ command.Context, ps []Participant) error {
		for _, p := range ps {
			costume, ok := col.CostumeByIndex(p.CostumeIndex)
			if !ok || costume.UseChar != p.CharacterIndex || costume.ID != p.CostumeID {
				return fmt.Errorf("not owned")
			}
		}
		return nil
	})
	s.AttachTotalWarTextCheck(func(command.Context, string, string) error { return nil })
	s.AttachTotalWarCompletion(f.completion)
	s.AttachRewards(func(ctx command.Context, id string, rewards []gamedata.Reward) ([]byte, error) {
		_, e := w.GrantQuestOnce(ctx, id, rewards)
		return nil, e
	})
	return totalWarRuntime{s, w, equipment, c, col}
}
func (f *totalWarFixture) command(t *testing.T, run func(command.Context, totalWarRuntime) error, wantError bool) {
	t.Helper()
	tx, e := f.repo.BeginCommand(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	ctx := command.Context{Identity: command.Identity{AccountID: "total-war-account", SessionID: "login"}, State: tx, InstanceIDs: instanceid.New(filepath.Join(filepath.Dir(f.path), "instance-ids.db"))}
	rt := f.open(t, ctx)
	e = run(ctx, rt)
	if wantError {
		if e == nil {
			_ = tx.Rollback()
			t.Fatal("invalid operation succeeded")
		}
		if e = tx.Rollback(); e != nil {
			t.Fatal(e)
		}
		return
	}
	if e != nil {
		_ = tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func totalWarDeckEntry(role, costume, char uint64) []byte {
	raw := wire.AppendVarint(nil, 1, role)
	raw = wire.AppendVarint(raw, 2, costume)
	return wire.AppendVarint(raw, 3, char)
}
func totalWarDeckRequest(seq uint64, deck ...[]byte) []byte {
	raw := request(seq)
	for _, entry := range deck {
		raw = wire.AppendBytes(raw, 2, entry)
	}
	return raw
}
func totalWarContent(char uint64, ids ...uint64) []byte {
	raw := wire.AppendVarint(nil, 1, char)
	for slot, id := range ids {
		ref := wire.AppendVarint(nil, 1, id)
		ref = wire.AppendVarint(ref, 2, uint64(slot))
		raw = wire.AppendBytes(raw, 2, ref)
	}
	return raw
}
func totalWarEquipmentFor(t *testing.T, body []byte, field int, char, slot uint64) uint64 {
	t.Helper()
	contents, e := msgs(body, field)
	if e != nil {
		t.Fatal(e)
	}
	for _, raw := range contents {
		if n(raw, 1) != char {
			continue
		}
		refs, e := msgs(raw, 2)
		if e != nil {
			t.Fatal(e)
		}
		for _, ref := range refs {
			if n(ref, 2) == slot {
				return n(ref, 1)
			}
		}
	}
	t.Fatalf("missing character %d slot %d", char, slot)
	return 0
}
func totalWarCall(ctx command.Context, rt totalWarRuntime, path string, req []byte) ([]byte, error) {
	_, body, handled, e := rt.service.Handle(ctx, path, req)
	if !handled {
		return nil, fmt.Errorf("unhandled %s", path)
	}
	return body, e
}

func TestTotalWarIndependentEquipmentTransferRenewAndReopen(t *testing.T) {
	f := newTotalWarFixture(t)
	var weapon, shield uint64
	deck := [][]byte{totalWarDeckEntry(0, 102, 81), totalWarDeckEntry(0, 101, 81), totalWarDeckEntry(1, 103, 82)}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		var e error
		empty, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(1))
		if e != nil {
			return e
		}
		if len(empty) != 0 {
			t.Fatal("uninitialized formation should trigger client InitialSort")
		}
		for _, item := range rt.equipment.All(ctx) {
			if item.ID == 700 {
				weapon = item.InvenIndex
			}
			if item.ID == 701 {
				shield = item.InvenIndex
			}
		}
		_, e = totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(2, deck...))
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(3))
		if e != nil {
			return e
		}
		rows, _ := msgs(body, 1)
		if len(rows) != 3 || n(rows[0], 2) != 102 || n(rows[1], 2) != 101 {
			t.Fatal("formation skill order or multiple costumes on same character lost on reopen")
		}
		if totalWarEquipmentFor(t, body, 2, 81, 0) != weapon || totalWarEquipmentFor(t, body, 2, 82, 1) != shield {
			t.Fatal("initial formation did not copy live equipment")
		}
		update := wire.AppendBytes(request(4), 2, totalWarContent(81, 0, shield))
		update = wire.AppendBytes(update, 2, totalWarContent(82, weapon, 0))
		_, e = totalWarCall(ctx, rt, "/TotalWarEquipSave", update)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(5))
		if e != nil {
			return e
		}
		if totalWarEquipmentFor(t, body, 2, 81, 1) != shield || totalWarEquipmentFor(t, body, 2, 82, 0) != weapon {
			t.Fatal("independent equipment transfer did not survive transaction reopen")
		}
		for _, item := range rt.equipment.All(ctx) {
			if item.ID == 700 && item.UseChar != 81 || item.ID == 701 && item.UseChar != 82 {
				t.Fatal("total war changed normal equipment ownership")
			}
		}
		return nil
	}, false)
	for seq, bad := range [][]byte{totalWarContent(81, weapon), totalWarContent(81, 999999), totalWarContent(81, shield)} {
		f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
			_, e := totalWarCall(ctx, rt, "/TotalWarEquipSave", wire.AppendBytes(request(uint64(10+seq)), 2, bad))
			return e
		}, true)
	}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		before, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(20))
		if e != nil {
			return e
		}
		if totalWarEquipmentFor(t, before, 2, 82, 0) != weapon {
			t.Fatal("rejected equipment updates changed saved transfer")
		}
		body, e := totalWarCall(ctx, rt, "/TotalWarContentsItemRenew", request(21))
		if e != nil {
			return e
		}
		if totalWarEquipmentFor(t, body, 1, 81, 0) != weapon || totalWarEquipmentFor(t, body, 1, 82, 1) != shield {
			t.Fatal("renew did not restore live equipment")
		}
		return nil
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(22))
		if e == nil && totalWarEquipmentFor(t, body, 2, 81, 0) != weapon {
			t.Fatal("renew was not persisted")
		}
		return e
	}, false)
}

func TestTotalWarPresetsMetadataDeleteAndSlotCurrency(t *testing.T) {
	f := newTotalWarFixture(t)
	preset := wire.AppendVarint(nil, 1, 0)
	preset = wire.AppendBytes(preset, 2, []byte("Boss"))
	preset = wire.AppendVarint(preset, 3, 11)
	preset = wire.AppendBytes(preset, 5, totalWarDeckEntry(0, 102, 81))
	preset = wire.AppendBytes(preset, 5, totalWarDeckEntry(0, 101, 81))
	saveRequest := wire.AppendBytes(request(30), 2, preset)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSave", saveRequest)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", request(31, 1))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if rt.wallet.Snapshot(ctx).Gold != 10000 {
			t.Fatal("current GameData five-slot cap charged gold")
		}
		metadata := wire.AppendBytes(request(32), 2, []byte("Renamed"))
		metadata = wire.AppendVarint(metadata, 3, 11)
		metadata = wire.AppendVarint(metadata, 4, 3)
		metadata = wire.AppendVarint(metadata, 5, 0)
		_, e := totalWarCall(ctx, rt, "/TotalWarPresetInfoChange", metadata)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetInfo", request(33))
		if e != nil {
			return e
		}
		rows, _ := msgs(body, 1)
		if len(rows) != 1 {
			t.Fatal("preset missing on reopen")
		}
		name, _, _ := wire.Bytes(rows[0], 2)
		deck, _ := msgs(rows[0], 5)
		if string(name) != "Renamed" || n(rows[0], 4) != 3 || len(deck) != 2 || n(deck[0], 2) != 102 || n(deck[1], 2) != 101 {
			t.Fatal("metadata change changed saved skill order")
		}
		deleteRequest := wire.AppendBytes(request(34), 2, []byte{0, 4})
		_, e = totalWarCall(ctx, rt, "/TotalWarPresetDelete", deleteRequest)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetInfo", request(35))
		if len(body) != 0 {
			t.Fatal("deleted preset still present")
		}
		slots, se := rt.service.TotalWarPresetSlotCount(ctx)
		if se != nil || slots != 5 {
			t.Fatal("deleting preset removed purchased capacity")
		}
		return e
	}, false)
	// A design with seven purchasable slots exercises the shared paid-slot rule.
	f.design.TotalWarPresets.Maximum = 8
	purchase := request(36, 2)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", purchase)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", purchase)
		if e != nil {
			return e
		}
		if rt.wallet.Snapshot(ctx).Gold != 6000 {
			t.Fatal("two slots must cost 4000 gold once across reopen")
		}
		slots, e := rt.service.TotalWarPresetSlotCount(ctx)
		if e != nil || slots != 7 {
			t.Fatal("purchased slots missing on reopen")
		}
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", request(36, 1))
		return e
	}, true)
	f.design.TotalWarPresets.Price = 7000
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", request(37, 1))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if rt.wallet.Snapshot(ctx).Gold != 6000 {
			t.Fatal("failed purchase charged currency")
		}
		slots, e := rt.service.TotalWarPresetSlotCount(ctx)
		if slots != 7 {
			t.Fatal("failed purchase created a slot")
		}
		return e
	}, false)
}

type failTotalWarSave struct{ stateio.AtomicEntryStore }

func (f failTotalWarSave) SaveWithEntries(domain string, core []byte, entries []stateio.EntryMutation) error {
	if domain == "missions" {
		return fmt.Errorf("injected mission write failure")
	}
	return f.AtomicEntryStore.SaveWithEntries(domain, core, entries)
}
func TestTotalWarSlotDebitRollsBackWithFormationPersistence(t *testing.T) {
	f := newTotalWarFixture(t)
	f.design.TotalWarPresets.Maximum = 8
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		ctx.State = failTotalWarSave{ctx.State}
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", request(40, 2))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if rt.wallet.Snapshot(ctx).Gold != 10000 {
			t.Fatal("formation failure committed wallet debit")
		}
		slots, e := rt.service.TotalWarPresetSlotCount(ctx)
		if slots != 5 {
			t.Fatal("failed command changed capacity")
		}
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSlotAdd", request(40, 2))
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if rt.wallet.Snapshot(ctx).Gold != 6000 {
			t.Fatal("retry after rollback did not pay exactly once")
		}
		return nil
	}, false)
}

func TestTotalWarInvalidOwnershipAndPresetFailuresKeepFormation(t *testing.T) {
	f := newTotalWarFixture(t)
	valid := totalWarDeckRequest(50, totalWarDeckEntry(0, 101, 81), totalWarDeckEntry(1, 103, 82))
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", valid)
		return e
	}, false)
	for seq, entries := range [][][]byte{{totalWarDeckEntry(0, 101, 82)}, {totalWarDeckEntry(0, 999, 81)}, {totalWarDeckEntry(0, 101, 81), totalWarDeckEntry(1, 101, 81)}, {totalWarDeckEntry(2, 101, 81)}} {
		f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
			_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(uint64(51+seq), entries...))
			return e
		}, true)
	}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(60))
		rows, _ := msgs(body, 1)
		if len(rows) != 2 || !bytes.Equal(rows[0], totalWarDeckEntry(0, 101, 81)) || !bytes.Equal(rows[1], totalWarDeckEntry(1, 103, 82)) {
			t.Fatal("rejected decks changed saved formation")
		}
		return e
	}, false)
}

func TestTotalWarTwentyAttackersAndOwnedCostumeBoundary(t *testing.T) {
	f := newTotalWarFixture(t)
	var deck [][]byte
	for i := range uint64(21) {
		f.costumes = append(f.costumes, roster.Costume{InvenIndex: 1001 + i, ID: 2001 + i, UseChar: 81})
		deck = append(deck, totalWarDeckEntry(0, 1001+i, 81))
	}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(70, deck...))
		return e
	}, true)
	deck[20] = totalWarDeckEntry(1, 1021, 81)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(71, deck...))
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(72))
		rows, _ := msgs(body, 1)
		if len(rows) != 21 || n(rows[20], 1) != 1 {
			t.Fatal("twenty attackers plus a cheering owned costume should persist")
		}
		return e
	}, false)
}

func TestTotalWarSavedBattleEquipmentAndFrozenBestCompletion(t *testing.T) {
	f := newTotalWarFixture(t)
	f.completion = func(ctx command.Context, id string, score uint64, costumes []uint64) error {
		if id != "login:81" || score != 300 || len(costumes) != 2 || costumes[0] != 92 || costumes[1] != 91 {
			return fmt.Errorf("completion lost start-time attackers or score")
		}
		return ctx.State.PutEntry("missions", "totalwar_test_publication", "highest", []byte("300:92:91"))
	}
	formation := [][]byte{totalWarDeckEntry(0, 102, 81), totalWarDeckEntry(0, 101, 81), totalWarDeckEntry(1, 103, 82)}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(80, formation...))
		return e
	}, false)
	var start []byte
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(800))
		if e != nil {
			return e
		}
		contents, _ := msgs(body, 2)
		start = request(81, 1)
		for i, entry := range formation {
			charID := uint64(71)
			if n(entry, 3) == 82 {
				charID = 72
			}
			blue := wire.AppendVarint(nil, 2, n(entry, 3))
			blue = wire.AppendVarint(blue, 3, charID)
			blue = wire.AppendVarint(blue, 6, n(entry, 2))
			costume, _ := rt.collection.CostumeByIndex(n(entry, 2))
			blue = wire.AppendVarint(blue, 7, costume.ID)
			blue = wire.AppendVarint(blue, 28, n(entry, 1))
			if n(entry, 1) == 0 {
				blue = wire.AppendVarint(blue, 1, uint64(i+1))
			}
			for _, content := range contents {
				if n(content, 1) != n(entry, 3) {
					continue
				}
				blue = wire.AppendVarint(blue, 29, n(content, 3))
				refs, _ := msgs(content, 2)
				for _, ref := range refs {
					if id := n(ref, 1); id != 0 {
						blue = wire.AppendVarint(blue, 12, id)
					}
				}
			}
			start = wire.AppendBytes(start, 3, blue)
		}
		red := wire.AppendVarint(nil, 1, 99)
		red = wire.AppendVarint(red, 3, 95)
		red = wire.AppendVarint(red, 4, 1000000)
		red = wire.AppendVarint(red, 5, 1)
		start = wire.AppendBytes(start, 4, red)
		return nil
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		invalid := request(810, 1)
		blue, _ := msgs(start, 3)
		for i, row := range blue {
			if i == 0 {
				row = wire.AppendVarint(row, 12, 99999)
			}
			invalid = wire.AppendBytes(invalid, 3, row)
		}
		red, _ := msgs(start, 4)
		invalid = wire.AppendBytes(invalid, 4, red[0])
		_, e := totalWarCall(ctx, rt, "/TotalWarBattleStart", invalid)
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarBattleEnd", request(811))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarBattleStart", start)
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(82, formation[1], formation[0], formation[2]))
		return e
	}, false)
	end := wire.AppendBytes(request(83), 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 92), 2, 100))
	end = wire.AppendBytes(end, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 91), 2, 200))
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarBattleEnd", end)
		return e
	}, false)
	if e := f.repo.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := accountstate.Open(f.path)
	if e != nil {
		t.Fatal(e)
	}
	f.repo = reopened
	t.Cleanup(func() {
		if e := reopened.Close(); e != nil {
			t.Error(e)
		}
	})
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		if _, e := totalWarCall(ctx, rt, "/TotalWarBattleEnd", end); e != nil {
			return e
		}
		raw, found, e := ctx.State.LoadEntry("missions", "totalwar_test_publication", "highest")
		if e != nil {
			return e
		}
		if !found || string(raw) != "300:92:91" {
			t.Fatal("highest-score start-time publication missing after SQLite reopen")
		}
		_, e = totalWarCall(ctx, rt, "/TotalWarReward", request(84))
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarReward", request(84))
		if e != nil {
			return e
		}
		if rt.wallet.Snapshot(ctx).Gold != 10003 {
			t.Fatal("300 damage must credit the 3-gold tier once")
		}
		return nil
	}, false)
}

func TestTotalWarPresetRejectsInvalidMetadataAndDeletionAtomically(t *testing.T) {
	f := newTotalWarFixture(t)
	preset := wire.AppendVarint(nil, 1, 0)
	preset = wire.AppendBytes(preset, 2, []byte("Boss"))
	preset = wire.AppendVarint(preset, 3, 11)
	preset = wire.AppendBytes(preset, 5, totalWarDeckEntry(0, 101, 81))
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSave", wire.AppendBytes(request(90), 2, preset))
		return e
	}, false)
	for i, tc := range []struct {
		name     string
		metadata []byte
	}{
		{"unavailable_slot", wire.AppendVarint(nil, 5, 5)},
		{"invalid_name", wire.AppendBytes(wire.AppendVarint(nil, 5, 0), 2, []byte("<invalid>"))},
		{"unknown_icon", wire.AppendVarint(wire.AppendVarint(nil, 5, 0), 3, 999)},
		{"undefined_color", wire.AppendVarint(wire.AppendVarint(nil, 5, 0), 4, 6)},
		{"icon_wrong_wire_type", wire.AppendBytes(wire.AppendVarint(nil, 5, 0), 3, []byte{11})},
		{"slot_wrong_wire_type", wire.AppendBytes(nil, 5, []byte{0})},
		{"icon_repeated", wire.AppendVarint(wire.AppendVarint(nil, 3, 11), 3, 11)},
		{"icon_invalid_then_valid", wire.AppendVarint(wire.AppendVarint(nil, 3, 999), 3, 11)},
		{"icon_valid_then_invalid", wire.AppendVarint(wire.AppendVarint(nil, 3, 11), 3, 999)},
		{"slot_repeated", wire.AppendVarint(wire.AppendVarint(nil, 5, 0), 5, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				_, e := totalWarCall(ctx, rt, "/TotalWarPresetInfoChange", append(request(uint64(1000+i)), tc.metadata...))
				return e
			}, true)
		})
	}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarPresetDelete", wire.AppendBytes(request(96), 2, []byte{0, 5}))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetInfo", request(97))
		if e != nil {
			return e
		}
		rows, _ := msgs(body, 1)
		if len(rows) != 1 || !bytes.Equal(rows[0], preset) {
			t.Fatal("invalid metadata or mixed valid/invalid deletion changed preset")
		}
		return nil
	}, false)
}

func TestTotalWarMalformedRequestScalarsPreserveFormationAndGold(t *testing.T) {
	f := newTotalWarFixture(t)
	f.design.TotalWarPresets.Maximum = 8
	formation := totalWarDeckEntry(0, 101, 81)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(2000, formation))
		return e
	}, false)
	for _, tc := range []struct {
		name, path string
		request    []byte
	}{
		{"duplicate_sequence", "/TotalWarDeckSave", wire.AppendVarint(totalWarDeckRequest(2001, totalWarDeckEntry(1, 101, 81)), 1, 2001)},
		{"wrong_sequence_type", "/TotalWarDeckSave", wire.AppendBytes(totalWarDeckRequest(2002, totalWarDeckEntry(1, 101, 81)), 1, []byte{1})},
		{"repeated_role", "/TotalWarDeckSave", totalWarDeckRequest(2003, wire.AppendVarint(formation, 1, 1))},
		{"wrong_character_type", "/TotalWarDeckSave", totalWarDeckRequest(2004, wire.AppendBytes(formation, 3, []byte{81}))},
		{"repeated_purchase_count", "/TotalWarDeckPresetSlotAdd", wire.AppendVarint(request(2005, 1), 2, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				_, e := totalWarCall(ctx, rt, tc.path, tc.request)
				return e
			}, true)
		})
	}
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckInfo", request(2010))
		if e != nil {
			return e
		}
		rows, _ := msgs(body, 1)
		if len(rows) != 1 || !bytes.Equal(rows[0], formation) {
			t.Fatal("malformed scalar changed formation")
		}
		slots, e := rt.service.TotalWarPresetSlotCount(ctx)
		if slots != 5 || rt.wallet.Snapshot(ctx).Gold != 10000 {
			t.Fatal("malformed purchase changed capacity or gold")
		}
		return e
	}, false)
}

func TestTotalWarPermanentSentinelExcludesTemporaryAndExpiredCharacters(t *testing.T) {
	for _, tc := range []struct {
		name      string
		character roster.Character
	}{
		{"expired_normal", roster.Character{InvenIndex: 83, ID: 73, Level: 1, ExpiryTime: 1}},
		{"story_zero_expiry", roster.Character{InvenIndex: roster.StoryCharacterIndexBase, ID: 73, Level: 1}},
		{"charm_permanent_expiry", roster.Character{InvenIndex: roster.CharmCharacterIndexBase, ID: 73, Level: 1, ExpiryTime: ^uint64(0) - 32399999}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTotalWarFixture(t, roster.Character{InvenIndex: 81, ID: 71, Level: 1, ExpiryTime: ^uint64(0) - 32399999}, roster.Character{InvenIndex: 82, ID: 72, Level: 1}, tc.character)
			f.costumes = []roster.Costume{{InvenIndex: 101, ID: 91, UseChar: 81}, {InvenIndex: 102, ID: 92, UseChar: 81}, {InvenIndex: 103, ID: 93, UseChar: 82}, {InvenIndex: 104, ID: 94, UseChar: tc.character.InvenIndex}}
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(2100, totalWarDeckEntry(0, 101, 81)))
				return e
			}, false)
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				_, e := totalWarCall(ctx, rt, "/TotalWarDeckSave", totalWarDeckRequest(2101, totalWarDeckEntry(0, 104, tc.character.InvenIndex)))
				return e
			}, true)
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				_, e := totalWarCall(ctx, rt, "/TotalWarEquipSave", wire.AppendBytes(request(2102), 2, totalWarContent(tc.character.InvenIndex, 0, 0, 0, 0, 0)))
				return e
			}, true)
			f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
				body, e := totalWarCall(ctx, rt, "/TotalWarContentsItemRenew", request(2103))
				if e != nil {
					return e
				}
				rows, _ := msgs(body, 1)
				if len(rows) != 2 {
					t.Fatal("renew did not retain both permanent expiry representations")
				}
				for _, row := range rows {
					if n(row, 1) != 81 && n(row, 1) != 82 {
						t.Fatal("renew included temporary or expired character")
					}
				}
				return nil
			}, false)
		})
	}
}

func TestTotalWarPresetNameApprovalPreservesStoredMetadata(t *testing.T) {
	f := newTotalWarFixture(t)
	preset := wire.AppendVarint(nil, 1, 0)
	preset = wire.AppendBytes(preset, 2, []byte("Approved"))
	preset = wire.AppendBytes(preset, 5, totalWarDeckEntry(0, 101, 81))
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		rt.service.AttachTotalWarTextCheck(func(_ command.Context, scope, name string) error {
			if scope != "total_war_preset_name" || name != "Approved" {
				return fmt.Errorf("unexpected approval request")
			}
			return nil
		})
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSave", wire.AppendBytes(request(2200), 2, preset))
		return e
	}, false)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		rt.service.AttachTotalWarTextCheck(func(_ command.Context, scope, name string) error {
			if scope != "total_war_preset_name" || name != "Rejected" {
				t.Fatal("wrong approval scope or name")
			}
			return fmt.Errorf("text rejected")
		})
		_, e := totalWarCall(ctx, rt, "/TotalWarPresetInfoChange", wire.AppendBytes(request(2201), 2, []byte("Rejected")))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		rt.service.AttachTotalWarTextCheck(nil)
		unchecked := wire.AppendBytes(wire.AppendVarint(nil, 1, 0), 2, []byte("Unchecked"))
		_, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetSave", wire.AppendBytes(request(2202), 2, unchecked))
		return e
	}, true)
	f.command(t, func(ctx command.Context, rt totalWarRuntime) error {
		rt.service.AttachTotalWarTextCheck(func(command.Context, string, string) error { t.Fatal("read requested new text approval"); return nil })
		body, e := totalWarCall(ctx, rt, "/TotalWarDeckPresetInfo", request(2203))
		if e != nil {
			return e
		}
		rows, _ := msgs(body, 1)
		if len(rows) != 1 || !bytes.Equal(rows[0], preset) {
			t.Fatal("rejected name changed saved preset")
		}
		return nil
	}, false)
}
