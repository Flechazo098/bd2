package player

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

func TestCharImmortalReturnsFullOwnedSnapshot(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	owned := []Character{
		{InvenIndex: 535604120, ID: 6010, HP: 7, Level: 1, TalentLevel: 1},
		{InvenIndex: 535607162, ID: 350, HP: 512, Level: 20, TalentLevel: 1},
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), owned, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	packed := binary.AppendUvarint(nil, owned[0].InvenIndex)
	request := wire.AppendBytes(wire.AppendVarint(nil, 1, 23), 2, packed)
	code, response, ok, err := characters.Handle("/CharImmortal", request)
	if err != nil || !ok || code != 96 {
		t.Fatalf("immortal: code=%d handled=%v err=%v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing revived character: %v", err)
	}
	index, _, _ := wire.Varint(encoded, 1)
	hp, _, _ := wire.Varint(encoded, 3)
	if index != owned[0].InvenIndex || hp != 7 {
		t.Fatalf("revived character index=%d hp=%d", index, hp)
	}
	for _, invalid := range [][]byte{
		wire.AppendVarint(wire.AppendVarint(nil, 1, 24), 2, 999),
		wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 25), 2, owned[0].InvenIndex), 2, owned[0].InvenIndex),
		wire.AppendVarint(nil, 1, 26),
	} {
		if _, _, handled, err := characters.Handle("/CharImmortal", invalid); err == nil || !handled {
			t.Fatalf("invalid immortal request accepted: handled=%v err=%v", handled, err)
		}
	}
}

