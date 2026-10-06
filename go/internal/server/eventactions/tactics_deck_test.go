package eventactions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"math"
	"os"
	"testing"
	"time"
)

func TestInstalledTacticsBlueParties23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	design, err := gamedata.LoadEventActionsDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{design: design}
	for _, stage := range design.Tables["TacticsBingoTable"] {
		party, err := s.tacticsBlueParty(stage.V(1))
		if err != nil {
			t.Fatalf("group %d stage %d: %v", stage.V(4), stage.V(5), err)
		}
		entries := make([]tacticsDeckEntry, 0, len(party))
		for id, costume := range party {
			order := uint64(len(entries) + 1)
			entries = append(entries, tacticsDeckEntry{1000000 + order, id, costume, order - 1, order})
		}
		decoded, err := decodeTacticsDeck(tacticsSave(1, entries...))
		if err != nil || !matchesTacticsParty(decoded, party) {
			t.Fatalf("group %d stage %d could not save its authored virtual party: %v", stage.V(4), stage.V(5), err)
		}
	}
	// GameData query: group 1 stage 1 has three Lv1 companions and these
	// CharTable defaults, distinct from ordinary account costume identities.
	party, err := s.tacticsBlueParty(2006301)
	if err != nil || len(party) != 3 || party[30011] != 300110 || party[30022] != 300220 || party[30141] != 301410 {
		t.Fatalf("installed fixed tutorial party mismatch: %v, %v", party, err)
	}
}

func tacticsSave(seq uint64, entries ...tacticsDeckEntry) []byte {
	b := req(seq)
	for _, e := range entries {
		var body []byte
		for i, value := range []uint64{e.index, e.character, e.costume, e.position, e.sequence} {
			body = wire.AppendVarint(body, i+1, value)
		}
		b = wire.AppendBytes(b, 2, body)
	}
	return b
}

func tacticsEnter(seq, stage, deck uint64) []byte {
	b := req(seq)
	for i, value := range map[int]uint64{4: deck, 5: 29, 8: 5, 9: stage} {
		b = wire.AppendVarint(b, i, value)
	}
	return b
}

func testTacticsEntries() []tacticsDeckEntry {
	return []tacticsDeckEntry{{900001, 30011, 30101, 0, 1}, {900002, 30022, 30201, 11, 2}}
}

func TestTacticsVirtualPartySavePersistsAndBindsEnteredStage(t *testing.T) {
	s, economy, store := setup(t)
	// A second valid authored party proves that matching any individual
	// available character is insufficient once a specific stage is entered.
	s.design.Tables["TacticsBingoTable"] = append(s.design.Tables["TacticsBingoTable"], row(map[int]uint64{1: 51, 2: 100, 4: 5, 5: 2}))
	s.design.Tables["CharGroupTable"] = append(s.design.Tables["CharGroupTable"], row(map[int]uint64{1: 30033, 2: 51, 3: 1, 5: 1}))
	s.design.Tables["CharTable"] = append(s.design.Tables["CharTable"], row(map[int]uint64{12: 30033, 5: 30301}))
	first := tacticsSave(1, testTacticsEntries()...)
	if _, _, _, err := s.Handle("/TacticsBingoDeckSave", first); err != nil {
		t.Fatalf("virtual system party rejected without player ownership: %v", err)
	}
	next, err := Open(store, s.design, s.registry, economy)
	if err != nil {
		t.Fatal(err)
	}
	next.now = s.now
	next.BeginSession("test")
	if !bytes.Equal(next.state.Deck, first) {
		t.Fatal("special deck was not persisted")
	}
	if _, _, _, err := next.Handle("/TacticsBingoDeckSave", first); err != nil {
		t.Fatalf("exact save replay rejected: %v", err)
	}
	if _, err := next.EnterBattle(tacticsEnter(2, 2, 100), "enter2"); err != nil {
		t.Fatalf("previous stage deck blocked normal stage entry: %v", err)
	}
	if len(next.state.Deck) != 0 {
		t.Fatal("previous scene virtual deck survived new entry")
	}
	wrongStage := tacticsSave(3, testTacticsEntries()...)
	if _, _, _, err := next.Handle("/TacticsBingoDeckSave", wrongStage); err == nil {
		t.Fatal("another stage's otherwise legal party accepted")
	}
	second := tacticsSave(4, tacticsDeckEntry{900003, 30033, 30301, 4, 1})
	if _, _, _, err := next.Handle("/TacticsBingoDeckSave", second); err != nil {
		t.Fatalf("bound stage's system party rejected: %v", err)
	}
	end := wire.AppendVarint(req(5), 2, 1)
	if _, err := next.CompleteBattle(end, "end5"); err != nil {
		t.Fatal(err)
	}
	if economy.calls != 0 || len(next.state.Tactics[2]) != 1 || next.state.Tactics[2][0] != 2 {
		t.Fatal("special deck changed ordinary economy or stage settlement")
	}
}

