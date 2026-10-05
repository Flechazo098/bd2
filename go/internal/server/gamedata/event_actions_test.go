package gamedata

import (
	"os"
	"testing"
)

func TestInstalledEventActions23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	d, e := LoadEventActionsDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Tables["VotingEventTable"]) != 6 || len(d.Tables["FriendshipSpecialEpisodeTable"]) != 120 || len(d.Tables["NpcQuizTable"]) != 43 {
		t.Fatal("event actions incomplete")
	}
	r := d.SpawnRewards[[2]uint64{3009, 3001}]
	if r.Type != 4 || r.Count != 10000 {
		t.Fatalf("spawn reward mismatch %+v", r)
	}
}
