package social_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/social"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type memoryStore struct {
	mu   sync.Mutex
	rows map[string]map[string][]byte
	deny bool
}
type memoryTx struct{ rows map[string]map[string][]byte }

func (tx memoryTx) Get(bucket, key string) ([]byte, bool, error) {
	v, ok := tx.rows[bucket][key]
	return append([]byte(nil), v...), ok, nil
}
func (tx memoryTx) Put(bucket, key string, v []byte) error {
	if tx.rows[bucket] == nil {
		tx.rows[bucket] = map[string][]byte{}
	}
	tx.rows[bucket][key] = append([]byte(nil), v...)
	return nil
}
func (tx memoryTx) Delete(bucket, key string) (bool, error) {
	_, ok := tx.rows[bucket][key]
	delete(tx.rows[bucket], key)
	return ok, nil
}
func (tx memoryTx) Scan(bucket, prefix, after string, limit int) ([]social.Entry, error) {
	var keys []string
	for key := range tx.rows[bucket] {
		if strings.HasPrefix(key, prefix) && key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	var rows []social.Entry
	for _, key := range keys {
		rows = append(rows, social.Entry{Key: key, Value: append([]byte(nil), tx.rows[bucket][key]...)})
	}
	return rows, nil
}
func (s *memoryStore) Update(_ context.Context, f func(social.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deny {
		return errors.New("shared store touched inside player transaction")
	}
	next := map[string]map[string][]byte{}
	for bucket, rows := range s.rows {
		next[bucket] = map[string][]byte{}
		for key, v := range rows {
			next[bucket][key] = append([]byte(nil), v...)
		}
	}
	if e := f(memoryTx{next}); e != nil {
		return e
	}
	s.rows = next
	return nil
}
func (s *memoryStore) View(_ context.Context, f func(social.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deny {
		return errors.New("shared store touched inside player transaction")
	}
	return f(memoryTx{s.rows})
}

var epoch = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newOwner(t *testing.T) (*social.Owner, *memoryStore) {
	t.Helper()
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	d := &gamedata.GuildDesign{Guild: gamedata.GuildRules{MaxMembers: 3, ApplicationLimit: 4, ApplicationHours: 8, LeaveDelayHours: 2, DeleteDelayHours: 1, Flags: map[uint64]bool{1: true}, Emblems: map[uint64]bool{1: true}, CreateCost: gamedata.Reward{Type: 3, Count: 7}, Attendance: gamedata.Reward{Type: 2, Count: 5}}, Raid: gamedata.GuildRaidRules{DailySupporterUses: 1, Bosses: map[uint64]map[uint64]gamedata.GuildRaidBoss{7: {1: {GroupID: 7, Level: 1, DeckID: 91, AddScore: 40}}}}}
	o, e := social.New(store, d)
	if e != nil {
		t.Fatal(e)
	}
	return o, store
}
func member(id string, index uint64) social.PublicMember {
	return social.PublicMember{AccountID: id, OwnerIndex: index, Name: id}
}
func proof(r social.Reservation) social.CommitProof {
	return social.CommitProof{OperationID: r.ID, ActorAccount: r.ActorAccount, Digest: r.Digest}
}
func commit(t *testing.T, o *social.Owner, op social.Operation) social.Reservation {
	t.Helper()
	r, e := o.Prepare(context.Background(), op)
	if e != nil {
		t.Fatal(e)
	}
	r, e = o.Confirm(context.Background(), proof(r))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func guild(t *testing.T, o *social.Owner, actor social.PublicMember, name string, join social.JoinType) uint64 {
	t.Helper()
	return commit(t, o, social.Operation{ID: "create:" + actor.AccountID, Kind: social.Create, Actor: actor, AtMS: epoch.UnixMilli(), Create: social.CreateDetails{Name: name, Icon: 100001, IconColor: "#112233FF", JoinType: join}}).GuildID
}

func TestPendingCreationRequiresMatchingDurableProof(t *testing.T) {
	o, _ := newOwner(t)
	ctx := context.Background()
	op := social.Operation{ID: "pending-create", Kind: social.Create, Actor: member("alice", 1), AtMS: epoch.UnixMilli(), Create: social.CreateDetails{Name: "TeamOne", Icon: 100001, IconColor: "#112233FF", JoinType: social.OpenJoin}}
	r, e := o.Prepare(ctx, op)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = o.Guild(ctx, r.GuildID); !errors.Is(e, social.ErrNotFound) {
		t.Fatalf("uncommitted guild became visible: %v", e)
	}
	bad := proof(r)
	bad.ActorAccount = "intruder"
	if _, e = o.Confirm(ctx, bad); !errors.Is(e, social.ErrConflict) {
		t.Fatalf("wrong player proof accepted: %v", e)
	}
	p := proof(r)
	confirmed, e := o.RecoverReservation(ctx, r.ID, &p)
	if e != nil {
		t.Fatal(e)
	}
	again, e := o.Confirm(ctx, p)
	if e != nil || again.View.Revision != confirmed.View.Revision {
		t.Fatalf("proof replay mutated guild: %#v %v", again, e)
	}
	pending, _, e := o.PendingOperations(ctx, "", 256)
	if e != nil || len(pending) != 0 {
		t.Fatalf("recovery did not clear pending intent: %v %v", pending, e)
	}
	g, e := o.Guild(ctx, r.GuildID)
	if e != nil || len(g.Members) != 1 || g.Members["alice"].Role != social.Admin {
		t.Fatalf("recovered membership missing: %#v %v", g, e)
	}
}

func TestCompetingAcceptsPreserveOneGuildMembership(t *testing.T) {
	o, _ := newOwner(t)
	ctx := context.Background()
	officerA, officerB, applicant := member("officerA", 1), member("officerB", 2), member("applicant", 3)
	guildA, guildB := guild(t, o, officerA, "TeamOne", social.ApplicationJoin), guild(t, o, officerB, "TeamTwo", social.ApplicationJoin)
	for i, id := range []uint64{guildA, guildB} {
		commit(t, o, social.Operation{ID: fmt.Sprintf("apply:%d", i), Kind: social.Join, Actor: applicant, GuildID: id, AtMS: epoch.UnixMilli() + 10})
	}
	first, e := o.Prepare(ctx, social.Operation{ID: "accept:A", Kind: social.Accept, Actor: officerA, TargetAccount: applicant.AccountID, GuildID: guildA, AtMS: epoch.UnixMilli() + 20})
	if e != nil {
		t.Fatal(e)
	}
	second := social.Operation{ID: "accept:B", Kind: social.Accept, Actor: officerB, TargetAccount: applicant.AccountID, GuildID: guildB, AtMS: epoch.UnixMilli() + 20}
	if _, e = o.Prepare(ctx, second); !errors.Is(e, social.ErrPending) {
		t.Fatalf("competing acceptance not blocked: %v", e)
	}
	if _, e = o.Cancel(ctx, proof(first)); e != nil {
		t.Fatal(e)
	}
	commit(t, o, second)
	a, e := o.Account(ctx, applicant.AccountID)
	if e != nil || a.GuildID != guildB {
		t.Fatalf("incorrect final membership: %#v %v", a, e)
	}
	g, e := o.Guild(ctx, guildA)
	if e != nil || len(g.Members) != 1 || len(g.Reserved) != 0 {
		t.Fatalf("cancelled acceptance retained capacity: %#v %v", g, e)
	}
	sent, _, e := o.ApplicationsSent(ctx, applicant.AccountID, "", 256)
	if e != nil || len(sent) != 0 {
		t.Fatalf("accepted member still has applications: %#v %v", sent, e)
	}
}

func TestSeasonFreezeWaitsForResultsAndSumsPersonalBest(t *testing.T) {
	o, store := newOwner(t)
	ctx := context.Background()
	alice := member("alice", 1)
	bob := member("bob", 2)
	id := guild(t, o, alice, "TeamOne", social.OpenJoin)
	commit(t, o, social.Operation{ID: "join:bob", Kind: social.Join, Actor: bob, GuildID: id, AtMS: epoch.UnixMilli()})
	season := social.Season{GuildID: id, ID: 8, BossGroupID: 7, RulesVersion: "rules", StartsMS: epoch.UnixMilli(), EndsMS: epoch.Add(time.Hour).UnixMilli()}
	if e := o.OpenSeason(ctx, season); e != nil {
		t.Fatal(e)
	}
	var entries []social.Reservation
	participants := []social.PublicMember{alice, alice, bob}
	for i, participant := range participants {
		entries = append(entries, commit(t, o, social.Operation{ID: fmt.Sprintf("raid:%d", i), Kind: social.RaidEnter, Actor: participant, GuildID: id, ExpectedMembershipEpoch: 1, Season: season.ID, Day: "2026-10-11", AtMS: epoch.UnixMilli() + int64(i+1)}))
	}
	if e := o.FinalizeSeason(ctx, season.ID, season.EndsMS); !errors.Is(e, social.ErrPending) {
		t.Fatalf("freeze did not wait for accepted battles: %v", e)
	}
	for i, score := range []uint64{120, 70, 90} {
		r := entries[i]
		result := social.RaidResult{EntryID: r.ID, AccountID: participants[i].AccountID, ResultID: r.ID + ":end", GuildID: id, Season: season.ID, MembershipEpoch: 1, Level: 1, Score: score, RulesVersion: "rules", Digest: r.Digest}
		if _, e := o.SettleRaid(ctx, result); e != nil {
			t.Fatal(e)
		}
		if _, e := o.SettleRaid(ctx, result); e != nil {
			t.Fatalf("retry changed result: %v", e)
		}
	}
	commit(t, o, social.Operation{ID: "leave:bob", Kind: social.Leave, Actor: bob, GuildID: id, ExpectedMembershipEpoch: 1, AtMS: season.EndsMS + 1})
	restarted, e := social.New(store, &gamedata.GuildDesign{Guild: gamedata.GuildRules{MaxMembers: 3}})
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.FinalizeSeason(ctx, season.ID, season.EndsMS); e != nil {
		t.Fatal(e)
	}
	frozen, e := restarted.Season(ctx, id, season.ID)
	if e != nil || frozen.TotalScore != 210 || frozen.Pending != 0 || frozen.State != "frozen" {
		t.Fatalf("season did not sum contributors' individual bests after restart: %#v %v", frozen, e)
	}
	if len(frozen.FrozenMembers) != 2 || frozen.FrozenMembers[0].AccountID != alice.AccountID || frozen.FrozenMembers[0].Score != 120 || frozen.FrozenMembers[1].AccountID != bob.AccountID || frozen.FrozenMembers[1].Score != 90 {
		t.Fatalf("frozen contributors changed after departure: %#v", frozen.FrozenMembers)
	}
	service, e := social.NewService(social.ServiceConfig{Owner: o, Profile: func(context.Context, command.Identity) (social.PublicMember, error) { return alice, nil }, Qualify: func(context.Context, command.Identity, string) error { return nil }, CheckText: func(context.Context, command.Identity, string, string) error { return nil }, Spend: func(command.Context, string, []gamedata.Reward) error { return nil }, Grant: func(command.Context, string, []gamedata.Reward) error { return nil }, RaidState: func(context.Context, command.Identity) (social.RaidState, error) { return social.RaidState{}, nil }, RaidSeasons: []social.Season{season}, Now: func() time.Time { return time.UnixMilli(season.EndsMS + 1) }})
	if e != nil {
		t.Fatal(e)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, season.ID)
	request = wire.AppendVarint(request, 3, bob.OwnerIndex)
	plan, handled, e := service.Prepare(ctx, command.Identity{AccountID: alice.AccountID, SessionID: "history"}, "/GuildRaidMemberPlayHistoryInfo", request)
	if e != nil || !handled {
		t.Fatalf("departed member history unavailable: handled=%v err=%v", handled, e)
	}
	entriesInHistory := 0
	if e = wire.Walk(plan.Response, func(field wire.Field) error {
		if field.Number == 1 && field.Type == 2 {
			level, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			if level != 1 {
				t.Fatalf("departed member history level=%d", level)
			}
			entriesInHistory++
		}
		return nil
	}); e != nil || entriesInHistory != 1 {
		t.Fatalf("departed member history entries=%d err=%v", entriesInHistory, e)
	}
}

func TestPlayerApplyHasNoSharedStoreAccessAndChargesOnce(t *testing.T) {
	o, store := newOwner(t)
	alice := member("alice", 1)
	charges := 0
	s, e := social.NewService(social.ServiceConfig{Owner: o, Profile: func(context.Context, command.Identity) (social.PublicMember, error) { return alice, nil }, Qualify: func(context.Context, command.Identity, string) error { return nil }, CheckText: func(context.Context, command.Identity, string, string) error { return nil }, Spend: func(_ command.Context, _ string, r []gamedata.Reward) error {
		charges++
		if len(r) != 1 || r[0].Count != 7 {
			t.Fatalf("wrong configured charge: %#v", r)
		}
		return nil
	}, Grant: func(command.Context, string, []gamedata.Reward) error { return nil }, Now: func() time.Time { return epoch }})
	if e != nil {
		t.Fatal(e)
	}
	identity := command.Identity{AccountID: alice.AccountID, SessionID: "login"}
	req := wire.AppendVarint(nil, 1, 9)
	req = wire.AppendString(req, 2, "TeamOne")
	req = wire.AppendVarint(req, 3, 100001)
	req = wire.AppendString(req, 4, "#112233FF")
	req = wire.AppendVarint(req, 6, uint64(social.OpenJoin))
	plan, handled, e := s.Prepare(context.Background(), identity, "/GuildCreate", req)
	if e != nil || !handled {
		t.Fatalf("prepare: %v %v", handled, e)
	}
	var events []command.Event
	ctx := command.Context{Identity: identity, State: stateio.NewMemory(), Emit: func(e command.Event) error { events = append(events, e); return nil }}
	store.deny = true
	for range 2 {
		if _, _, e = s.Apply(ctx, plan); e != nil {
			t.Fatalf("player apply accessed shared owner or failed replay: %v", e)
		}
	}
	store.deny = false
	if charges != 1 || len(events) != 1 {
		t.Fatalf("replayed payment or event: charges=%d events=%d", charges, len(events))
	}
	if e = s.Confirm(context.Background(), plan); e != nil {
		t.Fatal(e)
	}
}
