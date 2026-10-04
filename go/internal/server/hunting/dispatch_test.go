package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func TestDispatchSweepReplayAndValidation(t *testing.T) {
	store := stateio.NewMemory()
	inv, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchLoad = func(g, id uint64) (*gamedata.DispatchDesign, error) {
		return &gamedata.DispatchDesign{Group: g, ID: id, Pack: 1, GroundID: 1, AP: 6, ClearTime: 1200, Rewards: []gamedata.BattleReward{{Type: 4, Count: 10}, {Type: 8, ID: 1000, Count: 1}}}, nil
	}
	s.state.Packs["1"] = packState{Highest: 1}
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 2, 1)
	req = wire.AppendVarint(req, 3, 1)
	req = wire.AppendVarint(req, 4, 2)
	_, a, _, err := s.HandleSession("/HuntDispatch", req, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_, b, _, err := s.HandleSession("/HuntDispatch", req, "s1")
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("replay %v", err)
	}
	free, bonus, _ := s.HuntingAP()
	if free != 88 || bonus != 10 || wallet.Snapshot().Gold != 20 {
		t.Fatalf("AP/currency %d/%d %+v", free, bonus, wallet.Snapshot())
	}
	req = wire.AppendVarint(req, 4, 3)
	if _, _, _, err = s.HandleSession("/HuntDispatch", req, "s1"); err == nil {
		t.Fatal("sequence conflict accepted")
	}
	s.state.Packs["1"] = packState{}
	if _, _, _, err = s.HandleSession("/HuntDispatch", req, "s2"); err == nil {
		t.Fatal("uncleared ground accepted")
	}
}
func TestDispatchCancelRefundsUnplayedAP(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 10)
	s.dispatchLoad = func(g, id uint64) (*gamedata.DispatchDesign, error) {
		return &gamedata.DispatchDesign{Group: g, ID: id, Pack: 1, GroundID: 1, AP: 6, ClearTime: 1200, Rewards: []gamedata.BattleReward{{Type: 4, Count: 10}}}, nil
	}
	s.state.Packs["1"] = packState{Highest: 1}
	req := wire.AppendVarint(nil, 1, 1)
	req = wire.AppendVarint(req, 2, 1)
	req = wire.AppendVarint(req, 3, 1)
	req = wire.AppendVarint(req, 4, 2)
	if _, _, _, e := s.HandleSession("/HuntDispatchStart", req, "session"); e != nil {
		t.Fatal(e)
	}
	end := wire.AppendVarint(nil, 1, 2)
	end = wire.AppendVarint(end, 2, 1)
	end = wire.AppendVarint(end, 3, 1)
	if _, _, _, e := s.HandleSession("/HuntDispatchEnd", end, "session"); e != nil {
		t.Fatal(e)
	}
	free, bonus, _ := s.HuntingAP()
	if free != 10 || bonus != 10 || wallet.Snapshot().Gold != 0 {
		t.Fatalf("cancel %d %d %+v", free, bonus, wallet.Snapshot())
	}
}

func TestDispatchPreviewHasAPItemInfo(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 10)
	s.dispatchLoad = func(g, id uint64) (*gamedata.DispatchDesign, error) {
		return &gamedata.DispatchDesign{Group: g, ID: id, Pack: 1, GroundID: 1, AP: 6, ClearTime: 1200, Rewards: []gamedata.BattleReward{{Type: 4, Count: 10}}}, nil
	}
	s.state.Packs["1"] = packState{Highest: 1}
	req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 1), 3, 1), 4, 2)
	if _, _, _, e := s.HandleSession("/HuntDispatchStart", req, "session"); e != nil {
		t.Fatal(e)
	}
	preview := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1), 3, 1)
	_, out, _, e := s.HandleSession("/HuntDispatchRewardPreview", preview, "session")
	if e != nil {
		t.Fatal(e)
	}
	totals := map[uint64]uint64{}
	wire.Walk(out, func(f wire.Field) error {
		if f.Number == 2 {
			wire.Walk(f.Value, func(item wire.Field) error {
				if item.Number == 1 {
					typ, _, _ := wire.Varint(item.Value, 3)
					n, _, _ := wire.Varint(item.Value, 4)
					totals[typ] += n
				}
				return nil
			})
		}
		return nil
	})
	if totals[21] != 10 || totals[23] != 2 || totals[4] != 0 {
		t.Fatalf("preview refund ItemInfo %+v", totals)
	}
	free, bonus, _ := s.HuntingAP()
	if free != 0 || bonus != 8 {
		t.Fatal("preview changed AP")
	}
}
