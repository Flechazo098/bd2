//go:build integration

package app

import (
	"context"
	"encoding/json"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

func achievementInfoRow(t *testing.T, body []byte, group, content uint64) (value, claimed uint64) {
	t.Helper()
	for _, row := range profileRows(t, body, 1) {
		if profileVarint(t, row, 1) == group && profileVarint(t, row, 4) == content {
			return profileVarint(t, row, 2), profileVarint(t, row, 3)
		}
	}
	return 0, 0
}

func achievementClaimFields(group, id uint64) []byte {
	info := wire.AppendVarint(wire.AppendVarint(nil, 1, group), 2, id)
	return wire.AppendBytes(wire.AppendVarint(nil, 2, 1), 3, info)
}

func achievementWallet(g *puzzleGameplay) assets.Currency {
	g.t.Helper()
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	return g.owner.assembly.wallet.Snapshot(command.Context{State: tx})
}

// Current GameData group6004 is Lathel's four-part title, with child
// conditions112–115 for UniqueChar1 and a Type26 reward. The saved fixture
// predates counters; native claims must remain explicit and idempotent.
func TestAchievementReconcileHistoricalCompoundTitleRequiresClaim(t *testing.T) {
	g := profileGameplay(t)
	p := g.owner.assembly
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	c := command.Context{State: tx}
	var costume roster.Costume
	for _, owned := range p.collection.Costumes() {
		if p.design.costumePotentialDesign.CostumeUnique[owned.ID] == 1 {
			costume = owned
			break
		}
	}
	if costume.ID == 0 {
		if _, err := p.collection.GrantCostumes(c, "historical-title-lathel", []uint64{101}, p.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		var found bool
		costume, found = p.collection.CostumeByID(101)
		if !found {
			t.Fatal("historical Lathel grant missing")
		}
	}
	var duplicates []uint64
	for level := costume.Level; level < p.design.achievementGrades.CostumeMaximum[costume.ID]; level++ {
		duplicates = append(duplicates, costume.ID)
	}
	if _, err := p.collection.GrantCostumes(c, "historical-title-costume", duplicates, p.design.rewardCostumes); err != nil {
		t.Fatal(err)
	}
	var nodes []uint64
	for id := range p.design.costumePotentialDesign.Nodes[costume.ID] {
		nodes = append(nodes, id)
	}
	if err := p.collection.ActivateCostumePotential(c, costume.InvenIndex, nodes); err != nil {
		t.Fatal(err)
	}
	awake := roster.CharAwakeProgress{IsAwake: true}
	for slot, growth := range p.design.charAwakeDesign.Characters[1].ImprintGrowth {
		awake.ImprintLevels[slot] = uint64(len(growth))
	}
	if err := p.collection.UpdateCharAwake(c, 1, roster.CharAwakeProgress{}, awake); err != nil {
		t.Fatal(err)
	}
	var equipmentID uint64
	for id, unique := range p.design.achievementGrades.EquipmentPrivate {
		if unique == 1 && p.design.achievementGrades.Equipment[id] == 4 && (equipmentID == 0 || id < equipmentID) {
			equipmentID = id
		}
	}
	if equipmentID == 0 {
		t.Fatal("GameData has no Lathel UR private weapon")
	}
	if _, err := p.ownedEquipment.GrantOnce(c, "historical-title-weapon", equipmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.DeleteEntry("missions", "achievement_counts", "6004"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	key := gamedata.AchievementKey{ContentsGroup: 1, GroupID: 6004, ID: 1001}
	reward := p.design.missionDesign.Achievements[key].Rewards
	var titleID uint64
	for _, item := range reward {
		if item.Type == 26 {
			titleID = item.ID
		}
	}
	if titleID == 0 {
		t.Fatal("versioned compound achievement has no title reward")
	}
	beforeWallet := achievementWallet(g)
	g.reopen()
	info := g.call("/AchievementInfo", nil)
	if value, claimed := achievementInfoRow(t, info, 6004, 1); value != 4 || claimed != 1000 {
		t.Fatalf("historical title eligibility=%d claimed=%d", value, claimed)
	}
	if g.itemCount(26, titleID) != 0 || achievementWallet(g) != beforeWallet {
		t.Fatal("reconciliation granted an unclaimed reward")
	}
	if value, _ := achievementInfoRow(t, info, 6009, 1); value >= 4 {
		t.Fatal("Lathel's saved conditions completed another character's title")
	}
	g.call("/AchievementClear", achievementClaimFields(6004, 1001))
	if got := g.itemCount(26, titleID); got != 1 {
		t.Fatalf("native claim granted %d titles", got)
	}
	g.call("/AchievementClear", achievementClaimFields(6004, 1001))
	g.reopen()
	if got := g.itemCount(26, titleID); got != 1 {
		t.Fatalf("claim retry/startup granted %d titles", got)
	}
	if value, claimed := achievementInfoRow(t, g.call("/AchievementInfo", nil), 6004, 1); value != 4 || claimed != 1001 {
		t.Fatalf("claimed historical title lost progress/claim=%d/%d", value, claimed)
	}
}

func TestAchievementReconcilePreservesLargerHistoryAndCountsNewGrantOnce(t *testing.T) {
	g := profileGameplay(t)
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.PutEntry("missions", "achievement_counts", "100", json.RawMessage("50")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	g.reopen()
	if value, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 100, 0); value != 50 {
		t.Fatalf("remaining costumes replaced larger history: %d", value)
	}
	var costumeID uint64
	owned := map[uint64]bool{}
	for _, c := range g.owner.assembly.collection.Costumes() {
		owned[c.ID] = true
	}
	for id, grade := range g.factory.design.achievementGrades.Costumes {
		if grade == 5 && !owned[id] && (costumeID == 0 || id < costumeID) {
			costumeID = id
		}
	}
	if costumeID == 0 {
		t.Fatal("GameData has no additional costume fixture")
	}
	apply := func(commit bool) {
		t.Helper()
		tx, err := g.owner.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		c := command.Context{Identity: command.Identity{AccountID: g.account, SessionID: "history-grant"}, State: tx}
		request := wire.AppendVarint(nil, 1, 77)
		observer := g.owner.assembly.achievementObserver
		if err := observer.BeforeDispatch(c, "/GachaBuy", request); err != nil {
			t.Fatal(err)
		}
		if _, err := g.owner.assembly.collection.GrantCostumes(c, "new-real-costume", []uint64{costumeID}, g.factory.design.rewardCostumes); err != nil {
			t.Fatal(err)
		}
		if _, err := observer.AfterDispatch(c, "/GachaBuy", request, nil); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	apply(false)
	g.reopen()
	if value, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 100, 0); value != 50 {
		t.Fatalf("rolled-back grant increased history: %d", value)
	}
	apply(true)
	g.reopen()
	if value, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 100, 0); value != 51 {
		t.Fatalf("new grant/floor increased history incorrectly: %d", value)
	}
	apply(true)
	g.reopen()
	if value, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 100, 0); value != 51 {
		t.Fatalf("replayed grant increased history: %d", value)
	}
}

// LocalText81081000 says "Used Slimes to grow Characters: {0} times".
// CommonPacket sends one CharGrowthRequest containing all selected slimes.
func TestAchievementSlimeGrowthCountsNativeActionInsteadOfGainedLevels(t *testing.T) {
	g := profileGameplay(t)
	var character roster.Character
	for _, owned := range g.owner.assembly.worldService.CharacterService().RawAll() {
		if !roster.IsStoryCharacter(owned) && !roster.IsCharmCharacter(owned) && owned.Level == 1 {
			character = owned
			break
		}
	}
	if character.InvenIndex == 0 {
		t.Fatal("fixture has no level-one permanent character")
	}
	profileGrant(g, gamedata.Reward{Type: 8, ID: 9, Count: 10})
	before, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 81, 0)
	tx, err := g.owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var material assets.Item
	for _, item := range g.owner.assembly.ownedItems.All(command.Context{State: tx}) {
		if item.Type == 8 && item.ID == 9 {
			material = item
			break
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	material.Count = 5
	fields := wire.AppendBytes(wire.AppendVarint(nil, 2, character.InvenIndex), 3, assets.ItemWire(material))
	response := g.call("/CharGrowth", fields)
	grown := profileBytes(t, response, 1)
	if level := profileVarint(t, grown, 4); level <= character.Level+1 {
		t.Fatalf("real GameData fixture gained too few levels to distinguish contract: %d", level)
	}
	if count, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 81, 0); count != before+1 {
		t.Fatalf("one native slime-growth action counted %d instead of1", count-before)
	}
	material.Count = g.itemCount(8, 9) + 1
	bad := wire.AppendBytes(wire.AppendVarint(nil, 2, character.InvenIndex), 3, assets.ItemWire(material))
	g.execute(g.request("/CharGrowth", bad), true)
	g.reopen()
	if count, _ := achievementInfoRow(t, g.call("/AchievementInfo", nil), 81, 0); count != before+1 {
		t.Fatalf("failed growth/startup fabricated historical actions: %d", count-before)
	}
}
