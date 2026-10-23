//go:build integration

package app

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/identity"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/gateway/transport"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/storage/outbox"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type friendHTTPClient struct {
	t         *testing.T
	handler   http.Handler
	cookie    string
	key       []byte
	seq       uint64
	owner     uint64
	loginBody []byte
}

func (c *friendHTTPClient) send(path string, fields []byte, code, errorType int) []byte {
	c.t.Helper()
	c.seq++
	request := httptest.NewRequest(http.MethodPut, "/game"+path, strings.NewReader(string(nicknameEncrypt(c.t, append(appScalar(1, c.seq), fields...), c.key))))
	request.Header.Set("Cookie", c.cookie)
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		c.t.Fatalf("%s HTTP%d: %s", path, response.Code, response.Body.String())
	}
	var envelope struct {
		PacketCode, ErrorType int
		Data                  string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		c.t.Fatal(err)
	}
	if envelope.PacketCode != code || envelope.ErrorType != errorType {
		c.t.Fatalf("%s code/error=%d/%d want%d/%d: %s", path, envelope.PacketCode, envelope.ErrorType, code, errorType, response.Body.String())
	}
	return nicknameDecrypt(c.t, envelope.Data, c.key)
}

func friendHTTPLogin(t *testing.T, registry *playerRegistry, account string) *friendHTTPClient {
	t.Helper()
	game, err := session.NewServer(registry, nicknameAuthenticator{account})
	if err != nil {
		t.Fatal(err)
	}
	fixed := []byte("abcdefghijkrstuv024680wxyzlmnopq")
	login, err := game.DispatchRaw(context.Background(), "/LoginUser", nicknameEncrypt(t, appScalar(1, 1), fixed), "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Data string }
	if err := json.Unmarshal(login.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	loginBody := nicknameDecrypt(t, envelope.Data, fixed)
	users := appRows(loginBody, 1)
	if len(users) != 1 {
		t.Fatal("missing LoginUser")
	}
	return &friendHTTPClient{t: t, handler: transport.HTTP{Raw: game}.Handler(), cookie: "s=" + login.Cookie, key: []byte(nicknameField(t, users[0], 3)), seq: 1, owner: appValue(users[0], 1), loginBody: loginBody}
}

func drainFriendPublications(t *testing.T, registry *playerRegistry, shared *sharedRuntime, account string) {
	t.Helper()
	body, err := executePlayerSystem(context.Background(), registry, account, player.ReadOutbox, nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []outbox.Pending
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if err := shared.Consume(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		ack, _ := json.Marshal(struct {
			Key    string `json:"key"`
			Digest string `json:"digest"`
		}{event.Key, event.Digest})
		if _, err := executePlayerSystem(context.Background(), registry, account, player.AcknowledgeOutbox, ack); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeFriendRequestsAreBilateralDurableAndRetrySafe(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	type supporter struct{ character, costume, id uint64 }
	var supporters [2]supporter
	for i, account := range accounts {
		owner := openNicknamePlayer(t, factory, account)
		tx, err := owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		costumeID := []uint64{101, 201}[i]
		if _, err := owner.assembly.collection.GrantCostumes(command.Context{State: tx, InstanceIDs: factory.instanceIDs()}, "friend-supporter-fixture", []uint64{costumeID}, factory.design.rewardCostumes); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		characters := owner.assembly.worldService.CharacterService().All(command.Context{State: tx})
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for _, character := range characters {
			if roster.IsStoryCharacter(character) || roster.IsCharmCharacter(character) {
				continue
			}
			for _, costume := range owner.assembly.collection.Costumes() {
				if costume.ID != costumeID {
					continue
				}
				design, ok := factory.design.rewardCostumes.Character(costume.ID)
				if ok && (character.ID == design.ID || slices.Contains(design.GrowthCharacterIDs, character.ID)) {
					supporters[i] = supporter{character.InvenIndex, costume.InvenIndex, costume.ID}
				}
			}
		}
		if supporters[i].character == 0 {
			t.Fatal("no permanent owned supporter in fixture")
		}
		seedNicknameTickets(t, owner, 1)
		closeNicknamePlayer(t, owner)
	}
	var shared *sharedRuntime
	var registry *playerRegistry
	open := func() {
		var err error
		shared, err = openSharedRuntime(context.Background(), factory.options, factory.design)
		if err != nil {
			t.Fatal(err)
		}
		factory.shared, factory.pvpSource = shared, shared.pvp
		registry = newPlayerRegistry(factory, time.Minute)
	}
	close := func() {
		if registry != nil {
			if err := registry.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			registry = nil
		}
		if shared != nil {
			if err := shared.store.Close(); err != nil {
				t.Fatal(err)
			}
			shared = nil
		}
	}
	t.Cleanup(close)
	open()
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	for i, client := range []*friendHTTPClient{a, b} {
		client.send("/UserNicknameChange", appMessage(2, []byte([]string{"FriendAlpha", "FriendBeta"}[i])), 74, 0)
		client.send("/UserPortraitChange", appScalar(2, supporters[i].id), 75, 0)
		registration := wire.AppendVarint(nil, 2, 1)
		registration = wire.AppendVarint(registration, 3, supporters[i].character)
		registration = wire.AppendVarint(registration, 4, supporters[i].costume)
		client.send("/SupporterRegister", wire.AppendVarint(registration, 5, 123), 435, 0)
	}
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	assertRows := func(c *friendHTTPClient, path string, code int, field int, expected ...uint64) {
		t.Helper()
		rows := appRows(c.send(path, nil, code, 0), field)
		if len(rows) != len(expected) {
			t.Fatalf("%s rowcount%d want%d", path, len(rows), len(expected))
		}
		for i, row := range rows {
			if appValue(row, 1) != expected[i] || nicknameField(t, row, 4) == "" {
				t.Fatalf("%s invalid actualplayer row%x", path, row)
			}
		}
	}
	search := appRows(a.send("/FriendSearch", appScalar(2, b.owner), 212, 0), 1)
	if len(search) != 1 || appValue(search[0], 1) != b.owner {
		t.Fatal("numeric owner search did not find second player")
	}
	if nicknameField(t, search[0], 4) != "FriendBeta" || appValue(search[0], 2) != supporters[1].id {
		t.Fatal("search did not project target nickname and portrait")
	}
	assertRows(a, "/FriendRecommend", 211, 1, b.owner)
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	a.seq--
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	assertRows(a, "/FriendSendList", 209, 1, b.owner)
	assertRows(b, "/FriendReceiveList", 210, 1, a.owner)
	full := a.send("/FriendInfoList", appScalar(2, 1), 204, 0)
	if len(appRows(full, 3)) != 1 {
		t.Fatal("login allInfo omitted outgoing request")
	}
	accepted := appRows(b.send("/FriendAccept", appScalar(2, a.owner), 206, 0), 1)
	if len(accepted) != 1 || appValue(accepted[0], 1) != a.owner {
		t.Fatal("accept omitted native singularfriend response")
	}
	b.seq--
	b.send("/FriendAccept", appScalar(2, a.owner), 206, 0)
	b.send("/FriendAccept", appScalar(2, a.owner), 206, 1)
	a.send("/FriendSend", appScalar(2, b.owner), 205, 5)
	assertRows(a, "/FriendInfoList", 204, 1, b.owner)
	assertRows(b, "/FriendInfoList", 204, 1, a.owner)
	assertRows(a, "/FriendSendList", 209, 1)
	assertRows(b, "/FriendReceiveList", 210, 1)
	close()
	open()
	a, b = friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	assertRows(a, "/FriendInfoList", 204, 1, b.owner)
	assertRows(b, "/FriendInfoList", 204, 1, a.owner)
	a.send("/FriendRemove", appScalar(2, b.owner), 208, 0)
	assertRows(a, "/FriendInfoList", 204, 1)
	assertRows(b, "/FriendInfoList", 204, 1)
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	a.send("/FriendSendRemove", appScalar(2, b.owner), 213, 0)
	assertRows(a, "/FriendSendList", 209, 1)
	assertRows(b, "/FriendReceiveList", 210, 1)
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	b.send("/FriendRefuse", appScalar(2, a.owner), 207, 0)
	assertRows(a, "/FriendSendList", 209, 1)
	assertRows(b, "/FriendReceiveList", 210, 1)
	a.send("/FriendSearch", appScalar(2, 9223372036854775807), 212, 1)
	assertRows(a, "/FriendRecommend", 211, 1, b.owner)
	profile := a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0)
	if appValue(profile, 1) != b.owner {
		t.Fatal("friend profile click returned caller profile")
	}
	if nicknameField(t, profile, 2) != "FriendBeta" || appValue(profile, 4) != supporters[1].id {
		t.Fatal("profile click did not show target nickname and portrait")
	}
	for _, target := range []*friendHTTPClient{a, b} {
		detail := a.send("/SupporterDetail", append(appScalar(2, target.owner), appScalar(3, 1)...), 434, 0)
		slot := appRows(detail, 1)
		expected := supporters[0]
		if target == b {
			expected = supporters[1]
		}
		if len(slot) != 1 || appValue(slot[0], 1) != target.owner || appValue(slot[0], 3) != expected.id {
			t.Fatal("supporter detail did not show chosen target's actual registered character")
		}
		battle, err := base64.StdEncoding.DecodeString(nicknameField(t, slot[0], 6))
		if err != nil {
			t.Fatal(err)
		}
		characters := appRows(battle, 1)
		if len(characters) != 1 || appValue(characters[0], 1) != expected.character || appValue(characters[0], 5) != expected.id || appValue(characters[0], 7) != expected.costume {
			t.Fatal("supporter battle character does not belong to selected target")
		}
		ownedCostume := false
		for _, costume := range appRows(battle, 2) {
			if appValue(costume, 1) == expected.costume && appValue(costume, 2) == expected.id {
				ownedCostume = true
			}
		}
		if !ownedCostume {
			t.Fatal("supporter battle bundle omitted selected target's registered owned costume")
		}
	}
	a.send("/SupporterDetail", appScalar(2, b.owner), 434, 9003)
	// Bypass actor receipt caches for a shared-domain accept, then reopen the
	// actual SQLite store before retrying the identical operation identity.
	durableSend := command.Identity{AccountID: accounts[0], SessionID: "durable-friend", RequestID: "send"}
	durableAccept := command.Identity{AccountID: accounts[1], SessionID: "durable-friend", RequestID: "accept"}
	if _, _, err := shared.friends.Execute(context.Background(), durableSend, a.owner, "/FriendSend", append(appScalar(1, 1), appScalar(2, b.owner)...), 0); err != nil {
		t.Fatal(err)
	}
	acceptWire := append(appScalar(1, 1), appScalar(2, a.owner)...)
	_, acceptedBeforeRestart, err := shared.friends.Execute(context.Background(), durableAccept, b.owner, "/FriendAccept", acceptWire, 0)
	if err != nil {
		t.Fatal(err)
	}

	// UserInfoUI allows one global Like per reset, and BattlePacket consumes
	// one lender's daily quota across their registered supporter slots.
	a.send("/LikeUser", appScalar(2, a.owner), 203, 9003)
	a.send("/LikeUser", appScalar(2, 9223372036854775807), 203, 9003)
	durableLike := command.Identity{AccountID: accounts[0], SessionID: "durable-social", RequestID: "like"}
	likeWire := append(appScalar(1, 1), appScalar(2, b.owner)...)
	if _, _, err := shared.friends.Execute(context.Background(), durableLike, a.owner, "/LikeUser", likeWire, 0); err != nil {
		t.Fatal(err)
	}
	a.send("/LikeUser", appScalar(2, b.owner), 203, 9003)
	if got := appValue(a.send("/MyLikeInfo", nil, 218, 0), 1); got != b.owner {
		t.Fatalf("daily like target=%d want%d", got, b.owner)
	}
	profile = a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0)
	if appValue(profile, 10) != 1 || appValue(profile, 13) != 1 {
		t.Fatal("liked friend profile omitted received total or relation")
	}
	picker := command.Identity{AccountID: accounts[0], SessionID: "durable-social", RequestID: "picker"}
	if _, _, err := shared.friends.Execute(context.Background(), picker, a.owner, "/SupporterBattleInfo", append(appScalar(1, 1), appScalar(2, 1)...), 1000); err != nil {
		t.Fatal(err)
	}
	durableBorrow := command.Identity{AccountID: accounts[0], SessionID: "durable-social", RequestID: "borrow"}
	borrowWire := append(append(appScalar(1, 1), appScalar(2, b.owner)...), appScalar(3, 1)...)
	_, borrowBeforeRestart, err := shared.friends.Execute(context.Background(), durableBorrow, a.owner, "/SupporterBorrow", borrowWire, 1000)
	if err != nil {
		t.Fatal(err)
	}
	borrowedCharacters := appRows(appRows(borrowBeforeRestart, 3)[0], 1)
	if len(borrowedCharacters) != 1 || appValue(borrowedCharacters[0], 1) != supporters[1].character {
		t.Fatal("borrow returned another account's supporter bundle")
	}
	close()
	open()
	code, acceptedAfterRestart, err := shared.friends.Execute(context.Background(), durableAccept, b.owner, "/FriendAccept", acceptWire, 0)
	if err != nil || code != 206 || !slices.Equal(acceptedBeforeRestart, acceptedAfterRestart) {
		t.Fatalf("durable accept retry code=%d error=%v changed=%v", code, err, !slices.Equal(acceptedBeforeRestart, acceptedAfterRestart))
	}

	_, borrowAfterRestart, err := shared.friends.Execute(context.Background(), durableBorrow, a.owner, "/SupporterBorrow", borrowWire, 1000)
	if err != nil || !slices.Equal(borrowBeforeRestart, borrowAfterRestart) {
		t.Fatalf("durable borrow retry changed result: %v", err)
	}
	if _, _, err := shared.friends.Execute(context.Background(), durableLike, a.owner, "/LikeUser", likeWire, 0); err != nil {
		t.Fatal(err)
	}
	a, b = friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	assertRows(a, "/FriendInfoList", 204, 1, b.owner)
	assertRows(b, "/FriendInfoList", 204, 1, a.owner)

	if appValue(b.loginBody, 8) != 1 {
		t.Fatal("relogin omitted outstanding supporter reward badge")
	}
	if appValue(a.send("/MyLikeInfo", nil, 218, 0), 1) != b.owner {
		t.Fatal("daily like quota was lost across database reopen")
	}
	if appValue(a.send("/UserContentsInfo", appScalar(2, b.owner), 215, 0), 10) != 1 {
		t.Fatal("durable like retry incremented received count")
	}
	battleRows := appRows(a.send("/SupporterBattleInfo", appScalar(2, 1), 439, 0), 1)
	if len(battleRows) != 1 || appValue(battleRows[0], 1) != b.owner || appValue(battleRows[0], 10) != 1 || len(appRows(battleRows[0], 9)) != 1 {
		t.Fatal("battle supporter picker omitted lender, slot or durable usage count")
	}
	status := b.send("/SupporterStatus", appScalar(2, 50), 438, 0)
	history := appRows(status, 1)
	if len(history) != 1 || appValue(history[0], 2) != a.owner || appValue(history[0], 7) != b.owner || appValue(history[0], 8) != 1 || appValue(history[0], 11) != 1 || appValue(history[0], 12) != 0 {
		t.Fatal("rental history did not preserve borrower/lender, slot, friend type and claim status")
	}
	historyID := appValue(history[0], 1)
	a.send("/SupporterReward", appScalar(2, historyID), 442, 9003)
	b.send("/SupporterReward", append(appScalar(2, historyID), appScalar(2, historyID)...), 442, 9003)
	claim := b.send("/SupporterReward", appScalar(2, historyID), 442, 0)
	b.seq--
	if replay := b.send("/SupporterReward", appScalar(2, historyID), 442, 0); !slices.Equal(claim, replay) {
		t.Fatal("same claim identity changed its reward result")
	}
	b.send("/SupporterReward", appScalar(2, historyID), 442, 9003)
	if appValue(claim, 1) != historyID || len(appRows(claim, 2)) != 1 {
		t.Fatal("claim omitted rental ID or reward bundle")
	}
	status = b.send("/SupporterStatus", appScalar(2, 50), 438, 0)
	if appValue(status, 4) != 1 || appValue(appRows(status, 1)[0], 12) != 1 {
		t.Fatal("committed claim did not update daily claim count and history")
	}
	// Read-only GameData query: SupportCharacterDefaultTable 0 fields3/2/1/7/8
	// =3 friend uses,3 recommended uses,10 claims,type44,count2 (version20261008155037).
	for range 2 {
		a.send("/SupporterBorrow", append(appScalar(2, b.owner), appScalar(3, 1)...), 437, 0)
	}
	a.send("/SupporterBorrow", append(appScalar(2, b.owner), appScalar(3, 1)...), 437, 3)
	status = b.send("/SupporterStatus", appScalar(2, 50), 438, 0)
	history = appRows(status, 1)
	if len(history) != 3 {
		t.Fatalf("friend rental quota produced%d histories want3", len(history))
	}
	nextPage := b.send("/SupporterStatus", append(appScalar(2, 1), appScalar(3, appValue(history[0], 1))...), 438, 0)
	nextRows := appRows(nextPage, 1)
	if len(nextRows) != 1 || appValue(nextRows[0], 1) != appValue(history[1], 1) {
		t.Fatal("rental history cursor skipped or repeated a row")
	}
	baselineCurrency := appValue(appRows(b.loginBody, 1)[0], 54)
	close()
	open()
	a, b = friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	if got := appValue(appRows(b.loginBody, 1)[0], 54); got != baselineCurrency+2 {
		t.Fatalf("durable supporter reward currency=%d want%d", got, baselineCurrency+2)
	}
	if appValue(b.send("/SupporterStatus", appScalar(2, 50), 438, 0), 4) != 1 {
		t.Fatal("daily reward claim count was lost across reopen")
	}
	b.send("/FriendAccept", appScalar(2, a.owner), 206, 1)
	a.send("/FriendRemove", appScalar(2, b.owner), 208, 0)
	assertRows(a, "/FriendInfoList", 204, 1)
	assertRows(b, "/FriendReceiveList", 210, 1)
}

func TestNativeFriendSendRejectsFullOutgoingListWithoutChangingEitherPlayer(t *testing.T) {
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
	repository, ok := factory.profiles.(identity.Repository)
	if !ok {
		t.Fatal("integration identity store cannot register accounts")
	}
	registration, err := identity.New(identity.Config{Providers: map[string]string{"discord": "integration-client"}, DeviceTTL: time.Minute, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour}, repository)
	if err != nil {
		t.Fatal(err)
	}
	// CommunityDefineTable field6 supplies the outgoing limit; each target is
	// a registered account projected from its own persisted starter state.
	var targets []uint64
	for i := uint64(0); i < factory.design.friends.Sent; i++ {
		device, err := registration.CreateDevice("discord", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		auth, err := registration.Start("discord", device.ID, device.StartTicket)
		if err != nil {
			t.Fatal(err)
		}
		if err := registration.CompleteDevice(auth.ID, "discord", identity.ProviderIdentity{Issuer: "https://discord.com", Subject: fmt.Sprintf("%d", 30000000+i)}); err != nil {
			t.Fatal(err)
		}
		result, err := registration.Poll(device.ID, device.Secret)
		if err != nil {
			t.Fatal(err)
		}
		account, err := registration.ValidateAccess(result.Tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		owner := openNicknamePlayer(t, factory, account)
		if err := owner.readLocal(context.Background(), command.Identity{AccountID: account}, func(c command.Context) error {
			profile, err := owner.assembly.publicFriendProfile(c)
			if err != nil {
				return err
			}
			targets = append(targets, profile.OwnerIndex)
			return shared.friends.Publish(context.Background(), profile)
		}); err != nil {
			t.Fatal(err)
		}
		closeNicknamePlayer(t, owner)
	}
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	a, b := friendHTTPLogin(t, registry, accounts[0]), friendHTTPLogin(t, registry, accounts[1])
	drainFriendPublications(t, registry, shared, accounts[0])
	drainFriendPublications(t, registry, shared, accounts[1])
	for _, target := range targets {
		a.send("/FriendSend", appScalar(2, target), 205, 0)
	}
	before := a.send("/FriendSendList", nil, 209, 0)
	if len(appRows(before, 1)) != len(targets) {
		t.Fatal("outgoing requests did not reach the current GameData limit")
	}
	// Current native Send error3 maps to the user's full outgoing list (120503).
	a.send("/FriendSend", appScalar(2, b.owner), 205, 3)
	if after := a.send("/FriendSendList", nil, 209, 0); !slices.Equal(before, after) {
		t.Fatal("capacity error altered existing outgoing requests")
	}
	if rows := appRows(b.send("/FriendReceiveList", nil, 210, 0), 1); len(rows) != 0 {
		t.Fatal("capacity error created a counterpart incoming request")
	}
	a.send("/FriendSendRemove", appScalar(2, targets[0]), 213, 0)
	a.send("/FriendSend", appScalar(2, b.owner), 205, 0)
	rows := appRows(b.send("/FriendReceiveList", nil, 210, 0), 1)
	if len(rows) != 1 || appValue(rows[0], 1) != a.owner {
		t.Fatal("freeing one outgoing slot did not permit a bilateral request")
	}
}
