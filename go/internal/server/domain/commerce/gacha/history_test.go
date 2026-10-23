package gacha

import (
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/instanceid"
	"bd2server/internal/server/storage/stateio"
)

type historyAccount struct {
	repo    *accountstate.Repository
	path    string
	catalog *gamedata.RegularGachaCatalog
	now     time.Time
	name    string
}

func newHistoryAccount(t *testing.T, name string) *historyAccount {
	t.Helper()
	catalog, e := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Grades: map[uint64]uint64{101: 5}, Pool: []gamedata.WeightedCostume{{ID: 101, Weight: 1}}},
		2: {ID: 2, Count: 10, PriceType: 3, Price: 10, Grades: map[uint64]uint64{101: 5}, Pool: []gamedata.WeightedCostume{{ID: 101, Weight: 1}}},
		3: {ID: 3, Count: 1, PriceType: 3, Price: 1, Grades: map[uint64]uint64{102: 3}, Pool: []gamedata.WeightedCostume{{ID: 102, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{101: {ID: 501, HP: 10, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 10}, 102: {ID: 502, HP: 10, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 1}})
	if e != nil {
		t.Fatal(e)
	}
	if e = catalog.AddGroupDesign(gamedata.GachaGroupDesign{ID: 10, GachaType: 1, PickUpCostumeID: 101, PointCount: 1, OneTimeGachaID: 1, TenTimeGachaID: 2}, gamedata.GachaFixedDesign{}); e != nil {
		t.Fatal(e)
	}
	if e = catalog.AddGroupDesign(gamedata.GachaGroupDesign{ID: 20, GachaType: 1, PointCount: 1, OneTimeGachaID: 3}, gamedata.GachaFixedDesign{}); e != nil {
		t.Fatal(e)
	}
	a := &historyAccount{path: filepath.Join(t.TempDir(), "state.db"), catalog: catalog, now: time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC), name: name}
	a.repo, e = accountstate.Open(a.path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := a.repo.Close(); e != nil {
			t.Error(e)
		}
	})
	return a
}
func (a *historyAccount) reopen(t *testing.T) {
	t.Helper()
	if e := a.repo.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	a.repo, e = accountstate.Open(a.path)
	if e != nil {
		t.Fatal(e)
	}
}
func (a *historyAccount) call(t *testing.T, path string, raw []byte) ([]byte, error) {
	t.Helper()
	tx, e := a.repo.BeginCommand(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{Identity: command.Identity{AccountID: a.name, SessionID: "draw-session"}, State: tx, InstanceIDs: instanceid.New(filepath.Join(filepath.Dir(a.path), "instance-ids.db"))}
	collection, e := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	wallet, e := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{FreeJewelry: 100000})
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewService(nil, a.catalog, collection, wallet)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return a.now }
	_, body, handled, e := s.Handle(ctx, path, raw)
	if !handled {
		t.Fatalf("unhandled %s", path)
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return body, nil
}
func historyQuery(after uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(nil, 1, 999), 2, after)
}
func historyBuy(seq, id uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, id), 3, 1)
}
func historyFields(t *testing.T, raw []byte, field int) [][]byte {
	t.Helper()
	var rows [][]byte
	if e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == field {
			rows = append(rows, f.Value)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return rows
}
func historyNumber(t *testing.T, raw []byte, field int) uint64 {
	t.Helper()
	n, _, e := wire.Varint(raw, field)
	if e != nil {
		t.Fatal(e)
	}
	return n
}
func historyTotal(t *testing.T, raw []byte, kind, field uint64) uint64 {
	t.Helper()
	for _, row := range historyFields(t, raw, 2) {
		if historyNumber(t, row, 1) == kind {
			return historyNumber(t, row, int(field))
		}
	}
	t.Fatalf("missing total type %d", kind)
	return 0
}
func (a *historyAccount) must(t *testing.T, path string, raw []byte) []byte {
	t.Helper()
	body, e := a.call(t, path, raw)
	if e != nil {
		t.Fatal(e)
	}
	return body
}

func TestGachaHistoryUsesCommittedDrawsAndIncrementalCursorAfterReopen(t *testing.T) {
	a := newHistoryAccount(t, "alice")
	empty := a.must(t, "/GachaLog", historyQuery(0))
	if len(historyFields(t, empty, 1)) != 0 {
		t.Fatal("unplayed account has invented history")
	}
	a.must(t, "/GachaBuy", historyBuy(1, 1))
	first := a.must(t, "/GachaLog", historyQuery(0))
	rows := historyFields(t, first, 1)
	if len(rows) != 1 || historyNumber(t, rows[0], 1) != 10 || historyNumber(t, rows[0], 2) != 1 || historyNumber(t, rows[0], 4) != 1 {
		t.Fatalf("draw metadata not preserved: %x", rows)
	}
	cursor := historyNumber(t, rows[0], 13)
	a.must(t, "/GachaBuy", historyBuy(1, 1))
	a.reopen(t)
	a.must(t, "/GachaBuy", historyBuy(1, 1))
	if got := a.must(t, "/GachaLog", historyQuery(cursor)); len(historyFields(t, got, 1)) != 0 || historyTotal(t, got, 0, 2) != 1 {
		t.Fatal("retry created another draw or changed lifetime count")
	}
	// More than one storage page of genuine single draws share the same clock
	// millisecond. Every committed receipt must remain reachable from the cursor.
	for seq := uint64(2); seq <= 270; seq++ {
		a.must(t, "/GachaBuy", historyBuy(seq, 1))
	}
	a.must(t, "/GachaBuy", historyBuy(271, 3))
	a.must(t, "/GachaBuy", historyBuy(272, 2))
	a.reopen(t)
	increment := a.must(t, "/GachaLog", historyQuery(cursor))
	rows = historyFields(t, increment, 1)
	if len(rows) != 271 {
		t.Fatalf("increment lost records across entry pages: %d", len(rows))
	}
	previous := uint64(^uint64(0))
	for _, row := range rows {
		at := historyNumber(t, row, 13)
		if at <= cursor || at >= previous {
			t.Fatal("history cursor order duplicated or skipped a same-millisecond draw")
		}
		previous = at
	}
	if historyTotal(t, increment, 0, 2) != 280 || historyTotal(t, increment, 1, 5) != 1 {
		t.Fatalf("costume type totals disagree with 270 singles, ten-pull, and normal three-star draw: %x", historyFields(t, increment, 2))
	}
	if all := a.must(t, "/GachaLog", historyQuery(0)); len(historyFields(t, all, 1)) != 272 {
		t.Fatal("initial query imposed an unsupported history limit")
	}
	latest := historyNumber(t, rows[0], 13)
	a.must(t, "/GachaBuy", historyBuy(273, 1))
	tail := historyFields(t, a.must(t, "/GachaLog", historyQuery(latest)), 1)
	if len(tail) != 1 || historyNumber(t, tail[0], 13) <= latest {
		t.Fatal("new draw at incremental boundary was lost")
	}
}

func TestGachaHistoryDoesNotExposeAnotherAccountsDraws(t *testing.T) {
	alice := newHistoryAccount(t, "alice")
	bob := newHistoryAccount(t, "bob")
	alice.must(t, "/GachaBuy", historyBuy(1, 2))
	bob.reopen(t)
	if b := bob.must(t, "/GachaLog", historyQuery(0)); len(historyFields(t, b, 1)) != 0 || historyTotal(t, b, 0, 2) != 0 {
		t.Fatal("another account's history leaked")
	}
	bob.must(t, "/GachaBuy", historyBuy(1, 3))
	bob.reopen(t)
	b := bob.must(t, "/GachaLog", historyQuery(0))
	if historyTotal(t, b, 1, 5) != 1 || historyTotal(t, b, 0, 2) != 0 {
		t.Fatal("account totals were mixed")
	}
	if a := alice.must(t, "/GachaLog", historyQuery(0)); historyTotal(t, a, 0, 2) != 10 || historyTotal(t, a, 1, 5) != 0 {
		t.Fatal("second account changed first account's totals")
	}
	if _, e := bob.call(t, "/GachaBuy", historyBuy(2, 999)); e == nil {
		t.Fatal("unknown draw was accepted")
	}
	if b := bob.must(t, "/GachaLog", historyQuery(0)); len(historyFields(t, b, 1)) != 1 {
		t.Fatal("rejected draw entered history")
	}
}
