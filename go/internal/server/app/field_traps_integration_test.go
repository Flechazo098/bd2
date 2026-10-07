//go:build integration

package app

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	accountstate "bd2server/internal/server/storage/account"
)

type trapPlayerScenario struct {
	factory       *PlayerFactory
	owner         *playerInstance
	account       string
	leader, other uint64
	maximum       uint64
	commands      int
}

func newTrapPlayerScenario(t *testing.T, pack int) *trapPlayerScenario {
	t.Helper()
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
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		if _, err := p.collection.GrantCostumes(ctx, "trap-party-owned-costumes", []uint64{101, 201}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		if rule := p.design.world.Story.Packs[pack].Open; rule != nil && rule.TicketID != 0 {
			if _, err := p.ownedItems.GrantOnce(ctx, "trap-scenario-ticket", []gamedata.BattleReward{{Type: 19, ID: rule.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, _, err := p.worldService.Handle(ctx, "/PackInGameInfo", trapRequest(100, uint64(pack))); err != nil {
			t.Fatal(err)
		}
		var selected []roster.Character
		for _, c := range p.worldService.CharacterService().RawAll() {
			if !roster.IsStoryCharacter(c) && !roster.IsCharmCharacter(c) && !roster.CharacterExpired(c, time.Now()) {
				worn, owned := p.collection.CostumeByIndex(c.UseCostume)
				if !owned || worn.UseChar != c.InvenIndex {
					continue
				}
				connectedOwned := c.ConnectPotentialCostume == 0
				for _, costume := range p.collection.Costumes() {
					if costume.ID == c.ConnectPotentialCostume && p.design.costumePotentialDesign.CostumeUnique[costume.ID] == p.design.costumePotentialDesign.CharacterUnique[c.ID] {
						connectedOwned = true
						break
					}
				}
				if !connectedOwned {
					continue
				}
				maximum, err := p.worldService.CharacterService().MaxHealth(ctx, c.InvenIndex)
				if err != nil {
					t.Fatal(err)
				}
				if maximum == 0 || len(selected) == 0 && maximum <= 150 {
					continue
				}
				selected = append(selected, c)
				if len(selected) == 2 {
					break
				}
			}
		}
		if len(selected) < 2 {
			t.Fatal("player setup requires two owned permanent characters")
		}
		s.leader = selected[0].InvenIndex
		s.other = selected[1].InvenIndex
		s.maximum, err = p.worldService.CharacterService().MaxHealth(ctx, s.leader)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range selected {
			max, err := p.worldService.CharacterService().MaxHealth(ctx, c.InvenIndex)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.worldService.CharacterService().SetCurrentHealth(ctx, c.InvenIndex, max); err != nil {
				t.Fatal(err)
			}
		}
		req := wire.AppendVarint(nil, 1, 1)
		for i, c := range selected {
			row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(i+1)), 2, c.InvenIndex), 3, c.UseCostume)
			req = wire.AppendBytes(req, 2, row)
		}
		if _, _, _, err := p.deckStateStore.Handle(ctx, "/FieldDeckSave", req); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := p.deckStateStore.Handle(ctx, "/SaveFieldCharControlDeckType", wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1)); err != nil {
			t.Fatal(err)
		}
	})
	return s
}

