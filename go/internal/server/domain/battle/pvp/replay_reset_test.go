package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestMirrorAttackResetPersistsBaselineWithoutErasingSeasonWins(t *testing.T) {
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "alice", SessionID: "s"}, State: store}
	source := &progressSource{value: PersonalProgress{Found: true, VP: 1200, AttackCount: 10, Wins: 7, Losses: 3, DefenseWins: 4, DefenseLosses: 2}}
	service := &Service{now: func() time.Time { return time.UnixMilli(100) }, profile: Profile{OwnerID: "alice"}, source: source, rules: &Rules{Seasons: []Season{{Mode: Mirror, ID: 1, Start: 1, End: 1000}}, Design: &gamedata.PvpDesign{}}, state: state{Version: 1, Modes: map[Mode]*modeState{Mirror: {Progress: map[string]progress{}, Pending: map[string]pendingResult{}}}}}
	body, err := service.handle(ctx, Mirror, "/PvpBattleReset", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	date, _, _ := wire.Varint(body, 1)
	defenseDate, _, _ := wire.Varint(body, 2)
	if date != 100 || defenseDate != 0 {
		t.Fatalf("wrong reset side: %x", body)
	}
	raw, _, err := store.LoadEntry("pvp", "state", "core")
	if err != nil {
		t.Fatal(err)
	}
	var reopened state
	if err = json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	service.state = reopened
	source.value.Wins++
	p, err := service.progress(Mirror, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Wins != 8 || p.Losses != 3 || p.VP != 1200 || p.DeckWins != 1 || p.DeckLosses != 0 || p.DeckDefenseWins != 4 || p.DeckDefenseLosses != 2 {
		t.Fatalf("reset erased season/defense results or new wins: %+v", p)
	}
}

func TestMirrorReplayUsesEveryActuallyIssuedTurnSeedAfterReopening(t *testing.T) {
	store := stateio.NewMemory()
	ctx := command.Context{Identity: command.Identity{AccountID: "alice", SessionID: "s"}, State: store}
	blue := wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, 9001))
	red := wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, 9002))
	active := &challenge{ID: "challenge", BattleIndex: 42, Season: 1, Selected: 2, Attacker: PublishedSnapshot{Profile: Profile{OwnerID: "alice", OwnerIndex: 1}, Snapshot: FullSnapshot{FullDeck: blue}}, Candidates: []PublishedSnapshot{{Profile: Profile{OwnerID: "bob", OwnerIndex: 2}, Snapshot: FullSnapshot{FullDeck: red}}}}
	service := &Service{profile: Profile{OwnerID: "alice"}, source: &progressSource{}, now: func() time.Time { return time.UnixMilli(100) }, rules: &Rules{Seasons: []Season{{Mode: Mirror, ID: 1, Start: 1, End: 1000}}, Design: &gamedata.PvpDesign{}}, state: state{Version: 1, Modes: map[Mode]*modeState{Mirror: {Active: active, Progress: map[string]progress{}, Pending: map[string]pendingResult{}}}}}
	design := &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000, BoostMaximum: 1}, MirrorRanks: map[uint64][]gamedata.PvpRank{1: {{Group: 1, ID: 1, WinPoint: 20}}}}
	service.rules.Design = design
	service.rules.Policy = &LocalScorePolicy{Design: design}
	active.Multiplier = 1
	var events []command.Event
	ctx.Emit = func(e command.Event) error { events = append(events, e); return nil }
	service.grant = func(command.Context, string, []gamedata.BattleReward) ([]byte, error) { return nil, nil }
	var issued []uint64
	for turn := 1; turn <= 4; turn++ {
		body, err := service.start(ctx, Mirror, wire.AppendVarint(nil, 1, uint64(turn)))
		if err != nil {
			t.Fatal(err)
		}
		seed, _, _ := wire.Varint(body, 1)
		issued = append(issued, seed)
	}
	raw, _, _ := store.LoadEntry("pvp", "state", "core")
	var reopened state
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	service.state = reopened
	service.state.Modes[Mirror].Attack.Snapshot.FullDeck = wire.AppendBytes(nil, 1, wire.AppendVarint(nil, 1, 9999))
	if _, err := service.handle(ctx, Mirror, "/PvpBattleEnd", wire.AppendVarint(wire.AppendVarint(nil, 1, 5), 2, 1)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Topic != "pvp.battle.settled" {
		t.Fatal("settlement did not publish the durable battle")
	}
	service = &Service{profile: Profile{OwnerID: "alice"}}
	body, err := service.replay(ctx, Mirror, wire.AppendVarint(nil, 2, 42), true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ints(body, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(issued) {
		t.Fatalf("replay has %d seeds for %d turns", len(got), len(issued))
	}
	for i, seed := range issued {
		if got[i] != seed {
			t.Fatalf("turn %d replay RNG differs: %d != %d", i+1, got[i], seed)
		}
	}
	blueReply, _ := messages(body, 1)
	redReply, _ := messages(body, 2)
	if len(blueReply) != 1 || len(redReply) != 1 || !bytes.Equal(blueReply[0], blue) || !bytes.Equal(redReply[0], red) {
		t.Fatalf("replay lost frozen entered formations: %x", body)
	}
	ctx.AccountID = "mallory"
	service.profile.OwnerID = "mallory"
	if _, err = service.replay(ctx, Mirror, wire.AppendVarint(nil, 2, 42), true); err == nil {
		t.Fatal("unrelated account accessed private replay")
	}
}
