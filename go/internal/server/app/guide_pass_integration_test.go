//go:build integration

package app

import (
	"encoding/json"
	"testing"

	"bd2server/internal/server/domain/command"
)

// PassInfo.GetPassTableList includes schedule-free newbie passes. Current
// GameData pass76/step1 starts at level1 with box510031, which opens costume3303;
// level2 requires65 EXP. Homescreen mission3038 contributes10 EXP when claimed.
func TestGuidePassClaimsAndExperienceSurviveWithoutEventCalendarEntry(t *testing.T) {
	g := newMissionGameplay(t)
	passInfo := func() []byte { return g.call("/PassInfo", nil) }
	assertExperience := func(want uint64) {
		t.Helper()
		for _, row := range appRows(passInfo(), 1) {
			if appValue(row, 1) == 76 {
				if appValue(row, 2) != want {
					t.Fatalf("guide pass lost earned EXP: got%d want%d", appValue(row, 2), want)
				}
				return
			}
		}
		t.Fatal("native guide pass missing without a seasonal calendar entry")
	}
	reward := func(pass, level uint64) []byte {
		return append(appScalar(3, pass), appScalar(4, level)...)
	}
	assertExperience(0)
	if _, owned := g.owner.assembly.collection.CostumeByID(3303); owned {
		t.Fatal("guide reward costume already owned before the first guide reward")
	}
	if _, err := g.execute("/PassReward", reward(77, 1)); err == nil {
		t.Fatal("step2 pass rewarded while only step1 was unlocked")
	}
	if _, err := g.execute("/PassReward", reward(76, 2)); err == nil {
		t.Fatal("guide level2 rewarded without its65 EXP")
	}
	row := append(appScalar(1, 303), appScalar(2, 3038)...)
	row = append(row, appScalar(3, 1)...)
	row = append(row, appScalar(4, 7001)...)
	g.call("/MissionUpdate", appMessage(2, row))
	claim := append(appScalar(3, 2), appScalar(4, 303)...)
	claim = append(claim, appScalar(5, 3038)...)
	claim = append(claim, appScalar(6, 7001)...)
	g.call("/MissionClear", claim)
	assertExperience(10)
	// Reproduce the previous server's missing derived pass ledger while retaining
	// the mission reward receipt that independently proves the earned EXP.
	s := &trapPlayerScenario{owner: g.owner, account: g.account}
	s.run(t, true, func(ctx command.Context, p *playerAssembly) {
		raw, err := p.gameplayStore.Load(ctx.State, "eventtasks")
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		state["Passes"] = json.RawMessage(`{}`)
		raw, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.gameplayStore.Save(ctx.State, "eventtasks", raw); err != nil {
			t.Fatal(err)
		}
	})
	g.reopen()
	assertExperience(10)
	assertExperience(10)
	g.reopen()
	assertExperience(10)
	g.call("/PassReward", reward(76, 1))
	costume, owned := g.owner.assembly.collection.CostumeByID(3303)
	if !owned || costume.Level != 0 {
		t.Fatal("guide level1 box failed to grant its initial costume")
	}
	g.reopen()
	if _, err := g.execute("/PassReward", reward(76, 1)); err == nil {
		t.Fatal("guide reward paid again after player reopen")
	}
	if after, owned := g.owner.assembly.collection.CostumeByID(3303); !owned || after.InvenIndex != costume.InvenIndex || after.Level != 0 {
		t.Fatal("retry duplicated or enhanced the guide reward costume")
	}
	claimed := false
	for _, row := range appRows(passInfo(), 2) {
		if appValue(row, 1) == 76 && appValue(row, 2) == 1 && appValue(row, 3) == 1 {
			claimed = true
		}
	}
	if !claimed {
		t.Fatal("native pass UI cannot restore the claimed level1 marker")
	}
}
