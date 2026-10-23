//go:build integration

package app

import (
	"encoding/json"
	"testing"

	"bd2server/internal/server/domain/command"
)

func guideStepOne(t *testing.T, g *missionGameplay) (map[uint64]bool, uint64) {
	t.Helper()
	design := g.factory.design.eventTasksDesign
	groups := map[uint64]bool{}
	var passID uint64
	for id, pass := range design.Passes {
		if pass.ScheduleType != 0 || pass.NewbieGroup == 0 || pass.NewbieStep != 1 {
			continue
		}
		if passID == 0 {
			passID = id
		}
		for _, group := range design.MissionGroups[pass.MissionGroup].Groups {
			groups[group] = true
		}
	}
	if passID == 0 || len(groups) == 0 {
		t.Fatal("GameData has no guide pass step 1")
	}
	return groups, passID
}

func guideConditionMission(t *testing.T, g *missionGameplay, groups map[uint64]bool, condition, subtype uint64) ([]byte, uint64) {
	t.Helper()
	design := g.factory.design.eventTasksDesign
	for _, row := range appRows(g.call("/EventMissionInfo", nil), 1) {
		task, found := design.Missions[appValue(row, 3)]
		if found && groups[task.Group] && task.Type == condition && task.SubType == subtype {
			return row, task.ID
		}
	}
	return nil, 0
}

// clearEventMissions keeps durable reward claims but removes every derived
// mission value, reproducing an account that claimed before the pass existed.
func clearEventMissions(t *testing.T, g *missionGameplay) {
	t.Helper()
	s := &trapPlayerScenario{factory: g.factory, owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		raw, err := p.gameplayStore.Load(ctx.State, "eventtasks")
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		state["Missions"] = json.RawMessage(`{}`)
		raw, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.gameplayStore.Save(ctx.State, "eventtasks", raw); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
}

func missionTarget(t *testing.T, g *missionGameplay, id uint64) uint64 {
	t.Helper()
	target := g.factory.design.eventTasksDesign.Missions[id].Target
	if target == 0 {
		return 1
	}
	return target
}

// Define_MissionConditionType.McRewardToAchievementLevel is condition 221.
func TestGuidePassAchievementLevelMissionDerivesFromClaimedReward(t *testing.T) {
	g := newMissionGameplay(t)
	groups, _ := guideStepOne(t, g)
	row, id := guideConditionMission(t, g, groups, 221, 1)
	if row == nil {
		t.Fatal("achievement level mission missing from EventMissionInfo")
	}
	want := missionTarget(t, g, id)
	if got := appValue(row, 4); got != 0 {
		t.Fatalf("achievement level mission started at %d, want 0", got)
	}
	g.call("/UserLevelReward", appScalar(2, 1))
	clearEventMissions(t, g)
	row, _ = guideConditionMission(t, g, groups, 221, 1)
	if row == nil {
		t.Fatal("achievement level mission disappeared after claiming")
	}
	if got := appValue(row, 4); got != want {
		t.Fatalf("achievement level mission value=%d, want %d", got, want)
	}
	if again, _ := guideConditionMission(t, g, groups, 221, 1); appValue(again, 4) != want {
		t.Fatalf("repeated reconciliation changed mission value to %d", appValue(again, 4))
	}
}

// Define_MissionConditionType.McRewardToPassLevel is condition 223.
func TestGuidePassPassLevelMissionDerivesFromClaimedReward(t *testing.T) {
	g := newMissionGameplay(t)
	groups, passID := guideStepOne(t, g)
	row, id := guideConditionMission(t, g, groups, 223, 1)
	if row == nil {
		t.Fatal("pass level mission missing from EventMissionInfo")
	}
	want := missionTarget(t, g, id)
	if got := appValue(row, 4); got != 0 {
		t.Fatalf("pass level mission started at %d, want 0", got)
	}
	g.call("/PassReward", append(appScalar(3, passID), appScalar(4, 1)...))
	clearEventMissions(t, g)
	row, _ = guideConditionMission(t, g, groups, 223, 1)
	if row == nil {
		t.Fatal("pass level mission disappeared after claiming")
	}
	if got := appValue(row, 4); got != want {
		t.Fatalf("pass level mission value=%d, want %d", got, want)
	}
	if again, _ := guideConditionMission(t, g, groups, 223, 1); appValue(again, 4) != want {
		t.Fatalf("repeated reconciliation changed mission value to %d", appValue(again, 4))
	}
}
