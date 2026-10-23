package life_test

import (
	"errors"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/life"
)

// The 2.37.13 client regenerates the existing slot locally without a request or
// an ObjectId change (LifeResourceGenerateObject.OnRegenTimerComplete).
func TestRegenerationPreservesResourceVariantAndHarvestRewards(t *testing.T) {
	g := scenario(t)
	tables := map[string][][]byte{}
	for name, rows := range g.d.Tables {
		for _, row := range rows {
			tables[name] = append(tables[name], row)
		}
	}
	for i, row := range tables["LifeBuildingObjectTable"] {
		if scalar(t, row, 4) == 301 {
			tables["LifeBuildingObjectTable"][i] = proto(4, 301, 2, 1, 3, 60, 5, 1, 7, 1)
		}
	}
	tables["LifeGatheringObjectTable"] = append(tables["LifeGatheringObjectTable"], proto(4, 301, 6, 2, 10, 1, 1, 3, 2, 1, 3, 100, 8, 7, 9, 10))
	var err error
	g.d, err = gamedata.NewLifeCatalog(tables)
	if err != nil {
		t.Fatal(err)
	}
	selected := false
	g.options.Sample = func(limit uint64) (uint64, error) {
		if !selected {
			selected = true
			return 0, nil
		}
		return limit - 1, nil
	}
	g.reopen()
	g.call("/LifeWorldChoice", proto(2, 91))
	resource := func() []byte {
		t.Helper()
		place := nested(t, g.call("/LifeInfo", nil), 1)
		parent := nested(t, place, 2)
		return nested(t, parent, 10)
	}
	if inner := resource(); scalar(t, inner, 6) != 1 || scalar(t, inner, 7) != 1 {
		t.Fatalf("initial resource must be variant 1 at stage 1: %x", inner)
	}
	parent := child(proto(6, 301, 1, 2, 2, 3), 10, proto(6, 1, 4, 0, 7, 1))
	gather := child(nil, 2, child(proto(1, 1), 2, parent))
	checkBalance := func(want uint64) {
		t.Helper()
		got, err := g.s.Balance(g.ctx, 66, 501)
		if err != nil || got != want {
			t.Fatalf("harvest inventory: got %d, want %d (%v)", got, want, err)
		}
	}
	g.call("/LifeWorldObjectGathering", gather)
	checkBalance(2)
	g.now = g.now.Add(59 * time.Second)
	if _, err := g.request("/LifeWorldObjectGathering", gather); !errors.Is(err, life.ErrInvalid) {
		t.Fatalf("harvest before the 60-second regeneration interval: %v", err)
	}
	checkBalance(2)
	g.reopen()
	g.now = g.now.Add(time.Second)
	if inner := resource(); scalar(t, inner, 6) != 1 || scalar(t, inner, 7) != 1 {
		t.Fatalf("regeneration must preserve variant 1 at stage 1: %x", inner)
	}
	g.call("/LifeWorldObjectGathering", gather)
	checkBalance(4)
	if _, err := g.request("/LifeWorldObjectGathering", gather); !errors.Is(err, life.ErrInvalid) {
		t.Fatalf("harvested regenerated resource twice: %v", err)
	}
	checkBalance(4)
}
