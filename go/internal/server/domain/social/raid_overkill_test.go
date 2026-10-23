package social_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/social"
)

// LocalText 2001674/30061021: a guild's overkill score sums the overkill of
// its overkillMemberCount highest-scoring members and breaks score ties.
func TestGuildStandingsBreakTiesWithOverkillOfTopScoringMembers(t *testing.T) {
	store := &memoryStore{rows: map[string]map[string][]byte{}}
	design := &gamedata.GuildDesign{Guild: gamedata.GuildRules{MaxMembers: 30}, Raid: gamedata.GuildRaidRules{OverkillMemberCount: 10}}
	owner, err := social.New(store, design)
	if err != nil {
		t.Fatal(err)
	}
	const season = 5
	key := func(v uint64) string { return fmt.Sprintf("%020d", v) }
	put := func(tx social.Tx, bucket, k string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Put(bucket, k, raw); err != nil {
			t.Fatal(err)
		}
	}
	members := map[uint64][]social.MemberBest{
		// The lowest scorer's large overkill must not count: only the top ten by score do.
		1: {{AccountID: "low", OwnerIndex: 11, Score: 90, Overkill: 1000}},
		2: {{AccountID: "x", OwnerIndex: 1, Score: 50, Overkill: 20}, {AccountID: "y", OwnerIndex: 2, Score: 40, Overkill: 30}},
	}
	for i := range uint64(10) {
		members[1] = append(members[1], social.MemberBest{AccountID: fmt.Sprintf("top%d", i), OwnerIndex: i + 1, Score: 100 - i, Overkill: 1})
	}
	if err = store.Update(context.Background(), func(tx social.Tx) error {
		for guild, rows := range members {
			put(tx, "social.season_guilds", key(season)+":"+key(guild), guild)
			put(tx, "social.seasons", key(guild)+":"+key(season), social.Season{GuildID: guild, ID: season, TotalScore: 1000, GuildView: social.Guild{ID: guild}})
			for _, m := range rows {
				m.GuildID, m.Season = guild, season
				put(tx, "social.member_best", key(guild)+":"+key(season)+":"+m.AccountID, m)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	standings, err := owner.Standings(context.Background(), season)
	if err != nil {
		t.Fatal(err)
	}
	if len(standings) != 2 {
		t.Fatalf("standings=%d, want 2", len(standings))
	}
	got := map[uint64][2]uint64{}
	for _, s := range standings {
		got[s.Guild.ID] = [2]uint64{s.Overkill, s.Rank}
	}
	if got[2] != [2]uint64{50, 1} || got[1] != [2]uint64{10, 2} {
		t.Fatalf("guild overkill/rank = %v, want guild 2 {50 1}, guild 1 {10 2}", got)
	}
}
