package player

import (
	"bytes"
	"math"
	"testing"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestFriendshipInfoValidatesSequence(t *testing.T) {
	h := newFriendshipHarness(t)
	for _, request := range [][]byte{nil, wire.AppendVarint(nil, 1, 0), wire.AppendVarint(nil, 1, math.MaxInt32+1), {0x08, 0x80}} {
		if _, _, handled, err := h.service.Handle("/FriendshipInfo", request); !handled || err == nil {
			t.Fatal("invalid information request sequence was accepted")
		}
	}
	if code, body, handled, err := h.service.Handle("/FriendshipInfo", wire.AppendVarint(nil, 1, 1)); err != nil || !handled || code != 612 || len(body) == 0 {
		t.Fatalf("valid information request: code=%d handled=%v err=%v", code, handled, err)
	}
}

func TestFriendshipMutationsRequireSession(t *testing.T) {
	h := newFriendshipHarness(t)
	h.service.BeginSession("")
	item := h.item
	item.Count = 1
	for path, request := range map[string][]byte{"/FriendshipGift": giftRequest(1, 10, item), "/FriendshipCounseling": counselingRequest(1, 10, 1, 0, false)} {
		if _, _, handled, err := h.service.Handle(path, request); !handled || err == nil {
			t.Fatalf("%s accepted a mutation without a session", path)
		}
	}
	if len(h.collection.FriendshipEntries()) != 0 || h.wallet.Snapshot().FreeJewelry != 0 {
		t.Fatal("missing-session request mutated account state")
	}
	if err := h.inventory.CanConsume([]Item{h.item}); err != nil {
		t.Fatal("missing-session request consumed inventory")
	}
	h.service.BeginSession("authenticated-session")
	if _, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(1, 10, item)); err != nil {
		t.Fatal(err)
	}
}

type friendshipHarness struct {
	service    *FriendshipService
	store      *stateio.Memory
	collection *CollectionStore
	inventory  *Inventory
	wallet     *Wallet
	item       Item
	clock      time.Time
}

func newFriendshipHarness(t *testing.T) *friendshipHarness {
	t.Helper()
	h := &friendshipHarness{store: stateio.NewMemory(), clock: time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC)}
	var err error
	h.collection, err = OpenCollectionStore(h.store, []Costume{{InvenIndex: 1, ID: 100, Level: 5}, {InvenIndex: 2, ID: 200}, {InvenIndex: 3, ID: 300}, {InvenIndex: 4, ID: 400}})
	if err != nil {
		t.Fatal(err)
	}
	h.inventory, err = OpenInventory(h.store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := h.inventory.GrantOnce("gift-test", []gamedata.BattleReward{{Type: 8, ID: 7, Count: 50}})
	if err != nil {
		t.Fatal(err)
	}
	h.item = items[0]
	h.wallet, err = OpenWallet(h.store, Currency{})
	if err != nil {
		t.Fatal(err)
	}
	d := &gamedata.FriendshipDesign{Default: gamedata.FriendshipDefaultDesign{CorrectEXP: 100, IncorrectEXP: 80, MaxCounselingAP: 3, MaxCounselingAPByCostume: 1, QuickCounselingUnlockCount: 2, MaxLevels: [3]uint64{3, 4, 5}, CounselingRewards: []gamedata.Reward{{Type: 3, Count: 13}}}, Costumes: map[uint64]uint64{10: 100, 20: 200, 30: 300, 40: 400, 50: 500}, Gifts: map[[2]uint64]gamedata.FriendshipGiftDesign{{8, 7}: {Type: 8, ItemID: 7, EXP: 60, FavoriteEXP: 150, FavoriteCostumeIDs: []uint64{10}}}, Levels: map[gamedata.FriendshipKey]gamedata.FriendshipLevelDesign{}, Sessions: map[gamedata.FriendshipKey]gamedata.FriendshipSessionDesign{}}
	for id := range d.Costumes {
		for level := uint64(1); level <= 5; level++ {
			d.Levels[gamedata.FriendshipKey{GroupID: id, ID: level}] = gamedata.FriendshipLevelDesign{NextEXP: 100}
		}
		for session := uint64(1); session <= 3; session++ {
			d.Sessions[gamedata.FriendshipKey{GroupID: id, ID: session}] = gamedata.FriendshipSessionDesign{ChoiceCount: 2}
		}
	}
	d.Levels[gamedata.FriendshipKey{GroupID: 10, ID: 2}] = gamedata.FriendshipLevelDesign{NextEXP: 100, Rewards: []gamedata.Reward{{Type: 8, ID: 88, Count: 2}, {Type: 3, Count: 7}}}
	awake := &gamedata.CharAwakeDesign{Characters: map[uint64]gamedata.CharAwakeCharacter{1: {ImprintGrowth: [3][]gamedata.CharAwakeGrowth{{{}}, {{}}, {{}}}}}}
	potential := &gamedata.CostumePotentialDesign{CostumeUnique: map[uint64]uint64{100: 1}, Nodes: map[uint64]map[uint64]gamedata.CostumePotentialNode{100: {71: {ID: 71}, 72: {ID: 72}}}}
	h.service, err = NewFriendshipService(d, awake, potential, h.collection, h.inventory, h.wallet)
	if err != nil {
		t.Fatal(err)
	}
	h.service.now = func() time.Time { return h.clock }
	h.service.BeginSession("test-session")
	return h
}

