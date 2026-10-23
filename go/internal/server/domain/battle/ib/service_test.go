//go:build integration

package ib

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"testing"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type ledger struct {
	grants []gamedata.Reward
	fail   bool
}

func (l *ledger) Apply(_ command.Context, _ string, _ []gamedata.Reward, rewards []gamedata.Reward) ([]byte, error) {
	if l.fail {
		return nil, fmt.Errorf("reward storage unavailable")
	}
	l.grants = append(l.grants, rewards...)
	return []byte{8, 1}, nil
}
func liveDesign(t *testing.T) *gamedata.IBDesign {
	t.Helper()
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	versionconfig.Use(versions)
	d, err := gamedata.LoadIBDesign(versions.Resolve("data/resources/GameData"), versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func messages(t *testing.T, b []byte, field int) [][]byte {
	t.Helper()
	var out [][]byte
	if err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == field && f.Type == 2 {
			out = append(out, slices.Clone(f.Value))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertReservedOffer(t *testing.T, response, expected []byte) {
	t.Helper()
	var matches [][]byte
	for _, entry := range messages(t, response, 1) {
		if value(entry, 1) == value(expected, 1) {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("reserved slot occurred %d times, want exactly one", len(matches))
	}
	entry := matches[0]
	if value(entry, 3) != value(expected, 3) || value(entry, 4) != value(expected, 4) || value(entry, 7) != 1 {
		t.Fatal("reservation lost its item, price, or reserved flag")
	}
}

// LocalTextTable 7900025/26/84 and 7900122/23/29 (queried with gamedata_db.py,
// 20261021182514) define loss retries, one first-clear reward, run inventory,
// post-battle shop refresh, and reservations. Request/response fields and base64
// process payload are independently specified by 2.36.7 Proto.Net and IBPacket.
func TestBoardLifecyclePreservesBattleReservationAndFirstClear(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "ib-lifecycle"}, State: store}
	money := &ledger{}
	seasons := make([]uint64, 0, len(d.Seasons))
	for season := range d.Seasons {
		seasons = append(seasons, season)
	}
	slices.Sort(seasons)
	period := Period{Season: seasons[0], Regular: seasons[0], Open: true}
	open := func() *Service {
		t.Helper()
		s, e := Open(ctx, stateio.RootStore{}, d, money, Options{Period: func(command.Context) (Period, error) { return period, nil }})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := open()
	var sequence uint64
	call := func(path string, args ...uint64) ([]byte, error) {
		sequence++
		b := wire.AppendVarint(nil, 1, sequence)
		for n, v := range args {
			b = wire.AppendVarint(b, n+2, v)
		}
		_, out, handled, e := s.Handle(ctx, path, b)
		if !handled {
			t.Fatalf("unhandled %s", path)
		}
		return out, e
	}
	must := func(path string, args ...uint64) []byte {
		t.Helper()
		out, e := call(path, args...)
		if e != nil {
			t.Fatalf("%s: %v", path, e)
		}
		return out
	}
	var dungeon gamedata.IBDungeon
	for _, id := range d.Seasons[period.Season] {
		if candidate := d.Dungeons[id]; candidate.Unlock == 0 {
			dungeon = candidate
			break
		}
	}
	if dungeon.ID == 0 {
		t.Fatal("no initial board in selected season")
	}
	if _, e := call("/IbStageEnd", 1); e == nil {
		t.Fatal("settled without starting a battle")
	}
	entered := must("/IbDungeonEnter", dungeon.ID)
	playInfo := messages(t, entered, 1)[0]
	if value(playInfo, 3) != dungeon.Life || len(messages(t, entered, 2)) != len(d.Decks[d.StartDeck]) {
		t.Fatal("entry did not install authored starting board")
	}
	for _, id := range d.Seasons[period.Season] {
		if d.Dungeons[id].Unlock == dungeon.ID {
			must("/IbDungeonGiveUp")
			if _, e := call("/IbDungeonEnter", id); e == nil {
				t.Fatal("locked successor board entered without clear")
			}
			must("/IbDungeonEnter", dungeon.ID)
			break
		}
	}
	shop := messages(t, must("/IbShopInfo"), 1)
	reserved := slices.Clone(shop[0])
	slot := value(reserved, 1)
	must("/IbShopItemReserve", slot, 1)
	before := s.state.Play.Coin
	refreshed := must("/IbShopRefresh")
	if value(refreshed, 2) == 0 || value(refreshed, 3) >= before {
		t.Fatal("shop refresh did not consume local tokens")
	}
	assertReservedOffer(t, refreshed, reserved)
	s = open()
	start := must("/IbStageStart", dungeon.ID, dungeon.Stages[0].ID)
	encoded := messages(t, start, 1)[0]
	process, e := base64.StdEncoding.DecodeString(string(encoded))
	if e != nil {
		t.Fatal(e)
	}
	if value(process, 2) == 0 || value(process, 3) != dungeon.ID || len(messages(t, process, 7)) == 0 || len(messages(t, process, 8)) == 0 {
		t.Fatal("battle process omitted seeded authored opponent")
	}
	s = open()
	if resumed := must("/IbStageStart", dungeon.ID, dungeon.Stages[0].ID); !bytes.Equal(resumed, start) {
		t.Fatal("reconnect changed battle seed or decks")
	}
	if _, e := call("/IbShopBuy", slot); e == nil {
		t.Fatal("changed inventory while persisted battle was active")
	}
	lost := must("/IbStageEnd", 2)
	if value(lost, 4) != dungeon.Life-1 || value(lost, 5) != 0 {
		t.Fatal("defeat advanced stage or failed to consume retry")
	}
	s = open()
	assertReservedOffer(t, must("/IbShopInfo"), reserved)
	for n, stage := range dungeon.Stages {
		must("/IbStageStart", dungeon.ID, stage.ID)
		if n == len(dungeon.Stages)-1 {
			money.fail = true
			if _, e := call("/IbStageEnd", 1); e == nil {
				t.Fatal("failed reward grant committed clear")
			}
			money.fail = false
			s = open()
		}
		out := must("/IbStageEnd", 1)
		if n+1 < len(dungeon.Stages) {
			if value(out, 5) != dungeon.Stages[n+1].ID {
				t.Fatal("victory did not unlock next stage")
			}
		} else if value(out, 2) != 1 || value(out, 3) != 1 {
			t.Fatal("final victory was not marked first clear")
		}
		s = open()
	}
	if len(money.grants) != 1 || money.grants[0] != dungeon.Reward {
		t.Fatal("first clear did not grant the authored board reward exactly once")
	}
	if len(messages(t, must("/IbItemInfo"), 1)) != 0 || len(messages(t, must("/IbDeckInfo"), 1)) != 0 {
		t.Fatal("completed board retained inventory or deployed units")
	}
	for _, id := range d.Seasons[period.Season] {
		if d.Dungeons[id].Unlock == dungeon.ID {
			must("/IbDungeonEnter", id)
			must("/IbDungeonGiveUp")
			break
		}
	}
	must("/IbDungeonEnter", dungeon.ID)
	for _, stage := range dungeon.Stages {
		must("/IbStageStart", dungeon.ID, stage.ID)
		must("/IbStageEnd", 1)
	}
	if len(money.grants) != 1 {
		t.Fatal("rechallenge paid the first-clear reward twice")
	}
	if len(seasons) > 1 {
		period.Season = seasons[1]
		out := must("/IbMainInfo")
		if value(out, 2) != 1 || s.state.Cleared[dungeon.ID] {
			t.Fatal("season transition preserved old first-clear flags")
		}
		s = open()
	}
	period.Open = false
	if _, e := call("/IbDungeonEnter", d.Seasons[period.Season][0]); e == nil {
		t.Fatal("entered a board outside its season window")
	}
}

func TestInventoryPurchasesUpgradeReplayAndDeckValidation(t *testing.T) {
	d := liveDesign(t)
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{SessionID: "ib-inventory"}, State: store}
	var season, id uint64
	for se, ids := range d.Seasons {
		for _, candidate := range ids {
			if d.Dungeons[candidate].Unlock == 0 {
				season, id = se, candidate
				break
			}
		}
		if id != 0 {
			break
		}
	}
	ownedLevels := map[uint64]uint64{}
	open := func() *Service {
		t.Helper()
		s, e := Open(ctx, stateio.RootStore{}, d, &ledger{}, Options{CostumeLevel: func(_ command.Context, id uint64) (uint64, error) { return ownedLevels[id], nil }, Period: func(command.Context) (Period, error) { return Period{Season: season, Open: true}, nil }})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := open()
	seq := uint64(0)
	call := func(path string, body []byte) ([]byte, error) {
		seq++
		body = append(wire.AppendVarint(nil, 1, seq), body...)
		_, out, _, e := s.Handle(ctx, path, body)
		return out, e
	}
	if _, e := call("/IbDungeonEnter", wire.AppendVarint(nil, 2, id)); e != nil {
		t.Fatal(e)
	}
	var unit, floor item
	for _, i := range s.state.Items {
		if i.Type == 1 {
			unit = i
		}
		if i.Type == 0 {
			floor = i
		}
	}
	if unit.Index == 0 || floor.Index == 0 || len(s.state.Items) < 3 {
		t.Fatal("starting inventory cannot construct the formation scenarios")
	}
	if !slices.Equal(d.Items[unit.ID].Shape, []gamedata.IBPoint{{X: 0, Y: 0}, {X: 1, Y: 0}}) || !slices.Equal(d.Items[floor.ID].Shape, []gamedata.IBPoint{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}, {X: 1, Y: 1}}) {
		t.Fatal("formation scenarios require a horizontal two-cell unit and a two-by-two floor")
	}
	originalDeck := slices.Clone(s.state.Deck)
	invalid := wire.AppendBytes(nil, 2, scalars(unit.Index, 0, 0))
	if _, e := call("/IbDeckSave", invalid); e == nil {
		t.Fatal("accepted unit without supporting floor")
	}
	if !slices.Equal(originalDeck, s.state.Deck) {
		t.Fatal("rejected formation modified deployed board")
	}
	var valid []byte
	for _, p := range originalDeck {
		valid = wire.AppendBytes(valid, 2, deckWire(p))
	}
	if _, e := call("/IbDeckSave", valid); e != nil {
		t.Fatal(e)
	}
	s = open()
	if !slices.Equal(originalDeck, s.state.Deck) {
		t.Fatal("saved formation did not survive reconnect")
	}
	duplicate := wire.AppendBytes(nil, 2, scalars(floor.Index, 0, 0))
	duplicate = wire.AppendBytes(duplicate, 2, scalars(unit.Index, 0, 0))
	duplicate = wire.AppendBytes(duplicate, 2, scalars(unit.Index, 7, 0))
	if _, e := call("/IbDeckSave", duplicate); e == nil {
		t.Fatal("same inventory item was deployed twice")
	}
	if !slices.Equal(originalDeck, s.state.Deck) {
		t.Fatal("rejected duplicate reference modified the saved formation")
	}
	price, canUpgrade := d.UpgradeCosts[[3]uint64{unit.Type, d.Items[unit.ID].TypeID, 2}]
	if !canUpgrade || price == 0 || price > s.state.Play.Coin {
		t.Fatal("selected starting unit has no affordable next-grade enhancement")
	}
	{
		body := wire.AppendVarint(nil, 2, unit.Index)
		body = wire.AppendVarint(body, 3, 1)
		if _, e := call("/IbItemUpgrade", body); e == nil {
			t.Fatal("unowned costume bypassed the enhancement limit")
		}
		ownedLevels[d.Items[unit.ID].CostumeID] = 1
		seq++
		request := append(wire.AppendVarint(nil, 1, seq), body...)
		before := s.state.Play.Coin
		_, up, _, e := s.Handle(ctx, "/IbItemUpgrade", request)
		if e != nil {
			t.Fatal(e)
		}
		s = open()
		_, again, _, e := s.Handle(ctx, "/IbItemUpgrade", request)
		if e != nil || !bytes.Equal(up, again) || s.state.Play.Coin != before-price || s.state.Items[unit.Index].Level != 1 {
			t.Fatal("upgrade replay changed level or paid twice")
		}
		if _, e := call("/IbItemUpgrade", body); e == nil {
			t.Fatal("enhanced beyond the owned costume's enhancement")
		}
	}
	shop, err := call("/IbShopInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	var offer []byte
	for _, r := range messages(t, shop, 1) {
		if value(r, 4) <= s.state.Play.Coin {
			offer = r
			break
		}
	}
	if offer == nil {
		t.Fatal("no affordable initial offer")
	}
	seq++
	request := wire.AppendVarint(nil, 1, seq)
	request = wire.AppendVarint(request, 2, value(offer, 1))
	before := s.state.Play.Coin
	_, bought, _, err := s.Handle(ctx, "/IbShopBuy", request)
	if err != nil {
		t.Fatal(err)
	}
	purchased := messages(t, bought, 1)[0]
	index := value(purchased, 1)
	s = open()
	_, replay, _, err := s.Handle(ctx, "/IbShopBuy", request)
	if err != nil || !bytes.Equal(bought, replay) || s.state.Play.Coin != before-value(offer, 4) {
		t.Fatal("purchase replay duplicated debit")
	}
	if _, err = call("/IbShopBuy", wire.AppendVarint(nil, 2, value(offer, 1))); err == nil {
		t.Fatal("sold slot sold again with another sequence")
	}
	if _, err = call("/IbItemSell", wire.AppendVarint(nil, 2, index)); err != nil {
		t.Fatal(err)
	}
	s = open()
	if _, exists := s.state.Items[index]; exists {
		t.Fatal("sold item reappeared after restore")
	}
	if _, err = call("/IbItemSell", wire.AppendVarint(nil, 2, index)); err == nil {
		t.Fatal("sold item produced more tokens")
	}
	if _, err = call("/IbDungeonGiveUp", nil); err != nil {
		t.Fatal(err)
	}
	s = open()
	if len(s.state.Items) != 0 || len(s.state.Deck) != 0 || s.state.Play.Dungeon != 0 {
		t.Fatal("give-up survived restore as an active run")
	}
	if _, e := call("/IbDungeonEnter", wire.AppendVarint(nil, 2, id)); e != nil {
		t.Fatal(e)
	}
	board := d.Dungeons[id]
	for attempt := uint64(0); attempt < board.Life; attempt++ {
		args := wire.AppendVarint(nil, 2, id)
		args = wire.AppendVarint(args, 3, board.Stages[0].ID)
		if _, e := call("/IbStageStart", args); e != nil {
			t.Fatal(e)
		}
		out, e := call("/IbStageEnd", wire.AppendVarint(nil, 2, 2))
		if e != nil {
			t.Fatal(e)
		}
		if value(out, 4) != board.Life-attempt-1 {
			t.Fatal("loss consumed incorrect challenge count")
		}
		s = open()
	}
	if s.state.Play.Dungeon != 0 || len(s.state.Items) != 0 {
		t.Fatal("exhausted challenges left a resumable board")
	}
}
