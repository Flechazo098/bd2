package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"encoding/binary"
	"math"
	"testing"
)

func TestEquipmentHealthStatsUseRankCurveSubDefaultAndFractionPercent(t *testing.T) {
	d := &EquipmentStatDesign{Options: map[[2]uint64]EquipmentStatRule{
		{10, 1}: {Default: 7.9, Growth: 2, Levels: []float64{0, 1}, Ranks: [3][]float64{{0.5}, {1}, {1.5}}},
		{20, 2}: {Default: 0.01359, Growth: 0.01, Levels: []float64{0, 1}, Ranks: [3][]float64{{0.5}, {1}, {1.5}}},
	}}
	flat, err := d.HealthContribution(EquipmentOption{GroupID: 10, ID: 1, Level: 1, Rank: [3]int{1, 1, 1}}, false)
	if err != nil || flat.Flat != 15 {
		t.Fatalf("flat=%+v err=%v", flat, err)
	}
	percent, err := d.HealthContribution(EquipmentOption{GroupID: 20, ID: 2, Level: 1, Rank: [3]int{1, 1, 1}}, false)
	if err != nil || percent.Percent != 0.0535 {
		t.Fatalf("percent=%+v err=%v", percent, err)
	}
	sub, err := d.HealthContribution(EquipmentOption{GroupID: 20, ID: 2, Level: 99, Rank: [3]int{99, 99, 99}}, true)
	if err != nil || sub.Percent != 0.0135 {
		t.Fatalf("sub=%+v err=%v", sub, err)
	}
	if got := AggregateStats(BaseStats{Health: 100}, []StatContribution{flat, percent, sub}).Health; got != 122 {
		t.Fatalf("aggregated health=%v", got)
	}
	if _, err = d.HealthContribution(EquipmentOption{GroupID: 20, ID: 2, Level: 2}, false); err == nil {
		t.Fatal("accepted nonexistent level")
	}
	if _, err = d.HealthContribution(EquipmentOption{GroupID: 20, ID: 2, Rank: [3]int{2, 0, 0}}, false); err == nil {
		t.Fatal("accepted nonexistent rank")
	}
}

func TestEquipmentStatLoaderReadsCompositeKeysAndFloatCurves(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE EquipmentOptionTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	raw := wire.AppendDouble(nil, 1, 7.9)
	raw = wire.AppendDouble(raw, 4, 2)
	raw = wire.AppendVarint(raw, 3, 10)
	raw = wire.AppendVarint(raw, 5, 1)
	levels := binary.LittleEndian.AppendUint32(nil, math.Float32bits(0))
	levels = binary.LittleEndian.AppendUint32(levels, math.Float32bits(1))
	raw = wire.AppendBytes(raw, 6, levels)
	if _, err = db.Exec("INSERT INTO EquipmentOptionTable VALUES(10,1,?)", raw); err != nil {
		t.Fatal(err)
	}
	d, err := loadEquipmentStatDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.HealthContribution(EquipmentOption{GroupID: 10, ID: 1, Level: 1}, false)
	if err != nil || got.Flat != 9 {
		t.Fatalf("health=%+v err=%v", got, err)
	}
}

func TestEquipmentOptionContributionTruncatesRatherThanRounds(t *testing.T) {
	flat, err := optionContribution(1, 7.9)
	if err != nil || flat.Flat != 7 {
		t.Fatalf("flat=%+v err=%v", flat, err)
	}
	percent, err := optionContribution(2, 0.01359)
	if err != nil || percent.Percent != 0.0135 {
		t.Fatalf("fraction percent=%+v err=%v", percent, err)
	}
}
