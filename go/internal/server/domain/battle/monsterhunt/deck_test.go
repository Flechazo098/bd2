package monsterhunt

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

// MonsterHuntPacket's two SendDeckSaveRequest overloads send either team
// formations or per-character action settings, never both as a full snapshot.
func TestMonsterHuntPartialDeckSavesPreserveThreeTeamsAndActionSettings(t *testing.T) {
	ctx := command.Context{State: stateio.NewMemory(), Identity: command.Identity{AccountID: "hunter", SessionID: "session"}}
	inventory, err := assets.OpenInventory(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	characters, err := roster.OpenCharacterStore(ctx, stateio.RootStore{}, []roster.Character{
		{InvenIndex: 1, ID: 101, Level: 1}, {InvenIndex: 2, ID: 102, Level: 1}, {InvenIndex: 3, ID: 103, Level: 1},
	}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		storage: stateio.RootStore{}, characters: characters,
		seasons: []Season{{ID: 1, Hunt: 1, End: 2000}}, now: func() time.Time { return time.UnixMilli(1000) },
		state: snapshot{Version: versionconfig.State(), Users: map[string]user{"1": {Season: 1, Hunt: 1}}, Presets: map[string][]byte{}, Replies: map[string]reply{}},
	}
	var seq uint64
	save := func(fields []byte) snapshot {
		t.Helper()
		seq++
		_, _, handled, err := service.Handle(ctx, "/MonsterHuntDeckSave", append(wire.AppendVarint(nil, 1, seq), fields...))
		if err != nil || !handled {
			t.Fatalf("save handled=%t: %v", handled, err)
		}
		raw, err := service.storage.Load(ctx.State, "monsterhunt")
		if err != nil {
			t.Fatal(err)
		}
		var persisted snapshot
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		return persisted
	}
	team := func(index uint64) []byte {
		member := wire.AppendVarint(wire.AppendVarint(nil, 1, index), 3, 1)
		return wire.AppendBytes(wire.AppendVarint(nil, 1, index), 2, member)
	}
	var initial []byte
	for index := uint64(1); index <= 3; index++ {
		initial = wire.AppendBytes(initial, 2, team(index))
	}
	save(initial)
	setting := func(index, mode uint64) []byte {
		raw := wire.AppendVarint(nil, 1, index)
		raw = wire.AppendBytes(raw, 2, nil)
		return wire.AppendVarint(raw, 3, mode)
	}
	first, second := setting(1, BattleMode), setting(2, PracticeMode)
	save(wire.AppendBytes(nil, 3, first))
	persisted := save(wire.AppendBytes(nil, 3, second))
	if len(persisted.Decks) != 3 || len(persisted.Settings) != 2 || !bytes.Equal(persisted.Settings[0], first) || !bytes.Equal(persisted.Settings[1], second) {
		t.Fatal("partial action save erased teams or another character's settings")
	}
	for index := uint64(1); index <= 3; index++ {
		if !bytes.Equal(persisted.Decks[index-1], team(index)) {
			t.Fatalf("team %d changed on setting-only save", index)
		}
	}
	persisted = save(wire.AppendBytes(nil, 2, team(2)))
	if len(persisted.Decks) != 1 || len(persisted.Settings) != 2 || !bytes.Equal(persisted.Decks[0], team(2)) {
		t.Fatal("formation save did not replace teams while preserving action settings")
	}
	duplicate := wire.AppendBytes(team(1), 2, wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1), 3, 1))
	if err := service.validateDecks(ctx, [][]byte{duplicate}); err == nil {
		t.Fatal("two team members were allowed to share an action sequence")
	}
}
