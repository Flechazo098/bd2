//go:build integration

package app

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type skyWayPlayerScenario struct {
	factory  *PlayerFactory
	owner    *playerInstance
	account  string
	sequence uint64
	blue     []byte
}
type skyWayAssets struct{ gold, rice, torch, crystal uint64 }

func newSkyWayPlayerScenario(t *testing.T) *skyWayPlayerScenario {
	t.Helper()
	factory, accounts := newIntegrationFactory(t)
	for i := range factory.design.skywaySchedules {
		if factory.design.skywaySchedules[i].Group == 1 {
			factory.design.skywaySchedules[i].Bonus = 100
			factory.design.skywaySchedules[i].Days = []uint64{0, 1, 2, 3, 4, 5, 6}
		}
	}
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	s := &skyWayPlayerScenario{factory: factory, owner: owner, account: accounts[0]}
	t.Cleanup(func() {
		if s.owner != nil {
			if err := s.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{Identity: command.Identity{AccountID: s.account, SessionID: "skyway-client"}, State: tx}
	for _, character := range owner.assembly.worldService.CharacterService().RawAll() {
		costume, owned := owner.assembly.collection.CostumeByIndex(character.UseCostume)
		if !owned || costume.UseChar != character.InvenIndex || character.HP == 0 {
			continue
		}
		s.blue = wire.AppendVarint(nil, 1, 1)
		for field, value := range map[int]uint64{2: character.InvenIndex, 3: character.ID, 4: character.HP, 5: character.Level, 6: character.UseCostume, 7: costume.ID} {
			s.blue = wire.AppendVarint(s.blue, field, value)
		}
		break
	}
	if s.blue == nil {
		t.Fatal("SkyWay scenario requires an owned living battle participant")
	}
	if _, err := owner.assembly.ownedItems.GrantOnce(ctx, "skyway-entry-ticket", []gamedata.BattleReward{{Type: 19, ID: 13004, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.assembly.eventEconomy.Apply(ctx, "skyway-test-ap", nil, []gamedata.Reward{{Type: 21, Count: 200}, {Type: 32, Count: 200}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	beforePurchase := s.assets(t)
	purchase := s.request(3004)
	s.call(t, "/PackBuy", purchase)
	bought := beforePurchase
	bought.rice += 10
	if s.assets(t) != bought {
		t.Fatal("the first SkyWay pack purchase did not credit its ten rice")
	}
	s.call(t, "/PackBuy", purchase)
	if s.assets(t) != bought {
		t.Fatal("pack purchase retry duplicated rice")
	}
	s.call(t, "/PackInGameInfo", s.request(3004))
	for _, step := range []struct {
		quest uint64
		talks []uint64
	}{{1, []uint64{11}}, {2, []uint64{21, 22}}, {3, []uint64{31}}} {
		update := s.request(step.quest, 3004)
		for _, talk := range step.talks {
			update = wire.AppendVarint(update, 4, talk)
		}
		s.call(t, "/QuestUpdate", update)
		clear := s.request(step.quest, 3004)
		response := s.call(t, "/QuestClear", clear)
		if step.quest == 3 {
			complete := false
			for _, row := range appRows(response, 11) {
				if appValue(row, 1) == 3004 && appValue(row, 3) == 1 {
					complete = true
				}
			}
			if !complete {
				t.Fatal("final dialogue did not unlock dispatch in the client's pack cache")
			}
		}
		bought.gold += 500
		if s.assets(t) != bought {
			t.Fatal("introductory dialogue did not settle its authored gold")
		}
		s.call(t, "/QuestClear", clear)
		if s.assets(t) != bought {
			t.Fatal("introductory dialogue retry duplicated rewards")
		}
	}
	return s
}
func (s *skyWayPlayerScenario) request(values ...uint64) []byte {
	s.sequence++
	raw := wire.AppendVarint(nil, 1, s.sequence)
	for i, value := range values {
		raw = wire.AppendVarint(raw, i+2, value)
	}
	return raw
}
func (s *skyWayPlayerScenario) execute(path string, raw []byte) ([]byte, error) {
	c := registryCommand(s.account, fmt.Sprintf("skyway-%s-%x", path, raw), path, raw)
	c.Identity.SessionID = "skyway-client"
	reply, err := s.owner.Execute(context.Background(), c)
	if err != nil {
		return nil, err
	}
	if len(reply.Responses) != 1 {
		return nil, fmt.Errorf("%s has no response", path)
	}
	return reply.Responses[0].Body, nil
}
func (s *skyWayPlayerScenario) call(t *testing.T, path string, raw []byte) []byte {
	t.Helper()
	body, err := s.execute(path, raw)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return body
}
func (s *skyWayPlayerScenario) assets(t *testing.T) skyWayAssets {
	t.Helper()
	tx, err := s.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{Identity: command.Identity{AccountID: s.account, SessionID: "skyway-client"}, State: tx}
	free, bonus, err := s.owner.assembly.huntingService.HuntingAP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := s.owner.assembly.eventEconomy.AdditionalCurrencies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := skyWayAssets{gold: s.owner.assembly.wallet.Snapshot(ctx).Gold, rice: free + bonus, torch: extra[36] + extra[37]}
	for _, item := range s.owner.assembly.ownedItems.All(ctx) {
		if item.Type == 8 && item.ID == 111 {
			out.crystal += item.Count
		}
	}
	return out
}
func (s *skyWayPlayerScenario) reject(t *testing.T, path string, raw []byte) {
	t.Helper()
	before := s.assets(t)
	if _, err := s.execute(path, raw); err == nil {
		t.Fatalf("%s accepted an unavailable encounter", path)
	}
	if after := s.assets(t); after != before {
		t.Fatalf("rejected %s changed assets: before=%+v after=%+v", path, before, after)
	}
}
func (s *skyWayPlayerScenario) battle(t *testing.T, monster, mode, result uint64) ([]byte, []byte) {
	t.Helper()
	s.call(t, "/BattleEnter", s.request(0, monster, monster, mode))
	s.call(t, "/BattleStart", wire.AppendBytes(s.request(monster), 5, s.blue))
	end := s.request(result)
	return s.call(t, "/BattleEnd", end), end
}
func (s *skyWayPlayerScenario) maximum(t *testing.T, group uint64) int64 {
	t.Helper()
	info := s.call(t, "/SkyWayInfo", s.request(3004))
	for _, row := range appRows(info, 1) {
		if appValue(row, 2) == group {
			return int64(appValue(row, 4))
		}
	}
	t.Fatalf("SkyWayInfo omitted selected group %d", group)
	return -1
}
func (s *skyWayPlayerScenario) reopen(t *testing.T) {
	t.Helper()
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
		t.Fatal(err)
	}
}

// GameData group1/10 has five 1-rice encounters (1000 gold each) and a
// 1-rice boss (1500 gold). The configured 100% policy doubles concrete payouts.
func TestSkyWayGoblinBossUnlockAndRepeatedRunsConserveRewards(t *testing.T) {
	s := newSkyWayPlayerScenario(t)
	initial := s.assets(t)
	if s.maximum(t, 1) != -1 {
		t.Fatal("unplayed dungeon was advertised as cleared")
	}
	s.reject(t, "/SkyWayEnter", s.request(3004, 1, 11, 0))
	enter := s.request(3004, 1, 10, 0)
	enterReply := s.call(t, "/SkyWayEnter", enter)
	if s.assets(t) != initial {
		t.Fatal("entering a dungeon spent battle AP")
	}
	s.reject(t, "/BattleEnter", s.request(0, 2005, 2005, 9))
	s.battle(t, 2001, 9, 2)
	if s.assets(t) != initial {
		t.Fatal("losing the encounter charged AP or granted gold")
	}
	for i, monster := range []uint64{2001, 2002, 2003, 2004, 2006} {
		var response, end []byte
		if i == 0 {
			s.call(t, "/BattleRetry", s.request(monster))
			if s.assets(t) != initial {
				t.Fatal("retrying a lost battle charged AP before victory")
			}
			s.call(t, "/BattleStart", wire.AppendBytes(s.request(monster), 5, s.blue))
			end = s.request(1)
			response = s.call(t, "/BattleEnd", end)
		} else {
			response, end = s.battle(t, monster, 9, 1)
		}
		expected := initial
		expected.gold += uint64(i+1) * 2000
		expected.rice -= uint64(i + 1)
		if after := s.assets(t); after != expected {
			t.Fatalf("victory assets: got %+v want %+v", after, expected)
		}
		if retry := s.call(t, "/BattleEnd", end); !bytes.Equal(response, retry) || s.assets(t) != expected {
			t.Fatal("settlement retry charged or rewarded the player twice")
		}
		if i == 1 {
			s.reopen(t)
			command := registryCommand(s.account, "uncached-enter-retry", "/SkyWayEnter", enter)
			command.Identity.SessionID = "skyway-client"
			replay, err := s.owner.Execute(context.Background(), command)
			if err != nil || len(replay.Responses) != 1 || !bytes.Equal(replay.Responses[0].Body, enterReply) || s.assets(t) != expected {
				t.Fatalf("persisted native entry retry reset or charged the dungeon: %v", err)
			}
		}
	}
	if s.maximum(t, 1) != -1 {
		t.Fatal("normal encounters unlocked the next difficulty before the boss")
	}
	s.battle(t, 2005, 9, 1)
	afterCycle := initial
	afterCycle.gold += 13000
	afterCycle.rice -= 6
	if s.assets(t) != afterCycle || s.maximum(t, 1) != 0 {
		t.Fatal("boss did not settle one complete run or unlock hard difficulty")
	}
	s.reopen(t)
	if s.assets(t) != afterCycle || s.maximum(t, 1) != 0 {
		t.Fatal("reopen lost completed dungeon assets or difficulty")
	}
	s.battle(t, 2001, 9, 1)
	repeated := afterCycle
	repeated.gold += 2000
	repeated.rice--
	if s.assets(t) != repeated {
		t.Fatal("the new run reused the preceding run's reward identity")
	}
	s.call(t, "/SkyWayEnter", s.request(3004, 1, 11, 0))
	if s.assets(t) != repeated {
		t.Fatal("selecting the unlocked hard dungeon spent battle AP")
	}
	safe := wire.AppendBytes(s.request(3004), 3, []byte(`{"MapId":30021,"PlayerPosition":{"x":0,"y":0,"z":0},"ColleaguePositions":[]}`))
	s.call(t, "/SaveUserPosition", safe)
	s.reject(t, "/BattleEnter", s.request(0, 2011, 2011, 9))
	s.reopen(t)
	if s.maximum(t, 1) != 0 || s.assets(t) != repeated {
		t.Fatal("leaving the dungeon discarded permanent progress or assets")
	}
	s.call(t, "/SkyWayEnter", s.request(3004, 1, 11, 0))
}

// Fire normal encounters and boss each grant resource111 x2 and cost one torch;
// the instant two-run dispatch costs twelve torches and grants twenty-four.
func TestSkyWayCrystalDispatchUsesTorchesAndSurvivesRetryAndReopen(t *testing.T) {
	s := newSkyWayPlayerScenario(t)
	initial := s.assets(t)
	dispatch := s.request(2, 7, 2)
	s.reject(t, "/HuntDispatch", dispatch)
	s.call(t, "/SkyWayEnter", s.request(3004, 3, 30, 0))
	s.battle(t, 4001, 11, 2)
	if s.assets(t) != initial {
		t.Fatal("lost crystal battle spent torches or awarded crystals")
	}
	for _, monster := range []uint64{4001, 4002, 4003, 4004, 4006, 4005} {
		s.battle(t, monster, 11, 1)
	}
	afterCycle := initial
	afterCycle.torch -= 6
	afterCycle.crystal += 12
	if s.assets(t) != afterCycle || s.maximum(t, 3) != 0 {
		t.Fatal("crystal dungeon charged rice or failed to persist its rewards")
	}
	dispatch = s.request(2, 7, 2)
	reply := s.call(t, "/HuntDispatch", dispatch)
	afterDispatch := afterCycle
	afterDispatch.torch -= 12
	afterDispatch.crystal += 24
	if s.assets(t) != afterDispatch {
		t.Fatalf("instant dispatch charged the wrong currency or quantity: %+v want %+v", s.assets(t), afterDispatch)
	}
	if repeated := s.call(t, "/HuntDispatch", dispatch); !bytes.Equal(reply, repeated) || s.assets(t) != afterDispatch {
		t.Fatal("dispatch retry duplicated rewards or torch charges")
	}
	s.reopen(t)
	if s.assets(t) != afterDispatch || s.maximum(t, 3) != 0 {
		t.Fatal("reopen lost crystal rewards, torch balance or dispatch qualification")
	}
	if repeated := s.call(t, "/HuntDispatch", dispatch); !bytes.Equal(reply, repeated) || s.assets(t) != afterDispatch {
		t.Fatal("reopened dispatch duplicated assets")
	}
	s.call(t, "/HuntDispatchStart", s.request(2, 7, 2))
	reserved := afterDispatch
	reserved.torch -= 12
	if s.assets(t) != reserved {
		t.Fatal("timed dispatch did not reserve the correct torch cost")
	}
	preview := s.call(t, "/HuntDispatchRewardPreview", s.request(2, 7))
	if appValue(preview, 1) != 0 || s.assets(t) != reserved {
		t.Fatal("preview completed an unplayed dispatch or issued assets")
	}
	cancel := s.request(2, 7)
	cancelReply := s.call(t, "/HuntDispatchEnd", cancel)
	if s.assets(t) != afterDispatch {
		t.Fatal("cancelling unplayed runs did not refund exactly the reserved torches")
	}
	s.reopen(t)
	if retry := s.call(t, "/HuntDispatchEnd", cancel); !bytes.Equal(retry, cancelReply) || s.assets(t) != afterDispatch {
		t.Fatal("reopened cancellation refunded torches twice")
	}
}
