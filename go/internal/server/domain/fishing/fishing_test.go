package fishing

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"errors"
	"testing"
	"time"
)

type noEconomy struct{}

func (noEconomy) Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error) {
	panic("catch unexpectedly invoked currency economy")
}
func row(fields ...uint64) []byte {
	var b []byte
	for i := 0; i < len(fields); i += 2 {
		b = wire.AppendVarint(b, int(fields[i]), fields[i+1])
	}
	return b
}
func fishingFixture(t *testing.T) (*Service, command.Context, *gamedata.EventPlayCatalog) {
	t.Helper()
	d := &gamedata.EventPlayCatalog{Tables: map[string][][]byte{}}
	defaults := row(1, 10, 15, 3, 25, 0, 47, 10)
	defaults = wire.AppendVarint(defaults, 12, 1)
	defaults = wire.AppendVarint(defaults, 13, 4)
	defaults = wire.AppendVarint(defaults, 14, 57)
	defaults = wire.AppendDouble(defaults, 11, 2)
	defaults = wire.AppendDouble(defaults, 22, 60)
	d.Tables["FishingDefaultTable"] = [][]byte{defaults}
	d.Tables["GameDefaultTable"] = [][]byte{row(57, 0, 32, 30, 35, 30, 33, 200)}
	d.Tables["FishingCharTable"] = [][]byte{wire.AppendDouble(wire.AppendDouble(row(1, 1, 2, 100), 3, 10), 4, 100)}
	d.Tables["FishingBoatTable"] = [][]byte{row(8, 1, 5, 1)}
	d.Tables["FishingRodTable"] = [][]byte{row(5, 4)}
	d.Tables["FishingMapTable"] = [][]byte{row(1, 7, 2, 3, 3, 1, 6, 1)}
	d.Tables["FishingGradePoolTable"] = [][]byte{row(1, 0, 2, 1, 3, 1, 4, 1, 5, 1)}
	d.Tables["FishingFishPoolTable"] = [][]byte{row(1, 12, 2, 7, 3, 1, 4, 1)}
	d.Tables["FishingFishSizePoolTable"] = [][]byte{row(1, 0, 2, 1, 3, 1000, 4, 0, 5, 1)}
	d.Tables["FishingFishTable"] = [][]byte{wire.AppendDouble(wire.AppendDouble(row(4, 10, 12, 12, 22, 20), 14, 20), 15, 10)}
	d.Tables["FishingCastingTable"] = [][]byte{row(3, 1)}
	ctx := command.Context{Identity: command.Identity{AccountID: "a", SessionID: "s"}, State: stateio.NewMemory()}
	s, e := Open(ctx, d, noEconomy{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	s.sample = func(uint64) (uint64, error) { return 0, nil }
	return s, ctx, d
}

func TestCatchRequiresServerSelectedEncounterAndCommitsExactlyOnce(t *testing.T) {
	s, ctx, d := fishingFixture(t)
	call := func(path string, req []byte) []byte {
		t.Helper()
		_, body, handled, e := s.Handle(ctx, path, req)
		if !handled || e != nil {
			t.Fatalf("%s: %v", path, e)
		}
		return body
	}
	call("/FishingVoyageStart", row(1, 1, 2, 3))
	call("/FishingCasting", row(1, 2, 2, 1))
	body := call("/FishingBiteStart", row(1, 3))
	fish, _, _ := wire.Bytes(body, 1)
	if num(fish, 1) != 12 || num(fish, 2) != 10 {
		t.Fatalf("unexpected server selection %x", fish)
	}
	if _, _, _, e := s.Handle(ctx, "/FishingBiteEnd", row(1, 4)); e == nil {
		t.Fatal("unfinished encounter granted fish")
	}
	for _, seq := range []uint64{5, 6} {
		call("/FishingBiteFishStaminaUpdate", row(1, seq, 2, uint64(0xfffffffffffffff6)))
	}
	end := row(1, 7)
	first := call("/FishingBiteEnd", end)
	second := call("/FishingBiteEnd", end)
	if string(first) != string(second) || len(s.state.Fish) != 1 || s.state.Exp != 10 {
		t.Fatal("replayed end changed fish or experience")
	}
	if _, _, _, e := s.Handle(ctx, "/FishingBiteEnd", row(1, 8)); e == nil {
		t.Fatal("new sequence reused completed catch")
	}
	if raw, err := ctx.State.Load("fishing"); err != nil || raw != nil {
		t.Fatalf("fishing created a root snapshot outside the nine account domains: %x %v", raw, err)
	}
	if raw, found, err := ctx.State.LoadEntry("fishing", "state", "core"); err != nil || !found || len(raw) == 0 {
		t.Fatalf("fishing core entry was not persisted: found=%v err=%v", found, err)
	}
	reloaded, e := Open(ctx, d, noEconomy{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(reloaded.state.Fish) != 1 || reloaded.state.Collection[12].Min != 10 || reloaded.state.Exp != 10 {
		t.Fatal("committed fish did not survive reopen")
	}
}

func TestStaminaIsSignedDeltaAndCannotExceedDesignedAttack(t *testing.T) {
	s, ctx, _ := fishingFixture(t)
	for _, v := range []struct {
		path string
		b    []byte
	}{{"/FishingVoyageStart", row(1, 1, 2, 3)}, {"/FishingCasting", row(1, 2, 2, 1)}, {"/FishingBiteStart", row(1, 3)}} {
		if _, _, _, e := s.Handle(ctx, v.path, v.b); e != nil {
			t.Fatal(e)
		}
	}
	_, _, _, e := s.Handle(ctx, "/FishingBiteFishStaminaUpdate", row(1, 4, 2, uint64(0xffffffffffffffe0)))
	if e == nil {
		t.Fatal("attack exceeding designed critical strength was accepted")
	}
	_, body, _, e := s.Handle(ctx, "/FishingBiteFishStaminaUpdate", row(1, 5, 2, uint64(0xfffffffffffffffb)))
	if e != nil || num(body, 1) != 15 {
		t.Fatalf("signed damage did not reduce server HP: %x %v", body, e)
	}
	_, body, _, e = s.Handle(ctx, "/FishingBiteFishStaminaUpdate", row(1, 6, 2, 3))
	if e != nil || num(body, 1) != 18 {
		t.Fatalf("recovery did not add to current HP: %x %v", body, e)
	}
}

type countingEconomy struct{ calls int }

func (e *countingEconomy) Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error) {
	e.calls++
	return nil, nil
}

func TestShopChecksCapacityBeforeChargingAndUsesStackRemainders(t *testing.T) {
	s, ctx, design := fishingFixture(t)
	economy := &countingEconomy{}
	s.economy = economy
	design.Tables["FishingItemTable"] = [][]byte{row(4, 7, 6, 10)}
	design.Tables["FishingBuyItemTable"] = [][]byte{row(1, 1, 2, 1, 3, 1, 4, 7, 5, 58, 6, 2, 7, 1, 8, 1)}
	s.state.ItemSlots = 2
	s.state.Baits = []Item{{Index: 20, ID: 7, Count: 8}}
	s.state.Next = 21
	request := row(1, 1, 2, 1, 3, 1, 4, 5)
	_, body, _, err := s.Handle(ctx, "/FishingShopBuy", request)
	if err != nil {
		t.Fatal(err)
	}
	if economy.calls != 1 || len(s.state.Baits) != 2 || s.state.Baits[0].Count != 10 || s.state.Baits[1].Count != 3 {
		t.Fatalf("purchase did not fill remainder and create one bounded stack: %+v, charges=%d", s.state.Baits, economy.calls)
	}
	bundle, _, err := wire.Bytes(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	var added uint64
	if err = wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 1 {
			added += num(f.Value, 4)
		}
		return nil
	}); err != nil || added != 5 {
		t.Fatalf("client reward delta = %d, want 5; err=%v", added, err)
	}
	if _, _, _, err = s.Handle(ctx, "/FishingShopBuy", request); err != nil || economy.calls != 1 {
		t.Fatalf("replayed purchase charged twice: %v, charges=%d", err, economy.calls)
	}
	if _, _, _, err = s.Handle(ctx, "/FishingShopBuy", row(1, 2, 2, 1, 3, 1, 4, 8)); err == nil || economy.calls != 1 {
		t.Fatalf("over-capacity purchase charged: %v, charges=%d", err, economy.calls)
	}
	if len(s.state.Baits) != 2 || s.state.Baits[1].Count != 3 {
		t.Fatal("rejected purchase changed inventory")
	}
}

type unavailableRoom struct{ err error }

func (r unavailableRoom) StartRun(command.Context, string) (eventplay.NativeRun, error) {
	return eventplay.NativeRun{}, r.err
}
func (r unavailableRoom) Result(command.Context, eventplay.NativeRun) (eventplay.NativeResult, error) {
	return eventplay.NativeResult{}, r.err
}

func TestVoyageFallsBackToSoloOnlyWhenNoRoomExists(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		allowed bool
	}{{"no membership", eventplay.ErrNoRoom, true}, {"loading room", eventplay.ErrRoomNotFinished, false}, {"projection failure", errors.New("storage unavailable"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx, _ := fishingFixture(t)
			s.AttachRooms(unavailableRoom{err: tc.err})
			_, _, _, err := s.Handle(ctx, "/FishingVoyageStart", row(1, 1, 2, 3))
			if (err == nil) != tc.allowed {
				t.Fatalf("voyage allowed=%v, want %v: %v", err == nil, tc.allowed, err)
			}
			if s.state.MultiAP != 10 || s.state.Voyage != nil {
				t.Fatal("solo or rejected voyage consumed multiplayer admission")
			}
			if !tc.allowed && (s.state.Map != 0 || s.state.VoyageStarted != 0) {
				t.Fatal("rejected room admission started a solo voyage")
			}
		})
	}
}

// The native GameScheduleManager creates cost schedules from ChargeCostInfo.
// FishingMultiTicket is element55; FishingDefaultTable0 grants10 per UTC day.
func TestChargeInfoPreservesSpentFishingTicketsUntilDailyReset(t *testing.T) {
	service, ctx, design := fishingFixture(t)
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.state.APDay = now.Unix() / 86400
	service.state.MultiAP = 0
	if _, _, _, err := service.Handle(ctx, "/FishingItemInfo", row(1, 1)); err != nil {
		t.Fatal(err)
	}
	check := func(count uint64, midnight time.Time) {
		t.Helper()
		raw := service.ChargeInfo()
		records := 0
		if err := wire.Walk(raw, func(field wire.Field) error {
			if field.Number != 1 || field.Type != 2 {
				t.Fatal("unexpected charge field")
			}
			records++
			if num(field.Value, 1) != uint64(midnight.UnixMilli()) {
				t.Fatal("charge timestamp is not current UTC midnight in milliseconds")
			}
			item, found, err := wire.Bytes(field.Value, 2)
			if err != nil {
				return err
			}
			if !found || num(item, 3) != 55 || num(item, 4) != count {
				t.Fatalf("fishing charge item=%x want type55 count%d", item, count)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if records != 1 {
			t.Fatal("missing or duplicate fishing charge schedule")
		}
	}
	midnight := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	check(0, midnight)
	reopened, err := Open(ctx, design, noEconomy{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = service.now
	service = reopened
	check(0, midnight)
	now = now.Add(24 * time.Hour)
	check(10, midnight.Add(24*time.Hour))
	if _, _, _, err := service.Handle(ctx, "/FishingItemInfo", row(1, 2)); err != nil {
		t.Fatal(err)
	}
	if service.state.MultiAP != 10 || service.state.APDay != now.Unix()/86400 {
		t.Fatal("charge view disagrees with persisted daily allowance")
	}
}
