package calendar

import "testing"

// Current-version publication facts were verified against the client schemas,
// GameData identities and 2026-10-05 schedule comparison. No capture is read.
func TestPublishedRegularAndGachaCalendarCompleteness(t *testing.T) {
	set, err := LoadDirectory("../../../../schedules", "2.35.10", "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint64][2]uint64{1: {163, 164}, 2: {58, 58}, 3: {131, 999999}, 4: {877, 999999}, 5: {30, 31}, 6: {27, 28}, 7: {15, 999999}, 8: {44, 45}, 9: {2, 999999}}
	if len(set.RegularService.Contents) != len(want) || set.RegularService.CalculateMilliseconds != 32400000 {
		t.Fatal("regular content calendar incomplete")
	}
	for _, c := range set.RegularService.Contents {
		if want[c.ID] != [2]uint64{c.Current.ID, c.Next.ID} {
			t.Fatalf("content %d stale seasons %d/%d", c.ID, c.Current.ID, c.Next.ID)
		}
	}
	current := map[uint64]bool{166: true, 30010: true, 30011: true, 206: true, 205: true, 208: true, 153: true, 72: true, 71: true, 207: true}
	for _, g := range set.GachaSeed.Schedules {
		if current[g.GroupID] {
			delete(current, g.GroupID)
			if g.EndTime != 1791417599000 {
				t.Fatalf("gacha %d truncated window", g.GroupID)
			}
		}
	}
	if len(current) != 0 {
		t.Fatalf("missing published gacha groups %v", current)
	}
	if len(set.GachaSeed.StepUps) != 2 {
		t.Fatal("step-up schedule omitted")
	}
	for _, g := range set.GachaSeed.StepUps {
		if g.GroupID != 29 && g.GroupID != 30 || g.EndTime != 1791417599000 {
			t.Fatalf("step-up schedule incorrect %+v", g)
		}
	}
}
