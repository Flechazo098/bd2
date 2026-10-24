package life_test

import (
	"testing"
	"time"
)

// LifeObjectGrowthContext reads LifeWorldObjectDBInfo.start_time as the start
// of the current stage: it adds the current and later stage durations to find
// when the crop is ready. A crop in its second stage must report when that
// stage began, not when it was planted, or the client harvests it early.
func TestCropReportsTheStartOfItsCurrentStage(t *testing.T) {
	g := scenario(t)
	g.d.Tables["LifeObjectGroupTable"][[3]uint64{305}] = proto(5, 305, 12, 3, 7, 1)
	g.d.Tables["LifeBuildingObjectTable"][[3]uint64{305}] = proto(4, 305, 2, 3)
	g.d.Tables["LifeCropSeedTable"] = map[[3]uint64][]byte{{7}: proto(2, 7, 4, 1, 6, 64, 7, 600)}
	g.d.Gathering[305] = map[uint64][][]byte{7: {
		proto(4, 305, 6, 7, 10, 1, 11, 300),
		proto(4, 305, 6, 7, 10, 2, 11, 300),
		proto(4, 305, 6, 7, 10, 3, 11, 0),
	}}
	g.call("/LifeWorldChoice", proto(2, 91))
	g.fund("seed", 1, 0)
	g.call("/LifeWorldObjectPlaceSave", child(nil, 2, placement(305, 5, 6)))
	planted := g.now
	plot := child(proto(1, 1), 2, child(proto(6, 305, 1, 5, 2, 6), 10, proto(6, 7)))
	g.call("/LifeSeeding", child(nil, 2, child(nil, 2, plot)))

	g.now = planted.Add(591 * time.Second)
	var crop []byte
	for _, o := range repeated(t, nested(t, g.call("/LifeInfo", nil), 1), 2) {
		if scalar(t, o, 6) == 305 {
			crop = nested(t, o, 10)
		}
	}
	if crop == nil {
		t.Fatal("LifeInfo lists no farmland")
	}
	if status, start := scalar(t, crop, 7), scalar(t, crop, 8); status != 2 || start != uint64(planted.Add(300*time.Second).UnixMilli()) {
		t.Fatalf("crop status %d starts at %d, want stage 2 starting at %d", status, start, planted.Add(300*time.Second).UnixMilli())
	}
}
