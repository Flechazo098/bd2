package player

import (
	"strings"
	"testing"

	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestEarnedQuestCostumeMustBeRestoredBeforeBurstValidation(t *testing.T) {
	store := stateio.NewMemory()
	base := []Costume{{InvenIndex: 88, ID: 900}}
	reward := Costume{InvenIndex: 99, ID: 4202, UseChar: 77}
	collection, err := OpenCollectionStore(store, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := collection.AttachRewardCostume(reward); err != nil {
		t.Fatal(err)
	}
	record := CostumeBurstUpgradeRecord{CostumeID: reward.ID, Level: 1, Digest: strings.Repeat("a", 64), Code: 578, Body: wire.AppendVarint(nil, 1, 1)}
	if err := collection.ApplyCostumeBurst(reward.InvenIndex, 0, 1, record); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCollectionStore(store, base); err == nil {
		t.Fatal("accepted burst ledger with incomplete restored ownership")
	}
	reloaded, err := OpenCollectionStore(store, append(base, reward))
	if err != nil {
		t.Fatal(err)
	}
	got, owned := reloaded.CostumeByID(reward.ID)
	if !owned || got.InvenIndex != reward.InvenIndex || got.BurstLevel != 1 {
		t.Fatalf("restored reward costume=%+v owned=%v", got, owned)
	}
	if got, found := reloaded.CostumeBurstReplay(reward.InvenIndex, 1); !found || got.Digest != record.Digest {
		t.Fatal("restart lost burst upgrade replay")
	}
}
