package pvp

import (
	"strings"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// GameData 20261021182514: ColosseumSeasonTable gives season 47 rule group 25
// and season 46 group 24; only groups 25/26 carry rule 32
// (C_RULE_COSTUME_COST_LIMIT) with value 160. Unranked costumes cost 30.
func TestColosseumFormationRejectsCostumeCostAboveSeasonLimit(t *testing.T) {
	ctx := command.Context{Identity: command.Identity{AccountID: "a"}, State: stateio.NewMemory()}
	var characters []roster.Character
	var costumes []roster.Costume
	for i := uint64(1); i <= 4; i++ {
		characters = append(characters, roster.Character{InvenIndex: i, ID: 100 + i, Level: 1})
		costumes = append(costumes, roster.Costume{InvenIndex: 10 + i, ID: 1000 + i, UseChar: i})
	}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	characterStore, err := roster.OpenCharacterStore(ctx, stateio.RootStore{}, characters, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, costumes)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := assets.OpenEquipmentInventory(ctx, stateio.RootStore{})
	if err != nil {
		t.Fatal(err)
	}
	if err = characterStore.AttachCollection(ctx, collection); err != nil {
		t.Fatal(err)
	}
	if err = collection.BindBaseCharacters(ctx, characters); err != nil {
		t.Fatal(err)
	}
	if err = equipment.AttachCharacters(ctx, characterStore); err != nil {
		t.Fatal(err)
	}
	if err = equipment.AttachSlots(ctx, map[uint64]uint64{700: 0}); err != nil {
		t.Fatal(err)
	}
	builder := &OwnedSnapshotBuilder{
		Design: &gamedata.PvpDesign{
			Colosseum:        gamedata.ColosseumDefaults{CostDefault: 30},
			ColosseumSeasons: map[uint64]uint64{46: 24, 47: 25},
			ColosseumRules:   map[uint64]map[uint64][]uint64{24: {}, 25: {32: {160}}},
		},
		Characters: characterStore, Collection: collection, Equipment: equipment, OwnerIndex: 1,
		Buffs: func(command.Context) ([]gamedata.PictorialBuffStat, error) { return nil, nil },
		CostumeCosts: func(command.Context) (map[uint64]uint64, error) {
			return map[uint64]uint64{1001: 50, 1002: 50, 1003: 50}, nil
		},
	}
	deck := func(indices ...uint64) [][]byte {
		var out [][]byte
		for _, index := range indices {
			out = append(out, wire.AppendVarint(wire.AppendVarint(nil, 1, index), 4, 10+index))
		}
		return out
	}
	if _, err := builder.Freeze(ctx, FreezeRequest{Mode: Colosseum, Season: 47, Deck: deck(1, 2, 4)}); err != nil {
		t.Fatalf("cost 130 within limit 160 rejected: %v", err)
	}
	if _, err := builder.Freeze(ctx, FreezeRequest{Mode: Colosseum, Season: 47, Deck: deck(1, 2, 3, 4)}); err == nil || !strings.Contains(err.Error(), "exceeds season limit") {
		t.Fatalf("cost 180 above limit 160 accepted: %v", err)
	}
	if _, err := builder.Freeze(ctx, FreezeRequest{Mode: Colosseum, Season: 46, Deck: deck(1, 2, 3, 4)}); err != nil {
		t.Fatalf("season without cost rule enforced a limit: %v", err)
	}
}
