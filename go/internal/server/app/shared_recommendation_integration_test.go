//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
	sharedstate "bd2server/internal/server/storage/shared"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

type recommendationAssets struct {
	Wallet assets.Currency
	Views  [][]byte
}

func recommendationAssetState(g *puzzleGameplay) recommendationAssets {
	state := recommendationAssets{Wallet: g.owner.assembly.wallet.Snapshot(command.Context{})}
	for _, path := range []string{"/ItemInfo", "/EquipInfo", "/CharInfo", "/CostumeInfo", "/DeckInfo", "/FieldDeckInfo"} {
		state.Views = append(state.Views, g.call(path, nil))
	}
	return state
}
func recommendationEvents(t *testing.T, g *puzzleGameplay) []outbox.Pending {
	t.Helper()
	tx, e := g.owner.repository.BeginCommand(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback() }()
	pending, e := outbox.Read(tx, 128)
	if e != nil {
		t.Fatal(e)
	}
	var out []outbox.Pending
	for _, event := range pending {
		if event.Envelope.Event.Topic == deck.RecommendationTopic {
			out = append(out, event)
		}
	}
	return out
}
func recommendationBaseFromResponse(t *testing.T, response []byte) ([]byte, []byte) {
	t.Helper()
	rows := appRows(response, 1)
	if len(rows) != 1 {
		t.Fatalf("want one actual public winner, got%d", len(rows))
	}
	encoded := appRows(rows[0], 9)
	if len(encoded) != 1 {
		t.Fatalf("winner did not carry one snapshot: %x", rows[0])
	}
	base, e := base64.StdEncoding.DecodeString(string(encoded[0]))
	if e != nil {
		t.Fatal(e)
	}
	return rows[0], base
}
func recommendationQuestStart(t *testing.T, g *puzzleGameplay, chars []roster.Character) {
	t.Helper()
	g.call("/BattleEnter", append(append(appScalar(3, 1), appScalar(4, 1)...), appScalar(5, 1)...))
	start := appScalar(2, 1)
	for i, c := range chars {
		entry := append(append(appScalar(1, c.InvenIndex), appScalar(2, uint64(i))...), appScalar(3, uint64(i+1))...)
		start = append(start, appMessage(3, entry)...)
		blue := append(append(appScalar(2, c.InvenIndex), appScalar(3, 100)...), appScalar(4, 100)...)
		start = append(start, appMessage(5, blue)...)
	}
	g.call("/BattleStart", start)
}
func recommendationQuestSetup(t *testing.T, g *puzzleGameplay, costumes []uint64) []roster.Character {
	t.Helper()
	var chars []roster.Character
	inventorySetup(t, g, func(ctx command.Context, p *playerAssembly) {
		if len(costumes) != 0 {
			if _, e := p.collection.GrantCostumes(ctx, "recommendation-fixture", costumes, p.design.regularGacha); e != nil {
				t.Fatal(e)
			}
		}
		for _, c := range p.worldService.CharacterService().RawAll() {
			if !roster.IsStoryCharacter(c) && !roster.IsCharmCharacter(c) && (c.ID == 10 || c.ID == 20) {
				chars = append(chars, c)
			}
		}
		if e := p.progressState.SetActivePackID(ctx, 1); e != nil {
			t.Fatal(e)
		}
		for q := 1; q < 9; q++ {
			if e := p.progressState.ClearQuest(ctx, q, 1, 0); e != nil {
				t.Fatal(e)
			}
		}
	})
	return chars
}
func TestPublicRecommendationsFreezeRealWinnersAndCrossPlayerOwnedFilter(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	shared, e := openSharedRuntime(context.Background(), factory.options, factory.design)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = shared.store.Close() })
	factory.shared, factory.pvpSource = shared, shared.pvp
	a := newSquareGameplay(t, factory, accounts[0])
	b := newSquareGameplay(t, factory, accounts[1])
	entered := recommendationQuestSetup(t, a, []uint64{101, 201})
	if len(entered) != 2 {
		t.Fatal("winner fixture requires Lathel and Justia")
	}
	recommendationQuestSetup(t, b, []uint64{301})
	var equip assets.Equipment
	var owner uint64
	var name string
	inventorySetup(t, a, func(ctx command.Context, p *playerAssembly) {
		var e error
		owner, e = p.ProfileOwner(ctx)
		if e != nil {
			t.Fatal(e)
		}
		name = p.nicknameService.CurrentNickname()
		design := p.design.equipmentCraft.Equipment[43010]
		options := func(groups []gamedata.OptionGroup) []assets.EquipmentOption {
			var out []assets.EquipmentOption
			for _, g := range groups {
				if len(g.Choices) == 0 {
					t.Fatal("fixture equipment option missing")
				}
				out = append(out, assets.EquipmentOption{GroupID: g.ID, ID: g.Choices[0].ID})
			}
			return out
		}
		v := assets.Equipment{ID: 43010, Level: 1, Rank: []uint64{1, 1, 1}, UseChar: entered[0].InvenIndex, MainOption: options(design.Main), SubOption: options(design.Sub)}
		if private := options(design.Private); len(private) != 0 {
			v.PrivateOption = &private[0]
		}
		equip, e = p.ownedEquipment.GrantGeneratedOnce(ctx, "recommendation-weapon", v)
		if e != nil {
			t.Fatal(e)
		}
	})
	a.reopen()
	b.reopen()
	query := append(append(appScalar(2, 1), appScalar(3, 1)...), appScalar(4, 1)...)
	recommendationQuestStart(t, a, entered)
	if len(appRows(b.call("/RecommendDeckInfo", query), 1)) != 0 {
		t.Fatal("entered battle published an unearned recommendation")
	}
	// Change both costume growth and equipment after entry: the public win must retain the entered snapshot.
	inventorySetup(t, a, func(ctx command.Context, p *playerAssembly) {
		if _, e := p.collection.GrantCostumes(ctx, "mid-battle-costume-growth", []uint64{101}, p.design.regularGacha); e != nil {
			t.Fatal(e)
		}
	})
	a.call("/EquipClear", append(appScalar(2, equip.InvenIndex), appScalar(3, entered[0].InvenIndex)...))
	failed := a.request("/BattleEnd", appScalar(2, 1))
	failed.Requests = append(failed.Requests, player.Request{Path: "/ItemDump", Body: append(appScalar(1, 900), appMessage(2, inventoryItem(999999, 1, 8, 1))...)})
	a.execute(failed, true)
	a.reopen()
	if len(recommendationEvents(t, a)) != 0 {
		t.Fatal("rolled-back victory leaked a public outbox event")
	}
	end := a.request("/BattleEnd", appScalar(2, 1))
	a.execute(end, false)
	publications := recommendationEvents(t, a)
	if len(publications) != 1 {
		t.Fatalf("committed winner should emit one publication, got%d", len(publications))
	}
	if len(appRows(b.call("/RecommendDeckInfo", query), 1)) != 0 {
		t.Fatal("shared database was written from the unconsumed player transaction")
	}
	for range 2 {
		if e = shared.Consume(context.Background(), publications[0]); e != nil {
			t.Fatal(e)
		}
	}
	row, base := recommendationBaseFromResponse(t, b.call("/RecommendDeckInfo", query))
	if appValue(row, 1) != owner || string(appRows(row, 2)[0]) != name {
		t.Fatal("public recommendation impersonated its real owner/profile")
	}
	// GameData EquipmentGrowth43/1=569 and rank43 first values34,67,101; two level1 characters with one unenhanced costume each contribute520.
	if appValue(base, 1) != 1291 {
		t.Fatalf("published entered power=%d want1291", appValue(base, 1))
	}
	foundCostume := false
	for _, c := range appRows(base, 5) {
		if appValue(c, 2) == 101 {
			foundCostume = true
			if appValue(c, 3) != 0 {
				t.Fatal("settlement reconstructed costume level instead of freezing entry")
			}
		}
	}
	if !foundCostume {
		t.Fatal("entered costume missing from public snapshot")
	}
	equipment := appRows(base, 6)
	if len(equipment) != 1 || appValue(equipment[0], 1) != equip.InvenIndex || appValue(equipment[0], 2) != entered[0].InvenIndex {
		t.Fatalf("settlement lost entered equipment binding: %x", equipment)
	}
	b.call("/RecommendDeckUserOptionSave", appMessage(2, append(appScalar(1, 1), appScalar(2, 1)...)))
	if len(appRows(b.call("/RecommendDeckInfo", query), 1)) != 0 {
		t.Fatal("OnlyOwned displayed character families the querying player lacks")
	}
	inventorySetup(t, b, func(ctx command.Context, p *playerAssembly) {
		if _, e := p.collection.GrantCostumes(ctx, "same-families-other-costume", []uint64{102, 201}, p.design.regularGacha); e != nil {
			t.Fatal(e)
		}
	})
	if len(appRows(b.call("/RecommendDeckInfo", query), 1)) != 1 {
		t.Fatal("OnlyOwned rejected owned character families with a different costume")
	}
	var querying []roster.Character
	inventorySetup(t, b, func(ctx command.Context, p *playerAssembly) {
		for _, c := range p.worldService.CharacterService().RawAll() {
			if c.ID == 10 || c.ID == 20 {
				querying = append(querying, c)
			}
		}
	})
	if len(querying) != 2 || querying[0].InvenIndex == entered[0].InvenIndex {
		t.Fatal("test requires same character designs with different account inventory indices")
	}
	beforeA, beforeB := recommendationAssetState(a), recommendationAssetState(b)
	recommendationBaseFromResponse(t, b.call("/RecommendDeckInfo", query))
	if !reflect.DeepEqual(beforeA, recommendationAssetState(a)) || !reflect.DeepEqual(beforeB, recommendationAssetState(b)) {
		t.Fatal("cross-player recommendation query changed owned assets or active parties")
	}
	if e = shared.store.Close(); e != nil {
		t.Fatal(e)
	}
	shared, e = openSharedRuntime(context.Background(), factory.options, factory.design)
	if e != nil {
		t.Fatal(e)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp
	a.reopen()
	b.reopen()
	a.execute(end, false)
	if e = shared.Consume(context.Background(), publications[0]); e != nil {
		t.Fatal(e)
	}
	recommendationBaseFromResponse(t, b.call("/RecommendDeckInfo", query))
	if len(recommendationEvents(t, a)) != 1 {
		t.Fatal("reopened settlement retry duplicated the winning publication")
	}
	inventorySetup(t, a, func(ctx command.Context, _ *playerAssembly) {
		if e := outbox.Acknowledge(ctx.State, publications[0].Key, publications[0].Digest); e != nil {
			t.Fatal(e)
		}
	})
	a.reopen()
	a.execute(end, false)
	if len(recommendationEvents(t, a)) != 0 {
		t.Fatal("acknowledged publication was recreated by reopened settlement retry")
	}
	// Private is a visibility setting: later delivery of an older win must never undo it.
	a.call("/RecommendDeckUserOptionSave", appMessage(2, nil))
	pending := recommendationEvents(t, a)
	if len(pending) != 1 {
		t.Fatal("private setting did not emit one ordered visibility event")
	}
	for _, event := range slices.Backward(pending) {
		for range 2 {
			if e = shared.Consume(context.Background(), event); e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(appRows(b.call("/RecommendDeckInfo", query), 1)) != 0 {
		t.Fatal("private winner remained public after out-of-order replay")
	}
	if e = shared.Consume(context.Background(), publications[0]); e != nil {
		t.Fatal(e)
	}
	isolatedStore, e := sharedstate.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = isolatedStore.Close() }()
	isolated := &sharedRuntime{store: isolatedStore}
	if e = isolated.consumeRecommendation(context.Background(), pending[0]); e != nil {
		t.Fatal(e)
	}
	if e = isolated.consumeRecommendation(context.Background(), publications[0]); e != nil {
		t.Fatal(e)
	}
	reverse, e := isolated.queryRecommendations(command.Context{}, deck.RecommendContext{BattleMode: 1, PackID: 1, BattleIndex: 1})
	if e != nil || len(reverse) != 0 {
		t.Fatalf("first-delivery older victory overrode newer privacy: %+v %v", reverse, e)
	}
	recommendationQuestStart(t, b, querying)
	b.call("/RecommendDeckUserOptionSave", appMessage(2, appScalar(2, 1)))
	b.call("/BattleEnd", appScalar(2, 1))
	for _, event := range recommendationEvents(t, b) {
		var p deck.RecommendationPublication
		if json.Unmarshal(event.Envelope.Event.Payload, &p) != nil {
			t.Fatal("malformed recommendation publication")
		}
		if p.Record != nil {
			t.Fatal("winner opting out during battle published its frozen roster")
		}
	}
}
