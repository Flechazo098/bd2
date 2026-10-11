//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"testing"
)

func profileGameplay(t *testing.T) *puzzleGameplay {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return g
}

func profileGrant(g *puzzleGameplay, rewards ...gamedata.Reward) {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := g.owner.assembly.eventEconomy.Apply(command.Context{State: tx}, fmt.Sprintf("profile-test:%d", g.seq), nil, rewards); err != nil {
		g.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		g.t.Fatal(err)
	}
}

func profileSelf(g *puzzleGameplay) []byte {
	g.t.Helper()
	identity, err := g.factory.profiles.GameIdentity(context.Background(), g.account)
	if err != nil {
		g.t.Fatal(err)
	}
	return g.call("/UserContentsInfo", wire.AppendVarint(nil, 2, uint64(identity.OwnerIndex)))
}

func profileVarint(t *testing.T, b []byte, field int) uint64 {
	t.Helper()
	n, _, err := wire.Varint(b, field)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func profileBytes(t *testing.T, b []byte, field int) []byte {
	t.Helper()
	value, found, err := wire.Bytes(b, field)
	if err != nil || !found {
		t.Fatalf("missing field%d: %v", field, err)
	}
	return value
}

func profileRows(t *testing.T, b []byte, field int) [][]byte {
	t.Helper()
	var rows [][]byte
	if err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == field {
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			rows = append(rows, f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func profileRepeatedNumbers(t *testing.T, b []byte, field int) []uint64 {
	t.Helper()
	var out []uint64
	if err := wire.Walk(b, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return wire.ErrMalformed
		}
		for raw := f.Value; len(raw) > 0; {
			n, size := binary.Uvarint(raw)
			if size <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, n)
			raw = raw[size:]
			if f.Type == 0 && len(raw) > 0 {
				return wire.ErrMalformed
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPersonalProfileEditsPersistAndRejectUnownedSelections(t *testing.T) {
	g := profileGameplay(t)
	// TitleItemTable ID1 is real Type26; setup grants an owned test title.
	profileGrant(g, gamedata.Reward{Type: 26, ID: 1, Count: 1})
	g.call("/UserGreetingChange", wire.AppendString(nil, 2, "今天很好！"))
	order := []uint64{0, 5, 12, 1, 3, 4, 6, 7, 8, 9, 10, 11}
	options := wire.AppendVarint(nil, 2, 1)
	for _, hidden := range []uint64{1, 12} {
		options = wire.AppendVarint(options, 3, hidden)
	}
	for _, n := range order {
		options = wire.AppendVarint(options, 4, n)
	}
	g.call("/UpdateUserContentsInfoOption", options)
	g.call("/UserTitleChange", wire.AppendVarint(nil, 2, 1))
	portrait := g.owner.assembly.collection.Costumes()[0].ID
	g.call("/UserPortraitChange", wire.AppendVarint(nil, 2, portrait))
	g.reopen()
	assertSettings := func() {
		t.Helper()
		body := profileSelf(g)
		if string(profileBytes(t, body, 5)) != "今天很好！" || profileVarint(t, body, 11) != 1 || profileVarint(t, body, 3) != 1 || profileVarint(t, body, 4) != portrait {
			t.Fatal("saved profile edits are missing")
		}
		sorts := profileRepeatedNumbers(t, body, 27)
		hidden := profileRepeatedNumbers(t, body, 12)
		slices.Sort(hidden)
		if !slices.Equal(sorts, order) || !slices.Equal(hidden, []uint64{1, 12}) {
			t.Fatalf("saved visibility/order=%v/%v", hidden, sorts)
		}
		login := profileBytes(t, g.call("/LoginUser", nil), 1)
		if profileVarint(t, login, 66) != 1 {
			t.Fatal("LoginUser did not project selected owned title")
		}
	}
	assertSettings()
	for _, bad := range []string{"abc", "hello\tworld", "hello😀"} {
		g.execute(g.request("/UserGreetingChange", wire.AppendString(nil, 2, bad)), true)
	}
	for _, bad := range []struct {
		path   string
		fields []byte
	}{
		{"/UserTitleChange", wire.AppendVarint(nil, 2, 2)},
		{"/UserPortraitChange", wire.AppendVarint(nil, 2, math.MaxInt32)},
		{"/UpdateUserContentsInfoOption", wire.AppendVarint(nil, 3, 0)},
		{"/UpdateUserContentsInfoOption", wire.AppendVarint(wire.AppendVarint(nil, 4, 0), 4, 5)},
	} {
		g.execute(g.request(bad.path, bad.fields), true)
	}
	assertSettings()
	g.call("/UserTitleChange", nil)
	g.call("/UserPortraitChange", nil)
	g.reopen()
	if body := profileSelf(g); profileVarint(t, body, 3) != 0 || profileVarint(t, body, 4) != 0 {
		t.Fatal("cleared title/portrait did not persist")
	}
	if profileVarint(t, profileBytes(t, g.call("/LoginUser", nil), 1), 66) != 0 {
		t.Fatal("cleared title remains in LoginUser")
	}
}

func TestNewPlayerCanPersistGameDataDefaultPortraitWithoutOwningCostume(t *testing.T) {
	g := profileGameplay(t)
	login := profileBytes(t, g.call("/LoginUser", nil), 1)
	if profileVarint(t, login, 14) != 0 {
		t.Fatal("fresh player must start without a selected portrait")
	}
	// UserDBInfo.SetPortraitCostumeId sends GameDefaultTable field29 when the login portrait is zero.
	defaultPortrait := g.factory.design.profileDesign.DefaultPortraitCostumeID
	if defaultPortrait == 0 {
		t.Fatal("GameData default portrait is missing")
	}
	if _, owned := g.owner.assembly.collection.CostumeByID(defaultPortrait); owned {
		t.Fatal("starter already owns the default portrait; unowned default scenario was lost")
	}
	costumeCount := len(g.owner.assembly.collection.Costumes())
	changed := g.call("/UserPortraitChange", wire.AppendVarint(nil, 2, defaultPortrait))
	if profileVarint(t, changed, 1) != defaultPortrait {
		t.Fatal("default portrait request was not acknowledged")
	}
	g.reopen()
	assertDefault := func() {
		t.Helper()
		if profileVarint(t, profileSelf(g), 4) != defaultPortrait {
			t.Fatal("default portrait selection did not persist in public profile")
		}
		if profileVarint(t, profileBytes(t, g.call("/LoginUser", nil), 1), 14) != defaultPortrait {
			t.Fatal("LoginUser did not project the saved default portrait")
		}
		if _, owned := g.owner.assembly.collection.CostumeByID(defaultPortrait); owned || len(g.owner.assembly.collection.Costumes()) != costumeCount {
			t.Fatal("selecting the default portrait granted costume ownership")
		}
	}
	assertDefault()
	_, candidates := g.factory.design.regularGacha.MigrationFacts()
	var unowned uint64
	for _, candidate := range candidates {
		if candidate.CostumeID == defaultPortrait {
			continue
		}
		if _, owned := g.owner.assembly.collection.CostumeByID(candidate.CostumeID); !owned {
			unowned = candidate.CostumeID
			break
		}
	}
	if unowned == 0 {
		t.Fatal("current GameData has no unowned alternate costume for the ownership check")
	}
	g.execute(g.request("/UserPortraitChange", wire.AppendVarint(nil, 2, unowned)), true)
	g.reopen()
	assertDefault()
}

func profileCardItem(index, id uint64, x, y int32, scale float32, color string) []byte {
	b := wire.AppendVarint(nil, 1, index)
	b = wire.AppendVarint(b, 2, id)
	b = wire.AppendVarint(b, 3, uint64(int64(x)))
	b = wire.AppendVarint(b, 4, uint64(int64(y)))
	b = binary.AppendUvarint(b, 6<<3|5)
	b = binary.LittleEndian.AppendUint32(b, math.Float32bits(scale))
	return wire.AppendString(b, 8, color)
}

func profileBuy(id, price uint64) []byte {
	payment := wire.AppendVarint(nil, 3, 44)
	payment = wire.AppendVarint(payment, 4, price)
	item := wire.AppendVarint(nil, 1, id)
	item = wire.AppendVarint(item, 2, 1)
	return wire.AppendBytes(nil, 2, wire.AppendBytes(item, 3, payment))
}

func TestIdCardRecoveryShopLayoutsAndPresetsUseRealPersistentOwnership(t *testing.T) {
	g := profileGameplay(t)
	// Current common GameData: 920000 opens to 10001..10020 and40001,
	// each Type47 count1. Shop10101 costs100 Type44, illustration30001 costs150.
	if g.itemCount(47, 10001) != 0 {
		t.Fatal("fixture unexpectedly owns default card")
	}
	recovery := g.request("/IdCardRecovery", nil)
	first := g.execute(recovery, false)
	if !bytes.Equal(first, g.execute(recovery, false)) {
		t.Fatal("recovery replay differs")
	}
	profileBytes(t, first, 1)
	defaultCard := profileBytes(t, first, 2)
	background := profileBytes(t, defaultCard, 1)
	if profileVarint(t, background, 2) != 10001 || profileVarint(t, background, 1) == 0 {
		t.Fatal("default layout is not bound to actual inventory")
	}
	for id := uint64(10001); id <= 10020; id++ {
		if g.itemCount(47, id) != 1 {
			t.Fatalf("default background%d count !=1", id)
		}
	}
	if g.itemCount(47, 40001) != 1 {
		t.Fatal("default sticker grant missing")
	}
	g.reopen()
	recoveryReplay := recovery
	recoveryReplay.Identity.RequestID += "-domain-replay"
	if !bytes.Equal(first, g.execute(recoveryReplay, false)) {
		t.Fatal("reopened recovery domain receipt did not replay its actual reward bundle")
	}
	second := g.call("/IdCardRecovery", nil)
	if _, found, err := wire.Bytes(second, 1); found || err != nil {
		t.Fatal("fresh recovery repeated default grant")
	}
	for id := uint64(10001); id <= 10020; id++ {
		if g.itemCount(47, id) != 1 {
			t.Fatalf("recovery duplicated background%d", id)
		}
	}
	profileGrant(g, gamedata.Reward{Type: 44, Count: 500})
	budget := g.wordCurrency()
	g.execute(g.request("/IdCardShopBuy", profileBuy(10101, 99)), true)
	batch := append(profileBuy(10101, 100), profileBuy(30001, 149)...)
	g.execute(g.request("/IdCardShopBuy", batch), true)
	if g.wordCurrency() != budget || g.itemCount(47, 10101) != 0 || g.itemCount(47, 30001) != 0 {
		t.Fatal("rejected batch spent money or granted a partial purchase")
	}
	buy := g.request("/IdCardShopBuy", append(profileBuy(10101, 100), profileBuy(30001, 150)...))
	reply := g.execute(buy, false)
	if !bytes.Equal(reply, g.execute(buy, false)) {
		t.Fatal("purchase replay differs")
	}
	domainReplay := buy
	domainReplay.Identity.RequestID += "-domain-replay"
	if !bytes.Equal(reply, g.execute(domainReplay, false)) {
		t.Fatal("card purchase domain receipt did not replay for a new actor request identity")
	}
	conflict := buy
	conflict.Identity.RequestID += "-domain-conflict"
	conflict.Requests = slices.Clone(buy.Requests)
	// Independent GameData:10102 is unrestricted, unowned, max1 and costs100.
	// Its otherwise-valid purchase must fail solely because this seq is spent.
	if g.itemCount(47, 10102) != 0 {
		t.Fatal("conflict fixture already owns alternative product")
	}
	conflict.Requests[0].Body = append(wire.AppendVarint(nil, 1, profileVarint(t, buy.Requests[0].Body, 1)), profileBuy(10102, 100)...)
	conflict.Digest = sha256.Sum256(append([]byte("/IdCardShopBuy"), conflict.Requests[0].Body...))
	g.execute(conflict, true)
	if g.itemCount(47, 10102) != 0 || g.wordCurrency() != budget-250 {
		t.Fatal("conflicting economic retry granted alternative product or spent more currency")
	}
	if g.wordCurrency() != budget-250 || g.itemCount(47, 10101) != 1 || g.itemCount(47, 30001) != 1 {
		t.Fatal("card purchase did not exchange actual currency for actual items once")
	}
	g.execute(g.request("/IdCardShopBuy", profileBuy(10101, 100)), true)
	card := wire.AppendBytes(nil, 1, profileCardItem(0, 10101, -123, 45, 1, ""))
	card = wire.AppendBytes(card, 4, profileCardItem(0, 30001, 12, -34, 0.5, ""))
	card = wire.AppendBytes(card, 5, profileCardItem(0, 0, -218, 156, 0.75, "FFFFFF80"))
	card = wire.AppendVarint(card, 6, 90)
	g.call("/IdCardSave", wire.AppendBytes(nil, 2, card))
	for index := range uint64(5) {
		preset := wire.AppendVarint(nil, 1, index)
		preset = wire.AppendBytes(preset, 2, card)
		g.call("/IdCardPresetSave", wire.AppendBytes(nil, 2, preset))
	}
	g.reopen()
	assertLayout := func(saved []byte) {
		t.Helper()
		if profileVarint(t, saved, 6) != 90 {
			t.Fatal("card rotation did not persist")
		}
		bg := profileBytes(t, saved, 1)
		if profileVarint(t, bg, 2) != 10101 || profileVarint(t, bg, 1) == 0 || int32(profileVarint(t, bg, 3)) != -123 {
			t.Fatal("saved card lost purchased background/index/negative coordinate")
		}
		art := profileRows(t, saved, 4)
		if len(art) != 1 || profileVarint(t, art[0], 2) != 30001 || profileVarint(t, art[0], 1) == 0 {
			t.Fatal("owned illustration not saved with inventory instance")
		}
		if string(profileBytes(t, profileBytes(t, saved, 5), 8)) != "FFFFFF80" {
			t.Fatal("RGBA alpha did not persist")
		}
	}
	assertLayout(profileBytes(t, profileSelf(g), 28))
	if rows := profileRows(t, g.call("/IdCardPresetInfo", nil), 1); len(rows) != 5 {
		t.Fatalf("preset count=%d want5", len(rows))
	} else {
		seen := map[uint64]bool{}
		for _, row := range rows {
			id := profileVarint(t, row, 1)
			if id >= 5 || seen[id] {
				t.Fatal("preset IDs do not contain distinct0..4")
			}
			seen[id] = true
			assertLayout(profileBytes(t, row, 2))
		}
	}
	badCards := [][]byte{
		wire.AppendVarint(wire.AppendBytes(nil, 1, background), 6, 1),
		wire.AppendBytes(nil, 1, profileCardItem(999999999, 10101, 0, 0, 1, "")),
		wire.AppendBytes(nil, 4, profileCardItem(0, 30002, 0, 0, 1, "")),
		wire.AppendBytes(nil, 1, profileCardItem(0, 10101, 0, 0, float32(math.NaN()), "")),
		wire.AppendBytes(nil, 1, profileCardItem(0, 10101, 0, 0, 1, "BADBAD")),
		wire.AppendBytes(wire.AppendBytes(nil, 4, profileCardItem(0, 30001, 0, 0, 1, "")), 4, profileCardItem(0, 30001, 0, 0, 1, "")),
	}
	for _, bad := range badCards {
		g.execute(g.request("/IdCardSave", wire.AppendBytes(nil, 2, bad)), true)
	}
	preset := wire.AppendVarint(nil, 1, 5)
	preset = wire.AppendBytes(preset, 2, card)
	g.execute(g.request("/IdCardPresetSave", wire.AppendBytes(nil, 2, preset)), true)
	assertLayout(profileBytes(t, profileSelf(g), 28))
	g.call("/IdCardPresetDelete", wire.AppendVarint(nil, 2, 4))
	g.reopen()
	if rows := profileRows(t, g.call("/IdCardPresetInfo", nil), 1); len(rows) != 4 {
		t.Fatal("preset deletion did not persist")
	}
	counts := profileRows(t, g.call("/IdCardShopInfo", nil), 1)
	if len(counts) != 2 {
		t.Fatalf("persisted shop purchases=%d want2", len(counts))
	}
	for _, row := range counts {
		if profileVarint(t, row, 2) != 1 {
			t.Fatal("shop purchase count duplicated")
		}
	}
	if g.wordCurrency() != budget-250 || g.itemCount(47, 10101) != 1 || g.itemCount(47, 30001) != 1 {
		t.Fatal("card edits or reopening changed purchase economy")
	}
}

func TestSupporterRegistrationPreservesSlotsAndReturnsOwnedBattleCharacter(t *testing.T) {
	g := profileGameplay(t)
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Current CostumeTable101 is permanent character UniqueChar1. The
	// integration starter only has story characters, so grant actual ownership.
	if _, err := g.owner.assembly.collection.GrantCostumes(command.Context{State: tx}, "profile-supporter-fixture", []uint64{101}, g.factory.design.rewardCostumes); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	characters := g.owner.assembly.worldService.CharacterService().All(command.Context{State: tx})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var charIndex, costumeIndex, costumeID uint64
	for _, char := range characters {
		if roster.IsStoryCharacter(char) || roster.IsCharmCharacter(char) {
			continue
		}
		for _, costume := range g.owner.assembly.collection.Costumes() {
			d, ok := g.factory.design.rewardCostumes.Character(costume.ID)
			if ok && (char.ID == d.ID || slices.Contains(d.GrowthCharacterIDs, char.ID)) {
				charIndex, costumeIndex, costumeID = char.InvenIndex, costume.InvenIndex, costume.ID
				break
			}
		}
		if charIndex != 0 {
			break
		}
	}
	if charIndex == 0 || costumeIndex == 0 {
		t.Fatal("fixture has no permanent equipped character")
	}
	register := wire.AppendVarint(nil, 2, 1)
	register = wire.AppendVarint(register, 3, charIndex)
	register = wire.AppendVarint(register, 4, costumeIndex)
	register = wire.AppendVarint(register, 5, 123)
	g.call("/SupporterRegister", register)
	g.reopen()
	for _, query := range []struct {
		path  string
		field int
	}{{"/SupporterInfo", 1}, {"/SupporterStatus", 2}} {
		rows := profileRows(t, g.call(query.path, nil), query.field)
		if len(rows) != 2 || profileVarint(t, rows[0], 1) != 0 || profileVarint(t, rows[1], 1) != costumeID {
			t.Fatalf("%s lost supporter slot1 placeholder: %v", query.path, rows)
		}
	}
	identity, err := g.factory.profiles.GameIdentity(context.Background(), g.account)
	if err != nil {
		t.Fatal(err)
	}
	detail := wire.AppendVarint(nil, 2, uint64(identity.OwnerIndex))
	detail = wire.AppendVarint(detail, 3, 1)
	slot := profileBytes(t, g.call("/SupporterDetail", detail), 1)
	if profileVarint(t, slot, 1) != uint64(identity.OwnerIndex) || profileVarint(t, slot, 2) != 1 || profileVarint(t, slot, 3) != costumeID || profileVarint(t, slot, 4) != 123 || profileVarint(t, slot, 7) == 0 {
		t.Fatal("supporter details do not reflect actual registered state")
	}
	battle, err := base64.StdEncoding.DecodeString(string(profileBytes(t, slot, 6)))
	if err != nil {
		t.Fatal(err)
	}
	character := profileBytes(t, battle, 1)
	if profileVarint(t, character, 1) != charIndex || profileVarint(t, character, 5) != costumeID || profileVarint(t, character, 7) != costumeIndex {
		t.Fatal("supporter battle snapshot is not the chosen owned character/costume")
	}
	if len(profileRows(t, battle, 2)) == 0 {
		t.Fatal("supporter snapshot has no real costumes")
	}
	duplicate := wire.AppendVarint(nil, 3, charIndex)
	duplicate = wire.AppendVarint(duplicate, 4, costumeIndex)
	g.execute(g.request("/SupporterRegister", duplicate), true)
	for _, temporary := range characters {
		if roster.IsStoryCharacter(temporary) || roster.IsCharmCharacter(temporary) {
			bad := wire.AppendVarint(nil, 3, temporary.InvenIndex)
			bad = wire.AppendVarint(bad, 4, costumeIndex)
			g.execute(g.request("/SupporterRegister", bad), true)
			break
		}
	}
	g.execute(g.request("/SupporterRegister", wire.AppendVarint(wire.AppendVarint(nil, 2, 2), 3, charIndex)), true)
	g.execute(g.request("/SupporterRegister", wire.AppendVarint(wire.AppendVarint(nil, 3, math.MaxInt64), 4, costumeIndex)), true)
	g.execute(g.request("/SupporterDetail", wire.AppendVarint(wire.AppendVarint(nil, 2, math.MaxInt64), 3, 1)), true)
	g.execute(g.request("/SupporterReward", nil), true)
	g.call("/SupporterRemove", wire.AppendVarint(nil, 2, 1))
	g.reopen()
	rows := profileRows(t, g.call("/SupporterInfo", nil), 1)
	if len(rows) != 2 || profileVarint(t, rows[0], 1) != 0 || profileVarint(t, rows[1], 1) != 0 {
		t.Fatal("supporter removal did not persist")
	}
}
