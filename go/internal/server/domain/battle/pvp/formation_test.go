package pvp

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/protocol/wire"
)

// Current GameData has different Colosseum grid and party sizes per rule
// group: group 24 is 5x4 / six members, group 26 is 5x5 / seven members.
func TestPVPFormationUsesSeasonRulesWithoutChangingOrdinaryMirrorLimit(t *testing.T) {
	service := &Service{rules: &Rules{Design: &gamedata.PvpDesign{
		ColosseumSeasons: map[uint64]uint64{46: 24, 48: 26},
		ColosseumRules: map[uint64]map[uint64][]uint64{
			24: {1: {5, 4}, 2: {6}},
			26: {1: {5, 5}, 2: {7}},
		},
	}}}
	var deck [][]byte
	for index := uint64(1); index <= 7; index++ {
		raw := wire.AppendVarint(nil, 1, index)
		raw = wire.AppendVarint(raw, 2, 17+index)
		raw = wire.AppendVarint(raw, 3, index)
		deck = append(deck, wire.AppendVarint(raw, 4, 100+index))
	}
	if err := service.validateDeck(Colosseum, 48, deck); err != nil {
		t.Fatalf("seven-member season rejected its last slot/grid cell: %v", err)
	}
	if err := service.validateDeck(Colosseum, 46, deck[:3]); err == nil {
		t.Fatal("5x4 season accepted position 20")
	}
	// Keep every position inside both grids so capacity failures cannot be
	// hidden by the independent grid-bound check above.
	for index := range deck {
		deck[index] = wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(index+1)), 2, uint64(index)), 3, uint64(index+1)), 4, uint64(101+index))
	}
	if err := service.validateDeck(Colosseum, 46, deck[:6]); err != nil {
		t.Fatalf("six-member season rejected its legal capacity: %v", err)
	}
	if err := service.validateDeck(Colosseum, 46, deck); err == nil {
		t.Fatal("six-member season accepted seven members")
	}
	if err := service.validateDeck(Mirror, 0, deck[:5]); err != nil {
		t.Fatalf("ordinary mirror rejected five members: %v", err)
	}
	if err := service.validateDeck(Mirror, 0, deck[:6]); err == nil {
		t.Fatal("ordinary mirror formation inherited Colosseum's capacity")
	}
}
