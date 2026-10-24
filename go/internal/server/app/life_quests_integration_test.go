//go:build integration

package app

import (
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

// Entering the farm activates its current Life quest, which the farm HUD and
// the farm's first tutorial depend on; clearing it pays life coins into the
// life economy and moves the HUD to the next quest, also after a reopen.
func TestFarmEntryServesTheLifeQuestChainThroughAPlayerSession(t *testing.T) {
	const farm = 3011
	factory, accounts := newIntegrationFactory(t)
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	s := &trapPlayerScenario{factory: factory, owner: owner, account: accounts[0]}
	t.Cleanup(func() {
		if s.owner != nil {
			if err := s.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	quests := func(body []byte) map[uint64]bool {
		t.Helper()
		out := map[uint64]bool{}
		if err := wire.Walk(body, func(f wire.Field) error {
			if f.Number == 2 && f.Type == 2 {
				id, _, _ := wire.Varint(f.Value, 1)
				pack, _, _ := wire.Varint(f.Value, 6)
				if pack == farm {
					out[id] = true
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	enter := func(ctx command.Context, p *playerAssembly) map[uint64]bool {
		t.Helper()
		_, body, _, err := p.worldService.Handle(ctx, "/PackInGameInfo", trapRequest(100, farm))
		if err != nil {
			t.Fatal(err)
		}
		return quests(body)
	}
	quest := func(id uint64, values ...uint64) []byte {
		request := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 101), 2, id), 3, farm)
		for _, v := range values {
			request = wire.AppendVarint(request, 4, v)
		}
		return request
	}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if ticket := p.design.world.FieldPacks[farm].TicketID; ticket != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "farm-ticket", []gamedata.BattleReward{{Type: 19, ID: ticket, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		if got := enter(ctx, p); len(got) != 1 || !got[1] {
			t.Fatalf("farm entry listed life quests %v, want quest 1", got)
		}
		coins, err := p.lifeService.Coin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := p.worldService.Handle(ctx, "/QuestUpdate", quest(1, 1)); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := p.worldService.Handle(ctx, "/QuestClear", quest(1)); err != nil {
			t.Fatal(err)
		}
		if got, err := p.lifeService.Coin(ctx); err != nil || got != coins+50 {
			t.Fatalf("life coins %d -> %d, want +50 (%v)", coins, got, err)
		}
	})
	s.reopen(t)
	s.run(t, false, func(ctx command.Context, p *playerAssembly) {
		if got := enter(ctx, p); len(got) != 1 || !got[2] {
			t.Fatalf("after quest 1 the farm lists %v, want quest 2", got)
		}
	})
}
