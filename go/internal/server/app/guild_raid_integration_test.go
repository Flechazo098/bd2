//go:build integration

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/player"
)

func raidCurrency(t *testing.T, g *guildGameplay) uint64 {
	t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}()
	values, err := g.owner.assembly.eventEconomy.AdditionalCurrencies(command.Context{State: tx, Identity: command.Identity{AccountID: g.account}})
	if err != nil {
		t.Fatal(err)
	}
	return values[44]
}

func TestNewPlayerGuildBootstrapDoesNotRequireGuildUnlock(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared, factory.pvpSource = shared, shared.pvp
	g := &guildGameplay{t: t, factory: factory, account: accounts[0]}
	g.open()
	t.Cleanup(g.close)
	g.pvpCall("/LoginUser", nil)
	if err = g.owner.guildQualification(context.Background(), command.Identity{AccountID: g.account}, "/GuildRaidDeckSave"); !errors.Is(err, social.ErrIneligible) {
		t.Fatalf("new player must start with guild content locked: %v", err)
	}
	before := g.balance()
	// IntroUI.SetData sends both social requests even before guild content is unlocked.
	bootstrap := registryCommand(g.account, "new-player-guild-bootstrap", "/GuildInitInfo", appScalar(1, 2))
	bootstrap.Requests = append(bootstrap.Requests,
		player.Request{Path: "/GuildRaidSeasonReward", Body: appScalar(1, 3)},
		player.Request{Path: "/ItemInfo", Body: appScalar(1, 4)})
	reply, err := g.execute(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Responses) != 3 || reply.Responses[0].PacketCode != 332 || reply.Responses[1].PacketCode != 310 {
		t.Fatalf("incomplete startup batch: %+v", reply.Responses)
	}
	reward := reply.Responses[1].Body
	if appValue(reward, 2) != 0 || len(appRows(reward, 1)) != 0 {
		t.Fatalf("new player received a phantom guild raid reward: %x", reward)
	}
	if after := g.balance(); before != after {
		t.Fatalf("empty startup claim changed wallet: before=%+v after=%+v", before, after)
	}
	entitlements, err := shared.guild.Entitlements(context.Background(), g.account)
	if err != nil || len(entitlements) != 0 {
		t.Fatalf("startup created raid entitlements: %v, %v", entitlements, err)
	}
	g.member(0)
	_, err = g.execute(registryCommand(g.account, "still-locked-raid-deck", "/GuildRaidDeckSave", appScalar(1, 5)))
	var rejection *command.GameError
	if !errors.As(err, &rejection) || rejection.PacketCode != 313 || rejection.ErrorType != 9003 {
		t.Fatalf("locked guild mutation accepted after bootstrap: %v", err)
	}
	g.seq = 5
	if items := g.pvpCall("/ItemInfo", nil); len(items) == 0 {
		t.Fatal("normal player request failed after guild bootstrap")
	}
}

type raidFixture struct {
	factory       *PlayerFactory
	shared        *sharedRuntime
	a, b          *guildGameplay
	guild, season uint64
	seasonEnd     time.Time
	clock         *atomic.Int64
}

func (f *raidFixture) open(g *guildGameplay) {
	g.open()
	f.bindClock(g)
}

func (f *raidFixture) bindClock(g *guildGameplay) {
	config := g.owner.socialServiceConfig()
	config.Now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	service, err := social.NewService(config)
	if err != nil {
		g.t.Fatal(err)
	}
	g.owner.sharedSocial = service
	g.owner.assembly.battleService.AttachEventBattle(service)
}

