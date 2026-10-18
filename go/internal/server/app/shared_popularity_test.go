package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
)

func popularityFixture(t *testing.T) (*sharedRuntime, string) {
	t.Helper()
	directory := t.TempDir()
	store, e := shared.Open(directory)
	if e != nil {
		t.Fatal(e)
	}
	s := &sharedRuntime{store: store, statistics: &gamedata.CollectionStatisticsDesign{Characters: map[uint64]gamedata.StatisticsCharacter{501: {UniqueID: 5, Grade: 5}}, CostumeGrade: map[uint64]uint64{101: 5, 102: 5}, Equipment: map[uint64]gamedata.StatisticsEquipment{301: {UniqueID: 900, Slot: 0}, 302: {UniqueID: 901, Slot: 0}}, CastleStages: map[uint64]map[uint64]uint64{16: {900: 1, 901: 2}}}}
	t.Cleanup(func() {
		if e := s.store.Close(); e != nil {
			t.Error(e)
		}
	})
	return s, directory
}
func popularityEvent(t *testing.T, s *sharedRuntime, source, id, topic string, value any) outbox.Pending {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	var result outbox.Pending
	e = s.store.Update(context.Background(), func(tx *shared.Tx) error {
		q, e := tx.Outbox()
		if e != nil {
			return e
		}
		if e = outbox.Publish(q, source, command.Event{ID: id, Topic: topic, Payload: raw}); e != nil {
			return e
		}
		rows, e := outbox.Read(q, 256)
		if e != nil {
			return e
		}
		for _, r := range rows {
			if r.Envelope.Source == source && r.Envelope.Event.ID == id {
				result = r
				return nil
			}
		}
		return fmt.Errorf("missing committed publication")
	})
	if e != nil {
		t.Fatal(e)
	}
	return result
}
func popularityRecord(owner uint64, costume, equip uint64) deck.RecommendRecord {
	r := deck.RecommendRecord{Context: deck.RecommendContext{BattleMode: 16, BattleIndex: 900}, Identity: deck.RecommendIdentity{OwnerIndex: owner, UserID: fmt.Sprint(owner)}, Deck: []deck.DeckEntry{{CharacterInvenIndex: 11, Slot: 1}}, Characters: []roster.Character{{InvenIndex: 11, ID: 501, Level: 1, UseCostume: 21}}, Costumes: []deck.RecommendCostume{{Costume: roster.Costume{InvenIndex: 21, ID: costume, UseChar: 11}}}}
	if equip != 0 {
		r.Equipment = []assets.Equipment{{InvenIndex: 31, ID: equip, UseChar: 11}}
	}
	return r
}
func popularityRows(t *testing.T, s *sharedRuntime, path string, now time.Time, values ...uint64) [][]byte {
	t.Helper()
	raw := wire.AppendVarint(nil, 1, 1)
	for i, v := range values {
		raw = wire.AppendVarint(raw, i+2, v)
	}
	_, body, e := s.popularInfo(command.Context{}, path, raw, now)
	if e != nil {
		t.Fatal(e)
	}
	var rows [][]byte
	if e = wire.Walk(body, func(f wire.Field) error {
		if f.Number == 1 {
			rows = append(rows, f.Value)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return rows
}
func popularityScalar(t *testing.T, b []byte, field int) uint64 {
	t.Helper()
	v, _, e := wire.Varint(b, field)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestCastlePopularityCountsFirstActualBattlePerDayAndRecovers(t *testing.T) {
	s, dir := popularityFixture(t)
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	p := deck.PopularityPublication{CompletedAt: uint64(now.UnixMilli()), Record: popularityRecord(1, 101, 0)}
	event := popularityEvent(t, s, "alice", "attempt-1", deck.PopularityTopic, p)
	if e := s.consumePopularity(t.Context(), event); e != nil {
		t.Fatal(e)
	}
	if e := s.consumePopularity(t.Context(), event); e != nil {
		t.Fatal(e)
	}
	p.CompletedAt++
	if e := s.consumePopularity(t.Context(), popularityEvent(t, s, "alice", "attempt-2", deck.PopularityTopic, p)); e != nil {
		t.Fatal(e)
	}
	p.Record = popularityRecord(2, 102, 0)
	if e := s.consumePopularity(t.Context(), popularityEvent(t, s, "bob", "attempt-1", deck.PopularityTopic, p)); e != nil {
		t.Fatal(e)
	}
	if e := s.store.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	s.store, e = shared.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	rows := popularityRows(t, s, "/PopularCostumeInfo", now, 2, 0, 0, 0, 1)
	if len(rows) != 2 {
		t.Fatalf("two players' actual active costumes: %d", len(rows))
	}
	for _, row := range rows {
		if got := popularityScalar(t, row, 2); got != 1 {
			t.Fatalf("repeat battle inflated daily usage to %d", got)
		}
	}
	if rows := popularityRows(t, s, "/PopularCostumeInfo", now, 4, 0, 0, 0, 1); len(rows) != 0 {
		t.Fatal("greed usage escaped into rage content")
	}
	p.Record = popularityRecord(1, 101, 0)
	p.CompletedAt = uint64(now.Add(24 * time.Hour).UnixMilli())
	if e := s.consumePopularity(t.Context(), popularityEvent(t, s, "alice", "next-day", deck.PopularityTopic, p)); e != nil {
		t.Fatal(e)
	}
	rows = popularityRows(t, s, "/PopularCostumeInfo", now.Add(24*time.Hour), 2, 0, 0, 0, 1)
	if got := popularityScalar(t, rows[0], 2); got != 2 {
		t.Fatalf("next reset day did not count: %d", got)
	}
}

func TestEquipmentPopularityReplacesOwnerSnapshotAndChoosesMostUsedSlot(t *testing.T) {
	s, _ := popularityFixture(t)
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	for i, source := range []string{"alice", "bob", "carol"} {
		r := popularityRecord(uint64(i+1), 101, 301)
		r.Revision = 1
		event := popularityEvent(t, s, source, "saved", deck.RecommendationTopic, deck.RecommendationPublication{Revision: 1, Public: true, Record: &r})
		if e := s.consumeRecommendation(t.Context(), event); e != nil {
			t.Fatal(e)
		}
		if e := s.consumeRecommendation(t.Context(), event); e != nil {
			t.Fatal(e)
		}
	}
	r := popularityRecord(1, 101, 302)
	r.Revision = 2
	if e := s.consumeRecommendation(t.Context(), popularityEvent(t, s, "alice", "changed", deck.RecommendationTopic, deck.RecommendationPublication{Revision: 2, Public: true, Record: &r})); e != nil {
		t.Fatal(e)
	}
	rows := popularityRows(t, s, "/PopularEquipInfo", now, 5)
	if len(rows) != 1 || popularityScalar(t, rows[0], 3) != 900 || popularityScalar(t, rows[0], 4) != 2 {
		t.Fatalf("expected most-used item with two remaining owners: %x", rows)
	}
	if e := s.consumeRecommendation(t.Context(), popularityEvent(t, s, "bob", "hidden", deck.RecommendationTopic, deck.RecommendationPublication{Revision: 2, Public: false})); e != nil {
		t.Fatal(e)
	}
	rows = popularityRows(t, s, "/PopularEquipInfo", now, 5)
	if len(rows) != 1 || popularityScalar(t, rows[0], 4) != 1 {
		t.Fatalf("withdrawn owner still counted: %x", rows)
	}
}

func TestPopularityDoesNotFillUnobservedHistoricalWeeks(t *testing.T) {
	first := time.Date(2026, 10, 15, 14, 59, 0, 0, time.UTC)
	s, makeSnapshot := pvpPopularityFixture(t, first)
	update := pvp.PopularityPublished{Timestamp: uint64(first.UnixMilli()), Snapshot: makeSnapshot(1, false)}
	if err := s.consumePVP(t.Context(), popularityEvent(t, s, "alice", "actual-registration", "pvp.popularity", update)); err != nil {
		t.Fatal(err)
	}
	published := first.Add(time.Minute)
	if err := s.refreshPopularity(t.Context(), published); err != nil {
		t.Fatal(err)
	}
	rows := popularityRows(t, s, "/PopularCostumeInfo", published, 0, 0, 0, 1, 0)
	if len(rows) != 1 || popularityScalar(t, rows[0], 1) != 101 || popularityScalar(t, rows[0], 2) != 1 {
		t.Fatal("the actually observed registration did not publish at the weekly boundary")
	}
	later := first.Add(5 * 7 * 24 * time.Hour)
	if err := s.refreshPopularity(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if rows := popularityRows(t, s, "/PopularCostumeInfo", later, 0, 0, 0, 1, 0); len(rows) != 0 {
		t.Fatalf("unobserved weeks were synthesized: %x", rows)
	}
}

func pvpPopularityFixture(t *testing.T, now time.Time) (*sharedRuntime, func(uint64, bool) pvp.PublishedSnapshot) {
	t.Helper()
	s, _ := popularityFixture(t)
	design := &gamedata.PvpDesign{Mirror: gamedata.PvpDefaults{StartVP: 1000, MinimumVP: 1000}, MirrorSeasons: map[uint64]uint64{1: 1}, MirrorRanks: map[uint64][]gamedata.PvpRank{1: {{Group: 1, ID: 7, VP: 1000}}}}
	var e error
	s.pvp, e = newSharedPVP(t.Context(), s.store, design, []pvp.Season{{Mode: pvp.Mirror, ID: 1, Start: 1, End: uint64(now.AddDate(1, 0, 0).UnixMilli())}})
	if e != nil {
		t.Fatal(e)
	}
	s.statistics.PVPRankGroups = map[uint64]uint64{7: 1}
	s.statistics.PVPMinimumGroup = 1
	makeSnapshot := func(version uint64, reverse bool) pvp.PublishedSnapshot {
		order := []uint64{21, 22}
		if reverse {
			order = []uint64{22, 21}
		}
		entry := wire.AppendVarint(nil, 1, 11)
		for _, index := range order {
			entry = wire.AppendBytes(entry, 5, wire.AppendVarint(nil, 1, index))
		}
		full := wire.AppendBytes(nil, 1, entry)
		for i, id := range []uint64{101, 102} {
			c := wire.AppendVarint(nil, 1, uint64(i+21))
			c = wire.AppendVarint(c, 2, id)
			full = wire.AppendBytes(full, 3, c)
		}
		return pvp.PublishedSnapshot{Profile: pvp.Profile{OwnerID: "alice", OwnerIndex: 1}, Mode: pvp.Mirror, Season: 1, Version: version, VP: 1000, Snapshot: pvp.FullSnapshot{FullDeck: full}}
	}
	return s, makeSnapshot
}

func TestPVPPopularitySeparatesRegisteredSkillOrderAndDeckSide(t *testing.T) {
	now := time.Date(2026, 10, 15, 14, 59, 0, 0, time.UTC)
	s, makeSnapshot := pvpPopularityFixture(t, now)
	for side := range uint64(2) {
		update := pvp.PopularityPublished{Timestamp: uint64(now.UnixMilli()), Side: side, Snapshot: makeSnapshot(1, side == 1)}
		event := popularityEvent(t, s, "alice", fmt.Sprintf("registration-%d", side), "pvp.popularity", update)
		if e := s.consumePVP(t.Context(), event); e != nil {
			t.Fatal(e)
		}
		if e := s.consumePVP(t.Context(), event); e != nil {
			t.Fatal(e)
		}
	}
	if rows := popularityRows(t, s, "/PopularCostumeInfo", now, 0, 0, 0, 1, 0); len(rows) != 0 {
		t.Fatal("unpublished initial registrations were exposed")
	}
	published := now.Add(time.Minute)
	if e := s.refreshPopularity(t.Context(), published); e != nil {
		t.Fatal(e)
	}
	for _, q := range []struct{ side, order, want uint64 }{{0, 0, 101}, {0, 1, 102}, {1, 0, 102}, {1, 1, 101}} {
		rows := popularityRows(t, s, "/PopularCostumeInfo", published, 0, q.side, q.order, 1, 0)
		if len(rows) != 1 || popularityScalar(t, rows[0], 1) != q.want || popularityScalar(t, rows[0], 2) != 1 {
			t.Fatalf("side=%d order=%d wanted costume %d once: %x", q.side, q.order, q.want, rows)
		}
	}
	update := pvp.PopularityPublished{Timestamp: uint64(published.Add(time.Second).UnixMilli()), Side: 0, Snapshot: makeSnapshot(2, true)}
	if e := s.consumePVP(t.Context(), popularityEvent(t, s, "alice", "changed-registration", "pvp.popularity", update)); e != nil {
		t.Fatal(e)
	}
	rows := popularityRows(t, s, "/PopularCostumeInfo", published, 0, 0, 0, 1, 0)
	if len(rows) != 1 || popularityScalar(t, rows[0], 1) != 101 {
		t.Fatalf("in-period registration change leaked into published statistics: %x", rows)
	}
	published = published.Add(7 * 24 * time.Hour)
	if e := s.refreshPopularity(t.Context(), published); e != nil {
		t.Fatal(e)
	}
	rows = popularityRows(t, s, "/PopularCostumeInfo", published, 0, 0, 0, 1, 0)
	if len(rows) != 2 || popularityScalar(t, rows[0], 1) != 101 || popularityScalar(t, rows[0], 2) != 0 || popularityScalar(t, rows[0], 3) != 1 || popularityScalar(t, rows[1], 1) != 102 || popularityScalar(t, rows[1], 2) != 1 || popularityScalar(t, rows[1], 3) != 0 {
		t.Fatalf("weekly publication/history did not shift independently: %x", rows)
	}
}

func TestTotalWarPopularityUsesActualCommunityRanking(t *testing.T) {
	s, _ := popularityFixture(t)
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	publish := func(owner, score, costume uint64, at time.Time, id string) {
		t.Helper()
		p := deck.PopularityPublication{CompletedAt: uint64(at.UnixMilli()), Score: score, OwnerIndex: owner, TotalWarCostumes: []uint64{costume}}
		event := popularityEvent(t, s, fmt.Sprint(owner), id, deck.PopularityTopic, p)
		if e := s.consumePopularity(t.Context(), event); e != nil {
			t.Fatal(e)
		}
		if e := s.consumePopularity(t.Context(), event); e != nil {
			t.Fatal(e)
		}
	}
	publish(1, 999, 101, now, "best-run")
	if rows := popularityRows(t, s, "/PopularCostumeInfo", now, 3, 0, 0, 0, 0); len(rows) != 0 {
		t.Fatal("unpublished first result was exposed")
	}
	first := now.Add(7 * 24 * time.Hour)
	if e := s.refreshPopularity(t.Context(), first); e != nil {
		t.Fatal(e)
	}
	if rows := popularityRows(t, s, "/PopularCostumeInfo", first, 3, 0, 0, 0, 0); len(rows) != 0 {
		t.Fatal("one account cannot claim a top-ten-percent rank")
	}
	for owner := uint64(2); owner <= 10; owner++ {
		publish(owner, 1000-owner, 102, first.Add(time.Hour), "best-run")
	}
	if rows := popularityRows(t, s, "/PopularCostumeInfo", first, 3, 0, 0, 0, 0); len(rows) != 0 {
		t.Fatal("in-period population change bypassed weekly publication")
	}
	second := first.Add(7 * 24 * time.Hour)
	if e := s.refreshPopularity(t.Context(), second); e != nil {
		t.Fatal(e)
	}
	rows := popularityRows(t, s, "/PopularCostumeInfo", second, 3, 0, 0, 0, 0)
	if len(rows) != 1 || popularityScalar(t, rows[0], 1) != 101 || popularityScalar(t, rows[0], 2) != 1 {
		t.Fatalf("expected one actual top-ten-percent result: %x", rows)
	}
	publish(2, 2000, 102, second.Add(time.Minute), "new-best")
	rows = popularityRows(t, s, "/PopularCostumeInfo", second, 3, 0, 0, 0, 0)
	if len(rows) != 1 || popularityScalar(t, rows[0], 1) != 101 {
		t.Fatalf("new score leaked before the weekly boundary: %x", rows)
	}
	third := second.Add(7 * 24 * time.Hour)
	if e := s.refreshPopularity(t.Context(), third); e != nil {
		t.Fatal(e)
	}
	rows = popularityRows(t, s, "/PopularCostumeInfo", third, 3, 0, 0, 0, 0)
	if len(rows) != 2 || popularityScalar(t, rows[0], 1) != 101 || popularityScalar(t, rows[0], 2) != 0 || popularityScalar(t, rows[0], 3) != 1 || popularityScalar(t, rows[1], 1) != 102 || popularityScalar(t, rows[1], 2) != 1 || popularityScalar(t, rows[1], 3) != 0 {
		t.Fatalf("published ranking/history did not move at the weekly boundary: %x", rows)
	}
}
