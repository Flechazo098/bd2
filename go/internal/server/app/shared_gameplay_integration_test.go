//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

var errGuildConfirmationUnavailable = errors.New("guild confirmation unavailable")

type guildConfirmationFailure struct {
	social.Store
	failNext atomic.Bool
}

func (s *guildConfirmationFailure) Update(ctx context.Context, apply func(social.Tx) error) error {
	if s.failNext.CompareAndSwap(true, false) {
		return errGuildConfirmationUnavailable
	}
	return s.Store.Update(ctx, apply)
}

type guildConfirmationObserver struct{ store *guildConfirmationFailure }

func (g guildConfirmationObserver) BeforeDispatch(command.Context, string, []byte) error { return nil }
func (g guildConfirmationObserver) AfterDispatch(_ command.Context, path string, _, _ []byte) ([]byte, error) {
	if path == "/GuildCreate" {
		g.store.failNext.Store(true)
	}
	return nil, nil
}

type guildGameplay struct {
	t       *testing.T
	factory *PlayerFactory
	owner   *playerInstance
	runtime *player.Runtime
	account string
	seq     uint64
}

func (g *guildGameplay) open() {
	g.t.Helper()
	var err error
	g.owner, err = g.factory.open(g.account)
	if err != nil {
		g.t.Fatal(err)
	}
	g.runtime, err = player.New(g.account, g.owner, player.Limits{})
	if err != nil {
		g.t.Fatal(err)
	}
}
func (g *guildGameplay) close() {
	g.t.Helper()
	if g.runtime != nil {
		if err := g.runtime.Close(context.Background()); err != nil {
			g.t.Error(err)
		}
		g.runtime = nil
		g.owner = nil
	}
}
func (g *guildGameplay) execute(c player.Command) (player.Reply, error) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f, err := g.runtime.Submit(ctx, c)
	if err != nil {
		return player.Reply{}, err
	}
	return f.Wait(ctx)
}
func (g *guildGameplay) balance() assets.Currency {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			g.t.Error(err)
		}
	}()
	return g.owner.assembly.wallet.Snapshot(command.Context{State: tx})
}
func (g *guildGameplay) reconcile(r social.Reservation) {
	g.t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		g.t.Fatal(err)
	}
	_, err = g.execute(player.Command{Identity: command.Identity{AccountID: g.account, SessionID: "guild-recovery", RequestID: r.ID}, Digest: sha256.Sum256(raw), System: &player.SystemRequest{Kind: player.ReconcileSocial, Body: raw}})
	if err != nil {
		g.t.Fatal(err)
	}
}
func (g *guildGameplay) pending() social.Reservation {
	g.t.Helper()
	ops, _, err := g.factory.shared.guild.PendingOperations(context.Background(), "", 128)
	if err != nil {
		g.t.Fatal(err)
	}
	for _, op := range ops {
		if op.ActorAccount == g.account {
			return op
		}
	}
	g.t.Fatal("expected recoverable guild operation")
	return social.Reservation{}
}
func (g *guildGameplay) member(guildID uint64) {
	g.t.Helper()
	account, err := g.factory.shared.guild.Account(context.Background(), g.account)
	if err != nil {
		g.t.Fatal(err)
	}
	if account.GuildID != guildID {
		g.t.Fatalf("membership = %d, want %d", account.GuildID, guildID)
	}
	if guildID != 0 {
		guild, err := g.factory.shared.guild.Guild(context.Background(), guildID)
		if err != nil {
			g.t.Fatal(err)
		}
		member, exists := guild.Members[g.account]
		if !exists || member.Role != social.Admin || len(guild.Members) != 1 {
			g.t.Fatalf("created guild membership is wrong: %+v", guild)
		}
	}
}

