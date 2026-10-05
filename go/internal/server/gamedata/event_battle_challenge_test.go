package gamedata

import (
	"bd2server/internal/server/wire"
	"os"
	"testing"
)

func TestEventBattleChallengeInstalledSmoke(t *testing.T) {
	root, version := os.Getenv("BD2_GAMEDATA_ROOT"), os.Getenv("BD2_GAMEDATA_VERSION")
	if root == "" || version == "" {
		t.Skip("installed design env required")
	}
	d, e := LoadEventBattleChallenges(root, version)
	if e != nil {
		t.Fatal(e)
	}
	if len(d) == 0 {
		t.Fatal("empty challenges")
	}
}
func TestChallengeIndexesPackedAndDuplicate(t *testing.T) {
	definitions := []EventBattleChallenge{{Type: 8}, {Type: 2, Value1: 8}}
	req := wire.AppendBytes(nil, 5, []byte{0, 1})
	out, e := VerifySubmittedChallenges(req, definitions)
	if e != nil || len(out) != 2 {
		t.Fatal(out, e)
	}
	if _, e = VerifySubmittedChallenges(wire.AppendBytes(nil, 5, []byte{0, 0}), definitions); e == nil {
		t.Fatal("duplicate index")
	}
}
