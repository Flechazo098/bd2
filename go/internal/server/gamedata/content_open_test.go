package gamedata

import (
	"database/sql"
	"testing"
)

func contentOpeningTestDB(t *testing.T) *sql.DB {
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
	for _, q := range []string{"CREATE TABLE ContentOpenTable(groupId INTEGER,id INTEGER,ticketId INTEGER,ProtoBuf BLOB)", "CREATE TABLE ContentTicketTable(id INTEGER,ProtoBuf BLOB)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint64{901, 902} {
		if _, err := db.Exec("INSERT INTO ContentTicketTable VALUES(?,?)", id, friendshipTestProto(map[int][]uint64{4: {id}, 8: {1}})); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uint64{1, 2} {
		if _, err := db.Exec("INSERT INTO ContentOpenTable VALUES(23,?,?,?)", id, 900+id, friendshipTestProto(map[int][]uint64{1: {23}, 2: {id}, 4: {100 + id}, 5: {10 + id}, 6: {900 + id}})); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestContentOpeningUsesVersionTicketsAndRequirements(t *testing.T) {
	d, err := loadContentOpeningDesign(contentOpeningTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if d.Prerequisite != (ContentOpenRule{TicketID: 901, SquadLevel: 11, TutorialID: 101}) || d.Completion != (ContentOpenRule{TicketID: 902, SquadLevel: 12, TutorialID: 102}) {
		t.Fatalf("design=%+v", d)
	}
}

func TestContentOpeningRejectsIncompleteAndInvalidCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{"missing completion", "DELETE FROM ContentOpenTable WHERE id=2", nil},
		{"missing ticket", "DELETE FROM ContentTicketTable WHERE id=902", nil},
		{"temporary ticket", "UPDATE ContentTicketTable SET ProtoBuf=? WHERE id=902", []any{friendshipTestProto(map[int][]uint64{4: {902}, 8: {2}})}},
		{"wrong ticket identity", "UPDATE ContentTicketTable SET ProtoBuf=? WHERE id=901", []any{friendshipTestProto(map[int][]uint64{4: {999}, 8: {1}})}},
		{"wrong opening identity", "UPDATE ContentOpenTable SET ProtoBuf=? WHERE id=2", []any{friendshipTestProto(map[int][]uint64{1: {23}, 2: {1}, 6: {902}})}},
		{"invalid unrelated row", "INSERT INTO ContentOpenTable VALUES(1,1,999,?)", []any{friendshipTestProto(map[int][]uint64{1: {1}, 2: {1}, 6: {999}})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := contentOpeningTestDB(t)
			if _, err := db.Exec(tc.query, tc.args...); err != nil {
				t.Fatal(err)
			}
			if _, err := loadContentOpeningDesign(db); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}
