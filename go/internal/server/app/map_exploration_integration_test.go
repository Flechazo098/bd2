//go:build integration

package app

import (
	"encoding/binary"
	"slices"
	"strconv"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
)

// MapPacket and MapFogController use eight ordered hexadecimal row masks;
// PackInGameInfo clears the local cache before restoring field 10.
func TestMapFogKeepsRevealedCellsAcrossAreaChangesAndAccountReopen(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	players := make([]*trapPlayerScenario, 2)
	for i, account := range accounts {
		s := &trapPlayerScenario{factory: factory, account: account, owner: openNicknamePlayer(t, factory, account)}
		players[i] = s
		t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
		s.run(t, true, func(ctx command.Context, p *playerAssembly) {
			if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
				if _, err := p.ownedItems.GrantOnce(ctx, "fog-pack-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	a, b := players[0], players[1]
	reveal := func(s *trapPlayerScenario, pack, mapID uint64, rows ...string) {
		t.Helper()
		request := wire.AppendVarint(trapRequest(uint64(s.commands+1), pack), 3, mapID)
		for _, row := range rows {
			request = wire.AppendString(request, 4, row)
		}
		s.protocol(t, "/ActiveMap", request)
	}
	assertCells := func(s *trapPlayerScenario, pack uint64, expected map[uint64][]int) {
		t.Helper()
		entry := s.protocol(t, "/PackInGameInfo", trapRequest(uint64(s.commands+1), pack))
		maps := appRows(entry, 10)
		if len(maps) != len(expected) {
			t.Fatalf("field entry restored %d explored maps, want %d", len(maps), len(expected))
		}
		seen := map[uint64]bool{}
		for _, m := range maps {
			id := appValue(m, 1)
			wanted, exists := expected[id]
			if !exists || seen[id] {
				t.Fatalf("field entry restored unrelated or duplicated map %d", id)
			}
			seen[id] = true
			rows := appRows(m, 2)
			if len(rows) != 8 {
				t.Fatal("native fog renderer requires all eight rows, including empty rows")
			}
			var cells []int
			for r, text := range rows {
				var mask uint64
				if len(text) != 0 {
					var err error
					mask, err = strconv.ParseUint(string(text), 16, 8)
					if err != nil {
						t.Fatal(err)
					}
				}
				for column := range 8 {
					if mask&(1<<column) != 0 {
						cells = append(cells, r*8+column)
					}
				}
			}
			if !slices.Equal(cells, wanted) {
				t.Fatalf("map %d restored explored cells %v, want %v", id, cells, wanted)
			}
		}
	}
	assertCells(a, 5, nil)
	reveal(a, 5, 55, "1", "", "", "", "1", "", "", "")
	reveal(a, 5, 55, "3", "", "80", "", "1", "", "", "")
	reveal(a, 5, 57, "", "2", "", "", "", "", "", "")
	assertCells(a, 1, nil)
	// The older upload arrives after the newer snapshot and after leaving the pack.
	reveal(a, 5, 55, "1", "", "", "", "1", "", "", "")
	wantA := map[uint64][]int{55: {0, 1, 23, 32}, 57: {9}}
	assertCells(a, 5, wantA)
	assertCells(b, 5, nil)
	reveal(b, 5, 55, "", "", "", "", "", "", "", "80")
	for _, s := range players {
		closeNicknamePlayer(t, s.owner)
		s.owner = openNicknamePlayer(t, factory, s.account)
	}
	assertCells(a, 5, wantA)
	assertCells(b, 5, map[uint64][]int{55: {63}})
	assertCells(a, 1, nil)
	assertCells(a, 5, wantA)
}

// Current GameData pack5 has seven research points in map55, eight in map57,
// and six one-time chests in map52/group23. Native MapUI filters researched IDs
// by scene; PackMapRewardInfo builds map rows from PackDetailInfo's object IDs.
func TestExplorationDetailKeepsInteriorResearchAndChestMapsSeparateAfterRejoin(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0]}
	s.owner = openNicknamePlayer(t, factory, s.account)
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if rule := p.design.world.Story.Packs[5].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "map-exploration-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
	})
	s.protocol(t, "/PackInGameInfo", trapRequest(1, 5))
	assertMaps := func(research55, research57, chests52 uint64) {
		t.Helper()
		detail := s.protocol(t, "/PackDetailInfo", trapRequest(10, 5))
		var got55, got57, got52 uint64
		checkResearch := func(id uint64) {
			switch id {
			case 5501, 5502:
				got55++
			case 5601:
				got57++
			default:
				t.Fatalf("unvisited research point leaked into detail:%d", id)
			}
		}
		if err := wire.Walk(detail, func(f wire.Field) error {
			if f.Number != 3 {
				return nil
			}
			for raw := f.Value; len(raw) > 0; {
				id, size := binary.Uvarint(raw)
				if size <= 0 {
					t.Fatal("invalid research ID encoding")
				}
				checkResearch(id)
				raw = raw[size:]
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, row := range appRows(detail, 2) {
			if id := appValue(row, 1); id == 5240 {
				got52++
			} else {
				t.Fatalf("unopened chest leaked into detail:%d", id)
			}
		}
		if got55 != research55 || got57 != research57 || got52 != chests52 {
			t.Fatalf("map completion55=%d/7 57=%d/8 chest52=%d/6; want%d %d %d", got55, got57, got52, research55, research57, chests52)
		}
		summary := s.protocol(t, "/PackSummaryInfoList", wire.AppendVarint(nil, 1, 11))
		found := false
		for _, row := range appRows(summary, 1) {
			if appValue(row, 1) == 5 {
				found = true
				if appValue(row, 2) != chests52 || appValue(row, 5) != research55+research57 {
					t.Fatalf("pack summary disagrees with separate map progress:%x", row)
				}
			}
		}
		if !found {
			t.Fatal("chapter missing from exploration summary")
		}
	}
	assertMaps(0, 0, 0)
	s.position(t, 5, 55)
	s.protocol(t, "/FieldObjectResearch", wire.AppendVarint(trapRequest(2, 5), 3, 5501))
	assertMaps(1, 0, 0)
	s.protocol(t, "/FieldObjectResearch", wire.AppendVarint(trapRequest(3, 5), 3, 5502))
	s.position(t, 5, 57)
	s.protocol(t, "/FieldObjectResearch", wire.AppendVarint(trapRequest(4, 5), 3, 5601))
	s.position(t, 5, 52)
	s.protocol(t, "/FieldObjectReward", wire.AppendVarint(wire.AppendVarint(trapRequest(5, 5), 3, 23), 4, 5240))
	assertMaps(2, 1, 1)
	closeNicknamePlayer(t, s.owner)
	s.owner = openNicknamePlayer(t, factory, s.account)
	s.protocol(t, "/PackInGameInfo", trapRequest(6, 5))
	field := s.protocol(t, "/FieldObjectInfo", trapRequest(7, 5))
	if rows := appRows(field, 1); len(rows) != 1 || appValue(rows[0], 1) != 5240 {
		t.Fatal("native field entry did not restore the opened chest")
	}
	assertMaps(2, 1, 1)
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if err := p.progressState.SelectQuest(ctx, 5, progress.QuestSelection{Difficulty: 2}); err != nil {
			t.Fatal(err)
		}
	})
	assertMaps(2, 1, 1)
}
