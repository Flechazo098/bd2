//go:build integration

package life_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/life"
	"bd2server/internal/server/protocol/wire"
	accountstate "bd2server/internal/server/storage/account"
)

// LifePacket consumes server DB records for residents/helper slots; its
// PackInfoLife UI promises BaseLogging per HelperWorkBaseTime and caps accrual
// at MaxRewardTime. This exercises that contract through actual GameData,
// separate committed SQLite commands and a reopened repository.
func TestCurrentLifeGatherBuildRecruitSettleAndRelog(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	d, err := gamedata.LoadLifeCatalog(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	now := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	session := "initial-login"
	sequence := uint64(0)
	options := life.Options{Now: func() time.Time { return now }, Sample: func(uint64) (uint64, error) { return 0, nil }, ValidateAvatar: func(command.Context, []byte) error { return nil }}
	run := func(fn func(command.Context, *life.Service) error) {
		t.Helper()
		tx, err := repo.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx := command.Context{Identity: command.Identity{AccountID: "life-test", SessionID: session}, State: tx}
		if existing, err := tx.Load("missions"); err != nil {
			t.Fatal(err)
		} else if existing == nil {
			if err := tx.Save("missions", []byte("{}")); err != nil {
				t.Fatal(err)
			}
		}
		s, err := life.Open(ctx, d, options)
		if err != nil {
			t.Fatal(err)
		}
		if err := fn(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	call := func(path string, request []byte) []byte {
		t.Helper()
		sequence++
		var body []byte
		run(func(ctx command.Context, s *life.Service) error {
			_, out, handled, err := s.Handle(ctx, path, append(proto(1, sequence), request...))
			if !handled {
				t.Fatalf("unhandled %s", path)
			}
			body = out
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			return nil
		})
		return body
	}
	worlds := make([]uint64, 0, len(d.Tables["LifeWorldTable"]))
	for key := range d.Tables["LifeWorldTable"] {
		worlds = append(worlds, key[0])
	}
	sort.Slice(worlds, func(i, j int) bool { return worlds[i] < worlds[j] })
	if len(worlds) < 2 {
		t.Fatal("terrain choice requires at least two current design worlds")
	}
	call("/LifeUserInfo", nil)
	call("/LifeWorldChoice", proto(2, worlds[0]))
	initial := call("/LifeInfo", nil)
	var forestPlace, forestObject []byte
	var chunk uint64
	for _, place := range repeated(t, initial, 1) {
		if chunk == 0 {
			chunk = scalar(t, place, 1)
		}
		for _, o := range repeated(t, place, 2) {
			bd := d.Row("LifeBuildingObjectTable", scalar(t, o, 6))
			if scalar(t, bd, 2) == 1 && len(repeated(t, o, 10)) > 0 {
				forestPlace, forestObject = place, o
				break
			}
		}
		if forestObject != nil {
			break
		}
	}
	if forestObject == nil {
		t.Fatal("current terrain has no logging resource to play")
	}
	inner := repeated(t, forestObject, 10)[0]
	gatherReq := child(nil, 2, child(proto(1, scalar(t, forestPlace, 1)), 2, child(proto(1, scalar(t, forestObject, 1), 2, scalar(t, forestObject, 2), 6, scalar(t, forestObject, 6)), 10, proto(4, scalar(t, inner, 4), 6, scalar(t, inner, 6)))))
	harvest := call("/LifeWorldObjectGathering", gatherReq)
	if len(repeated(t, nested(t, harvest, 2), 1)) == 0 {
		t.Fatal("actual logging yielded no materials")
	}
	earned := nested(t, harvest, 1)
	build := func(function, x uint64) []byte {
		t.Helper()
		var id uint64
		for key, r := range d.Tables["LifeBuildingObjectTable"] {
			group := d.Row("LifeObjectGroupTable", key[0])
			if scalar(t, r, 2) == function && scalar(t, group, 7) != 0 && scalar(t, group, 13) <= 1 && (id == 0 || key[0] < id) {
				id = key[0]
			}
		}
		if id == 0 {
			t.Fatalf("missing initial building function %d", function)
		}
		bd := d.Row("LifeBuildingObjectTable", id)
		types, ids, counts := plainInts(t, bd, 10), plainInts(t, bd, 9), plainInts(t, bd, 8)
		var funding []gamedata.Reward
		for i, typ := range types {
			funding = append(funding, gamedata.Reward{Type: typ, ID: ids[i], Count: counts[i]})
		}
		run(func(ctx command.Context, s *life.Service) error {
			_, err := s.Apply(ctx, "building-budget-"+string(rune(function)), nil, funding)
			return err
		})
		p := child(proto(1, chunk), 2, proto(6, id, 1, x, 2, 1))
		placed := nested(t, call("/LifeWorldObjectPlaceSave", child(nil, 2, p)), 1)
		if scalar(t, nested(t, placed, 2), 7) == 0 {
			now = now.Add(time.Duration(scalar(t, bd, 11)) * time.Second)
			placed = nested(t, call("/LifeWorldObjectBuildCompleted", child(nil, 2, p)), 1)
		}
		return placed
	}
	residentHouse, workerHouse := build(102, 1), build(101, 2)
	candidates := repeated(t, call("/LifeHelperGacha", child(nil, 2, workerHouse)), 1)
	if len(candidates) == 0 {
		t.Fatal("worker recruitment offered no candidates")
	}
	candidate := candidates[0]
	avatar := nested(t, candidate, 5)
	resident := nested(t, call("/LifeCitizenRecruit", child(child(nil, 2, residentHouse), 3, child(nil, 3, avatar))), 1)
	defaults := d.Row("LifeDefaultTable", 0)
	hirePrice := scalar(t, defaults, 19)
	run(func(ctx command.Context, s *life.Service) error {
		_, err := s.Apply(ctx, "hire-budget", nil, []gamedata.Reward{{Type: 64, Count: hirePrice}})
		return err
	})
	call("/LifeHelperRecruit", child(child(nil, 3, candidate), 4, workerHouse))
	assigned := proto(1, scalar(t, candidate, 1), 6, 1)
	call("/LifeHelperAssign", child(child(nil, 2, assigned), 3, workerHouse))
	period := scalar(t, defaults, 20)
	now = now.Add(time.Duration(period) * time.Second)
	preview := call("/LifeHelperRewardInfo", nil)
	total := uint64(0)
	for _, it := range repeated(t, nested(t, preview, 2), 1) {
		total += scalar(t, it, 4)
	}
	species := d.Row("LifeHelperSpeciesTable", scalar(t, candidate, 2))
	if total != scalar(t, species, 3) {
		t.Fatalf("one logging cycle promised %d items, previewed %d", scalar(t, species, 3), total)
	}
	claim := call("/LifeHelperReward", nil)
	if len(repeated(t, nested(t, claim, 1), 1)) == 0 {
		t.Fatal("settlement yielded no persisted materials")
	}
	profileBefore := call("/LifeUserInfo", nil)
	itemsBefore := call("/LifeInfo", nil)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	session = "relogin"
	restored := call("/LifeUserInfo", nil)
	if !bytes.Equal(profileBefore, restored) || !bytes.Equal(nested(t, restored, 3), resident) {
		t.Fatal("reopened account lost resident/helper/user records")
	}
	if !bytes.Equal(nested(t, nested(t, restored, 2), 3), earned) {
		t.Fatal("reopened account lost earned growth")
	}
	if !bytes.Equal(itemsBefore, call("/LifeInfo", nil)) {
		t.Fatal("reopened account lost placements/inventory")
	}
	run(func(ctx command.Context, s *life.Service) error {
		coin, err := s.Coin(ctx)
		if err == nil && coin != 0 {
			t.Fatalf("hire debit not conserved across database reopen: %d", coin)
		}
		return err
	})
	secondPreview := call("/LifeHelperRewardInfo", nil)
	if len(repeated(t, nested(t, secondPreview, 2), 1)) != 0 {
		t.Fatal("relogin reclaimed the same helper interval")
	}
}

func plainInts(t *testing.T, b []byte, field int) []uint64 {
	t.Helper()
	var out []uint64
	for _, f := range extractFields(t, b) {
		if f.Number == field {
			if f.Type == 0 {
				out = append(out, scalar(t, b, field))
			} else if f.Type == 2 {
				for raw := f.Value; len(raw) > 0; {
					v, n := binary.Uvarint(raw)
					if n <= 0 {
						t.Fatal("bad packed rule")
					}
					out = append(out, v)
					raw = raw[n:]
				}
			}
		}
	}
	return out
}
func extractFields(t *testing.T, b []byte) []wire.Field {
	t.Helper()
	var out []wire.Field
	if err := wire.Walk(b, func(f wire.Field) error { out = append(out, f); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCurrentLifeEveryFoodCanBeBoughtEatenInBulkAndRestored(t *testing.T) {
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	d, err := gamedata.LoadLifeCatalog(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	g := scenario(t)
	g.d = d
	g.reopen()
	foods := map[uint64]uint64{}
	for _, r := range d.Tables["LifeItemTable"] {
		if scalar(t, r, 9) == 3 {
			id := scalar(t, r, 5)
			buffs := plainInts(t, r, 1)
			if len(buffs) == 0 {
				t.Fatalf("food %d has no buffs", id)
			}
			for _, buff := range buffs {
				duration := scalar(t, d.Row("LifeBuffTable", buff), 1)
				if duration == 0 {
					t.Fatalf("food %d buff %d has no duration", id, buff)
				}
				if duration > foods[id] {
					foods[id] = duration
				}
			}
		}
	}
	if len(foods) == 0 {
		t.Fatal("current design contains no food")
	}
	cookable := map[uint64]bool{}
	for _, r := range d.Tables["LifeCookTable"] {
		cookable[scalar(t, r, 4)] = true
	}
	bought := map[uint64]bool{}
	for _, r := range d.Tables["LifeBuyItemTable"] {
		id := scalar(t, r, 4)
		if _, ok := foods[id]; !ok || scalar(t, r, 5) != 66 || bought[id] {
			continue
		}
		if !cookable[id] {
			t.Fatalf("food %d is absent from cooking outputs", id)
		}
		if scalar(t, r, 3) != 1 || scalar(t, r, 8) != 64 {
			t.Fatalf("food %d shop units/currency changed", id)
		}
		price := scalar(t, r, 6) * 3
		if price > 0 {
			if _, err := g.s.Apply(g.ctx, fmt.Sprintf("food-budget-%d", id), nil, []gamedata.Reward{{Type: 64, Count: price}}); err != nil {
				t.Fatal(err)
			}
		}
		g.call("/LifeShopBuy", proto(2, scalar(t, r, 1), 3, scalar(t, r, 2), 4, 3))
		bought[id] = true
	}
	if len(bought) != len(foods) {
		t.Fatalf("only %d/%d foods purchasable", len(bought), len(foods))
	}
	selected := map[uint64][]byte{}
	var batch []byte
	for _, it := range repeated(t, g.call("/LifeInfo", nil), 3) {
		id := scalar(t, it, 2)
		if _, ok := foods[id]; !ok {
			continue
		}
		selected[id] = proto(1, scalar(t, it, 1), 2, id, 3, 66, 4, 1)
		batch = child(batch, 2, proto(1, scalar(t, it, 1), 2, id, 3, 66, 4, 2))
	}
	if len(selected) != len(foods) {
		t.Fatal("purchased food missing from inventory")
	}
	start := g.now
	out := g.call("/LifeEatFood", batch)
	if len(repeated(t, out, 1)) != len(foods) {
		t.Fatal("bulk food response omitted selected food")
	}
	for _, f := range repeated(t, out, 1) {
		id := scalar(t, f, 1)
		if scalar(t, f, 2) != uint64(start.UnixMilli())+foods[id]*2*1000 {
			t.Fatalf("food %d multi-serving duration wrong", id)
		}
		if balance, _ := g.s.Balance(g.ctx, 66, id); balance != 1 {
			t.Fatalf("food %d remaining=%d", id, balance)
		}
	}
	g.now = g.now.Add(time.Minute)
	batch = nil
	for _, it := range selected {
		batch = child(batch, 2, it)
	}
	g.call("/LifeEatFood", batch)
	g.reopen()
	g.ctx.SessionID = "all-food-relogin"
	restored := repeated(t, g.call("/LifeInfo", nil), 6)
	if len(restored) != len(foods) {
		t.Fatal("relogin lost food effects")
	}
	for _, f := range restored {
		id := scalar(t, f, 1)
		if scalar(t, f, 2) != uint64(start.UnixMilli())+foods[id]*3*1000 {
			t.Fatalf("food %d repeat truncated remaining time", id)
		}
		if balance, _ := g.s.Balance(g.ctx, 66, id); balance != 0 {
			t.Fatalf("food %d was not fully consumed: %d", id, balance)
		}
	}
}
