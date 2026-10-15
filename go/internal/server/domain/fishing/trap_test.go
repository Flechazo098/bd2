package fishing

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestFishSaleRoundsDecimalPremiumLikeClient(t *testing.T) {
	s, _, d := fishingFixture(t)
	// FishingInfo.GetTotalPriceWithPremium converts float to decimal before
	// AwayFromZero rounding; binary0.95*30 would incorrectly produce28.
	d.Tables["FishingPricePremiumDefaultTable"] = [][]byte{wire.AppendBytes(row(2, 1000, 3, 0), 4, binary.LittleEndian.AppendUint32(nil, math.Float32bits(0.95)))}
	price, err := s.fishPrice(Item{ID: 12, Size: 15}, 30)
	if err != nil || price != 29 {
		t.Fatalf("sale credited %d, expected decimal28.5 rounded to29: %v", price, err)
	}
}

// LocalText 38040–38041, 38037 and 38132 define codex-only successes,
// optional sale and elapsed placement time; the packet callback grants only at claim.
func TestTrapOfflineCatchPreviewClaimAndReopen(t *testing.T) {
	s, ctx, d := fishingFixture(t)
	now := time.Unix(2000000000, 0)
	s.now = func() time.Time { return now }
	s.economy = &countingEconomy{}
	ctx.Emit = func(command.Event) error { return nil }
	d.Tables["FishingDefaultTable"][0] = append(d.Tables["FishingDefaultTable"][0], row(17, 999, 18, 13, 19, 86400, 20, 2)...)
	d.Tables["FishingBoatTable"] = append(d.Tables["FishingBoatTable"], row(8, 2, 5, 2, 6, 3600, 7, 1))
	d.Tables["FishingGradePoolTable"] = append(d.Tables["FishingGradePoolTable"], row(3, 13, 2, 1, 4, 1, 5, 2))
	d.Tables["FishingFishPoolTable"] = append(d.Tables["FishingFishPoolTable"], row(1, 12, 2, 999, 3, 1, 4, 1))
	d.Tables["FishingCharTable"] = append(d.Tables["FishingCharTable"], row(1, 2, 2, 100), row(1, 3, 2, 100))
	seq := uint64(0)
	call := func(path string, fields []byte) []byte {
		t.Helper()
		seq++
		request := append(row(1, seq), fields...)
		_, body, handled, err := s.Handle(ctx, path, request)
		if err != nil || !handled {
			t.Fatalf("%s: %v", path, err)
		}
		return body
	}
	call("/FishingVoyageStart", row(2, 3))
	call("/FishingCasting", row(2, 1))
	call("/FishingBiteStart", nil)
	call("/FishingBiteFishStaminaUpdate", row(2, ^uint64(9)))
	call("/FishingBiteFishStaminaUpdate", row(2, ^uint64(9)))
	call("/FishingBiteEnd", nil)
	upgrade := call("/FishingBoatUpgrade", nil)
	if int64(num(upgrade, 2)) != now.Unix() {
		t.Fatal("boat upgrade did not place trap")
	}
	now = now.Add(30 * time.Hour)
	preview := call("/FishingTrapInfo", nil)
	var catches int
	if err := wire.Walk(preview, func(f wire.Field) error {
		if f.Number == 2 {
			catches++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if catches != 24 || num(preview, 3) != 240 || num(preview, 4) != 3 || num(preview, 5) != 50 {
		t.Fatalf("offline cap/experience preview is wrong: catches=%d response=%x", catches, preview)
	}
	if len(s.state.Fish) != 1 || s.state.Level != 1 || s.state.Exp != 10 {
		t.Fatal("query granted unclaimed fish or experience")
	}
	var err error
	s, err = Open(ctx, d, s.economy, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.sample = func(uint64) (uint64, error) { t.Fatal("reopening rerolled settled trap attempts"); return 0, nil }
	if next := call("/FishingTrapInfo", nil); !bytes.Equal(next, preview) {
		t.Fatal("pending trap fish changed on reopen")
	}
	seq++
	request := row(1, seq)
	_, claimed, _, err := s.Handle(ctx, "/FishingTrapCumulativeReward", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.state.Fish) != 25 || s.state.Level != 3 || s.state.Exp != 50 {
		t.Fatal("claim did not commit fish and level progression")
	}
	indices := map[uint64]bool{}
	for _, fish := range s.state.Fish {
		if fish.Index == 0 || indices[fish.Index] {
			t.Fatal("claimed fish reused an inventory index")
		}
		indices[fish.Index] = true
	}
	s, err = Open(ctx, d, s.economy, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	_, repeated, _, err := s.Handle(ctx, "/FishingTrapCumulativeReward", request)
	if err != nil || !bytes.Equal(repeated, claimed) || len(s.state.Fish) != 25 || s.state.Exp != 50 {
		t.Fatal("claim retry after reopen duplicated or lost rewards")
	}
	if num(call("/FishingTrapInfo", nil), 3) != 0 {
		t.Fatal("claimed trap retained unclaimed experience")
	}
}

func TestTrapUnknownSpeciesFailInsteadOfRenormalizingPool(t *testing.T) {
	s, ctx, d := fishingFixture(t)
	now := time.Unix(2000000000, 0)
	s.now = func() time.Time { return now }
	d.Tables["FishingDefaultTable"][0] = append(d.Tables["FishingDefaultTable"][0], row(17, 999, 18, 13, 19, 86400, 20, 2)...)
	d.Tables["FishingBoatTable"] = append(d.Tables["FishingBoatTable"], row(8, 2, 5, 2, 6, 3600, 7, 1))
	s.state.Boat = 2
	d.Tables["FishingGradePoolTable"] = append(d.Tables["FishingGradePoolTable"], row(3, 13, 2, 1, 4, 1, 5, 2))
	d.Tables["FishingFishPoolTable"] = append(d.Tables["FishingFishPoolTable"], row(1, 99, 1, 12, 2, 999, 3, 1, 4, 1, 4, 1))
	s.state.Collection[12] = Collection{Min: 10, Max: 10, Created: now.Unix() - 1}
	_, _, _, err := s.Handle(ctx, "/FishingTrapInfo", row(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	_, body, _, err := s.Handle(ctx, "/FishingTrapInfo", row(1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if fish, _, _ := wire.Bytes(body, 2); len(fish) != 0 || num(body, 3) != 0 {
		t.Fatal("unregistered sampled species was replaced by a guaranteed registered catch")
	}
	trap, err := loadTrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if trap.Cursor != now.Unix() || len(s.state.Fish) != 0 || s.state.Exp != 0 {
		t.Fatal("failed trap attempts did not consume elapsed slots without rewards")
	}
}
