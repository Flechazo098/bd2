package player

import (
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func TestBattleRewardPersistsWithoutDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.json")
	starter := &Starter{Version: "2.35.10"}
	inv, err := OpenInventory(testStore(path), starter)
	if err != nil {
		t.Fatal(err)
	}
	reward := []gamedata.BattleReward{{Type: 8, ID: 8, Count: 3}}
	items, err := inv.GrantOnce("pack21:monster1", reward)
	if err != nil || len(items) != 1 || items[0].Count != 3 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	inv, err = OpenInventory(testStore(path), starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err = inv.GrantOnce("pack21:monster1", reward)
	if err != nil || len(items) != 0 {
		t.Fatalf("duplicate grant=%+v err=%v", items, err)
	}
	code, response, ok, err := inv.Handle("/ItemInfo", wire.AppendVarint(nil, 1, 77))
	if err != nil || !ok || code != 21 {
		t.Fatalf("response=%d ok=%v err=%v", code, ok, err)
	}
	item, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatal("reward missing from inventory")
	}
	got, _, _ := wire.Varint(item, 2)
	if got != 8 {
		t.Fatalf("item id=%d", got)
	}
}

func TestSkinGrantSkipsOwnedAndBatchDuplicatesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.json")
	starter := &Starter{Version: "2.35.10", Items: []Item{{InvenIndex: 1, ID: 4001, Type: 45, Count: 1}}}
	inv, err := OpenInventory(testStore(path), starter)
	if err != nil {
		t.Fatal(err)
	}
	if items, err := inv.GrantOnce("first", []gamedata.BattleReward{{Type: 45, ID: 4091, Count: 1}}); err != nil || len(items) != 1 {
		t.Fatalf("first grant=%+v err=%v", items, err)
	}
	rewards := []gamedata.BattleReward{{Type: 45, ID: 4001, Count: 1}, {Type: 45, ID: 4091, Count: 1}, {Type: 45, ID: 4092, Count: 1}, {Type: 45, ID: 4092, Count: 1}, {Type: 8, ID: 8, Count: 3}}
	items, err := inv.GrantOnce("mixed", rewards)
	if err != nil || len(items) != 2 || items[0].ID != 4092 || items[1].Type != 8 {
		t.Fatalf("mixed grant=%+v err=%v", items, err)
	}
	inv, err = OpenInventory(testStore(path), starter)
	if err != nil {
		t.Fatal(err)
	}
	if items, err := inv.GrantOnce("mixed", rewards); err != nil || len(items) != 0 {
		t.Fatalf("replay grant=%+v err=%v", items, err)
	}
	if prior := inv.GrantedItems("mixed"); len(prior) != 2 || prior[0].ID != 4092 {
		t.Fatalf("replay items=%+v", prior)
	}
	if items, err := inv.GrantOnce("owned-only", rewards[:2]); err != nil || len(items) != 0 {
		t.Fatalf("owned-only grant=%+v err=%v", items, err)
	}
	inv, err = OpenInventory(testStore(path), starter)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.WasGranted("owned-only") || len(inv.GrantedItems("owned-only")) != 0 || len(inv.owned.Items) != 3 {
		t.Fatalf("owned-only identity or inventory changed: %+v", inv.owned)
	}
}

func TestUseRandomBoxPersistsExactStackAndRewardFromInstalledGameData(t *testing.T) {
	root := os.Getenv("BD2_TEST_GAMEDATA_ROOT")
	if root == "" {
		t.Skip("set BD2_TEST_GAMEDATA_ROOT for installed GameData integration test")
	}
	design, err := gamedata.LoadRandomBoxDesign(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "items.json")
	inv, err := OpenInventory(testStore(path), &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.AttachRandomBoxes(design); err != nil {
		t.Fatal(err)
	}
	boxes, err := inv.GrantOnce("mail:box", []gamedata.BattleReward{{Type: 9, ID: 433302, Count: 100000}})
	if err != nil || len(boxes) != 1 {
		t.Fatalf("grant box = %+v, %v", boxes, err)
	}
	request := wire.AppendVarint(nil, 1, 73)
	request = wire.AppendVarint(request, 2, boxes[0].InvenIndex)
	request = wire.AppendVarint(request, 3, 100000)
	code, response, handled, err := inv.Handle("/UseRandomBox", request)
	if err != nil || !handled || code != 143 {
		t.Fatalf("UseRandomBox code=%d handled=%t err=%v", code, handled, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("UseRandomBox reward bundle missing: found=%t err=%v", found, err)
	}
	item, found, err := wire.Bytes(bundle, 1)
	if err != nil || !found {
		t.Fatalf("UseRandomBox ItemDBInfo missing: found=%t err=%v", found, err)
	}
	id, _, _ := wire.Varint(item, 2)
	typ, _, _ := wire.Varint(item, 3)
	count, _, _ := wire.Varint(item, 4)
	if id != 704 || typ != 8 || count != 100000 {
		t.Fatalf("UseRandomBox response item id=%d type=%d count=%d", id, typ, count)
	}
	all := inv.All()
	if len(all) != 1 || all[0].ID != 704 || all[0].Type != 8 || all[0].Count != 100000 {
		t.Fatalf("UseRandomBox persisted inventory=%+v", all)
	}
	restored, err := OpenInventory(testStore(path), &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	all = restored.All()
	if len(all) != 1 || all[0].ID != 704 || all[0].Count != 100000 {
		t.Fatalf("UseRandomBox restored inventory=%+v", all)
	}
}
