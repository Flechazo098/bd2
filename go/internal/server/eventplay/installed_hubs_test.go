package eventplay

import (
	"os"
	"testing"

	"bd2server/internal/server/calendar"
	"bd2server/internal/server/events"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestInstalledHubCalendarsNeverRequestTheWrongPrefabKind(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	set, err := calendar.LoadDirectory("../../../../schedules", "2.35.10", "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	if err := set.ValidateDesign(root, "20260923193640"); err != nil {
		t.Fatal(err)
	}
	registry := events.NewRegistry()
	if err := registry.Replace(set.Events); err != nil {
		t.Fatal(err)
	}
	store := stateio.NewMemory()
	s, err := Open(store, root, "20260923193640", registry, &econ{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := set.ApplyReadonly(&readonly.Seed{Version: "2.35.10", Responses: map[string]readonly.Response{}})
	if err != nil {
		t.Fatal(err)
	}
	s.AttachHubCalendars(seed)
	for _, route := range []struct {
		path string
		kind uint64
	}{{"/EventHubInfo", 0}, {"/MiniEventHubInfo", 1}} {
		_, body, _, err := s.HandleSession(route.path, wire.AppendVarint(nil, 1, 1), "session")
		if err != nil {
			t.Fatal(route.path, err)
		}
		got := map[uint64]bool{}
		if err := wire.Walk(body, func(f wire.Field) error {
			if f.Number != 1 || f.Type != 2 {
				return nil
			}
			hubID := num(f.Value, 2)
			row, err := s.design.Row("PackEventHubTable", 14, hubID)
			if err != nil {
				return err
			}
			if num(row, 13) != route.kind {
				t.Fatalf("%s exposes incompatible HubType for hub %d", route.path, hubID)
			}
			got[num(f.Value, 1)] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, hub := range set.EventHubs {
			row, err := s.design.Row("PackEventHubTable", 14, hub.HubID)
			if err != nil {
				t.Fatal(err)
			}
			if got[hub.UID] != (num(row, 13) == route.kind) {
				t.Fatal("published hub lost or appeared in both routes", route.path, hub.HubID)
			}
		}
	}
	if saved, err := store.Load("eventplay"); err != nil || len(saved) != 0 {
		t.Fatal("public calendars persisted player state", err)
	}
}