func seedGuildQualification(t *testing.T, owner *playerInstance) {
	t.Helper()
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}()
	c := command.Context{Identity: command.Identity{AccountID: owner.accountID}, State: tx}
	rule := owner.factory.design.guild.Open
	grant := owner.factory.design.guild.Guild.CreateCost
	grant.Count *= 4
	if _, err = owner.assembly.eventEconomy.Apply(c, "guild-fixture-budget", nil, []gamedata.Reward{grant}); err != nil {
		t.Fatal(err)
	}
	if rule.TicketID != 0 {
		if _, err = owner.assembly.ownedItems.GrantOnce(c, "guild-fixture-unlock", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err = owner.assembly.missionService.EnsurePersisted(c); err != nil {
		t.Fatal(err)
	}
	raw, err := tx.Load("missions")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	var claimed []string
	if err = json.Unmarshal(state["claimed"], &claimed); err != nil {
		t.Fatal(err)
	}
	keys := make([]gamedata.AchievementKey, 0, len(owner.factory.design.missionDesign.Achievements))
	for key := range owner.factory.design.missionDesign.Achievements {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.ContentsGroup != b.ContentsGroup {
			return a.ContentsGroup < b.ContentsGroup
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		return a.ID < b.ID
	})
	var experience uint64
	for _, key := range keys {
		entry := fmt.Sprintf("achievement:%d/%d/%d", key.ContentsGroup, key.GroupID, key.ID)
		if slices.Contains(claimed, entry) {
			experience += owner.factory.design.missionDesign.Achievements[key].AddExp
		}
	}
	for _, key := range keys {
		if owner.factory.design.levelDesign.Level(experience) >= rule.SquadLevel {
			break
		}
		entry := fmt.Sprintf("achievement:%d/%d/%d", key.ContentsGroup, key.GroupID, key.ID)
		if slices.Contains(claimed, entry) {
			continue
		}
		claimed = append(claimed, entry)
		experience += owner.factory.design.missionDesign.Achievements[key].AddExp
	}
	if owner.factory.design.levelDesign.Level(experience) < rule.SquadLevel {
		t.Fatal("current GameData has insufficient achievement experience for guild unlock")
	}
	state["claimed"], err = json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Save("missions", raw); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = owner.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func guildCreateCommand(t *testing.T, factory *PlayerFactory, account, id, name string, seq uint64) player.Command {
	t.Helper()
	var flags, emblems []uint64
	for id := range factory.design.guild.Guild.Flags {
		flags = append(flags, id)
	}
	for id := range factory.design.guild.Guild.Emblems {
		emblems = append(emblems, id)
	}
	slices.Sort(flags)
	slices.Sort(emblems)
	if len(flags) == 0 || len(emblems) == 0 {
		t.Fatal("current GameData has no guild icons")
	}
	body := appScalar(1, seq)
	body = append(body, appMessage(2, []byte(name))...)
	body = append(body, appScalar(3, emblems[0]*100000+flags[0])...)
	body = append(body, appMessage(4, []byte("#FFFFFFFF"))...)
	body = append(body, appMessage(5, []byte("Guild integration"))...)
	body = append(body, appScalar(6, uint64(social.OpenJoin))...)
	return registryCommand(account, id, "/GuildCreate", body)
}
func guildIDFromCreate(t *testing.T, reply player.Reply) uint64 {
	t.Helper()
	if len(reply.Responses) != 1 || reply.Responses[0].PacketCode != 296 {
		t.Fatalf("invalid GuildCreate response: %+v", reply)
	}
	info := appRows(reply.Responses[0].Body, 1)
	if len(info) != 1 {
		t.Fatal("GuildCreate lacks guild info")
	}
	base := appRows(info[0], 1)
	if len(base) != 1 {
		t.Fatal("guild info lacks base info")
	}
	id := appValue(base[0], 1)
	if id == 0 {
		t.Fatal("created guild has no identity")
	}
	return id
}
func expectedGuildCharge(t *testing.T, factory *PlayerFactory, before assets.Currency) assets.Currency {
	t.Helper()
	cost := factory.design.guild.Guild.CreateCost
	switch cost.Type {
	case 3:
		if before.FreeJewelry < cost.Count {
			t.Fatal("fixture budget too small")
		}
		before.FreeJewelry -= cost.Count
	case 2:
		before.Jewelry -= cost.Count
	case 4:
		before.Gold -= cost.Count
	default:
		t.Fatalf("unsupported current GD guild currency %d", cost.Type)
	}
	return before
}

func TestSharedGuildGameplayTransactionsAndRecovery(t *testing.T) {
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
	failures := &guildConfirmationFailure{Store: socialStore{shared.store}}
	shared.guild, err = social.New(failures, factory.design.guild)
	if err != nil {
		t.Fatal(err)
	}
	factory.shared, factory.pvpSource = shared, shared.pvp

	t.Run("rollback recovers absent receipt then create charges once", func(t *testing.T) {
		g := &guildGameplay{t: t, factory: factory, account: accounts[0]}
		g.open()
		t.Cleanup(g.close)
		seedGuildQualification(t, g.owner)
		before := g.balance()
		failed := guildCreateCommand(t, factory, g.account, "guild-batch-fail", "Rollback", 1)
		failed.Requests = append(failed.Requests, player.Request{Path: "/GuildIntegrationUnknownPacket", Body: appScalar(1, 2)})
		failed.Digest = sha256.Sum256([]byte("guild-batch-rollback"))
		if _, err := g.execute(failed); err == nil {
			t.Fatal("batch with unimplemented packet unexpectedly committed")
		}
		if got := g.balance(); got != before {
			t.Fatalf("failed batch charged guild creation: %+v want %+v", got, before)
		}
		g.member(0)
		reservation := g.pending()
		g.close()
		g.open()
		g.reconcile(reservation)
		status, err := shared.guild.Reservation(context.Background(), reservation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status != social.Cancelled {
			t.Fatalf("absent committed receipt was not cancelled: %s", status.Status)
		}
		if _, err := shared.guild.Guild(context.Background(), reservation.GuildID); !errors.Is(err, social.ErrNotFound) {
			t.Fatalf("rolled back guild remained visible: %v", err)
		}
		create := guildCreateCommand(t, factory, g.account, "guild-create-ok", "Committed", 3)
		reply, err := g.execute(create)
		if err != nil {
			t.Fatal(err)
		}
		gid := guildIDFromCreate(t, reply)
		g.member(gid)
		want := expectedGuildCharge(t, factory, before)
		if got := g.balance(); got != want {
			t.Fatalf("guild create charge = %+v want %+v", got, want)
		}
		duplicate, err := g.execute(create)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(duplicate.Responses[0].Body, reply.Responses[0].Body) {
			t.Fatal("duplicate changed guild response")
		}
		if got := g.balance(); got != want {
			t.Fatal("duplicate guild create charged twice")
		}
		g.close()
		g.open()
		if _, err = g.execute(create); err != nil {
			t.Fatal(err)
		}
		g.member(gid)
		if got := g.balance(); got != want {
			t.Fatal("durable command replay charged twice")
		}
	})
	t.Run("committed receipt confirms after player reload", func(t *testing.T) {
		g := &guildGameplay{t: t, factory: factory, account: accounts[1]}
		g.open()
		t.Cleanup(g.close)
		seedGuildQualification(t, g.owner)
		before := g.balance()
		g.owner.assembly.observers = append(g.owner.assembly.observers, guildConfirmationObserver{store: failures})
		create := guildCreateCommand(t, factory, g.account, "guild-confirm-unavailable", "Recovered", 1)
		if _, err := g.execute(create); !errors.Is(err, errGuildConfirmationUnavailable) {
			t.Fatalf("expected post-commit confirmation failure, got %v", err)
		}
		want := expectedGuildCharge(t, factory, before)
		if got := g.balance(); got != want {
			t.Fatalf("player transaction was not committed before confirmation failed: %+v", got)
		}
		g.member(0)
		reservation := g.pending()
		g.close()
		g.open()
		g.reconcile(reservation)
		g.member(reservation.GuildID)
		status, err := shared.guild.Reservation(context.Background(), reservation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status != social.Confirmed {
			t.Fatalf("durable receipt was not confirmed: %s", status.Status)
		}
		replay, err := g.execute(create)
		if err != nil {
			t.Fatal(err)
		}
		if id := guildIDFromCreate(t, replay); id != reservation.GuildID {
			t.Fatalf("replayed guild %d != recovered %d", id, reservation.GuildID)
		}
		if got := g.balance(); got != want {
			t.Fatal("recovery or replay charged twice")
		}
	})
}

type rankingStorageGate struct {
	ranking.Store
	blocked chan struct{}
	armed   atomic.Bool
}

func (s *rankingStorageGate) Update(ctx context.Context, write func(ranking.Tx) error) error {
	if s.armed.CompareAndSwap(true, false) {
		close(s.blocked)
	}
	return s.Store.Update(ctx, write)
}

func TestSharedPVPReadsContinueWhileProjectionWaitsForStorage(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	state, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.store.Close(); err != nil {
			t.Error(err)
		}
	})
	gate := &rankingStorageGate{Store: rankingStore{state.store}, blocked: make(chan struct{})}
	projection, err := ranking.NewPVP(context.Background(), gate, factory.design.pvp.Design, factory.design.pvp.Seasons)
	if err != nil {
		t.Fatal(err)
	}
	season := factory.design.pvp.Seasons[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- state.store.Update(ctx, func(*shared.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	released := false
	projectionDone := make(chan error, 1)
	defer func() {
		if !released {
			close(release)
		}
		if err := <-holderDone; err != nil {
			t.Error(err)
		}
		if err := <-projectionDone; err != nil {
			t.Error(err)
		}
	}()
	publication := pvp.DefensePublished{Version: 1, Defense: pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: accounts[0], OwnerIndex: 1}, Mode: season.Mode, Season: season.ID, Version: 1}}
	payload, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	gate.armed.Store(true)
	go func() {
		projectionDone <- projection.Consume(ctx, accounts[0], command.Event{ID: "pvp-read-lock-probe", Topic: "pvp.defense.published", Payload: payload})
	}()
	select {
	case <-gate.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("projection never reached its storage transaction")
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := projection.Personal(pvp.ProgressQuery{Mode: season.Mode, Season: season.ID, Owner: accounts[1]})
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("published PVP reads waited for shared storage commit")
	}
	close(release)
	released = true
}

func (g *guildGameplay) pvpCall(path string, fields []byte) []byte {
	g.t.Helper()
	g.seq++
	request := registryCommand(g.account, fmt.Sprintf("pvp-%d", g.seq), path, append(appScalar(1, g.seq), fields...))
	if path == "/LoginUser" {
		request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	}
	reply, err := g.execute(request)
	if err != nil {
		g.t.Fatalf("%s: %v", path, err)
	}
	if len(reply.Responses) != 1 {
		g.t.Fatalf("%s returned %d responses", path, len(reply.Responses))
	}
	return reply.Responses[0].Body
}
func (g *guildGameplay) drainPVP() {
	g.t.Helper()
	g.seq++
	request := player.Command{Identity: command.Identity{AccountID: g.account, SessionID: "pvp-delivery", RequestID: fmt.Sprint(g.seq)}, System: &player.SystemRequest{Kind: player.ReadOutbox}}
	reply, err := g.execute(request)
	if err != nil {
		g.t.Fatal(err)
	}
	var pending []outbox.Pending
	if err = json.Unmarshal(reply.Responses[0].Body, &pending); err != nil {
		g.t.Fatal(err)
	}
	for _, event := range pending {
		if err = g.factory.shared.Consume(context.Background(), event); err != nil {
			g.t.Fatal(err)
		}
		raw, err := json.Marshal(struct {
			Key    string `json:"key"`
			Digest string `json:"digest"`
		}{event.Key, event.Digest})
		if err != nil {
			g.t.Fatal(err)
		}
		g.seq++
		request.Identity.RequestID = fmt.Sprint(g.seq)
		request.System = &player.SystemRequest{Kind: player.AcknowledgeOutbox, Body: raw}
		request.Digest = sha256.Sum256(raw)
		if _, err = g.execute(request); err != nil {
			g.t.Fatal(err)
		}
	}
}
func (g *guildGameplay) ownedPVPDeck() ([]byte, uint64) {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			g.t.Error(err)
		}
	}()
	ctx := command.Context{State: tx, Identity: command.Identity{AccountID: g.account}}
	for _, char := range g.owner.assembly.worldService.CharacterService().All(ctx) {
		if int64(char.ExpiryTime) > 0 {
			continue
		}
		for _, costume := range g.owner.assembly.collection.Costumes() {
			if costume.UseChar == char.InvenIndex {
				return append(appScalar(1, char.InvenIndex), appScalar(4, costume.InvenIndex)...), char.InvenIndex
			}
		}
	}
	g.t.Fatal("current starter contains no permanent owned PVP character")
	return nil, 0
}
func TestNativePVPGameplayUsesOwnedSnapshotsAndTickets(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	state, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared, factory.pvpSource = state, state.pvp
	attacker := &guildGameplay{t: t, factory: factory, account: accounts[0]}
	defender := &guildGameplay{t: t, factory: factory, account: accounts[1]}
	attacker.open()
	defender.open()
	t.Cleanup(attacker.close)
	t.Cleanup(defender.close)
	login := appRows(attacker.pvpCall("/LoginUser", nil), 1)
	if len(login) != 1 {
		t.Fatal("login user missing")
	}
	if got := appValue(login[0], 57); got != factory.design.pvp.Design.Presets.BaseCount {
		t.Fatalf("Colosseum preset slots=%d want %d", got, factory.design.pvp.Design.Presets.BaseCount)
	}
	if got := appValue(login[0], 16); got != factory.design.pvp.Design.FreeMirrorTickets {
		t.Fatalf("free mirror ticket initial grant=%d", got)
	}
	attackDeck, character := attacker.ownedPVPDeck()
	defenseDeck, _ := defender.ownedPVPDeck()
	attacker.pvpCall("/PvpBattleDeckSave", append(appScalar(2, 0), appMessage(3, attackDeck)...))
	defender.pvpCall("/PvpBattleDeckSave", append(appScalar(2, 1), appMessage(3, defenseDeck)...))
	defender.drainPVP()
	mirror := attacker.pvpCall("/PvpBattleMatching", appScalar(2, 1))
	opponent, err := factory.profiles.GameIdentity(context.Background(), defender.account)
	if err != nil {
		t.Fatal(err)
	}
	if appValue(mirror, 4) != uint64(opponent.OwnerIndex) {
		t.Fatal("matching did not use the other account's published defense")
	}
	attacker.pvpCall("/PvpBattleEnd", append(appScalar(2, 1), appScalar(3, character)...))
	attacker.drainPVP()
	login = appRows(attacker.pvpCall("/LoginUser", nil), 1)
	if got, want := appValue(login[0], 16), factory.design.pvp.Design.FreeMirrorTickets-factory.design.pvp.Design.Mirror.TicketCost; got != want {
		t.Fatalf("mirror battle ticket debit=%d want %d", got, want)
	}
	history := attacker.pvpCall("/PvpBattleHistory", nil)
	if appValue(history, 5) != 1 || len(appRows(history, 1)) != 1 {
		t.Fatal("native mirror battle did not settle attack history")
	}
	attacker.pvpCall("/ColosseumDeckSave", appMessage(2, attackDeck))
	defender.pvpCall("/ColosseumDeckSave", appMessage(2, defenseDeck))
	defender.drainPVP()
	info := attacker.pvpCall("/ColosseumUserInfo", nil)
	season := appValue(info, 1)
	amount, err := factory.design.pvp.Design.ColosseumRule(season, 9)
	if err != nil {
		t.Fatal(err)
	}
	login = appRows(attacker.pvpCall("/LoginUser", nil), 1)
	if len(amount) != 1 || appValue(login[0], 58) != amount[0] {
		t.Fatal("arena free tickets do not match current season charge rule")
	}
	match := attacker.pvpCall("/ColosseumBattleMatching", nil)
	enemies := appRows(match, 3)
	if len(enemies) == 0 || appValue(enemies[0], 1) != uint64(opponent.OwnerIndex) {
		t.Fatal("arena did not match published opponent")
	}
	attacker.pvpCall("/ColosseumBattleStart", appScalar(2, uint64(opponent.OwnerIndex)))
	attacker.pvpCall("/ColosseumBattleEnd", appScalar(2, 1))
	attacker.drainPVP()
	history = attacker.pvpCall("/ColosseumBattleHistory", nil)
	if appValue(history, 5) != 1 || len(appRows(history, 1)) != 1 {
		t.Fatal("native arena battle did not settle attack history")
	}
	login = appRows(attacker.pvpCall("/LoginUser", nil), 1)
	want := amount[0]
	if want > 0 {
		want--
	}
	if got := appValue(login[0], 58); got != want {
		t.Fatalf("arena battle ticket debit=%d want %d", got, want)
	}
}