func TestTacticsDeckRejectsForgedOrIncompleteSystemParties(t *testing.T) {
	valid := testTacticsEntries()
	tests := []struct {
		name   string
		change func([]tacticsDeckEntry) []tacticsDeckEntry
	}{
		{"empty", func(e []tacticsDeckEntry) []tacticsDeckEntry { return nil }},
		{"missing companion", func(e []tacticsDeckEntry) []tacticsDeckEntry { return e[:1] }},
		{"invented character", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].character = 55555; return e }},
		{"wrong costume", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].costume++; return e }},
		{"duplicate character", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[1].character = e[0].character; return e }},
		{"duplicate virtual index", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[1].index = e[0].index; return e }},
		{"negative virtual index", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].index = math.MaxUint64; return e }},
		{"overlapping positions", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[1].position = e[0].position; return e }},
		{"unassigned position", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].position = math.MaxUint64; return e }},
		{"outside standard grid", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].position = 12; return e }},
		{"duplicate order", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[1].sequence = e[0].sequence; return e }},
		{"missing order", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[0].sequence = 0; return e }},
		{"order gap", func(e []tacticsDeckEntry) []tacticsDeckEntry { e[1].sequence = 3; return e }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := setup(t)
			accepted := tacticsSave(1, valid...)
			if _, _, _, err := s.Handle("/TacticsBingoDeckSave", accepted); err != nil {
				t.Fatal(err)
			}
			bad := tacticsSave(2, tt.change(append([]tacticsDeckEntry(nil), valid...))...)
			if _, _, _, err := s.Handle("/TacticsBingoDeckSave", bad); err == nil {
				t.Fatal("forged deck accepted")
			}
			if !bytes.Equal(s.state.Deck, accepted) {
				t.Fatal("failed validation changed saved special deck")
			}
		})
	}
}

func TestTacticsDeckRejectsInactiveEventAndMixedAuthoredGroups(t *testing.T) {
	s, _, _ := setup(t)
	s.design.Tables["TacticsBingoTable"] = append(s.design.Tables["TacticsBingoTable"], row(map[int]uint64{1: 51, 2: 100, 4: 5, 5: 2}))
	s.design.Tables["CharGroupTable"] = append(s.design.Tables["CharGroupTable"], row(map[int]uint64{1: 30033, 2: 51, 3: 1, 5: 1}), row(map[int]uint64{1: 30044, 2: 51, 3: 2, 5: 1}))
	s.design.Tables["CharTable"] = append(s.design.Tables["CharTable"], row(map[int]uint64{12: 30033, 5: 30301}), row(map[int]uint64{12: 30044, 5: 30401}))
	mixed := testTacticsEntries()
	mixed[1].character, mixed[1].costume = 30033, 30301
	if _, _, _, err := s.Handle("/TacticsBingoDeckSave", tacticsSave(1, mixed...)); err == nil {
		t.Fatal("mixed characters from different authored parties accepted")
	}
	s.now = func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	if _, _, _, err := s.Handle("/TacticsBingoDeckSave", tacticsSave(2, testTacticsEntries()...)); err == nil {
		t.Fatal("expired event system party accepted")
	}
	if len(s.state.Deck) != 0 {
		t.Fatal("invalid saves changed deck")
	}
}
