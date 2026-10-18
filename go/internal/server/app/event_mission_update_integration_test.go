//go:build integration

package app

import (
	"testing"
	"time"

	"bd2server/internal/server/domain/events"
)

// LocalText5700035 asks to change the homescreen. LobbySettingUI saves its
// slots locally and reports condition39 only through MissionUpdate. Current
// GameData mission3038 grants four type8/id14 materials and belongs to the
// third day of EventMissionGroup7001; mission3037 requires equipment upgrades.
func TestHomescreenMissionPersistsAndPaysOnceWithoutAuthorizingEquipmentProgress(t *testing.T) {
	g := newMissionGameplay(t)
	now := time.Now().UTC()
	g.now = now
	schedule := events.Schedule{UID: 9001, Type: 4, ID: 7001, Start: now.Add(-72 * time.Hour).UnixMilli(), End: now.Add(24 * time.Hour).UnixMilli()}
	installCalendar := func() {
		t.Helper()
		if err := g.owner.assembly.eventRegistry.Replace([]events.Schedule{schedule}); err != nil {
			t.Fatal(err)
		}
	}
	installCalendar()
	update := func(id, value uint64) error {
		t.Helper()
		row := append(appScalar(1, 303), appScalar(2, id)...)
		row = append(row, appScalar(3, value)...)
		row = append(row, appScalar(4, 7001)...)
		_, err := g.execute("/MissionUpdate", appMessage(2, row))
		return err
	}
	assertHomescreen := func(want uint64, claimed bool) {
		t.Helper()
		for _, row := range appRows(g.call("/EventMissionInfo", nil), 1) {
			if appValue(row, 1) == 7001 && appValue(row, 3) == 3038 {
				if appValue(row, 4) != want || (appValue(row, 5) == 1) != claimed {
					t.Fatalf("homescreen progress not restored: value=%d claimed=%v", appValue(row, 4), appValue(row, 5) == 1)
				}
				return
			}
		}
		t.Fatal("third-day homescreen task missing")
	}
	claim := append(appScalar(3, 2), appScalar(4, 303)...)
	claim = append(claim, appScalar(5, 3038)...)
	claim = append(claim, appScalar(6, 7001)...)
	before := g.itemCount(8, 14)
	if _, err := g.execute("/MissionClear", claim); err == nil {
		t.Fatal("homescreen reward was available before changing the setting")
	}
	if err := update(3037, 1); err == nil {
		t.Fatal("a client report manufactured equipment upgrades")
	}
	assertHomescreen(0, false)
	if err := update(3038, 1); err != nil {
		t.Fatalf("native local homescreen change rejected: %v", err)
	}
	if g.itemCount(8, 14) != before {
		t.Fatal("reporting a setting change paid its unclaimed reward")
	}
	g.reopen()
	installCalendar()
	assertHomescreen(1, false)
	if err := update(3038, 2); err != nil {
		t.Fatalf("repeated local change beyond completed target rejected: %v", err)
	}
	assertHomescreen(1, false)
	response := g.call("/MissionClear", claim)
	if missionRewardCount(t, response, 8, 14) != 4 || g.itemCount(8, 14) != before+4 {
		t.Fatal("homescreen task did not pay its four crafting materials")
	}
	g.reopen()
	installCalendar()
	assertHomescreen(1, true)
	if _, err := g.execute("/MissionClear", claim); err == nil {
		t.Fatal("a new claim paid the same homescreen task twice")
	}
	if g.itemCount(8, 14) != before+4 {
		t.Fatal("reopening or retrying duplicated homescreen materials")
	}
	if err := update(3038, 0); err != nil {
		t.Fatal(err)
	}
	assertHomescreen(1, true)
}
