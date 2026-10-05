package eventtasks

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func passClaimRequest(seq, rewardType uint64, all bool, level uint64) []byte {
	b := req(seq)
	if all {
		b = wire.AppendVarint(b, 2, 1)
	}
	b = wire.AppendVarint(b, 3, 8)
	if level != 0 {
		b = wire.AppendVarint(b, 4, level)
	}
	if rewardType != 0 {
		b = wire.AppendVarint(b, 5, rewardType)
	}
	return b
}

func claimPass(t *testing.T, s *Service, request []byte) []byte {
	t.Helper()
	code, response, handled, err := s.Handle("/PassReward", request)
	if err != nil || code != 126 || !handled {
		t.Fatalf("pass claim failed: code=%d handled=%t err=%v", code, handled, err)
	}
	if _, present, err := wire.Bytes(response, 1); err != nil || !present {
		t.Fatalf("native receiver requires a non-null reward bundle: %x, %v", response, err)
	}
	return response
}

func TestPassRewardNativeOneClickAfterPurchaseAndRestart(t *testing.T) {
	s, e, store := setup(t)
	s.design.PassLevels[8] = []gamedata.EventPassLevel{
		{ID: 1, NeedExp: 20, Basic: gamedata.Reward{Type: 3, Count: 1}, Premium: gamedata.Reward{Type: 4, Count: 10}},
		{ID: 2, NeedExp: 30, Basic: gamedata.Reward{Type: 3, Count: 2}, Premium: gamedata.Reward{Type: 4, Count: 20}},
		{ID: 3, NeedExp: 40, Basic: gamedata.Reward{Type: 3, Count: 3}, Premium: gamedata.Reward{Type: 4, Count: 30}},
		{ID: 4, Basic: gamedata.Reward{Type: 3, Count: 4}, Premium: gamedata.Reward{Type: 4, Count: 40}},
	}
	s.design.PassBuys = map[uint64][]gamedata.EventPassBuy{8: {{ID: 1, Type: 1, LevelsGranted: 3, Cost: gamedata.Reward{Type: 3, Count: 1000}}}}
	buy := wire.AppendVarint(req(1), 2, 8)
	buy = wire.AppendVarint(buy, 3, 1)
	if _, _, _, err := s.Handle("/PassBuy", buy); err != nil {
		t.Fatal(err)
	}

	// PassRootUI.ReceiveAllReward sends BASIC first, then PREMIUM_1 in its callback.
	basic := passClaimRequest(2, 0, true, 0)
	basicReply := claimPass(t, s, basic)
	for _, lv := range s.design.PassLevels[8] {
		p := s.pass(s.registry.List()[2])
		if !p.Claimed[key(lv.ID, 0)] || p.Claimed[key(lv.ID, 1)] {
			t.Fatalf("basic phase incorrectly claimed premium at level %d", lv.ID)
		}
	}
	if len(e.rewards) != 4 || e.rewards[0].Type != 3 || e.rewards[3].Count != 4 {
		t.Fatalf("basic phase granted wrong rewards: %+v", e.rewards)
	}
	reopened, err := Open(store, s.design, s.registry, e)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = s.now
	reopened.SetSession("test")
	if replay := claimPass(t, reopened, basic); !bytes.Equal(replay, basicReply) || e.calls != 2 {
		t.Fatal("restart replay repeated basic grants")
	}
	premium := passClaimRequest(3, 1, true, 0)
	premiumReply := claimPass(t, reopened, premium)
	if len(e.rewards) != 8 || e.rewards[4].Type != 4 || e.rewards[7].Count != 40 || e.calls != 3 {
		t.Fatalf("premium phase granted wrong rewards: %+v", e.rewards)
	}
	for _, lv := range reopened.design.PassLevels[8] {
		p := reopened.pass(reopened.registry.List()[2])
		if !p.Claimed[key(lv.ID, 0)] || !p.Claimed[key(lv.ID, 1)] {
			t.Fatalf("claim state incomplete at level %d", lv.ID)
		}
	}
	if replay := claimPass(t, reopened, premium); !bytes.Equal(replay, premiumReply) || e.calls != 3 {
		t.Fatal("premium replay repeated grants")
	}
	// A new one-click request after claiming must complete without granting again.
	for typ := uint64(0); typ <= 1; typ++ {
		response := claimPass(t, reopened, passClaimRequest(4+typ, typ, true, 0))
		bundle, _, _ := wire.Bytes(response, 1)
		if len(bundle) != 0 || e.calls != 3 || len(e.rewards) != 8 {
			t.Fatal("already-claimed all request repeated rewards")
		}
	}
}

