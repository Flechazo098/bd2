package gamedata

import (
	"bd2server/internal/server/wire"
	"database/sql"
	"testing"
)

func TestFoodRecoveryUsesFavoriteReplacementAndFloatMidpointEven(t *testing.T) {
	food := Food{Type: 1, Point: 10, FavoritePoint: 15, FavoriteUniqueCharIDs: []uint64{35}}
	got, err := food.Recovery(350, 100, 2)
	if err != nil || got != 30 {
		t.Fatalf("favorite=%d err=%v", got, err)
	}
	got, err = food.Recovery(360, 100, 2)
	if err != nil || got != 20 {
		t.Fatalf("ordinary=%d err=%v", got, err)
	}
	for _, test := range []struct{ maximum, want uint64 }{{5, 2}, {7, 4}} {
		got, err = (Food{Point: 50, RecoveryType: 1}).Recovery(350, test.maximum, 1)
		if err != nil || got != test.want {
			t.Fatalf("percentage max=%d got=%d err=%v", test.maximum, got, err)
		}
	}
	for _, food := range []Food{{Type: 2, Point: 100}, {FoodBuffID: 101, Point: 100}, {RecoveryType: 2, Point: 100}, {}} {
		if _, err := food.Recovery(350, 100, 1); err == nil {
			t.Fatalf("accepted invalid food %+v", food)
		}
	}
}

func TestFoodLoaderReadsFavoriteIDsAndProtoDefaults(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = db.Exec("CREATE TABLE FoodTable(id INTEGER PRIMARY KEY,ProtoBuf BLOB)"); err != nil {
		t.Fatal(err)
	}
	raw := wire.AppendVarint(nil, 7, 101)
	raw = wire.AppendVarint(raw, 13, 10)
	if _, err = db.Exec("INSERT INTO FoodTable VALUES(101,?)", raw); err != nil {
		t.Fatal(err)
	}
	raw = wire.AppendVarint(nil, 7, 201)
	for field, value := range map[int]uint64{1: 15, 2: 35, 4: 1, 13: 10} {
		raw = wire.AppendVarint(raw, field, value)
	}
	if _, err = db.Exec("INSERT INTO FoodTable VALUES(201,?)", raw); err != nil {
		t.Fatal(err)
	}
	design, err := loadFoodDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	if design.Foods[101].Type != 0 || design.Foods[201].FavoritePoint != 15 || design.Foods[201].FavoriteUniqueCharIDs[0] != 35 {
		t.Fatalf("design=%+v", design)
	}
}
