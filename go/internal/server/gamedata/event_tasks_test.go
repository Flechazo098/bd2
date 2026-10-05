package gamedata

import (
	"os"
	"testing"
)

func TestInstalledEventTasks23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	d, e := LoadEventTasksDesign(root, "20260923193640")
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Attendance) != 109 || len(d.MissionGroups) != 519 || len(d.Passes) != 141 {
		t.Fatal("event design tables not fully loaded")
	}
	p := d.Passes[141]
	if p.MissionGroup != 51 || p.LevelGroup != 141 || d.PassLevels[141][0].NeedExp != 20 || d.PassLevels[141][0].Basic.Type != 9 {
		t.Fatalf("pass 141 mismatch %+v", p)
	}
	if d.MissionGroups[51].Type != 3 || len(d.MissionGroups[51].Groups) != 7 {
		t.Fatal("daily-open groups lost")
	}
}