func newRaidFixture(t *testing.T) *raidFixture {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	clock := new(atomic.Int64)
	clock.Store(time.Now().UnixNano())
	var season uint64
	var seasonEnd time.Time
	for i, content := range factory.options.calendars.RegularService.Contents {
		if content.ID != 6 {
			continue
		}
		season = content.Current.ID
		now := time.Unix(0, clock.Load())
		content.Current.StartMilliseconds = uint64(now.Add(-time.Hour).UnixMilli())
		seasonEnd = now.Add(time.Hour)
		content.Current.EndMilliseconds = uint64(seasonEnd.UnixMilli())
		factory.options.calendars.RegularService.Contents[i] = content
		break
	}
	if season == 0 {
		t.Fatal("selected calendar has no guild raid season")
	}
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared, factory.pvpSource = shared, shared.pvp
	a := &guildGameplay{t: t, factory: factory, account: accounts[0]}
	b := &guildGameplay{t: t, factory: factory, account: accounts[1]}
	f := &raidFixture{factory: factory, shared: shared, a: a, b: b, season: season, seasonEnd: seasonEnd, clock: clock}
	f.open(a)
	f.open(b)
	t.Cleanup(a.close)
	t.Cleanup(b.close)
	seedGuildQualification(t, a.owner)
	seedGuildQualification(t, b.owner)
	f.bindClock(a)
	f.bindClock(b)
	create := guildCreateCommand(t, factory, a.account, "raid-guild-create", "RaidGuild", 1)
	reply, err := a.execute(create)
	if err != nil {
		t.Fatal(err)
	}
	guild := guildIDFromCreate(t, reply)
	f.guild = guild
	b.pvpCall("/GuildJoin", appScalar(2, guild))
	return f
}

