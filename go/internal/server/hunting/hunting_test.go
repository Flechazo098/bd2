package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

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
	if _, _, err := s.CompleteBattle(73, 5, 31, 81, "sessionB:2"); err != nil {
		t.Fatal(err)
	}
	if len(inv.All()) != 2 || inv.All()[0].Count+inv.All()[1].Count != 4 {
		t.Fatalf("repeated rewards=%+v", inv.All())
	}
	free, _, _ = s.HuntingAP()
	if free != 14 {
		t.Fatalf("repeat AP=%d", free)
	}
	if _, _, err := s.CompleteBattle(73, 5, 41, 91, "sessionB:3"); err != nil {
		t.Fatal(err)
	}
	if s.state.Packs["73"].Highest != 9 {
		t.Fatal("boss did not unlock next ground")
	}
}
