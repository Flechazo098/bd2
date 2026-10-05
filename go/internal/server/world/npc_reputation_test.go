package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
	"time"
)

func TestNPCReputationCompletionRetriesDoNotExtendDiscount(t *testing.T) {
	s := testService()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	store := stateio.NewMemory()
	d := gamedata.NPCReputationDesign{Groups: map[uint64]gamedata.NPCReputationRule{9: {ID: 9, DownHours: 2, GoodInn: 10, GoodPrice: 20}}, MapGroups: map[int]uint64{211: 9}}
	s.npcReputation = &npcReputationRuntime{store: store, now: func() time.Time { return now }, load: func(int) (gamedata.NPCReputationDesign, error) { return d, nil }}
	b, err := s.CompleteNPCReputation("quest-1", 21, 9)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := wire.Varint(b, 2); v != 2 {
		t.Fatal("completion did not grant good reputation")
	}
	now = now.Add(time.Hour)
	b, err = s.CompleteNPCReputation("quest-1", 21, 9)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed, _, _ := wire.Varint(b, 3); elapsed != 3600 {
		t.Fatal("retry reset good reputation expiry")
	}
	now = now.Add(time.Hour)
	state, _, err := s.reputationState(21, d.Groups[9])
	if err != nil || state != 1 {
		t.Fatal("good reputation did not expire after GameData hours")
	}
	if _, err = s.CompleteNPCReputation("quest-2", 21, 99); err == nil {
		t.Fatal("unknown reputation group accepted")
	}
}

func TestInnContextUsesCurrentMapAndShopCanProjectOtherUnlockedPacks(t *testing.T) {
	s := testService()
	s.storyCatalog.Packs[77] = gamedata.StoryPack{ID: 77}
	position := wire.AppendString(wire.AppendVarint(nil, 2, 21), 3, `{"mapId":211}`)
	if err := s.state.SaveUserPosition(position); err != nil {
		t.Fatal(err)
	}
	d := gamedata.NPCReputationDesign{Groups: map[uint64]gamedata.NPCReputationRule{9: {ID: 9, DownHours: 2, GoodInn: 10, GoodPrice: 20}}, MapGroups: map[int]uint64{211: 9}}
	s.npcReputation = &npcReputationRuntime{store: stateio.NewMemory(), now: time.Now, load: func(int) (gamedata.NPCReputationDesign, error) { return d, nil }, inns: func(int) ([]gamedata.InnRule, error) {
		return []gamedata.InnRule{{NPCID: 7, MapID: 211, MapGroup: 9}}, nil
	}}
	if _, state, err := s.InnContext(21, 7); err != nil || state != 1 {
		t.Fatal("valid motel context rejected")
	}
	if _, _, err := s.InnContext(21, 8); err == nil {
		t.Fatal("unrelated NPC treated as motel")
	}
	if _, _, err := s.InnContext(77, 7); err == nil {
		t.Fatal("motel from another pack accepted")
	}
	if _, err := s.CompleteNPCReputation("other-pack-delegation", 77, 9); err != nil {
		t.Fatal(err)
	}
	state, discount, err := s.NPCShopReputation(77)
	if err != nil || state != 2 || discount != 20 {
		t.Fatal("ShopOpen cannot project reputation for another unlocked pack")
	}
}