func (s *trapPlayerScenario) run(t *testing.T, commit bool, action func(command.Context, *playerAssembly)) {
	t.Helper()
	tx, err := s.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{Identity: command.Identity{AccountID: s.account, SessionID: "trap-client"}, State: tx}
	action(ctx, s.owner.assembly)
	problems, err := tx.Validate()
	if err != nil || len(problems) != 0 {
		t.Fatalf("complete player validation failed: %v %v", problems, err)
	}
	if commit {
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *trapPlayerScenario) reopen(t *testing.T) {
	t.Helper()
	status, err := s.owner.repository.InitializationState("characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet")
	if err != nil || status != accountstate.InitializationComplete {
		t.Fatalf("trap operations changed the nine-domain account schema: status=%v err=%v", status, err)
	}
	if err := s.owner.repository.RequireDomains("bootstrap", "characters", "collection", "deck", "equipment", "items", "mail", "missions", "progress", "wallet"); err != nil {
		t.Fatal(err)
	}
	problems, err := s.owner.repository.Validate()
	if err != nil || len(problems) != 0 {
		t.Fatalf("persisted player validation: %v %v", problems, err)
	}
	if err := s.owner.Close(); err != nil {
		t.Fatal(err)
	}
	s.owner = nil
	s.owner, err = s.factory.open(s.account)
	if err != nil {
		t.Fatalf("reopen complete player after trap operation: %v", err)
	}
}

func trapRequest(seq, id uint64) []byte {
	return wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, id)
}
func trapHealth(t *testing.T, ctx command.Context, p *playerAssembly, index uint64) uint64 {
	t.Helper()
	hp, err := p.worldService.CharacterService().CurrentHealth(ctx, index)
	if err != nil {
		t.Fatal(err)
	}
	return hp
}

func (s *trapPlayerScenario) execute(path string, request []byte) (player.Reply, error) {
	c := registryCommand(s.account, fmt.Sprintf("trap-command-%d", s.commands), path, request)
	s.commands++
	c.Identity.SessionID = "trap-client"
	return s.owner.Execute(context.Background(), c)
}
func (s *trapPlayerScenario) protocol(t *testing.T, path string, request []byte) []byte {
	t.Helper()
	reply, err := s.execute(path, request)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if len(reply.Responses) != 1 {
		t.Fatalf("%s missing response", path)
	}
	return reply.Responses[0].Body
}
func (s *trapPlayerScenario) health(t *testing.T, index uint64) uint64 {
	t.Helper()
	var hp uint64
	s.run(t, false, func(ctx command.Context, p *playerAssembly) { hp = trapHealth(t, ctx, p, index) })
	return hp
}
func (s *trapPlayerScenario) position(t *testing.T, pack, mapID int) {
	t.Helper()
	raw := wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(s.commands+1)), 2, uint64(pack)), 3, []byte(fmt.Sprintf(`{"MapId":%d}`, mapID)))
	s.protocol(t, "/SaveUserPosition", raw)
}

// The real player's leader takes spike damage; retries, failed transactions,
// and reassembly must preserve HP and the account's initialized domain set.
func TestPlayerTrapDamagePersistsHealthAndReplaysAcrossTransactions(t *testing.T) {
	s := newTrapPlayerScenario(t, 2)
	request := trapRequest(1, 11)
	s.position(t, 2, 22)
	otherHP := s.health(t, s.other)
	reply := s.protocol(t, "/TrapDamage", request)
	rows := appRows(reply, 1)
	if len(rows) != 1 || appValue(rows[0], 1) != s.leader || appValue(rows[0], 3) != s.maximum-50 {
		t.Fatalf("spikes must return the actual leader's reduced HP: %x", reply)
	}
	if s.health(t, s.leader) != s.maximum-50 || s.health(t, s.other) != otherHP {
		t.Fatal("spikes changed the wrong party member's health")
	}
	s.reopen(t)
	if retry := s.protocol(t, "/TrapDamage", request); !bytes.Equal(retry, reply) {
		t.Fatal("reopened player changed the one-hit retry")
	}
	if s.health(t, s.leader) != s.maximum-50 {
		t.Fatal("retry deducted HP again")
	}
	if _, err := s.execute("/TrapDamage", trapRequest(1, 21)); err == nil {
		t.Fatal("changed replay accepted")
	}
	s.position(t, 2, 23)
	s.protocol(t, "/TrapDamage", trapRequest(2, 11))
	if s.health(t, s.leader) != s.maximum-100 {
		t.Fatal("shared trap ID in its second scene did not apply damage")
	}
	s.position(t, 2, 21)
	before := s.health(t, s.leader)
	if _, err := s.execute("/TrapDamage", trapRequest(3, 11)); err == nil {
		t.Fatal("wrong-scene trap request accepted")
	}
	if s.health(t, s.leader) != before {
		t.Fatal("wrong-scene trap request changed HP")
	}
	s.position(t, 2, 22)
	failed := registryCommand(s.account, "trap-then-rejected-batch", "/TrapDamage", trapRequest(4, 11))
	failed.Identity.SessionID = "trap-client"
	failed.Requests = append(failed.Requests, player.Request{Path: "/TrapDamage", Body: trapRequest(5, 999999)})
	if _, err := s.owner.Execute(context.Background(), failed); err == nil {
		t.Fatal("invalid batch committed its preceding trap hit")
	}
	s.reopen(t)
	if s.health(t, s.leader) != s.maximum-100 {
		t.Fatal("rolled-back HP was persisted")
	}
	s.protocol(t, "/TrapDamage", trapRequest(4, 11))
	if s.health(t, s.leader) != s.maximum-150 {
		t.Fatal("rolled-back receipt blocked the next real hit")
	}
	s.reopen(t)
}

