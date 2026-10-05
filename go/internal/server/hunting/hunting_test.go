package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestHuntingCatalogPlaceholdersAndDetailRequireEnteredRun(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, err := Open(store, "", "", inv, wallet, func() (int, error) { return 73, nil }, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	d := &gamedata.HuntingPack{Grounds: []gamedata.HuntingGround{{ID: 9, MapID: 123, BossID: 41, Monsters: []uint64{31, 32, 33, 34, 35}}}, Monsters: map[uint64]gamedata.HuntingMonster{}}
	for _, id := range []uint64{31, 32, 33, 34, 35, 41} {
		d.Monsters[id] = gamedata.HuntingMonster{ID: id, Decks: []uint64{id + 100}}
	}
	s.load = func(pack int) (*gamedata.HuntingPack, error) {
		switch pack {
		case 73:
			return d, nil
		case 84, 85:
			return &gamedata.HuntingPack{}, nil
		default:
			return nil, fmt.Errorf("unknown pack")
		}
	}
	list := wire.AppendVarint(nil, 1, 1)
	for _, id := range []uint64{84, 73, 85} {
		list = wire.AppendVarint(list, 2, id)
	}
	code, response, ok, err := s.Handle("/HuntingGroundInfoList", list)
	if err != nil || !ok || code != 387 {
		t.Fatalf("list %d %v %v", code, ok, err)
	}
	var entries [][]byte
	if err := wire.Walk(response, func(f wire.Field) error {
		if f.Number == 1 {
			entries = append(entries, f.Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !bytes.Equal(entries[0], wire.AppendVarint(nil, 5, 84)) || !bytes.Equal(entries[2], wire.AppendVarint(nil, 5, 85)) {
		t.Fatalf("catalog lost ordered pack placeholders: %x", entries)
	}
	current, _, _ := wire.Varint(entries[1], 2)
	monsters := 0
	_ = wire.Walk(entries[1], func(f wire.Field) error {
		if f.Number == 4 {
			monsters++
		}
		return nil
	})
	if current != 9 || monsters != 5 {
		t.Fatalf("catalog current=%d monsters=%d", current, monsters)
	}
	for _, id := range []uint64{73, 84} {
		req := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, id)
		_, detail, _, err := s.Handle("/HuntingGroundInfo", req)
		if err != nil || !bytes.Equal(detail, wire.AppendBytes(nil, 1, nil)) {
			t.Fatalf("unentered pack %d returned active state: %x %v", id, detail, err)
		}
	}
	if len(s.state.Packs) != 0 {
		t.Fatal("query created hunting progress")
	}
	enter := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 3), 2, 73), 3, 9)
	if _, _, _, err := s.Handle("/HuntingGroundEnter", enter); err != nil {
		t.Fatal(err)
	}
	_, detail, _, err := s.Handle("/HuntingGroundInfo", wire.AppendVarint(wire.AppendVarint(nil, 1, 4), 2, 73))
	active, found, _ := wire.Bytes(detail, 1)
	if err != nil || !found || len(active) == 0 {
		t.Fatalf("entered detail missing: %x %v", detail, err)
	}
}

func TestHuntingRepeatEnterReceiptAPAndDefeated(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, err := Open(store, "", "", inv, wallet, func() (int, error) { return 73, nil }, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	d := &gamedata.HuntingPack{NormalAP: 3, BossAP: 7, Grounds: []gamedata.HuntingGround{{ID: 9, MapID: 123, BossID: 41, Monsters: []uint64{31}}, {ID: 15, MapID: 123, BossID: 51, Monsters: []uint64{32}}}, Monsters: map[uint64]gamedata.HuntingMonster{31: {ID: 31, Decks: []uint64{81}, Rewards: map[uint64][]gamedata.BattleReward{81: {{Type: 8, ID: 999, Count: 2}, {Type: 4, Count: 10}}}}, 41: {ID: 41, Type: 1, Decks: []uint64{91}, Rewards: map[uint64][]gamedata.BattleReward{91: {{Type: 8, ID: 998, Count: 4}}}}}}
	s.load = func(int) (*gamedata.HuntingPack, error) { return d, nil }
	enter := func(id uint64) error {
		r := wire.AppendVarint(nil, 1, 1)
		r = wire.AppendVarint(r, 2, 73)
		r = wire.AppendVarint(r, 3, id)
		_, _, _, err := s.Handle("/HuntingGroundEnter", r)
		return err
	}
	if err := enter(15); err == nil {
		t.Fatal("higher difficulty unlocked")
	}
	if err := enter(9); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBattle(73, 5, 31, 91); err == nil {
		t.Fatal("wrong deck accepted")
	}
	if _, _, err := s.CompleteBattle(73, 5, 31, 81, "sessionA:2"); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBattle(73, 5, 31, 81); err == nil {
		t.Fatal("defeated monster replay accepted")
	}
	if _, _, err := s.CompleteBattle(73, 5, 31, 81, "sessionA:2"); err != nil {
		t.Fatal(err)
	}
	free, bonus, _ := s.HuntingAP()
	if free != 17 || bonus != 5 {
		t.Fatalf("AP=%d/%d", free, bonus)
	}
	info, err := s.info(73, d)
	if err != nil {
		t.Fatal(err)
	}
	var inactive bool
	wire.Walk(info, func(f wire.Field) error {
		if f.Number == 4 {
			id, _, _ := wire.Varint(f.Value, 1)
			active, _, _ := wire.Varint(f.Value, 6)
			if id == 31 {
				inactive = active == 0
			}
		}
		return nil
	})
	if !inactive {
		t.Fatal("info resurrected defeated monster")
	}
	if err := enter(9); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteBattle(73, 5, 31, 81, "sessionB:2"); err == nil {
		t.Fatal("same difficulty entry reset round")
	}
	if _, _, err := s.CompleteBattle(73, 5, 41, 91, "sessionB:boss"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteBattle(73, 5, 31, 81, "sessionB:2"); err != nil {
		t.Fatal(err)
	}
	var normal uint64
	for _, item := range inv.All() {
		if item.ID == 999 {
			normal += item.Count
		}
	}
	if normal != 4 {
		t.Fatalf("repeated rewards=%+v", inv.All())
	}
	free, _, _ = s.HuntingAP()
	if free != 7 {
		t.Fatalf("repeat AP=%d", free)
	}
	if _, _, err := s.CompleteBattle(73, 5, 41, 91, "sessionB:3"); err != nil {
		t.Fatal(err)
	}
	if s.state.Packs["73"].Highest != 9 {
		t.Fatal("boss did not unlock next ground")
	}
}

func TestHuntingStageLoopAndBattleReplay(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 100, 0)
	d := &gamedata.HuntingPack{NormalAP: 1, BossAP: 1, Grounds: []gamedata.HuntingGround{{ID: 1, MapID: 13, BossID: 3, Monsters: []uint64{1, 2}}}, Monsters: map[uint64]gamedata.HuntingMonster{}}
	for _, id := range []uint64{1, 2, 3} {
		typ := uint64(0)
		if id == 3 {
			typ = 1
		}
		d.Monsters[id] = gamedata.HuntingMonster{ID: id, Type: typ, Decks: []uint64{id}, Rewards: map[uint64][]gamedata.BattleReward{id: {{Type: 4, Count: 10}}}}
	}
	s.load = func(int) (*gamedata.HuntingPack, error) { return d, nil }
	if _, e := s.EnsureForPack(1); e != nil {
		t.Fatal(e)
	}
	if e := s.ValidateBattle(1, 5, 3, 3); e == nil {
		t.Fatal("early boss accepted")
	}
	if _, _, e := s.CompleteBattle(1, 5, 1, 1, "a:1"); e != nil {
		t.Fatal(e)
	}
	req := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1), 3, 1)
	if _, _, _, e := s.HandleSession("/HuntingGroundEnter", req, "a"); e != nil {
		t.Fatal(e)
	}
	if e := s.ValidateBattle(1, 5, 1, 1); e == nil {
		t.Fatal("enter reset partial stage")
	}
	bundle, updates, e := s.CompleteBattle(1, 5, 2, 2, "a:3")
	if e != nil || len(updates) != 2 {
		t.Fatalf("boss transition %v %+v", e, updates)
	}
	again, replay, e := s.CompleteBattle(1, 5, 2, 2, "a:3")
	if e != nil || !bytes.Equal(bundle, again) || len(replay) != 2 {
		t.Fatal("battle replay lost response")
	}
	if _, _, e = s.CompleteBattle(1, 5, 3, 3, "a:3"); e == nil {
		t.Fatal("battle receipt conflict accepted")
	}
	_, updates, e = s.CompleteBattle(1, 5, 3, 3, "a:4")
	if e != nil || len(updates) != 3 || len(s.state.Packs["1"].Defeated) != 0 {
		t.Fatalf("round reset %v %v", e, updates)
	}
	if e = s.ValidateBattle(1, 5, 1, 1); e != nil {
		t.Fatal(e)
	}
	free, _, _ := s.HuntingAP()
	if free != 97 || wallet.Snapshot().Gold != 30 {
		t.Fatal("duplicate AP/reward")
	}
}
func TestHuntingAPExchangeReplayAndOverflow(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 2)
	cost := []gamedata.Reward{{Type: 21, Count: 4}}
	reward := []gamedata.Reward{{Type: 23, Count: 3}}
	if e := s.ExchangeAPOnce("x", cost, reward); e != nil {
		t.Fatal(e)
	}
	if e := s.ExchangeAPOnce("x", cost, reward); e != nil {
		t.Fatal(e)
	}
	free, bonus, _ := s.HuntingAP()
	if free != 6 || bonus != 5 {
		t.Fatal("AP replay")
	}
	if e := s.ExchangeAPOnce("x", nil, reward); e == nil {
		t.Fatal("identity conflict")
	}
	if e := s.CanExchangeAP(nil, []gamedata.Reward{{Type: 21, Count: 2147483647}}); e == nil {
		t.Fatal("AP overflow")
	}
}

