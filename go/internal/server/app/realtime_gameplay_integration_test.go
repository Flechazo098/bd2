//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/platform/textmoderation"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bd2server/internal/server/runtime/room"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

func TestNativeRoomGameplayUsesRealDesignAndCommitsParticipantRewardsOnce(t *testing.T) {
	for _, family := range []string{"Action", "Defense"} {
		t.Run(family, func(t *testing.T) {
			factory, accounts := newIntegrationFactory(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			catalog := factory.design.eventPlay
			kind := uint64(8)
			if family == "Defense" {
				kind = 7
			}
			var eventID uint64
			for _, row := range catalog.Tables["PackEventMiniGameTable"] {
				if appValue(row, 3) == kind {
					eventID = appValue(row, 8)
					break
				}
			}
			if eventID == 0 {
				t.Fatal("current GameData has no native family")
			}
			var selected gamedata.NativeMinigame
			var character uint64
			if family == "Defense" {
				var err error
				selected, err = catalog.NativeMinigameRules(family, eventID, 0, 0, false)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for _, char := range catalog.Tables["ActionGameCharTable"] {
					for _, stage := range catalog.Tables["ActionGameStageTable"] {
						rules, err := catalog.NativeMinigameRules(family, eventID, appValue(char, 14), appValue(stage, 1), false)
						if err == nil {
							selected, character = rules, appValue(char, 14)
							break
						}
					}
					if character != 0 {
						break
					}
				}
				if character == 0 {
					t.Fatal("current GameData has no approved party action selection")
				}
			}
			// Only calendar dates are fixtures. All event, stage, monster and reward IDs come from installed GameData.
			now := time.Now()
			schedule := events.Schedule{UID: eventID, Type: 11, ID: eventID, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}
			factory.options.calendars.Events = []events.Schedule{schedule}
			state, err := openSharedRuntime(ctx, factory.options, factory.design)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := state.store.Close(); err != nil {
					t.Error(err)
				}
			})
			factory.shared, factory.pvpSource, factory.roomResults = state, state.pvp, state.rooms
			registry := newPlayerRegistry(factory, time.Hour)
			t.Cleanup(func() {
				if err := registry.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			clock := new(atomic.Int64)
			clock.Store(now.UnixNano())
			published := make(chan []room.Delivery, 8)
			manager, err := room.New(room.Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000, Now: func() time.Time { return time.Unix(0, clock.Load()) }, PublishResult: state.rooms.publish, Publish: func(d []room.Delivery) { published <- d }}, roomRules{players: registry, shared: state})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := manager.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			state.rooms.rooms = manager
			var actors [2]*player.Runtime
			var clients [2]room.Connection
			var seq [2]uint64
			for i, account := range accounts {
				actor, release, err := registry.Acquire(ctx, account)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
				actors[i] = actor
				profile, err := factory.profiles.GameIdentity(ctx, account)
				if err != nil {
					t.Fatal(err)
				}
				clients[i] = room.Connection{ID: fmt.Sprintf("matching-%d", i), Channel: room.Matching, Principal: room.Principal{AccountID: account, OwnerIndex: profile.OwnerIndex, SessionID: "registry-client", Generation: actor.Generation()}}
			}
			call := func(i int, path string, fields []byte) (player.Command, player.Reply, error) {
				seq[i]++
				body := wire.AppendVarint(nil, 1, seq[i])
				body = append(body, fields...)
				cmd := registryCommand(accounts[i], fmt.Sprintf("native-%s-%d", path, seq[i]), path, body)
				future, err := actors[i].Submit(ctx, cmd)
				if err != nil {
					return cmd, player.Reply{}, err
				}
				reply, err := future.Wait(ctx)
				return cmd, reply, err
			}
			items := func(i int) map[[2]uint64]uint64 {
				t.Helper()
				_, reply, err := call(i, "/ItemInfo", nil)
				if err != nil {
					t.Fatal(err)
				}
				return appItemTotals(reply.Responses[0].Body)
			}
			drain := func(i int) {
				t.Helper()
				seq[i]++
				command := player.Command{Identity: command.Identity{AccountID: accounts[i], SessionID: "native-delivery", RequestID: fmt.Sprint(seq[i])}, System: &player.SystemRequest{Kind: player.ReadOutbox}}
				reply := registryExecute(t, actors[i], command)
				var pending []outbox.Pending
				if err := json.Unmarshal(reply.Responses[0].Body, &pending); err != nil {
					t.Fatal(err)
				}
				for _, event := range pending {
					if err := state.Consume(ctx, event); err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(struct {
						Key    string `json:"key"`
						Digest string `json:"digest"`
					}{event.Key, event.Digest})
					if err != nil {
						t.Fatal(err)
					}
					seq[i]++
					command.Identity.RequestID = fmt.Sprint(seq[i])
					command.Digest = sha256.Sum256(raw)
					command.System = &player.SystemRequest{Kind: player.AcknowledgeOutbox, Body: raw}
					registryExecute(t, actors[i], command)
				}
			}
			handle := func(i int, typ int32, payload []byte) []room.Delivery {
				t.Helper()
				d, err := manager.Handle(ctx, clients[i], room.Frame{Type: typ, Payload: payload})
				if err != nil {
					t.Fatalf("native type %d: %v", typ, err)
				}
				return d
			}
			if family == "Action" {
				b := wire.AppendVarint(nil, 2, schedule.UID)
				b = wire.AppendVarint(b, 4, 2)
				b = wire.AppendVarint(b, 5, character)
				b = wire.AppendVarint(b, 6, selected.MonsterID)
				handle(0, 82, b)
			} else {
				handle(0, 76, wire.AppendVarint(wire.AppendVarint(nil, 2, schedule.UID), 3, 2))
			}
			view, err := manager.View(ctx, clients[0].Principal, room.Kind(bytes.ToLower([]byte(family))))
			if err != nil {
				t.Fatal(err)
			}
			if family == "Action" {
				handle(1, 90, wire.AppendVarint(wire.AppendString(nil, 2, view.RoomCode), 3, character))
				handle(0, 104, nil)
			} else {
				handle(1, 76, wire.AppendString(wire.AppendVarint(wire.AppendVarint(nil, 2, schedule.UID), 3, 3), 4, view.RoomCode))
				handle(0, 74, nil)
			}
			if uint64(view.Selection.GroupID) != selected.GroupID || uint64(view.Selection.StageID) != selected.StageID || view.Selection.Capacity != selected.Capacity {
				t.Fatalf("native room selection differs from GameData: %+v want %+v", view.Selection, selected)
			}
			var guids [2]string
			for i := range clients {
				clients[i].Channel = room.Kind(bytes.ToLower([]byte(family)))
				clients[i].ID = fmt.Sprintf("game-%d", i)
				d := handle(i, 23, wire.AppendString(nil, 2, view.RoomCode))
				guid, found, err := wire.Bytes(d[0].Frame.Payload, 4)
				if err != nil || !found {
					t.Fatal("native entry missing guid")
				}
				guids[i] = string(guid)
				handle(i, 45, wire.AppendString(nil, 2, guids[i]))
			}
			before := [2]map[[2]uint64]uint64{items(0), items(1)}
			for i := range clients {
				_, _, err := call(i, "/MiniGame"+family+"Start", wire.AppendVarint(nil, 2, schedule.UID))
				if err != nil {
					t.Fatal(err)
				}
				drain(i)
			}
			view, err = manager.View(ctx, clients[0].Principal, clients[0].Channel)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range view.Members {
				if !m.AcceptedRun || m.RunGUID != m.NativeGUID {
					t.Fatal("player commit did not admit native room participant")
				}
			}
			endFields := func(i int) []byte {
				b := wire.AppendString(nil, 2, guids[i])
				if family == "Action" {
					b = wire.AppendVarint(b, 3, selected.GroupID)
					b = wire.AppendVarint(b, 4, selected.StageID)
					b = wire.AppendVarint(b, 6, selected.MonsterID)
					b = wire.AppendVarint(b, 7, 1)
				} else {
					b = wire.AppendVarint(b, 3, 1)
				}
				return b
			}
			if _, _, err := call(0, "/MiniGame"+family+"End", endFields(0)); !errors.Is(err, eventplay.ErrRoomNotFinished) {
				t.Fatalf("unfinished native room accepted HTTP completion: %v", err)
			}
			clock.Add(int64(5 * time.Second))
			if family == "Action" {
				monster := wire.AppendVarint(nil, 3, selected.MonsterID)
				if d := handle(0, 111, wire.AppendBytes(wire.AppendString(nil, 2, guids[0]), 5, monster)); len(d) != 0 {
					t.Fatal("terminal notification preceded durable persistence")
				}
			} else {
				for i := range clients {
					b := wire.AppendString(nil, 2, guids[i])
					b = wire.AppendVarint(b, 3, 5)
					b = wire.AppendVarint(b, 5, selected.LastWave)
					handle(i, 48, b)
				}
			}
			var final room.FinalResult
			select {
			case final = <-state.rooms.queue:
			case <-ctx.Done():
				t.Fatal("native terminal result was not queued")
			}
			if !final.Clear || final.ScoreMilliseconds != 5000 || len(final.View.Members) != 2 {
				t.Fatalf("native terminal projection wrong: %+v", final)
			}
			if err := state.rooms.persist(ctx, final); err != nil {
				t.Fatal(err)
			}
			select {
			case frames := <-published:
				terminal := int32(108)
				if family == "Defense" {
					terminal = 51
				}
				count := 0
				for _, d := range frames {
					if d.Frame.Type == terminal {
						count++
					}
				}
				if count != 2 {
					t.Fatalf("durable ack released %d participant terminal frames", count)
				}
			case <-ctx.Done():
				t.Fatal("durable persistence did not release terminal frames")
			}
			wantRewards := map[[2]uint64]uint64{}
			if family == "Defense" {
				for _, row := range catalog.Tables["MGDRewardTable"] {
					if appValue(row, 6) <= selected.LastWave {
						rr, err := gamedata.EventPlayRewards(row, 5, 3, 2)
						if err != nil {
							t.Fatal(err)
						}
						for _, r := range rr {
							wantRewards[[2]uint64{r.Type, r.ID}] += r.Count
						}
					}
				}
				if len(wantRewards) == 0 {
					t.Fatal("current Defense design has no terminal rewards")
				}
			}
			for i := range clients {
				otherBefore := items(1 - i)
				cmd, reply, err := call(i, "/MiniGame"+family+"End", endFields(i))
				if err != nil {
					t.Fatal(err)
				}
				if family == "Action" && appValue(reply.Responses[0].Body, 1) != 5000 {
					t.Fatal("HTTP score overrode authoritative native completion")
				}
				got := items(i)
				want := make(map[[2]uint64]uint64, len(before[i])+len(wantRewards))
				maps.Copy(want, before[i])
				for key, n := range wantRewards {
					want[key] += n
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("participant%d assets differ from GameData rewards: got=%v want=%v", i, got, want)
				}
				if !reflect.DeepEqual(items(1-i), otherBefore) {
					t.Fatal("participant completion changed another player's assets")
				}
				replay := registryExecute(t, actors[i], cmd)
				if !bytes.Equal(replay.Responses[0].Body, reply.Responses[0].Body) || !reflect.DeepEqual(items(i), got) {
					t.Fatal("HTTP replay changed response or granted twice")
				}
				drain(i)
				if i == 0 {
					prepared, err := state.rooms.PrepareResults(ctx, accounts[1], []eventplay.NativeRun{{InstanceID: final.View.InstanceID, GUID: guids[1], Family: family}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := prepared.Result(command.Context{Identity: command.Identity{AccountID: accounts[1]}}, eventplay.NativeRun{InstanceID: final.View.InstanceID, GUID: guids[1], Family: family}); err != nil {
						t.Fatal("first claim removed second participant entitlement", err)
					}
				}
			}
			if err := state.store.View(ctx, func(tx *shared.Tx) error {
				_, found, err := tx.Get("room.results", final.View.InstanceID)
				if found {
					t.Error("all participant claims retained full result")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if family == "Action" {
				rows, err := state.rooms.NativeRankings(command.Context{Identity: command.Identity{AccountID: accounts[0]}}, schedule.UID, selected.MonsterID, 2)
				if err != nil || len(rows) != 1 || rows[0].Score != 5000 || len(rows[0].Members) != 2 {
					t.Fatalf("real room completion ranking=%+v err=%v", rows, err)
				}
			}
		})
	}
}

func TestNativeFishingVoyageUsesRealBoatAndIndependentParticipantClaims(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	base := t.TempDir()
	policy := gameconfig.DefaultTextModeration()
	policy.RuleFiles = []string{moderationRulesFile(t, base, `{"rules":[{"text":"Blocked","match":"exact","scopes":["chat"]}]}`)}
	moderator, err := textmoderation.New(policy, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := moderator.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.options.textModerator = moderator
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	state, err := openSharedRuntime(ctx, factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared, factory.pvpSource, factory.roomResults = state, state.pvp, state.rooms
	var starterBoat, partyBoat uint64
	for _, row := range factory.design.eventPlay.Tables["FishingBoatTable"] {
		id := appValue(row, 8)
		if id > 0 && (starterBoat == 0 || id < starterBoat) {
			starterBoat = id
		}
		if appValue(row, 9) >= 2 && (partyBoat == 0 || id < partyBoat) {
			partyBoat = id
		}
	}
	if partyBoat == 0 {
		t.Fatal("current GameData has no two-person boat")
	}
	var budget []gamedata.Reward
	for id := starterBoat + 1; id <= partyBoat; id++ {
		row, err := factory.design.eventPlay.Row("FishingBoatTable", 8, id)
		if err != nil {
			t.Fatal(err)
		}
		budget = append(budget, gamedata.Reward{Type: appValue(row, 4), ID: appValue(row, 3), Count: appValue(row, 2)})
	}
	// Grant only the real upgrade costs; the host still purchases each boat through the player route.
	fixture, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.repository.BeginCommand(ctx)
	if err != nil {
		_ = fixture.Close()
		t.Fatal(err)
	}
	if _, err := fixture.assembly.eventEconomy.Apply(command.Context{Identity: command.Identity{AccountID: accounts[0]}, State: tx}, "native-fishing-upgrade-budget", nil, budget); err != nil {
		_ = tx.Rollback()
		_ = fixture.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		_ = fixture.Close()
		t.Fatal(err)
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	registry := newPlayerRegistry(factory, time.Hour)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	clock := new(atomic.Int64)
	clock.Store(time.Now().UnixNano())
	published := make(chan []room.Delivery, 8)
	rules := roomRules{players: registry, shared: state}
	manager, err := room.New(room.Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000, Now: func() time.Time { return time.Unix(0, clock.Load()) }, PublishResult: state.rooms.publish, Publish: func(d []room.Delivery) { published <- d }}, rules)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	state.rooms.rooms = manager
	var actors [2]*player.Runtime
	var clients [2]room.Connection
	var seq [2]uint64
	for i, account := range accounts {
		actor, release, err := registry.Acquire(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		actors[i] = actor
		profile, err := factory.profiles.GameIdentity(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = room.Connection{ID: fmt.Sprintf("fish-matching-%d", i), Channel: room.Matching, Principal: room.Principal{AccountID: account, OwnerIndex: profile.OwnerIndex, SessionID: "registry-client", Generation: actor.Generation()}}
	}
	call := func(i int, path string, fields []byte) (player.Command, player.Reply) {
		t.Helper()
		seq[i]++
		body := wire.AppendVarint(nil, 1, seq[i])
		body = append(body, fields...)
		cmd := registryCommand(accounts[i], fmt.Sprintf("fish-%s-%d", path, seq[i]), path, body)
		if path == "/LoginUser" {
			cmd.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
		}
		return cmd, registryExecute(t, actors[i], cmd)
	}
	drain := func(i int) {
		t.Helper()
		seq[i]++
		cmd := player.Command{Identity: command.Identity{AccountID: accounts[i], SessionID: "fish-delivery", RequestID: fmt.Sprint(seq[i])}, System: &player.SystemRequest{Kind: player.ReadOutbox}}
		reply := registryExecute(t, actors[i], cmd)
		var pending []outbox.Pending
		if err := json.Unmarshal(reply.Responses[0].Body, &pending); err != nil {
			t.Fatal(err)
		}
		for _, event := range pending {
			if err := state.Consume(ctx, event); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(struct {
				Key    string `json:"key"`
				Digest string `json:"digest"`
			}{event.Key, event.Digest})
			if err != nil {
				t.Fatal(err)
			}
			seq[i]++
			cmd.Identity.RequestID = fmt.Sprint(seq[i])
			cmd.Digest = sha256.Sum256(raw)
			cmd.System = &player.SystemRequest{Kind: player.AcknowledgeOutbox, Body: raw}
			registryExecute(t, actors[i], cmd)
		}
	}
	handle := func(i int, typ int32, payload []byte) []room.Delivery {
		t.Helper()
		d, err := manager.Handle(ctx, clients[i], room.Frame{Type: typ, Payload: payload})
		if err != nil {
			t.Fatalf("native fishing type%d: %v", typ, err)
		}
		return d
	}
	for id := starterBoat + 1; id <= partyBoat; id++ {
		_, reply := call(0, "/FishingBoatUpgrade", nil)
		if appValue(reply.Responses[0].Body, 1) != id {
			t.Fatal("host upgrade did not purchase GameData boat")
		}
	}
	profile, err := rules.Profile(ctx, clients[0].Principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.FishingMaps) == 0 {
		t.Fatal("real starter owns no fishing map")
	}
	mapID := profile.FishingMaps[0]
	boat, err := factory.design.eventPlay.NativeFishingRules(mapID, uint64(profile.BoatLevel), uint64(profile.BoatSkinID))
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 3, uint64(profile.BoatLevel))
	request = wire.AppendVarint(request, 4, mapID)
	request = wire.AppendVarint(request, 5, uint64(profile.BoatSkinID))
	handle(0, 149, request)
	view, err := manager.View(ctx, clients[0].Principal, room.Fishing)
	if err != nil {
		t.Fatal(err)
	}
	handle(1, 155, wire.AppendString(nil, 2, view.RoomCode))
	message := func(value string) []byte {
		return wire.AppendBytes(nil, 2, wire.AppendString(wire.AppendString(nil, 1, view.RoomCode), 4, value))
	}
	rejected := handle(0, 172, message("Blocked"))
	if len(rejected) != 1 || rejected[0].ConnectionID != clients[0].ID || rejected[0].Frame.Type != 173 {
		t.Fatalf("blocked matching text must receive EParam without broadcasting: %+v", rejected)
	}
	header, _, err := wire.Bytes(rejected[0].Frame.Payload, 1)
	if err != nil || appValue(header, 2) != 2 {
		t.Fatal("blocked matching text response omitted EParam")
	}
	allowed := handle(0, 172, message("Allowed"))
	guestReceived := false
	for _, delivery := range allowed {
		if delivery.ConnectionID == clients[1].ID && delivery.Frame.Type == 174 {
			body, _, err := wire.Bytes(delivery.Frame.Payload, 2)
			if err != nil {
				t.Fatal(err)
			}
			text, _, err := wire.Bytes(body, 4)
			guestReceived = err == nil && string(text) == "Allowed"
		}
	}
	if !guestReceived {
		t.Fatal("valid matching text did not reach guest after rejected text")
	}
	handle(0, 157, nil)
	if view.Selection.Capacity != boat.Capacity || view.Selection.ReconnectWindow != boat.ReconnectWindow || view.Selection.Duration != boat.Duration {
		t.Fatalf("native fishing limits differ from GD boat: %+v want %+v", view.Selection, boat)
	}
	var guids [2]string
	for i := range clients {
		clients[i].Channel = room.Fishing
		clients[i].ID = fmt.Sprintf("fish-game-%d", i)
		d := handle(i, 23, wire.AppendString(nil, 2, view.RoomCode))
		guid, found, err := wire.Bytes(d[0].Frame.Payload, 4)
		if err != nil || !found {
			t.Fatal("fishing entry missing GUID")
		}
		guids[i] = string(guid)
	}
	var beforeAP [2]uint64
	loginAP := func(i int) uint64 {
		t.Helper()
		_, reply := call(i, "/LoginUser", nil)
		rows := appRows(reply.Responses[0].Body, 1)
		if len(rows) != 1 {
			t.Fatal("LoginUser missing player")
		}
		return appValue(rows[0], 62)
	}
	for i := range clients {
		beforeAP[i] = loginAP(i)
	}
	if beforeAP[0] == 0 {
		t.Fatal("real starter has no multiplayer fishing ticket")
	}
	var starts [2]player.Command
	for i := range clients {
		cmd, reply := call(i, "/FishingVoyageStart", wire.AppendVarint(nil, 2, mapID))
		starts[i] = cmd
		if reply.Responses[0].PacketCode != 500 {
			t.Fatal("wrong FishingVoyageStart packet")
		}
		if i == 0 && appValue(reply.Responses[0].Body, 1) != 1 {
			t.Fatal("host response omitted room host indication")
		}
		drain(i)
	}
	// Read the guest's committed snapshot independently of the room projection.
	path := filepath.Join(factory.options.stateDirectory, "accounts", accountDirectoryName(accounts[1]), "state.db")
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}
	query := u.Query()
	query.Set("mode", "ro")
	query.Set("_pragma", "query_only(1)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	var saved []byte
	readErr := db.QueryRowContext(ctx, "SELECT payload FROM domain_entry WHERE domain_name='fishing' AND bucket='state' AND entry_key='core'").Scan(&saved)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	var guest struct {
		Boat                    uint64
		Voyage                  *eventplay.NativeRun
		VoyageStarted, NextAuto int64
	}
	if err := json.Unmarshal(saved, &guest); err != nil {
		t.Fatal(err)
	}
	if guest.Boat != starterBoat || guest.Voyage == nil || guest.Voyage.BoatLevel != partyBoat || guest.Voyage.Members != 2 {
		t.Fatalf("guest's personal boat or host voyage changed: %+v", guest)
	}
	boatRow, err := factory.design.eventPlay.Row("FishingBoatTable", 8, partyBoat)
	if err != nil {
		t.Fatal(err)
	}
	waitReduction := float64(0)
	for _, group := range appPacked(boatRow, 1) {
		for _, row := range factory.design.eventPlay.Rows("FishingBoatBuffTable", 2, group) {
			if appValue(row, 3) != 2 {
				continue
			}
			buff, err := factory.design.eventPlay.Row("FishingBuffTable", 4, appValue(row, 1))
			if err != nil {
				t.Fatal(err)
			}
			if appValue(buff, 2) == 7 {
				waitReduction += nativeFixtureDouble(t, buff, 3)
			}
		}
	}
	defaults, err := factory.design.eventPlay.Row("FishingDefaultTable", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	baseWait := nativeFixtureDouble(t, defaults, 5)
	if waitReduction <= 0 {
		t.Fatal("selected real party boat has no waiting-time buff to distinguish guest boat")
	}
	wantWait := int64(math.Ceil(math.Max(1, baseWait-waitReduction)))
	if guest.NextAuto-guest.VoyageStarted != wantWait {
		t.Fatalf("guest auto waiting time=%d want host boat two-person buff=%d", guest.NextAuto-guest.VoyageStarted, wantWait)
	}
	if got := loginAP(0); got != beforeAP[0]-1 {
		t.Fatalf("host ticket debit=%d want%d", got, beforeAP[0]-1)
	}
	if got := loginAP(1); got != beforeAP[1] {
		t.Fatal("guest spent host multiplayer ticket")
	}
	registryExecute(t, actors[0], starts[0])
	if got := loginAP(0); got != beforeAP[0]-1 {
		t.Fatal("replayed Start spent another ticket")
	}
	for i := range clients {
		b := wire.AppendString(nil, 2, guids[i])
		b = wire.AppendString(b, 3, view.RoomCode)
		b = wire.AppendBytes(b, 4, wire.AppendVarint(nil, 1, uint64(clients[i].Principal.OwnerIndex)))
		handle(i, 212, b)
	}
	view, err = manager.View(ctx, clients[0].Principal, room.Fishing)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range view.Members {
		if !m.AcceptedRun || !m.Ready {
			t.Fatal("committed fishing admission did not reach ready native state")
		}
	}
	chat := func(value string) []byte {
		payload := wire.AppendString(wire.AppendVarint(nil, 1, uint64(clients[0].Principal.OwnerIndex)), 2, value)
		data := append(binary.LittleEndian.AppendUint32(nil, 9004), payload...)
		return wire.AppendBytes(wire.AppendString(nil, 2, guids[0]), 3, data)
	}
	if rejected := handle(0, 70, chat("Blocked")); len(rejected) != 0 {
		t.Fatal("blocked fishing chat was broadcast")
	}
	allowed = handle(0, 70, chat("Allowed"))
	guestReceived = false
	for _, delivery := range allowed {
		guestReceived = guestReceived || delivery.ConnectionID == clients[1].ID && delivery.Frame.Type == 71 && bytes.Contains(delivery.Frame.Payload, []byte("Allowed"))
	}
	if !guestReceived {
		t.Fatal("valid fishing chat did not reach guest after rejected text")
	}
	info := wire.AppendVarint(nil, 1, uint64(clients[1].Principal.OwnerIndex))
	d := handle(1, 210, wire.AppendVarint(wire.AppendBytes(wire.AppendString(nil, 2, guids[1]), 3, info), 4, 1))
	if len(d) != 1 || d[0].ConnectionID != clients[0].ID || d[0].Frame.Type != 211 {
		t.Fatal("guest fishing state was not relayed only to other participant")
	}
	clock.Add(int64(5 * time.Second))
	handle(0, 192, wire.AppendString(nil, 2, guids[0]))
	var final room.FinalResult
	select {
	case final = <-state.rooms.queue:
	case <-ctx.Done():
		t.Fatal("native fishing terminal missing")
	}
	if final.View.Kind != room.Fishing || len(final.View.Members) != 2 {
		t.Fatal("incomplete fishing terminal participants")
	}
	if err := state.rooms.persist(ctx, final); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-published:
		if len(d) != 2 || d[0].Frame.Type != 204 || d[1].Frame.Type != 204 {
			t.Fatal("durable fishing terminal not sent to both participants")
		}
	case <-ctx.Done():
		t.Fatal("fishing persistence acknowledgement missing")
	}
	reopen := func() {
		t.Helper()
		if err := registry.Close(ctx); err != nil {
			t.Fatal(err)
		}
		registry = newPlayerRegistry(factory, time.Hour)
		for i, account := range accounts {
			actor, release, err := registry.Acquire(ctx, account)
			if err != nil {
				t.Fatal("reopen committed fishing player", err)
			}
			t.Cleanup(release)
			actors[i] = actor
		}
	}
	reopen()
	if loginAP(0) != beforeAP[0]-1 || loginAP(1) != beforeAP[1] {
		t.Fatal("reopen lost committed voyage ticket debit")
	}
	var ends [2]player.Command
	for i := range clients {
		cmd, reply := call(i, "/FishingVoyageEnd", nil)
		ends[i] = cmd
		if reply.Responses[0].PacketCode != 508 {
			t.Fatal("wrong FishingVoyageEnd packet")
		}
		drain(i)
		replay := registryExecute(t, actors[i], cmd)
		if !bytes.Equal(reply.Responses[0].Body, replay.Responses[0].Body) {
			t.Fatal("fishing End replay changed response")
		}
		if i == 0 {
			if err := state.store.View(ctx, func(tx *shared.Tx) error {
				_, found, err := tx.Get("room.results", final.View.InstanceID)
				if !found {
					t.Error("host claim removed guest's retained result")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if loginAP(0) != beforeAP[0]-1 || loginAP(1) != beforeAP[1] {
		t.Fatal("fishing End or replay changed multiplayer tickets")
	}
	reopen()
	for i := range actors {
		registryExecute(t, actors[i], ends[i])
		registryExecute(t, actors[i], starts[i])
	}
	if loginAP(0) != beforeAP[0]-1 || loginAP(1) != beforeAP[1] {
		t.Fatal("durable replay after reopen changed fishing tickets")
	}
	if err := state.store.View(ctx, func(tx *shared.Tx) error {
		for _, bucket := range []string{"room.results", "room.runs", "room.claims"} {
			rows, err := tx.Scan(bucket, final.View.InstanceID, "", 128)
			if err != nil {
				return err
			}
			if len(rows) != 0 {
				t.Errorf("claimed fishing instance retained%s", bucket)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func nativeFixtureDouble(t *testing.T, raw []byte, number int) float64 {
	t.Helper()
	var value float64
	if err := wire.Walk(raw, func(field wire.Field) error {
		if field.Number == number {
			if field.Type != 1 {
				return wire.ErrMalformed
			}
			value = math.Float64frombits(binary.LittleEndian.Uint64(field.Value))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return value
}
