package missions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

func TestUserLevelRewardEligibilityBatchPersistenceAndRetry(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	d := &gamedata.MissionDesign{Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{{GroupID: 9, ID: 1}: {AddExp: 10}}}
	s, _ := Open(store, d, inv)
	levels := &gamedata.AchievementLevelDesign{Levels: []gamedata.AchievementLevel{{ID: 17, NeedEXP: 4, Rewards: []gamedata.Reward{{Type: 8, ID: 987, Count: 3}}}, {ID: 23, NeedEXP: 8, Rewards: []gamedata.Reward{{Type: 8, ID: 988, Count: 7}}}, {ID: 41, NeedEXP: 10, Rewards: []gamedata.Reward{{Type: 8, ID: 989, Count: 9}}}}}
	if err := s.AttachUserLevelRewards(levels); err != nil {
		t.Fatal(err)
	}
	req := func(ids ...uint64) []byte {
		r := wire.AppendVarint(nil, 1, 1)
		for _, id := range ids {
			r = wire.AppendVarint(r, 2, id)
		}
		return r
	}
	for _, ids := range [][]uint64{{23}, {17, 23}, {17, 17}, {99}} {
		if _, _, _, err := s.Handle("/UserLevelReward", req(ids...)); err == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	if len(inv.All()) != 0 {
		t.Fatal("invalid batch granted rewards")
	}
	s.state.Claimed = []string{"achievement:" + achievementName(gamedata.AchievementKey{GroupID: 9, ID: 1})}
	if _, _, _, err := s.Handle("/UserLevelReward", req(17, 23, 41)); err == nil {
		t.Fatal("accepted unearned level")
	}
	if _, _, _, err := s.Handle("/UserLevelReward", req(17, 23)); err != nil {
		t.Fatal(err)
	}
	if count, _ := s.LevelRewardCount(); count != 23 {
		t.Fatalf("count=%d", count)
	}
	if len(inv.All()) != 2 || inv.All()[0].Count+inv.All()[1].Count != 10 {
		t.Fatalf("items=%+v", inv.All())
	}
	reopened, _ := Open(store, d, inv)
	if err := reopened.AttachUserLevelRewards(levels); err != nil {
		t.Fatal(err)
	}
	reopened.state.Claimed = s.state.Claimed
	if _, _, _, err := reopened.Handle("/UserLevelReward", req(17, 23)); err != nil {
		t.Fatal(err)
	}
	if len(inv.All()) != 2 {
		t.Fatal("retry granted twice")
	}
	if n, _ := reopened.LevelRewardCount(); n != 23 {
		t.Fatalf("reopened=%d", n)
	}
}
