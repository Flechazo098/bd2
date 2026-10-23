//go:build integration

package app

import "testing"

// PackTable in GameData 20261021182514 (tools/python/gamedata_db.py): 1001 is
// Define_PackType 1 (Char), 2001 is 6 (Event), 3009 is 11 (Square), 3012 is
// 14 (Life). All four open with the Edge of Dimensions ticket.
func TestFieldPacksIncludeTicketGatedContentCards(t *testing.T) {
	g := newMissionGameplay(t)
	packs := g.owner.assembly.design.world.FieldPacks
	for id, want := range map[int]uint64{1001: 1, 2001: 6, 3009: 11, 3012: 14} {
		pack, found := packs[id]
		if !found || pack.Type != want {
			t.Fatalf("pack %d = %+v (found %v), want type %d", id, pack, found, want)
		}
		if !pack.HasOpenRule || pack.TicketID != 11 {
			t.Fatalf("pack %d open rule = %+v, want ticket 11", id, pack)
		}
	}
}