func TestHuntingRewardAPCallbackDoesNotDeadlock(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 10, 0)
	s.load = func(int) (*gamedata.HuntingPack, error) {
		return &gamedata.HuntingPack{NormalAP: 1, Grounds: []gamedata.HuntingGround{{ID: 1, BossID: 2, Monsters: []uint64{1}}}, Monsters: map[uint64]gamedata.HuntingMonster{1: {ID: 1, Decks: []uint64{1}, Rewards: map[uint64][]gamedata.BattleReward{1: {{Type: 21, Count: 3}}}}, 2: {ID: 2, Decks: []uint64{2}}}}, nil
	}
	s.AttachRewards(func(id string, _ []gamedata.Reward) ([]byte, error) {
		return nil, s.ExchangeAPOnce(id, nil, []gamedata.Reward{{Type: 21, Count: 3}})
	})
	s.EnsureForPack(1)
	done := make(chan error, 1)
	go func() { _, _, e := s.CompleteBattle(1, 5, 1, 1, "apreward"); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reward callback deadlocked")
	}
	free, _, _ := s.HuntingAP()
	if free != 12 {
		t.Fatalf("AP grant lost %d", free)
	}
}

func TestHuntingAPDailyRefreshPreservesBonus(t *testing.T) {
	store := stateio.NewMemory()
	inv, _ := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	wallet, _ := player.OpenWallet(store, player.Currency{})
	s, _ := Open(store, "", "", inv, wallet, func() (int, error) { return 1, nil }, 4, 7)
	if e := s.AttachAPRefresh(gamedata.HuntingAPDesign{Max: 90, ResetSeconds: 9 * 3600}); e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.HuntingAP()
	now = now.Add(2 * time.Minute)
	free, bonus, e := s.HuntingAP()
	if e != nil || free != 90 || bonus != 7 {
		t.Fatalf("refresh %d %d %v", free, bonus, e)
	}
	s.state.Free = 12
	free, _, _ = s.HuntingAP()
	if free != 12 {
		t.Fatal("refreshed same day twice")
	}
}
