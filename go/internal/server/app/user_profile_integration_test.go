//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/monsterhunt"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// 2.36.7 UserInfoPacket consumes UserContentsInfo fields 1/2/4/17/26/28,
// and then sends IdCardRecovery for an unsaved default card. Current GameData
// 20261008155037: achievement 0/1/1 needs 500 progress, adds 3 EXP; cumulative
// level requirements 2 and 15 make this level 2. Default background is 10001.
func TestUserContentsInfoReflectsCommittedProfileAndRejectsOtherOwners(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	now := time.Now()
	presets, err := gamedata.LoadMonsterHuntPresetDesign(factory.options.gameData, factory.options.gameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	factory.design.monsterHunt, err = monsterhunt.NewRules([]monsterhunt.Season{{ID: 81, Hunt: 75, Start: uint64(now.Add(-time.Hour).UnixMilli()), End: uint64(now.Add(time.Hour).UnixMilli())}}, factory.seeds.defaults, presets, factory.design.source.MonsterHunt)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := factory.profiles.GameIdentity(context.Background(), accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := factory.profiles.GameIdentity(context.Background(), accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner != nil {
			if err := owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	var seq uint64
	request := func(path string, fields []byte) player.Command {
		seq++
		body := append(wire.AppendVarint(nil, 1, seq), fields...)
		return player.Command{
			Identity: command.Identity{AccountID: accounts[0], SessionID: "profile-client", RequestID: fmt.Sprintf("profile-%d", seq)},
			Digest:   sha256.Sum256(append([]byte(path), body...)), Requests: []player.Request{{Path: path, Body: body}},
		}
	}
	call := func(c player.Command, code int) []byte {
		t.Helper()
		reply, err := owner.Execute(context.Background(), c)
		if err != nil || len(reply.Responses) != 1 {
			t.Fatalf("%s: %v, responses=%d", c.Requests[0].Path, err, len(reply.Responses))
		}
		if reply.Responses[0].PacketCode != code {
			t.Fatalf("%s packet code=%d want %d", c.Requests[0].Path, reply.Responses[0].PacketCode, code)
		}
		return reply.Responses[0].Body
	}
	self := func() []byte {
		return call(request("/UserContentsInfo", wire.AppendVarint(nil, 2, uint64(identity.OwnerIndex))), 215)
	}
	before := self()
	if got, _, _ := wire.Varint(before, 26); got != 1 {
		t.Fatalf("unearned profile achievement level=%d want 1", got)
	}
	var portrait uint64
	for _, costume := range owner.assembly.collection.Costumes() {
		if costume.ID != 0 && (portrait == 0 || costume.ID < portrait) {
			portrait = costume.ID
		}
	}
	if portrait == 0 {
		t.Fatal("integration starter has no real owned portrait costume")
	}
	call(request("/UserPortraitChange", wire.AppendVarint(nil, 2, portrait)), 75)
	call(request("/SaveTotalBattlePower", wire.AppendVarint(nil, 2, 12345)), 258)
	update := wire.AppendVarint(nil, 2, 1)
	update = wire.AppendVarint(update, 3, 500)
	call(request("/AchievementUpdate", update), 167)
	clear := wire.AppendVarint(nil, 1, 1)
	clear = wire.AppendVarint(clear, 2, 1)
	claim := request("/AchievementClear", wire.AppendBytes(nil, 3, clear))
	claimResponse := call(claim, 168)
	if retry := call(claim, 168); !bytes.Equal(claimResponse, retry) {
		t.Fatal("achievement reward retry changed response")
	}
	wantHuntRank := uint64(0)
	cardRecovered := false
	assertCard := func(card []byte) {
		t.Helper()
		for _, expected := range []struct {
			Field int
			ID    uint64
			X, Y  int32
			Scale float32
			Color string
		}{{1, 10001, 0, 0, 1, ""}, {4, 40001, -234, 52, 1, ""}, {5, 0, -218, 156, 0.75, "FFFFFF"}} {
			item, found, err := wire.Bytes(card, expected.Field)
			if err != nil || !found {
				t.Fatalf("default card is missing layout component%d: %v", expected.Field, err)
			}
			index, _, err := wire.Varint(item, 1)
			if err != nil || (cardRecovered && expected.ID != 0) != (index > 0) {
				t.Fatalf("card component%d inventory index=%d recovered=%v: %v", expected.Field, index, cardRecovered, err)
			}
			for field, want := range map[int]uint64{2: expected.ID, 7: 0} {
				if got, _, err := wire.Varint(item, field); err != nil || got != want {
					t.Fatalf("card component%d field%d=%d want %d error=%v", expected.Field, field, got, want, err)
				}
			}
			for field, want := range map[int]int32{3: expected.X, 4: expected.Y} {
				if got, _, err := wire.Varint(item, field); err != nil || int32(got) != want {
					t.Fatalf("card component%d position%d=%d want %d error=%v", expected.Field, field, int32(got), want, err)
				}
			}
			values := map[int]float32{}
			if err := wire.Walk(item, func(f wire.Field) error {
				if f.Number == 5 || f.Number == 6 {
					if f.Type != 5 {
						return wire.ErrMalformed
					}
					values[f.Number] = math.Float32frombits(binary.LittleEndian.Uint32(f.Value))
				}
				return nil
			}); err != nil || values[5] != 0 || values[6] != expected.Scale {
				t.Fatalf("card component%d rotate/scale=%v want 0/%g error=%v", expected.Field, values, expected.Scale, err)
			}
			color, _, err := wire.Bytes(item, 8)
			if err != nil || string(color) != expected.Color {
				t.Fatalf("card component%d color=%q want %q error=%v", expected.Field, color, expected.Color, err)
			}
		}
	}
	assertProfile := func(body []byte) {
		t.Helper()
		for field, want := range map[int]uint64{1: uint64(identity.OwnerIndex), 4: portrait, 9: wantHuntRank, 16: 12345, 26: 2} {
			if got, _, err := wire.Varint(body, field); err != nil || got != want {
				t.Fatalf("profile field%d=%d want %d error=%v", field, got, want, err)
			}
		}
		name, _, err := wire.Bytes(body, 2)
		if err != nil || string(name) != identity.UserID {
			t.Fatalf("profile name=%q want %q error=%v", name, identity.UserID, err)
		}
		guild, found, err := wire.Bytes(body, 17)
		if err != nil || !found {
			t.Fatal("guildless profile must send the non-null GuildBaseInfo used by the client")
		}
		if got, _, err := wire.Varint(guild, 1); err != nil || got != 0 {
			t.Fatalf("guildless profile guild ID=%d want 0 error=%v", got, err)
		}
		room, found, err := wire.Bytes(body, 14)
		if err != nil || !found {
			t.Fatal("profile room callback has no owner identity")
		}
		if got, _, _ := wire.Varint(room, 1); got != uint64(identity.OwnerIndex) {
			t.Fatal("room identity differs from queried owner")
		}
		for _, field := range []int{6, 7, 8, 10, 15, 20, 21, 22, 23, 24, 25, 31, 32, 33} {
			if got, _, _ := wire.Varint(body, field); got != 0 {
				t.Fatalf("profile invented unearned progress field%d=%d", field, got)
			}
		}
		card, found, err := wire.Bytes(body, 28)
		if err != nil || !found {
			t.Fatal("profile omitted ID card layout required by its UI")
		}
		assertCard(card)
	}
	after := self()
	assertProfile(after)
	recovery := call(request("/IdCardRecovery", nil), 452)
	cardRecovered = true
	recovered, found, err := wire.Bytes(recovery, 2)
	if err != nil || !found {
		t.Fatal("self-profile's ID card recovery did not complete with a layout")
	}
	assertCard(recovered)
	if bundle, found, _ := wire.Bytes(recovery, 1); !found || len(bundle) == 0 {
		t.Fatal("initial ID card recovery did not return the actual default item grant")
	}
	if bundle, found, _ := wire.Bytes(call(request("/IdCardRecovery", nil), 452), 1); found && len(bundle) != 0 {
		t.Fatal("second ID card recovery granted default items again")
	}
	hunt, err := factory.design.source.MonsterHunt(75)
	if err != nil {
		t.Fatal(err)
	}
	combat := func(action func(command.Context) ([]byte, error)) {
		t.Helper()
		tx, err := owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := action(command.Context{Identity: command.Identity{AccountID: accounts[0], SessionID: "profile-combat"}, State: tx}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	entry := wire.AppendVarint(nil, 4, hunt.DeckID)
	entry = wire.AppendVarint(entry, 5, monsterhunt.BattleMode)
	entry = wire.AppendVarint(entry, 6, hunt.ID)
	entry = wire.AppendVarint(entry, 10, 1)
	combat(func(ctx command.Context) ([]byte, error) {
		return owner.assembly.monsterHuntService.EnterBattle(ctx, entry, "profile-hunt-clear")
	})
	attack := wire.AppendVarint(nil, 1, 1)
	attack = wire.AppendVarint(attack, 2, 1000)
	end := wire.AppendVarint(nil, 7, 0)
	end = wire.AppendBytes(end, 10, wire.AppendBytes(nil, 1, attack))
	combat(func(ctx command.Context) ([]byte, error) {
		return owner.assembly.monsterHuntService.CompleteBattle(ctx, end, "profile-hunt-clear")
	})
	wantHuntRank = 1
	assertProfile(self())
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = nil
	owner, err = factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	assertProfile(self())
	for _, target := range []uint64{uint64(other.OwnerIndex), math.MaxInt64, math.MaxUint64, 0} {
		c := request("/UserContentsInfo", wire.AppendVarint(nil, 2, target))
		reply, err := owner.Execute(context.Background(), c)
		if !errors.Is(err, profile.ErrNotFound) || len(reply.Responses) != 0 {
			t.Fatalf("target%d received a self-profile or leaked another account: response=%v error=%v", target, reply.Responses, err)
		}
	}
	assertProfile(self())
}