// A saved Preserve override controls what GetInitialActiveState restores. Its
// map filter and disabled damage behavior must survive a complete player reopen.
func TestPlayerTrapOverrideRestoresAcrossReopenAndFiltersMaps(t *testing.T) {
	s := newTrapPlayerScenario(t, 2)
	query := func(mapID uint64) []byte {
		t.Helper()
		return s.protocol(t, "/FieldTrapInfo", wire.AppendVarint(trapRequest(10, 2), 3, mapID))
	}
	s.position(t, 2, 22)
	if len(appRows(query(0), 1)) != 0 {
		t.Fatal("new player received an override without saving one")
	}
	s.protocol(t, "/TrapDamage", trapRequest(11, 11))
	if s.health(t, s.leader) != s.maximum-50 {
		t.Fatal("new player did not use the trap's enabled default")
	}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		// The setup is a legal saved state for the real Preserve trap 11. It
		// does not change GameData or invent a currently absent switch chain.
		payload := []byte(`{"States":{"2/22/11":{"Pack":2,"Map":22,"Trap":11,"Enabled":false,"Partial":null},"2/23/11":{"Pack":2,"Map":23,"Trap":11,"Enabled":true,"Partial":null}},"Hits":{},"Requests":{}}`)
		if err := p.gameplayStore.Save(ctx.State, "field_traps", payload); err != nil {
			t.Fatal(err)
		}
	})
	s.reopen(t)
	rows := appRows(query(22), 1)
	if len(rows) != 1 || appValue(rows[0], 1) != 2 || appValue(rows[0], 2) != 22 || appValue(rows[0], 3) != 11 || appValue(rows[0], 4) != 0 {
		t.Fatalf("disabled trap override not restored in the requested map: %x", rows)
	}
	if reply := s.protocol(t, "/TrapDamage", trapRequest(12, 11)); len(reply) != 0 || s.health(t, s.leader) != s.maximum-50 {
		t.Fatal("restored disabled trap caused damage")
	}
	rows = appRows(query(23), 1)
	if len(rows) != 1 || appValue(rows[0], 2) != 23 || appValue(rows[0], 3) != 11 || appValue(rows[0], 4) != 1 {
		t.Fatalf("second-map enabled override not restored: %x", rows)
	}
	if len(appRows(query(0), 1)) != 2 {
		t.Fatal("whole-pack query lost a persisted map override")
	}
	s.position(t, 2, 23)
	s.protocol(t, "/TrapDamage", trapRequest(13, 11))
	if s.health(t, s.leader) != s.maximum-100 {
		t.Fatal("enabled override in the other map failed to damage the leader")
	}
	s.reopen(t)
}
