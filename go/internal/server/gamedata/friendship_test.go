package gamedata

import (
	"database/sql"
	"reflect"
	"testing"

	"bd2server/internal/server/wire"
)

func TestFriendshipDesignLoadsProtocolDefaultsAndIndependentIdentities(t *testing.T) {
	db := friendshipTestDB(t)
	d, err := loadFriendshipDesign(db)
	if err != nil {
		t.Fatal(err)
	}
	if d.Default.CorrectSelectDialogIndex != 0 || d.Default.IncorrectEXP != 80 || d.Default.MaxLevels != [3]uint64{2, 3, 4} || !reflect.DeepEqual(d.Default.CounselingRewards, []Reward{{Type: 3, Count: 100}}) {
		t.Fatalf("defaults=%+v", d.Default)
	}
	if d.Costumes[901] != 902 {
		t.Fatalf("costume mapping=%v", d.Costumes)
	}
	gift := d.Gifts[[2]uint64{8, 920}]
	if gift.Experience(901) != 40 || gift.Experience(902) != 20 {
		t.Fatalf("gift favorite uses wrong identity: %+v", gift)
	}
	if got := d.Gifts[[2]uint64{8, 921}].Experience(901); got != 150 {
		t.Fatalf("universal experience=%d", got)
	}
	if session := d.Sessions[FriendshipKey{901, 7}]; session.DialogGroupID != 931 || session.ChoiceCount != 2 {
		t.Fatalf("session=%+v", session)
	}
	if rewards := d.Levels[FriendshipKey{901, 3}].Rewards; !reflect.DeepEqual(rewards, []Reward{{Type: 47, ID: 950, Count: 1}}) {
		t.Fatalf("level rewards=%+v", rewards)
	}
}

func TestFriendshipDesignRejectsPartialRewardsAndBrokenReferences(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{"partial reward", "UPDATE FriendshipLevelTable SET ProtoBuf=? WHERE id=3", []any{friendshipTestProto(map[int][]uint64{1: {901}, 2: {3}, 3: {60}, 6: {47}})}},
		{"missing curve level", "DELETE FROM FriendshipLevelTable WHERE id=2", nil},
		{"missing choices", "DELETE FROM SelectDialogTable", nil},
		{"unknown favorite", "UPDATE FriendshipGiftTable SET ProtoBuf=? WHERE id=1", []any{friendshipTestProto(map[int][]uint64{1: {20}, 2: {40}, 3: {999}, 5: {1}, 6: {920}, 7: {8}})}},
		{"mismatched session identity", "UPDATE CounselingSessionTable SET ProtoBuf=?", []any{friendshipTestProto(map[int][]uint64{1: {901}, 2: {8}, 4: {931}})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := friendshipTestDB(t)
			if _, err := db.Exec(tc.query, tc.args...); err != nil {
				t.Fatal(err)
			}
			if _, err := loadFriendshipDesign(db); err == nil {
				t.Fatal("invalid design accepted")
			}
		})
	}
}

func friendshipTestProto(fields map[int][]uint64) []byte {
	var proto []byte
	for field, values := range fields {
		for _, value := range values {
			proto = wire.AppendVarint(proto, field, value)
		}
	}
	return proto
}

func friendshipTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, query := range []string{
		"CREATE TABLE FriendshipDefaultTable(id INTEGER,ProtoBuf BLOB)",
		"CREATE TABLE FriendshipCostumeTable(id INTEGER,ProtoBuf BLOB)",
		"CREATE TABLE FriendshipGiftTable(id INTEGER,ProtoBuf BLOB)",
		"CREATE TABLE FriendshipLevelTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)",
		"CREATE TABLE CounselingSessionTable(groupId INTEGER,id INTEGER,ProtoBuf BLOB)",
		"CREATE TABLE VisualNovelDialogTable(groupId INTEGER,type INTEGER,selectDialogId INTEGER)",
		"CREATE TABLE SelectDialogTable(id INTEGER,ProtoBuf BLOB)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	insert("INSERT INTO FriendshipDefaultTable VALUES(0,?)", friendshipTestProto(map[int][]uint64{1: {100}, 3: {100}, 5: {3}, 7: {2}, 8: {3}, 9: {4}, 12: {80}, 13: {3}, 14: {1}, 18: {5}}))
	insert("INSERT INTO FriendshipCostumeTable VALUES(901,?)", friendshipTestProto(map[int][]uint64{1: {902}, 2: {901}}))
	insert("INSERT INTO FriendshipGiftTable VALUES(1,?)", friendshipTestProto(map[int][]uint64{1: {20}, 2: {40}, 3: {901}, 5: {1}, 6: {920}, 7: {8}}))
	insert("INSERT INTO FriendshipGiftTable VALUES(2,?)", friendshipTestProto(map[int][]uint64{2: {150}, 4: {1}, 5: {2}, 6: {921}, 7: {8}}))
	for level := uint64(1); level <= 4; level++ {
		fields := map[int][]uint64{1: {901}, 2: {level}}
		if level < 4 {
			fields[3] = []uint64{60}
		}
		if level == 3 {
			fields[4] = []uint64{1}
			fields[5] = []uint64{950}
			fields[6] = []uint64{47}
		}
		insert("INSERT INTO FriendshipLevelTable VALUES(901,?,?)", level, friendshipTestProto(fields))
	}
	insert("INSERT INTO CounselingSessionTable VALUES(901,7,?)", friendshipTestProto(map[int][]uint64{1: {901}, 2: {7}, 4: {931}}))
	insert("INSERT INTO VisualNovelDialogTable VALUES(931,4,940)")
	insert("INSERT INTO SelectDialogTable VALUES(940,?)", friendshipTestProto(map[int][]uint64{1: {960, 961}, 3: {940}}))
	return db
}
