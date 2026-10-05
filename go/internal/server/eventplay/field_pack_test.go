package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/wire"
	"os"
	"testing"
	"time"
)

func TestClientConstantFieldBindingRequiresActiveMatchingEvent(t *testing.T) {
	s, _ := makeService(t)
	s.design.FieldPacks = map[int]gamedata.EventFieldPack{734: {ID: 734, MapIDs: []int{735}, InitialPosition: "{}"}}
	s.fieldBindings = []FieldBinding{{PackID: 734, EventType: 20, ContentOpenType: 20}}
	reg := events.NewRegistry()
	if err := reg.Replace([]events.Schedule{{UID: 32, Type: 20, ID: 81, Start: 100, End: 200}}); err != nil {
		t.Fatal(err)
	}
	s.registry = reg
	p, ok, err := s.ResolveEventFieldPack(734)
	if err != nil || !ok || p.ScheduleUID != 32 || p.ContentOpenType != 20 {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	s.now = func() time.Time { return time.UnixMilli(200) }
	if _, ok, err := s.ResolveEventFieldPack(734); err != nil || ok {
		t.Fatalf("expired tactics %v %v", ok, err)
	}
}

func TestInstalledTacticsFieldBinding(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	reg := events.NewRegistry()
	if err := reg.Replace([]events.Schedule{{UID: 81, Type: 20, ID: 8, Start: 1, End: 9999999999999}}); err != nil {
		t.Fatal(err)
	}
	// Use the real catalog but an in-memory account and controlled clock.
	d, err := gamedata.LoadEventPlayCatalog(root, "20260923193640")
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := makeService(t)
	svc.design = d
	svc.registry = reg
	if err = svc.AttachFieldBindingsFile("../../../seed/v2_35_10/event_field_bindings.json"); err != nil {
		t.Fatal(err)
	}
	p, ok, err := svc.ResolveEventFieldPack(20000)
	if err != nil || !ok || p.ContentOpenType != 20 || len(p.MapIDs) != 1 || p.MapIDs[0] != 200001 || p.BuyPrice != 0 {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
}

func TestHiddenHubPackUsesCalendarPlayWindowAndInstalledMaps(t *testing.T) {
	s, _ := makeService(t)
	s.registry = events.NewRegistry()
	s.design.FieldPacks = map[int]gamedata.EventFieldPack{912: gamedata.EventFieldPack{ID: 912, MapIDs: []int{991, 992}, InitialPosition: "{}", BuyType: 4, BuyPrice: 20}}
	s.design.Tables["PackEventHubTable"] = [][]byte{wire.AppendVarint(wire.AppendVarint(nil, 14, 55), 20, 912)}
	scalar := func(n int, v uint64) readonly.Field { return readonly.Field{Number: n, Varint: v} }
	s.AttachHubCalendars(&readonly.Seed{Responses: map[string]readonly.Response{"/EventHubInfo": {PacketCode: 222, Fields: []readonly.Field{{Number: 1, Type: 2, Fields: []readonly.Field{scalar(1, 77), scalar(2, 55), scalar(3, 100), scalar(4, 200), scalar(5, 300)}}}}}})
	p, ok, err := s.ResolveEventFieldPack(912)
	if err != nil || !ok || p.ScheduleUID != 77 || p.HubID != 55 || p.End != 200 || p.BuyPrice != 20 || p.InitialMapID != 991 || p.InitialPosition != "{}" {
		t.Fatalf("pack=%+v ok=%v err=%v", p, ok, err)
	}
	p.MapIDs[0] = 0
	again, _, _ := s.ResolveEventFieldPack(912)
	if again.MapIDs[0] != 991 {
		t.Fatal("caller changed design maps")
	}
	s.now = func() time.Time { return time.UnixMilli(200) }
	if _, ok, err := s.ResolveEventFieldPack(912); err != nil || ok {
		t.Fatalf("post-play purchase ok=%v err=%v", ok, err)
	}
}

func TestHiddenMiniGamePackRejectsForeignMapAndInactiveCalendar(t *testing.T) {
	s, _ := makeService(t)
	s.design.FieldPacks = map[int]gamedata.EventFieldPack{812: {ID: 812, MapIDs: []int{811}}}
	s.design.Tables["PackEventMiniGameTable"] = [][]byte{row(map[int]uint64{8: 1, 12: 812, 9: 810, 13: 101})}
	if _, _, err := s.ResolveEventFieldPack(812); err == nil {
		t.Fatal("foreign map accepted")
	}
	s.design.Tables["PackEventMiniGameTable"] = [][]byte{row(map[int]uint64{8: 1, 12: 812, 9: 811, 13: 101})}
	p, ok, err := s.ResolveEventFieldPack(812)
	if err != nil || !ok || p.GameID != 1 || p.PointPositionID != 101 {
		t.Fatalf("%+v %v %v", p, ok, err)
	}
	s.now = func() time.Time { return time.UnixMilli(9999999) }
	if _, ok, err := s.ResolveEventFieldPack(812); err != nil || ok {
		t.Fatalf("expired game available %v %v", ok, err)
	}
}