func TestNativeGuildRaidPresetsPersistPurchaseAndMetadata(t *testing.T) {
	f := newRaidFixture(t)
	factory, a := f.factory, f.a
	presets := factory.design.presetDesign
	if presets.BaseCount >= presets.Maximum {
		t.Fatal("current guild preset design has no purchasable slot")
	}
	login := appRows(a.pvpCall("/LoginUser", nil), 1)
	if len(login) != 1 || appValue(login[0], 53) != presets.BaseCount {
		t.Fatal("guild preset count before purchase does not match GameData")
	}
	tx, err := a.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := command.Context{Identity: command.Identity{AccountID: a.account}, State: tx}
	if _, err = a.owner.assembly.eventEconomy.Apply(c, "raid-preset-budget", nil, []gamedata.Reward{{Type: presets.PriceType, ID: presets.PriceID, Count: presets.Price}}); err != nil {
		if rollback := tx.Rollback(); rollback != nil {
			t.Error(rollback)
		}
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	walletBefore := a.balance()
	a.seq++
	purchase := registryCommand(a.account, "raid-slot-purchase", "/GuildRaidPresetSlotAdd", append(appScalar(1, a.seq), appScalar(2, 1)...))
	if _, err = a.execute(purchase); err != nil {
		t.Fatal(err)
	}
	walletAfter := walletBefore
	switch presets.PriceType {
	case 2:
		walletAfter.Jewelry -= presets.Price
	case 3:
		walletAfter.FreeJewelry -= presets.Price
	case 4:
		walletAfter.Gold -= presets.Price
	case 12:
		walletAfter.Catalyst -= presets.Price
	default:
		t.Fatal("unsupported authored preset currency")
	}
	if actual := a.balance(); actual != walletAfter {
		t.Fatalf("guild preset purchase charged %+v, want %+v", actual, walletAfter)
	}
	login = appRows(a.pvpCall("/LoginUser", nil), 1)
	if len(login) != 1 || appValue(login[0], 53) != presets.BaseCount+1 {
		t.Fatal("purchased guild preset slot was not projected into login")
	}
	a.close()
	f.open(a)
	login = appRows(a.pvpCall("/LoginUser", nil), 1)
	if len(login) != 1 || appValue(login[0], 53) != presets.BaseCount+1 {
		t.Fatal("guild preset count did not survive player reload")
	}
	if _, err = a.execute(purchase); err != nil {
		t.Fatal(err)
	}
	if a.balance() != walletAfter {
		t.Fatal("durable guild preset slot replay charged twice")
	}
	changedSlot := presets.BaseCount
	fields := appMessage(2, []byte("Raid Setup"))
	fields = append(fields, appScalar(3, 0)...)
	fields = append(fields, appScalar(4, 1)...)
	fields = append(fields, appScalar(5, changedSlot)...)
	a.pvpCall("/GuildRaidPresetInfoChange", fields)
	rows := appRows(a.pvpCall("/GuildRaidPresetInfo", nil), 1)
	found := false
	for _, row := range rows {
		if appValue(row, 4) == changedSlot {
			names := appRows(row, 1)
			found = len(names) == 1 && string(names[0]) == "Raid Setup" && appValue(row, 3) == 1
		}
	}
	if !found {
		t.Fatal("native guild preset metadata update did not persist")
	}
	a.pvpCall("/GuildRaidPresetDelete", appScalar(2, changedSlot))
	for _, row := range appRows(a.pvpCall("/GuildRaidPresetInfo", nil), 1) {
		if appValue(row, 4) == changedSlot {
			t.Fatal("native deleted guild preset remains visible")
		}
	}
}

func TestNativeGuildRaidSettlesBothMembersAndClaimsOnce(t *testing.T) {
	f := newRaidFixture(t)
	factory, shared, a, b, guild, season := f.factory, f.shared, f.a, f.b, f.guild, f.season
	bosses := factory.design.guild.Raid.Bosses[season]
	if len(bosses) == 0 {
		t.Fatal("calendar raid season has no current GameData bosses")
	}
	level := uint64(1)
	boss, exists := bosses[level]
	if !exists {
		t.Fatal("current raid season lacks first boss")
	}
	before := map[string]uint64{a.account: raidCurrency(t, a), b.account: raidCurrency(t, b)}
	scores := map[string]uint64{}
	for i, g := range []*guildGameplay{a, b} {
		_, character := g.ownedPVPDeck()
		deck := appScalar(1, character)
		g.pvpCall("/GuildRaidDeckSave", appMessage(2, deck))
		entered := g.pvpCall("/GuildRaidBossBattleEnter", appScalar(2, level))
		if appValue(entered, 4) != 1 {
			t.Fatal("raid enter omitted native engine mode")
		}
		if i == 0 {
			g.close()
			f.open(g)
			resume := g.pvpCall("/BattleResume", nil)
			mode := appRows(resume, 1)
			if len(mode) != 1 || appValue(mode[0], 1) != social.RaidBattleMode || appValue(mode[0], 2) != boss.DeckID || appValue(mode[0], 3) != 0 || appValue(resume, 3) != level || appValue(resume, 4) != boss.MonsterID || appValue(resume, 17) != season {
				t.Fatal("reconnecting before the first raid turn lost the entered boss or season")
			}
		}
		g.pvpCall("/BattleStart", appScalar(2, boss.DeckID))
		g.seq++
		end := registryCommand(g.account, fmt.Sprintf("raid-end-%d", i), "/BattleEnd", append(appScalar(1, g.seq), appScalar(2, 1)...))
		settled, err := g.execute(end)
		if err != nil {
			t.Fatal(err)
		}
		if len(settled.Responses) != 1 || settled.Responses[0].PacketCode != 15 {
			t.Fatalf("raid did not use native battle settlement: %+v", settled)
		}
		details := appRows(settled.Responses[0].Body, 25)
		if len(details) != 1 {
			t.Fatal("raid result lacks authored score breakdown")
		}
		turn := uint64(math.Max(0, factory.design.guild.Raid.TurnAddScore-factory.design.guild.Raid.TurnScoreValue))
		if appValue(details[0], 1) != boss.AddScore || appValue(details[0], 2) != turn || appValue(details[0], 3) != 0 {
			t.Fatalf("raid score breakdown does not follow one completed round: %x", details[0])
		}
		scores[g.account] = boss.AddScore + turn
		afterBoss := raidCurrency(t, g)
		var expectedBossCurrency uint64
		for _, reward := range boss.Rewards {
			if reward.Type == 39 {
				expectedBossCurrency += reward.Count
			}
		}
		if afterBoss-before[g.account] != expectedBossCurrency {
			t.Fatalf("boss reward currency gain=%d want %d", afterBoss-before[g.account], expectedBossCurrency)
		}
		g.drainPVP()
		retry, err := g.execute(end)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(retry.Responses[0].Body, settled.Responses[0].Body) || raidCurrency(t, g) != afterBoss {
			t.Fatal("raid battle replay duplicated boss reward or changed score")
		}
	}
	members, err := shared.guild.MemberStandings(context.Background(), guild, season)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for _, member := range members {
		got[member.AccountID] = member.Score
	}
	if !maps.Equal(got, scores) {
		t.Fatalf("shared raid bests missing participants: %v want %v", got, scores)
	}
	standing, err := shared.guild.Season(context.Background(), guild, season)
	if err != nil {
		t.Fatal(err)
	}
	total := scores[a.account] + scores[b.account]
	if standing.TotalScore != total || standing.Pending != 0 {
		t.Fatalf("guild score must sum distinct personal bests: %+v", standing)
	}
	ranked := a.pvpCall("/GuildRaidSeasonRanking", appScalar(2, season))
	own := appRows(ranked, 2)
	if len(own) != 1 || appValue(own[0], 3) != guild || appValue(own[0], 2) != total {
		t.Fatal("native guild season ranking omitted own settled guild total")
	}

	beforeRepeat := raidCurrency(t, a)
	a.pvpCall("/GuildRaidBossBattleEnter", appScalar(2, level))
	a.pvpCall("/BattleStart", appScalar(2, boss.DeckID))
	a.pvpCall("/BattleEnd", appScalar(2, 1))
	a.drainPVP()
	standing, err = shared.guild.Season(context.Background(), guild, season)
	if err != nil {
		t.Fatal(err)
	}
	if standing.TotalScore != total || raidCurrency(t, a) != beforeRepeat {
		t.Fatal("equal raid score was summed twice or boss reward repeated")
	}
	f.clock.Store(f.seasonEnd.Add(time.Millisecond).UnixNano())
	if err = shared.guild.FinalizeSeason(context.Background(), season, time.Unix(0, f.clock.Load()).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	b.pvpCall("/GuildLeave", nil)
	departed, err := shared.guild.Account(context.Background(), b.account)
	if err != nil {
		t.Fatal(err)
	}
	if departed.GuildID != 0 {
		t.Fatal("native guild leave did not remove departed member")
	}
	for _, g := range []*guildGameplay{a, b} {
		beforeClaim := raidCurrency(t, g)
		itemsBefore := appItemTotals(g.pvpCall("/ItemInfo", nil))
		g.seq++
		claim := registryCommand(g.account, "raid-score-claim", "/GuildRaidSeasonReward", appScalar(1, g.seq))
		claimed, err := g.execute(claim)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed.Responses) != 1 || claimed.Responses[0].PacketCode != 310 {
			t.Fatal("raid claim has wrong native response")
		}
		if len(claimed.Responses[0].Body) == 0 {
			t.Fatal("earned score reward omitted native grant bundle")
		}
		afterClaim := raidCurrency(t, g)
		itemsAfter := appItemTotals(g.pvpCall("/ItemInfo", nil))
		if afterClaim == beforeClaim && maps.Equal(itemsBefore, itemsAfter) {
			t.Fatal("earned guild score reward did not change any owned assets")
		}
		retry, err := g.execute(claim)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(claimed.Responses[0].Body, retry.Responses[0].Body) || raidCurrency(t, g) != afterClaim || !maps.Equal(itemsAfter, appItemTotals(g.pvpCall("/ItemInfo", nil))) {
			t.Fatal("guild score reward replay credited twice")
		}
		g.close()
		f.open(g)
		durable, err := g.execute(claim)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(durable.Responses[0].Body, claimed.Responses[0].Body) || raidCurrency(t, g) != afterClaim {
			t.Fatal("raid reward durable retry changed grant")
		}
		g.drainPVP()
		ent, err := shared.guild.Entitlements(context.Background(), g.account)
		if err != nil {
			t.Fatal(err)
		}
		if len(ent) != 0 {
			t.Fatalf("confirmed reward remains claimable: %+v", ent)
		}
	}
}
