package room

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type testRules struct{}

func (testRules) Profile(_ context.Context, p Principal) (Profile, error) {
	return Profile{UserInfo: integer(nil, 1, p.OwnerIndex)}, nil
}
func (testRules) ValidateSelection(_ Profile, s Selection) (Selection, error) {
	s.Capacity = 2
	s.MonsterID = 104
	return s, nil
}
func nativeParty(t *testing.T, publish func(FinalResult) bool, deliver func([]Delivery)) (*Manager, *atomic.Int64, Connection, Connection, string, string) {
	t.Helper()
	clock := new(atomic.Int64)
	clock.Store(time.Now().UnixNano())
	m, err := New(Config{AdvertisedHost: "127.0.0.1", TCPPort: 9000, Now: func() time.Time { return time.Unix(0, clock.Load()) }, ResultRetention: time.Millisecond, PublishResult: publish, Publish: deliver}, testRules{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	a := Connection{ID: "match-a", Channel: Matching, Principal: Principal{AccountID: "a", OwnerIndex: 11, SessionID: "sa", Generation: 1}}
	b := Connection{ID: "match-b", Channel: Matching, Principal: Principal{AccountID: "b", OwnerIndex: 22, SessionID: "sb", Generation: 1}}
	request := func(c Connection, typ int32, p []byte) []Delivery {
		t.Helper()
		d, e := m.Handle(context.Background(), c, Frame{Type: typ, Payload: p})
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	request(a, 82, integer(integer(nil, 4, 2), 5, 1))
	v, e := m.View(context.Background(), a.Principal, Action)
	if e != nil {
		t.Fatal(e)
	}
	request(b, 90, text(integer(nil, 3, 2), 2, v.RoomCode))
	request(a, 104, nil)
	a.Channel = Action
	a.ID = "game-a"
	b.Channel = Action
	b.ID = "game-b"
	d := request(a, 23, text(nil, 2, v.RoomCode))
	ga := stringField(d[0].Frame.Payload, 4)
	d = request(b, 23, text(nil, 2, v.RoomCode))
	gb := stringField(d[0].Frame.Payload, 4)
	request(a, 45, text(nil, 2, ga))
	request(b, 45, text(nil, 2, gb))
	for _, pair := range []struct {
		c Connection
		g string
	}{{a, ga}, {b, gb}} {
		if e := m.AcceptRun(context.Background(), pair.c.Principal, Action, v.InstanceID, pair.g); e != nil {
			t.Fatal(e)
		}
	}
	return m, clock, a, b, ga, gb
}

func TestReconnectPreservesHostMigrationAndFencesOldSocket(t *testing.T) {
	m, clock, a, b, ga, _ := nativeParty(t, nil, nil)
	d, e := m.Disconnect(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	changed := false
	for _, delivery := range d {
		if delivery.Frame.Type == 107 && number(delivery.Frame.Payload, 2) == b.Principal.OwnerIndex {
			changed = true
		}
	}
	if !changed {
		t.Fatal("disconnect did not notify surviving host")
	}
	reconnected := a
	reconnected.ID = "game-a-reconnected"
	d, e = m.Handle(context.Background(), reconnected, Frame{Type: 109, Sequence: 73, Payload: text(nil, 2, ga)})
	if e != nil {
		t.Fatal(e)
	}
	if len(d) == 0 || d[0].Frame.Type != 110 || d[0].Frame.Sequence != 73 || number(d[0].Frame.Payload, 8) != b.Principal.OwnerIndex {
		t.Fatalf("bad reconnect response: %+v", d)
	}
	_, e = m.Handle(context.Background(), a, Frame{Type: 111, Payload: text(nil, 2, ga)})
	if !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("old socket retained authority: %v", e)
	}
	_, e = m.Disconnect(context.Background(), reconnected)
	if e != nil {
		t.Fatal(e)
	}
	clock.Add(int64(16 * time.Second))
	reconnected.ID = "too-late"
	d, e = m.Handle(context.Background(), reconnected, Frame{Type: 109, Payload: text(nil, 2, ga)})
	if e != nil || len(d) != 1 || number(bytesField(d[0].Frame.Payload, 1), 2) != 6 {
		t.Fatalf("expired reconnect accepted: %+v %v", d, e)
	}
}

func TestTerminalResultWaitsForDurableAcknowledgment(t *testing.T) {
	results := make(chan FinalResult, 1)
	deliveries := make(chan []Delivery, 2)
	m, clock, a, _, ga, _ := nativeParty(t, func(result FinalResult) bool { results <- result; return true }, func(d []Delivery) { deliveries <- d })
	monster := integer(nil, 3, 104)
	d, err := m.Handle(context.Background(), a, Frame{Type: 111, Payload: blob(text(nil, 2, ga), 5, monster)})
	if err != nil {
		t.Fatal(err)
	}
	result := <-results
	if !result.Clear || len(result.View.Members) != 2 || result.View.InstanceID == "" {
		t.Fatalf("incomplete terminal receipt: %+v", result)
	}
	if len(d) != 0 {
		t.Fatalf("terminal frame returned before persistence acknowledgment: %+v", d)
	}
	select {
	case d := <-deliveries:
		t.Fatalf("terminal frame published before persistence acknowledgment: %+v", d)
	default:
	}
	clock.Add(int64(time.Minute))
	// Expiration runs on the owner, so submit it without relying on a wall-clock sleep.
	o := m.lookup(a.Principal, Action)
	_, _, err = o.submit(context.Background(), 0, func(s *state) (any, []Delivery, error) { return nil, s.expire(m.config.Now()), nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Result(context.Background(), a.Principal, Action, result.View.InstanceID); err != nil {
		t.Fatalf("unacknowledged result was lost: %v", err)
	}
	if err = m.AcknowledgeResult(context.Background(), result.View.InstanceID); err != nil {
		t.Fatal(err)
	}
	d = <-deliveries
	if len(d) != 2 || d[0].Frame.Type != 108 || d[1].Frame.Type != 108 {
		t.Fatalf("durable result did not release one terminal frame per player: %+v", d)
	}
	if err = m.AcknowledgeResult(context.Background(), result.View.InstanceID); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-deliveries:
		t.Fatalf("repeated persistence acknowledgment duplicated terminal frames: %+v", d)
	default:
	}
	_, _, err = o.submit(context.Background(), 0, func(s *state) (any, []Delivery, error) {
		s.expire(m.config.Now())
		if !s.closed {
			return nil, nil, errors.New("durably acknowledged result did not expire")
		}
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestShutdownRetriesTerminalResultUntilQueued(t *testing.T) {
	var attempts atomic.Int64
	results := make(chan FinalResult, 1)
	m, _, a, _, guid, _ := nativeParty(t, func(result FinalResult) bool {
		if attempts.Add(1) == 1 {
			return false
		}
		results <- result
		return true
	}, nil)
	monster := integer(nil, 3, 104)
	if _, err := m.Handle(context.Background(), a, Frame{Type: 111, Payload: blob(text(nil, 2, guid), 5, monster)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		if !result.Clear || attempts.Load() < 2 {
			t.Fatal("shutdown did not retry the terminal snapshot")
		}
	default:
		t.Fatal("shutdown discarded an unqueued terminal snapshot")
	}
}
