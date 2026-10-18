//go:build integration

package app

import (
	"bytes"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func TestRealCostumeSelectorConsumesTicketAndRestoresChosenCostume(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	s := &trapPlayerScenario{factory: factory, account: accounts[0], owner: openNicknamePlayer(t, factory, accounts[0])}
	t.Cleanup(func() { closeNicknamePlayer(t, s.owner) })
	var selected, index uint64
	for id := range factory.design.resourceItems.Selections[1501] {
		if _, owned := s.owner.assembly.collection.CostumeByID(id); !owned {
			selected = id
			break
		}
	}
	if selected == 0 {
		t.Fatal("selector has no unowned allowed costume")
	}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		items, err := p.ownedItems.GrantOnce(ctx, "selector-earned-ticket", []gamedata.BattleReward{{Type: 8, ID: 1501, Count: 2}})
		if err != nil || len(items) != 1 {
			t.Fatalf("earn selector ticket: %v", err)
		}
		index = items[0].InvenIndex
	})
	s.reopen(t)
	request := wire.AppendVarint(wire.AppendVarint(trapRequest(1, index), 3, 1), 4, selected)
	response := s.protocol(t, "/UseResourceItem", request)
	chosen, owned := s.owner.assembly.collection.CostumeByID(selected)
	if !owned || chosen.Level != 0 {
		t.Fatal("using an earned selector did not grant the chosen first costume")
	}
	s.reopen(t)
	if !bytes.Equal(response, s.protocol(t, "/UseResourceItem", request)) {
		t.Fatal("selector retry lost the committed response")
	}
	restored, owned := s.owner.assembly.collection.CostumeByID(selected)
	if !owned || restored.Level != 0 {
		t.Fatal("selector retry granted an extra enhancement")
	}
	count := appItemTotals(s.protocol(t, "/ItemInfo", appScalar(1, 4)))[[2]uint64{8, 1501}]
	if count != 1 {
		t.Fatal("selector retry consumed a second ticket")
	}
	other := &trapPlayerScenario{factory: factory, account: accounts[1], owner: openNicknamePlayer(t, factory, accounts[1])}
	defer closeNicknamePlayer(t, other.owner)
	if _, err := other.execute("/UseResourceItem", request); err == nil {
		t.Fatal("another account used a ticket it does not own")
	}
	if _, owned := other.owner.assembly.collection.CostumeByID(selected); owned {
		t.Fatal("selector costume crossed account boundaries")
	}
}