func TestPassRewardBasicAlreadyClaimedBeforePremiumPurchase(t *testing.T) {
	s, e, _ := setup(t)
	s.design.PassLevels[8] = []gamedata.EventPassLevel{{ID: 1, Basic: gamedata.Reward{Type: 3, Count: 5}, Premium: gamedata.Reward{Type: 4, Count: 50}}}
	claimPass(t, s, passClaimRequest(1, 0, true, 0))
	s.design.PassBuys = map[uint64][]gamedata.EventPassBuy{8: {{ID: 1, Type: 1, Cost: gamedata.Reward{Type: 3, Count: 1000}}}}
	buy := wire.AppendVarint(req(2), 2, 8)
	buy = wire.AppendVarint(buy, 3, 1)
	if _, _, _, err := s.Handle("/PassBuy", buy); err != nil {
		t.Fatal(err)
	}
	claimPass(t, s, passClaimRequest(3, 0, true, 0))
	if len(e.rewards) != 1 || e.calls != 2 {
		t.Fatal("empty basic phase granted again")
	}
	claimPass(t, s, passClaimRequest(4, 1, true, 0))
	if len(e.rewards) != 2 || e.rewards[1].Type != 4 || e.rewards[1].Count != 50 || e.calls != 3 {
		t.Fatalf("empty basic phase prevented premium claim: %+v", e.rewards)
	}
}

func TestPassRewardSingleClaimsAndLockedLevels(t *testing.T) {
	s, e, _ := setup(t)
	s.design.PassLevels[8] = []gamedata.EventPassLevel{
		{ID: 1, NeedExp: 20, Basic: gamedata.Reward{Type: 3, Count: 5}, Premium: gamedata.Reward{Type: 4, Count: 50}},
		{ID: 2, Basic: gamedata.Reward{Type: 3, Count: 6}, Premium: gamedata.Reward{Type: 4, Count: 60}},
	}
	if _, _, _, err := s.Handle("/PassReward", passClaimRequest(1, 1, true, 0)); err == nil || e.calls != 0 {
		t.Fatal("premium reward granted without purchase")
	}
	claimPass(t, s, passClaimRequest(2, 0, false, 1))
	if len(e.rewards) != 1 || e.rewards[0].Count != 5 {
		t.Fatal("single basic reward incorrect")
	}
	for _, request := range [][]byte{passClaimRequest(3, 0, false, 1), passClaimRequest(4, 0, false, 2), passClaimRequest(5, 0, false, 99), passClaimRequest(6, 2, true, 0)} {
		if _, _, _, err := s.Handle("/PassReward", request); err == nil || e.calls != 1 {
			t.Fatal("duplicate, locked or fabricated reward accepted")
		}
	}
	s.pass(s.registry.List()[2]).Premium = true
	claimPass(t, s, passClaimRequest(7, 1, false, 1))
	if len(e.rewards) != 2 || e.rewards[1].Count != 50 || s.pass(s.registry.List()[2]).Claimed[key(2, 1)] {
		t.Fatal("single premium claim changed another level")
	}
}

func TestPassRewardKeepsCurrentNewbieStepInEveryResponse(t *testing.T) {
	s, _, _ := setup(t)
	d := s.design.Passes[8]
	d.NewbieStep = 1
	s.design.Passes[8] = d
	s.state.NewbieStep = 1
	s.design.PassLevels[8] = []gamedata.EventPassLevel{{ID: 1, NeedExp: 20, Basic: gamedata.Reward{Type: 3, Count: 5}}, {ID: 2, Basic: gamedata.Reward{Type: 3, Count: 6}}}
	response := claimPass(t, s, passClaimRequest(1, 0, true, 0))
	if scalar(response, 2) != 1 || s.state.NewbieStep != 1 {
		t.Fatal("partial claim resets native guide step")
	}
	response = claimPass(t, s, passClaimRequest(2, 0, true, 0))
	if scalar(response, 2) != 1 {
		t.Fatal("empty phase resets native guide step")
	}
}