func giftRequest(seq, id uint64, item Item) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, id)
	return wire.AppendBytes(b, 3, ItemWire(item))
}
func counselingRequest(seq, id, session, choice uint64, quick bool) []byte {
	b := wire.AppendVarint(nil, 1, seq)
	b = wire.AppendVarint(b, 2, id)
	b = wire.AppendVarint(b, 3, session)
	if choice != 0 {
		b = wire.AppendVarint(b, 4, choice)
	}
	if quick {
		b = wire.AppendVarint(b, 5, 1)
	}
	return b
}

func TestFriendshipGiftConsumesFavoriteAndRestoresExactReplay(t *testing.T) {
	h := newFriendshipHarness(t)
	item := h.item
	item.Count = 1
	request := giftRequest(1, 10, item)
	code, body, handled, err := h.service.Handle("/FriendshipGift", request)
	if err != nil || !handled || code != 614 {
		t.Fatalf("gift %d %v %v", code, handled, err)
	}
	exp, _, _ := wire.Varint(body, 3)
	if exp != 150 {
		t.Fatalf("favorite exp=%d", exp)
	}
	state := h.collection.FriendshipEntries()[friendshipStateKey(10)].State
	if state.Level != 2 || state.EXP != 50 {
		t.Fatalf("state=%+v", state)
	}
	if h.wallet.Snapshot().FreeJewelry != 7 {
		t.Fatal("level currency was not granted")
	}
	if err := h.inventory.CanConsume([]Item{{InvenIndex: item.InvenIndex, ID: 7, Type: 8, Count: 50}}); err == nil {
		t.Fatal("gift stack was not consumed")
	}
	if _, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(1, 20, item)); err == nil {
		t.Fatal("changed request reused sequence")
	}
	var openErr error
	h.collection, openErr = OpenCollectionStore(h.store, []Costume{{InvenIndex: 1, ID: 100, Level: 5}, {InvenIndex: 2, ID: 200}, {InvenIndex: 3, ID: 300}, {InvenIndex: 4, ID: 400}})
	if openErr != nil {
		t.Fatal(openErr)
	}
	h.inventory, openErr = OpenInventory(h.store, &Starter{Version: "2.35.10"})
	if openErr != nil {
		t.Fatal(openErr)
	}
	h.wallet, openErr = OpenWallet(h.store, Currency{})
	if openErr != nil {
		t.Fatal(openErr)
	}
	reopened, openErr := NewFriendshipService(h.service.design, h.service.awake, h.service.potential, h.collection, h.inventory, h.wallet)
	if openErr != nil {
		t.Fatal(openErr)
	}
	reopened.BeginSession("test-session")
	_, replay, _, err := reopened.Handle("/FriendshipGift", request)
	if err != nil || !bytes.Equal(body, replay) {
		t.Fatalf("durable replay=%v", err)
	}
	if h.wallet.Snapshot().FreeJewelry != 7 {
		t.Fatal("retry granted currency twice")
	}
	_, info, _, err := reopened.Handle("/FriendshipInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	var level uint64
	if err := wire.Walk(info, func(field wire.Field) error {
		if field.Number == 1 {
			id, _, _ := wire.Varint(field.Value, 1)
			if id == 10 {
				level, _, _ = wire.Varint(field.Value, 2)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if level != 2 {
		t.Fatalf("restored level=%d", level)
	}
}

func TestFriendshipRejectsCounterfeitGiftsAndUnownedCostumes(t *testing.T) {
	for _, test := range []struct {
		name   string
		id     uint64
		change func(*Item)
	}{
		{"unowned", 50, func(*Item) {}}, {"fake-index", 10, func(i *Item) { i.InvenIndex++ }}, {"fake-type", 10, func(i *Item) { i.Type = 7 }}, {"fake-id", 10, func(i *Item) { i.ID = 8 }}, {"too-many", 10, func(i *Item) { i.Count = 51 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFriendshipHarness(t)
			item := h.item
			item.Count = 1
			test.change(&item)
			_, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(1, test.id, item))
			if err == nil {
				t.Fatal("accepted counterfeit")
			}
			if len(h.collection.FriendshipEntries()) != 0 || h.wallet.Snapshot().FreeJewelry != 0 {
				t.Fatal("rejected gift mutated state")
			}
			if err := h.inventory.CanConsume([]Item{h.item}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFriendshipCounselingDailyLimitsChoicesAndUTCRollover(t *testing.T) {
	h := newFriendshipHarness(t)
	_, body, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(1, 10, 1, 0, false))
	if err != nil {
		t.Fatal(err)
	}
	correct, _, _ := wire.Varint(body, 3)
	exp, _, _ := wire.Varint(body, 4)
	if correct != 1 || exp != 100 {
		t.Fatalf("correct=%d exp=%d", correct, exp)
	}
	if h.wallet.Snapshot().FreeJewelry != 20 {
		t.Fatal("level plus counseling currency incorrect")
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(2, 10, 2, 0, false)); err == nil {
		t.Fatal("same costume counseled twice")
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(2, 20, 99, 0, false)); err == nil {
		t.Fatal("unknown session accepted")
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(2, 20, 1, 2, false)); err == nil {
		t.Fatal("unknown choice accepted")
	}
	_, body, _, err = h.service.Handle("/FriendshipCounseling", counselingRequest(2, 20, 1, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	exp, _, _ = wire.Varint(body, 4)
	correct, _, _ = wire.Varint(body, 3)
	if exp != 80 || correct != 0 {
		t.Fatal("incorrect choice got correct exp")
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(3, 30, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	ap, _ := h.service.FriendshipAP()
	if ap != 0 {
		t.Fatalf("ap=%d", ap)
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(4, 40, 1, 0, false)); err == nil {
		t.Fatal("fourth account AP accepted")
	}
	h.clock = h.clock.Add(2 * time.Minute)
	ap, _ = h.service.FriendshipAP()
	if ap != 3 {
		t.Fatalf("rollover ap=%d", ap)
	}
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(4, 10, 2, 1, false)); err != nil {
		t.Fatal(err)
	}
	state := h.collection.FriendshipEntries()[friendshipStateKey(10)].State
	if len(state.Sessions) != 2 || state.LastCounselingDate != uint64(h.clock.UnixMilli()) {
		t.Fatalf("counsel state=%+v", state)
	}
}

func TestFriendshipQuickUnlockAndAwakeningPotentialGates(t *testing.T) {
	h := newFriendshipHarness(t)
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(1, 10, 1, 1, true)); err == nil {
		t.Fatal("locked quick accepted")
	}
	state := FriendshipState{CostumeID: 10, Level: 1, Sessions: []uint64{1, 2}, LastCounselingDate: uint64(h.clock.Add(-24 * time.Hour).UnixMilli()), CounselingDay: h.clock.Add(-24 * time.Hour).Format("2006-01-02"), CounselingCount: 1}
	if err := h.collection.ApplyFriendship(state, nil, "reply:"+string(bytes.Repeat([]byte("a"), 64)), FriendshipReply{Digest: string(bytes.Repeat([]byte("b"), 64)), Code: 613, Body: wire.AppendBytes(nil, 2, friendshipWire(state))}); err != nil {
		t.Fatal(err)
	}
	_, body, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(1, 10, 3, 1, true))
	if err != nil {
		t.Fatal(err)
	}
	correct, _, _ := wire.Varint(body, 3)
	if correct != 1 {
		t.Fatal("quick compared fixed choice1 with normal correct choice0")
	}
	state = *h.collection.FriendshipEntries()[friendshipStateKey(10)].State
	if len(state.Sessions) != 2 {
		t.Fatal("quick added a new completed story")
	}
	costume, _ := h.collection.CostumeByID(100)
	if max := h.service.maximum(costume); max != 3 {
		t.Fatalf("+5 incorrectly unlocked max=%d", max)
	}
	if err := h.collection.UpdateCharAwake(1, CharAwakeProgress{}, CharAwakeProgress{IsAwake: true, ImprintLevels: [3]uint64{1, 1, 1}}); err != nil {
		t.Fatal(err)
	}
	if max := h.service.maximum(costume); max != 4 {
		t.Fatalf("awake max=%d", max)
	}
	if err := h.collection.ActivateCostumePotential(costume.InvenIndex, []uint64{71, 72}); err != nil {
		t.Fatal(err)
	}
	costume, _ = h.collection.CostumeByID(100)
	if max := h.service.maximum(costume); max != 5 {
		t.Fatalf("potential max=%d", max)
	}
}

func TestFriendshipGiftCapDiscardsOverflowAndPaysEachReachedLevel(t *testing.T) {
	h := newFriendshipHarness(t)
	h.service.design.Levels[gamedata.FriendshipKey{GroupID: 10, ID: 3}] = gamedata.FriendshipLevelDesign{NextEXP: 100, Rewards: []gamedata.Reward{{Type: 47, ID: 900, Count: 1}}}
	item := h.item
	item.Count = 5
	_, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(1, 10, item))
	if err != nil {
		t.Fatal(err)
	}
	state := h.collection.FriendshipEntries()[friendshipStateKey(10)].State
	if state.Level != 3 || state.EXP != 0 {
		t.Fatalf("gate did not discard overflow %+v", state)
	}
	var rewards []Item
	for _, entry := range h.inventory.owned.Items {
		if entry.Type == 47 {
			rewards = append(rewards, entry)
		}
	}
	if len(rewards) != 1 || rewards[0].ID != 900 || rewards[0].Count != 1 {
		t.Fatalf("cap level ID card reward %+v", rewards)
	}
	item.Count = 1
	if _, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(2, 10, item)); err == nil {
		t.Fatal("gift accepted at locked gate")
	}
	if err := h.collection.UpdateCharAwake(1, CharAwakeProgress{}, CharAwakeProgress{IsAwake: true, ImprintLevels: [3]uint64{1, 1, 1}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(2, 10, item)); err != nil {
		t.Fatal(err)
	}
	state = h.collection.FriendshipEntries()[friendshipStateKey(10)].State
	if state.Level != 4 || state.EXP != 0 {
		t.Fatalf("awake gate state %+v", state)
	}
}

func TestFriendshipFinalCapStoryPlaybackDoesNotConsumeAPOrRepeatReward(t *testing.T) {
	h := newFriendshipHarness(t)
	if err := h.collection.UpdateCharAwake(1, CharAwakeProgress{}, CharAwakeProgress{IsAwake: true, ImprintLevels: [3]uint64{1, 1, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := h.collection.ActivateCostumePotential(1, []uint64{71, 72}); err != nil {
		t.Fatal(err)
	}
	item := h.item
	item.Count = 5
	if _, _, _, err := h.service.Handle("/FriendshipGift", giftRequest(1, 10, item)); err != nil {
		t.Fatal(err)
	}
	_, body, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(2, 10, 1, 0, false))
	if err != nil {
		t.Fatal(err)
	}
	exp, _, _ := wire.Varint(body, 4)
	if exp != 0 {
		t.Fatal("final cap counseling gained exp")
	}
	before := h.wallet.Snapshot().FreeJewelry
	if _, _, _, err := h.service.Handle("/FriendshipCounseling", counselingRequest(3, 10, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	if before != 20 || h.wallet.Snapshot().FreeJewelry != before {
		t.Fatal("final cap story reward repeated")
	}
	ap, _ := h.service.FriendshipAP()
	if ap != 3 {
		t.Fatalf("final cap playback consumed ap=%d", ap)
	}
}

func TestFriendshipRejectsCorruptSavedProgress(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*FriendshipState)
	}{
		{"date", func(s *FriendshipState) {
			s.LastCounselingDate = 1
			s.CounselingDay = "2026-10-01"
			s.CounselingCount = 1
		}},
		{"experience", func(s *FriendshipState) { s.EXP = 100 }},
		{"session", func(s *FriendshipState) { s.Sessions = []uint64{999} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFriendshipHarness(t)
			state := FriendshipState{CostumeID: 10, Level: 1}
			test.mutate(&state)
			h.collection.data.Friendships[friendshipStateKey(10)] = FriendshipEntry{State: &state}
			if err := validateFriendshipEntries(h.collection.data.Friendships); err != nil {
				return
			}
			if _, err := NewFriendshipService(h.service.design, h.service.awake, h.service.potential, h.collection, h.inventory, h.wallet); err == nil {
				t.Fatal("accepted corrupt progress")
			}
		})
	}
}
