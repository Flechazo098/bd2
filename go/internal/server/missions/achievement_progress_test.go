package missions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

type testAchievementProgress uint64

func (p *testAchievementProgress) AchievementValue(uint64) (uint64, error) { return uint64(*p), nil }
func TestAchievementClearRequiresProgressBeforeAnyBatchReward(t *testing.T) {
	store := stateio.NewMemory()
	inv, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	design := &gamedata.MissionDesign{Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{{GroupID: 987, ID: 1}: {Target: 5, CounterGroup: 987, AddExp: 7, Rewards: []gamedata.Reward{{Type: 8, ID: 123, Count: 1}}}, {GroupID: 987, ID: 2}: {Target: 10, CounterGroup: 987, AddExp: 9, Rewards: []gamedata.Reward{{Type: 8, ID: 123, Count: 2}}}}}
	s, err := Open(store, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	progress := testAchievementProgress(5)
	if err := s.AttachAchievementProgress(&progress); err != nil {
		t.Fatal(err)
	}
	info := wire.AppendVarint(nil, 1, 987)
	info = wire.AppendVarint(info, 2, 1)
	info = wire.AppendVarint(info, 2, 2)
	req := wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 3, info)
	if _, _, _, err := s.Handle("/AchievementClear", req); err == nil {
		t.Fatal("unearned batch claimed")
	}
	if len(inv.All()) != 0 || len(s.state.Claimed) != 0 {
		t.Fatal("earlier batch reward mutated")
	}
	progress = 10
	if _, _, _, err := s.Handle("/AchievementClear", req); err != nil {
		t.Fatal(err)
	}
	if exp, err := s.AchievementExperience(); err != nil || exp != 16 {
		t.Fatalf("exp=%d err=%v", exp, err)
	}
	if len(inv.All()) != 2 || inv.All()[0].Count+inv.All()[1].Count != 3 {
		t.Fatalf("rewards=%+v", inv.All())
	}
	if _, _, _, err := s.Handle("/AchievementClear", req); err != nil {
		t.Fatal(err)
	}
	if len(inv.All()) != 2 || inv.All()[0].Count+inv.All()[1].Count != 3 {
		t.Fatal("replay granted twice")
	}
}

func TestAchievementCurrencyRewardsCreditWalletAndReplay(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	d := &gamedata.MissionDesign{Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{{GroupID: 987, ID: 1}: {Target: 1, AddExp: 9, Rewards: []gamedata.Reward{{Type: 3, Count: 50}, {Type: 4, Count: 100}}}}}
	s, err := Open(store, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	p := testAchievementProgress(1)
	if err := s.AttachAchievementProgress(&p); err != nil {
		t.Fatal(err)
	}
	req := wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 3, wire.AppendVarint(wire.AppendVarint(nil, 1, 987), 2, 1))
	for range 2 {
		if _, _, _, err := s.Handle("/AchievementClear", req); err != nil {
			t.Fatal(err)
		}
	}
	if c := wallet.Snapshot(); c.Gold != 100 || c.FreeJewelry != 50 {
		t.Fatalf("wallet=%+v", c)
	}
	if len(inv.All()) != 0 {
		t.Fatal("currency stored in items")
	}
}

func TestAchievementTierOrderUsesActualIDsAndAllowsUnorderedBatch(t *testing.T) {
	for _, content := range []uint64{0, 1} {
		store := stateio.NewMemory()
		inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
		first, last := uint64(17), uint64(93)
		if content == 1 {
			first, last = 1017, 1093
		}
		d := &gamedata.MissionDesign{Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{{ContentsGroup: content, GroupID: 987, ID: first}: {Target: 1, AddExp: 7}, {ContentsGroup: content, GroupID: 987, ID: last}: {Target: 2, AddExp: 9}}}
		s, err := Open(store, d, inv)
		if err != nil {
			t.Fatal(err)
		}
		p := testAchievementProgress(2)
		if err := s.AttachAchievementProgress(&p); err != nil {
			t.Fatal(err)
		}
		request := func(ids ...uint64) []byte {
			info := wire.AppendVarint(nil, 1, 987)
			for _, id := range ids {
				info = wire.AppendVarint(info, 2, id)
			}
			return wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, content), 3, info)
		}
		if _, _, _, err := s.Handle("/AchievementClear", request(last)); err == nil {
			t.Fatal("skipped earlier tier")
		}
		if len(s.state.Claimed) != 0 {
			t.Fatal("invalid jump mutated")
		}
		if _, _, _, err := s.Handle("/AchievementClear", request(last, first)); err != nil {
			t.Fatal(err)
		}
		if exp, err := s.AchievementExperience(); err != nil || exp != 16 {
			t.Fatalf("exp=%d err=%v", exp, err)
		}
		s.state.Claimed = append(s.state.Claimed, "achievement:0/12345/1")
		if _, err := s.AchievementExperience(); err == nil {
			t.Fatal("unknown historical claim omitted")
		}
	}
}
