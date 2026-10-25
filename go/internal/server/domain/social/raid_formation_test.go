package social

import (
	"bytes"
	"math"
	"testing"

	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func raidFormationRuntime(t *testing.T) (*Service, command.Context) {
	t.Helper()
	ctx := command.Context{Identity: command.Identity{AccountID: "raider"}, State: stateio.NewMemory()}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var characters []roster.Character
	for index := uint64(1); index <= 8; index++ {
		characters = append(characters, roster.Character{InvenIndex: index, ID: 100 + index, Level: 1})
	}
	owned, err := roster.OpenCharacterStore(ctx, stateio.RootStore{}, characters, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{config: ServiceConfig{Characters: owned, Collection: collection}}, ctx
}

func raidFormationEntry(index, position, sequence uint64) []byte {
	raw := wire.AppendVarint(nil, 1, index)
	raw = wire.AppendVarint(raw, 2, position)
	return wire.AppendVarint(raw, 3, sequence)
}

func raidFormationSave(t *testing.T, service *Service, ctx command.Context, raw []byte) RaidState {
	t.Helper()
	request, err := parsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.raidLocal(ctx, "/GuildRaidDeckSave", request, "save", Guild{}, Season{}, GuildStanding{}); err != nil {
		t.Fatal(err)
	}
	state, err := LoadRaidState(ctx.State)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// GuildRaidPacket.GetMaxDeckCount=7 and ReassignUnifiedSequence starts at 1;
// BattlePlayLibrary gives GuildRaidBoss and its practice mode a 5x5 grid.
func TestGuildRaidSavesSevenMembersIncludingSequenceSevenAndLastGridCell(t *testing.T) {
	service, ctx := raidFormationRuntime(t)
	var raw []byte
	for index := uint64(1); index <= 7; index++ {
		raw = wire.AppendBytes(raw, 2, raidFormationEntry(index, 17+index, index))
	}
	state := raidFormationSave(t, service, ctx, raw)
	if len(state.Deck) != 7 || !bytes.Equal(state.Deck[6], raidFormationEntry(7, 24, 7)) {
		t.Fatalf("last guild member was lost: %x", state.Deck)
	}
	for name, invalid := range map[string][]byte{
		"zero sequence":   raidFormationEntry(1, 0, 0),
		"eighth sequence": raidFormationEntry(1, 0, 8),
		"outside grid":    raidFormationEntry(1, 25, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.validateRaidFormation(ctx, [][]byte{invalid}, nil); err == nil {
				t.Fatal("invalid raid entry accepted")
			}
		})
	}
}

func TestGuildRaidSupporterSharesPositionsAndOneBasedSequencesWithOwnedMembers(t *testing.T) {
	var deck [][]byte
	for index := uint64(1); index <= 6; index++ {
		deck = append(deck, raidFormationEntry(index, index-1, index))
	}
	supporter := func(position, sequence uint64) []byte {
		return wire.AppendVarint(wire.AppendVarint(nil, 3, position), 4, sequence)
	}
	if err := validateCombinedPositions(deck, [][]byte{supporter(24, 7)}); err != nil {
		t.Fatalf("valid seventh supporter rejected: %v", err)
	}
	for name, supporters := range map[string][][]byte{
		"duplicate sequence": {supporter(24, 6)},
		"duplicate position": {supporter(5, 7)},
		"eighth member":      {supporter(23, 7), supporter(24, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCombinedPositions(deck, supporters); err == nil {
				t.Fatal("invalid combined raid formation accepted")
			}
		})
	}
}

// The native setting-only overload sends field 5 without fields 2 or 3.
// ApplyCostumeSetting replaces by character, battle mode and monster ID.
func TestGuildRaidSettingOnlySavePreservesPartyAndOtherActionSettings(t *testing.T) {
	service, ctx := raidFormationRuntime(t)
	deck := raidFormationEntry(1, 24, 7)
	raidFormationSave(t, service, ctx, wire.AppendBytes(nil, 2, deck))
	setting := func(index, mode, monster, action uint64) []byte {
		raw := wire.AppendVarint(nil, 1, index)
		raw = wire.AppendBytes(raw, 2, wire.AppendVarint(nil, 1, action))
		raw = wire.AppendVarint(raw, 3, mode)
		return wire.AppendVarint(raw, 4, monster)
	}
	first := setting(1, RaidBattleMode, 100, 0)
	practice := setting(1, RaidPracticeMode, 100, 0)
	otherBoss := setting(1, RaidBattleMode, 101, 0)
	var raw []byte
	for _, row := range [][]byte{first, practice, otherBoss} {
		raw = wire.AppendBytes(raw, 5, row)
	}
	raidFormationSave(t, service, ctx, raw)
	changed := setting(1, RaidBattleMode, 100, math.MaxUint64)
	outsideParty := setting(8, RaidBattleMode, 100, 0)
	raw = wire.AppendBytes(wire.AppendBytes(nil, 5, changed), 5, outsideParty)
	state := raidFormationSave(t, service, ctx, raw)
	if len(state.Deck) != 1 || !bytes.Equal(state.Deck[0], deck) {
		t.Fatalf("setting-only save replaced party: %x", state.Deck)
	}
	want := [][]byte{changed, practice, otherBoss, outsideParty}
	if len(state.Settings) != len(want) {
		t.Fatalf("settings count=%d, want %d", len(state.Settings), len(want))
	}
	for index, row := range want {
		if !bytes.Equal(state.Settings[index], row) {
			t.Fatalf("setting %d changed: %x, want %x", index, state.Settings[index], row)
		}
	}
	state = raidFormationSave(t, service, ctx, wire.AppendBytes(nil, 2, raidFormationEntry(2, 0, 1)))
	if len(state.Settings) != 4 || !bytes.Equal(state.Settings[0], changed) {
		t.Fatal("formation-only save discarded per-character action settings")
	}
}