func TestCharacterGrowthConsumesMaterialAndPersists(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("battle", []gamedata.BattleReward{{Type: 8, ID: 8, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	characters.grow = func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return 20, 0, []gamedata.GrowthMaterial{{ID: 7, Count: 6}}, nil
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 77)
	material := ItemWire(items[0])
	request = wire.AppendBytes(request, 3, material)
	code, response, ok, err := characters.Handle("/CharGrowth", request)
	if err != nil || !ok || code != 433 {
		t.Fatalf("growth: code=%d ok=%v err=%v", code, ok, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("character response: %v", err)
	}
	level, found, err := wire.Varint(encoded, 4)
	if err != nil || !found || level != 20 {
		t.Fatalf("level=%d found=%v err=%v", level, found, err)
	}
	restored, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 1 || got[0].Level != 20 {
		t.Fatalf("restored=%+v", got)
	}
	_, itemResponse, _, err := inventory.Handle("/ItemInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	owned, found, err := wire.Bytes(itemResponse, 1)
	if err != nil || !found {
		t.Fatalf("refund absent: found=%v err=%v", found, err)
	}
	id, _, _ := wire.Varint(owned, 2)
	count, _, _ := wire.Varint(owned, 4)
	if id != 7 || count != 6 {
		t.Fatalf("wrong refund id=%d count=%d", id, count)
	}
	bundle, found, err := wire.Bytes(response, 2)
	if err != nil || !found {
		t.Fatalf("missing growth reward bundle: %v", err)
	}
	reward, found, err := wire.Bytes(bundle, 1)
	if err != nil || !found {
		t.Fatalf("missing reward item: %v", err)
	}
	rewardID, _, _ := wire.Varint(reward, 2)
	if rewardID != 7 {
		t.Fatalf("reward id=%d", rewardID)
	}
	if err := inventory.Consume(items); err == nil {
		t.Fatal("already spent growth material accepted again")
	}
}

func TestGrowthAndImmortalShareDynamicMaximumHealth(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("growth", []gamedata.BattleReward{{Type: 8, ID: 8, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, HP: 122, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	characters.grow = func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return 20, 0, nil, nil
	}
	if err := characters.AttachMaxHealth(func(character Character) (uint64, error) {
		// The callback must be safe to query account ownership while growth
		// changes the same CharacterStore.
		if len(characters.RawAll()) != 1 {
			t.Fatal("owned character disappeared during health calculation")
		}
		if character.Level == 20 {
			return 513, nil
		}
		return 122, nil
	}); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 11), 2, 77)
	request = wire.AppendBytes(request, 3, ItemWire(items[0]))
	_, response, _, err := characters.Handle("/CharGrowth", request)
	if err != nil {
		t.Fatal(err)
	}
	grown, _, err := wire.Bytes(response, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hp, _, _ := wire.Varint(grown, 3); hp != 513 {
		t.Fatalf("grown HP=%d want 513", hp)
	}
	immortal := wire.AppendVarint(wire.AppendVarint(nil, 1, 12), 2, 77)
	_, response, _, err = characters.Handle("/CharImmortal", immortal)
	if err != nil {
		t.Fatal(err)
	}
	revived, _, err := wire.Bytes(response, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hp, _, _ := wire.Varint(revived, 3); hp != 513 {
		t.Fatalf("revived HP=%d want 513", hp)
	}
	data, err := os.ReadFile(filepath.Join(dir, "characters.json"))
	if err != nil || !bytes.Contains(data, []byte(`"hp":513`)) {
		t.Fatalf("growth HP was not persisted: %s err=%v", data, err)
	}
}

func TestCharacterPromotionUsesExactGameDataCosts(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("promotion-material", []gamedata.BattleReward{{Type: 8, ID: 11, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(filepath.Join(dir, "wallet.json"), Currency{Gold: 1500})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, Level: 20, HP: 513}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	characters.promoteGrowth = func(character Character, submitted []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error) {
		if character.ID != 350 || character.Level != 20 {
			return gamedata.PromotionGrowthResult{}, fmt.Errorf("not promotable: %+v", character)
		}
		if !promotionCostsEqual(submitted, []gamedata.PromotionCost{{Type: 8, ID: 11, Count: 1}, {Type: 4, Count: 1000}}) {
			return gamedata.PromotionGrowthResult{}, fmt.Errorf("unexpected submitted costs: %+v", submitted)
		}
		return gamedata.PromotionGrowthResult{CharacterID: 351, Level: 20, Costs: submitted}, nil
	}
	item := items[0]
	item.Count = 1
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77)
	request = wire.AppendBytes(request, 3, ItemWire(item))
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 1000}))
	code, response, handled, err := characters.Handle("/CharGrowth", request)
	if err != nil || !handled || code != 433 {
		t.Fatalf("promote code=%d handled=%v err=%v", code, handled, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing promoted character %v", err)
	}
	if id, _, _ := wire.Varint(encoded, 2); id != 351 {
		t.Fatalf("promoted id=%d", id)
	}
	if snapshot := wallet.Snapshot(); snapshot.Gold != 500 {
		t.Fatalf("gold after promotion=%d", snapshot.Gold)
	}
	loaded, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), nil, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.All(); len(got) != 1 || got[0].ID != 351 {
		t.Fatalf("persisted promotion=%+v", got)
	}
	if err := inventory.Consume([]Item{item}); err != nil {
		t.Fatalf("remaining material x1 should exist: %v", err)
	}
	if _, _, _, err := characters.Handle("/CharGrowth", request); err == nil {
		t.Fatal("duplicate class-up accepted")
	}
}

func TestCharacterGrowthPromotesAndLevelsInOneRequestAcrossStacks(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := inventory.GrantOnce("slime-a", []gamedata.BattleReward{{Type: 8, ID: 9, Count: 7}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := inventory.GrantOnce("slime-b", []gamedata.BattleReward{{Type: 8, ID: 9, Count: 103}})
	if err != nil {
		t.Fatal(err)
	}
	classUp, err := inventory.GrantOnce("class-up", []gamedata.BattleReward{{Type: 8, ID: 12, Count: 3}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(filepath.Join(dir, "wallet.json"), Currency{Gold: 3000})
	if err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 351, Level: 40}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	characters.promoteGrowth = func(character Character, submitted []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error) {
		if character.ID != 351 || character.Level != 40 {
			return gamedata.PromotionGrowthResult{}, fmt.Errorf("unexpected stage: %+v", character)
		}
		if !promotionCostsEqual(submitted, []gamedata.PromotionCost{{Type: 8, ID: 9, Count: 110}, {Type: 8, ID: 12, Count: 2}, {Type: 4, Count: 2000}}) {
			return gamedata.PromotionGrowthResult{}, fmt.Errorf("unexpected submitted costs: %+v", submitted)
		}
		return gamedata.PromotionGrowthResult{CharacterID: 352, Level: 60, Costs: submitted, Refunds: []gamedata.GrowthMaterial{{ID: 7, Count: 1}}}, nil
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77)
	for _, material := range []Item{first[0], second[0], {InvenIndex: classUp[0].InvenIndex, ID: 12, Type: 8, Count: 2}, {Type: 4, Count: 2000}} {
		request = wire.AppendBytes(request, 3, ItemWire(material))
	}
	code, response, handled, err := characters.Handle("/CharGrowth", request)
	if err != nil || !handled || code != 433 {
		t.Fatalf("combined growth code=%d handled=%v err=%v", code, handled, err)
	}
	encoded, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing character: %v", err)
	}
	if id, _, _ := wire.Varint(encoded, 2); id != 352 {
		t.Fatalf("promoted character id=%d", id)
	}
	if level, _, _ := wire.Varint(encoded, 4); level != 60 {
		t.Fatalf("grown level=%d", level)
	}
	if wallet.Snapshot().Gold != 1000 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	if err := inventory.Consume([]Item{first[0]}); err == nil {
		t.Fatal("first experience stack was not consumed")
	}
	if err := inventory.Consume([]Item{second[0]}); err == nil {
		t.Fatal("second experience stack was not consumed")
	}
	if err := inventory.Consume([]Item{{InvenIndex: classUp[0].InvenIndex, ID: 12, Type: 8, Count: 1}}); err != nil {
		t.Fatalf("one unspent class-up material must remain: %v", err)
	}
	if bundle, found, err := wire.Bytes(response, 2); err != nil || !found || len(bundle) == 0 {
		t.Fatalf("missing refunded slime bundle: found=%v err=%v", found, err)
	}
}

func promotionCostsEqual(got, want []gamedata.PromotionCost) bool {
	counts := make(map[[2]uint64]uint64, len(got))
	for _, cost := range got {
		counts[[2]uint64{cost.Type, cost.ID}] += cost.Count
	}
	if len(counts) != len(want) {
		return false
	}
	for _, cost := range want {
		if counts[[2]uint64{cost.Type, cost.ID}] != cost.Count {
			return false
		}
	}
	return true
}

func TestCollectionCharacterCombinedGrowthChangesIDWithoutChargingTwice(t *testing.T) {
	dir := t.TempDir()
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), &Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := inventory.GrantOnce("growth-materials", []gamedata.BattleReward{{Type: 8, ID: 9, Count: 800}, {Type: 8, ID: 11, Count: 1}, {Type: 8, ID: 12, Count: 2}, {Type: 8, ID: 13, Count: 3}, {Type: 8, ID: 14, Count: 4}})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(filepath.Join(dir, "wallet.json"), Currency{Gold: 12000})
	if err != nil {
		t.Fatal(err)
	}
	collectionPath := filepath.Join(dir, "collection.json")
	collection, err := OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	collection.data.Characters = []Character{{InvenIndex: 920000054, ID: 6510, Level: 1}}
	if err := collection.commit(collection.data); err != nil {
		t.Fatal(err)
	}
	characters, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 77, ID: 350, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	if err := characters.AttachCollection(collection); err != nil {
		t.Fatal(err)
	}
	characters.promoteGrowth = func(character Character, submitted []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error) {
		if character.ID != 6510 || character.Level != 1 || !promotionCostsEqual(submitted, []gamedata.PromotionCost{{Type: 8, ID: 9, Count: 753}, {Type: 8, ID: 11, Count: 1}, {Type: 8, ID: 12, Count: 2}, {Type: 8, ID: 13, Count: 3}, {Type: 8, ID: 14, Count: 4}, {Type: 4, Count: 10000}}) {
			return gamedata.PromotionGrowthResult{}, fmt.Errorf("unexpected combined growth: %+v %+v", character, submitted)
		}
		return gamedata.PromotionGrowthResult{CharacterID: 6514, Level: 100, Refunds: []gamedata.GrowthMaterial{{ID: 7, Count: 3}}}, nil
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 920000054)
	for i, material := range items {
		if i == 0 {
			material.Count = 753
		}
		request = wire.AppendBytes(request, 3, ItemWire(material))
	}
	request = wire.AppendBytes(request, 3, ItemWire(Item{Type: 4, Count: 10000}))
	code, _, handled, err := characters.Handle("/CharGrowth", request)
	if err != nil || !handled || code != 433 {
		t.Fatalf("collection promotion code=%d handled=%v err=%v", code, handled, err)
	}
	if got, found := collection.FindCharacter(920000054); !found || got.ID != 6514 || got.Level != 100 {
		t.Fatalf("promoted collection character=%+v found=%v", got, found)
	}
	if wallet.Snapshot().Gold != 2000 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
	if _, _, _, err := characters.Handle("/CharGrowth", request); err == nil {
		t.Fatal("replay of the old promotion was accepted")
	}
	if wallet.Snapshot().Gold != 2000 {
		t.Fatal("replay charged gold again")
	}
	reloaded, err := OpenCollectionStore(collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, found := reloaded.FindCharacter(920000054); !found || got.ID != 6514 || got.Level != 100 {
		t.Fatalf("reloaded promoted collection character=%+v found=%v", got, found)
	}
}

func TestCharacterStoreMergesNewSeedCharacters(t *testing.T) {
	dir := t.TempDir()
	starter := &Starter{Version: "2.34.13"}
	inventory, err := OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	first, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 1, ID: 10, Level: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.persist(first.All()); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenCharacterStore(filepath.Join(dir, "characters.json"), []Character{{InvenIndex: 1, ID: 10, Level: 1}, {InvenIndex: 2, ID: 20, Level: 15}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.All(); len(got) != 2 || got[1].InvenIndex != 2 {
		t.Fatalf("merged=%+v", got)
	}
}
