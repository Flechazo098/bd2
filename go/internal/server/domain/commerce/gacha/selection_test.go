package gacha

import (
	"reflect"
	"slices"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func selectionService(t *testing.T) (*Service, command.Context) {
	t.Helper()
	characters := map[uint64]gamedata.CharacterDesign{}
	grades := map[uint64]uint64{700: 4}
	var pool []gamedata.WeightedCostume
	for id := uint64(100); id < 112; id++ {
		characters[id] = gamedata.CharacterDesign{ID: id}
		grades[id] = 5
		if id != 100 {
			pool = append(pool, gamedata.WeightedCostume{ID: id, Weight: 1})
		}
	}
	characters[700] = gamedata.CharacterDesign{ID: 700}
	pool = append(pool, gamedata.WeightedCostume{ID: 700, Weight: 1})
	catalog, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{
		1: {ID: 1, Count: 1, PriceType: 3, Price: 200, Pool: []gamedata.WeightedCostume{{ID: 100, Weight: 1}}, Grades: grades},
		2: {ID: 2, Count: 10, PriceType: 3, Price: 2000, Pool: pool, Grades: grades},
	}, characters)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.AddGroupDesign(gamedata.GachaGroupDesign{ID: 10, GachaType: 1, GachaSubType: 1, PointCount: 1, OneTimeGachaID: 1, TenTimeGachaID: 2, SelectCount: 12}, gamedata.GachaFixedDesign{}); err != nil {
		t.Fatal(err)
	}
	ctx := command.Context{Identity: command.Identity{SessionID: "selection-test"}, State: stateio.NewMemory()}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(&gamedata.InfiniteGachaDesign{FiveStarIDs: []uint64{999}}, catalog, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	return service, ctx
}

func selectionRequest(choices []roster.GachaSelection) []byte {
	request := wire.AppendVarint(nil, 1, 1)
	for _, choice := range choices {
		entry := wire.AppendVarint(nil, 1, choice.GroupID)
		if choice.Slot != 0 {
			entry = wire.AppendVarint(entry, 2, choice.Slot)
		}
		if choice.ItemID != 0 {
			entry = wire.AppendVarint(entry, 3, choice.ItemID)
		}
		request = wire.AppendBytes(request, 2, entry)
	}
	return request
}

func selectionInfo(t *testing.T, service *Service, ctx command.Context) []roster.GachaSelection {
	t.Helper()
	code, body, handled, err := service.Handle(ctx, "/GachaInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil || !handled || code != 145 {
		t.Fatalf("GachaInfo: code=%d handled=%v error=%v", code, handled, err)
	}
	var choices []roster.GachaSelection
	if err := wire.Walk(body, func(field wire.Field) error {
		if field.Number == 5 {
			group, _, err := wire.Varint(field.Value, 1)
			if err != nil {
				return err
			}
			slot, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			item, _, err := wire.Varint(field.Value, 3)
			choices = append(choices, roster.GachaSelection{GroupID: group, Slot: slot, ItemID: item})
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return choices
}

// GachaSelectionUI merges the single/ten reward lists and requires all twelve
// targets (LocalText 1500076/2638); it sends a GroupId-only row to clear them.
func TestSelectionUsesOwnBannerPoolAndRestoresAndClearsAfterRestart(t *testing.T) {
	service, ctx := selectionService(t)
	var want []roster.GachaSelection
	for i := range uint64(12) {
		want = append(want, roster.GachaSelection{GroupID: 10, Slot: i, ItemID: 100 + i})
	}
	reordered := append([]roster.GachaSelection(nil), want...)
	slices.Reverse(reordered)
	code, body, handled, err := service.Handle(ctx, "/GachaSelectionSave", selectionRequest(reordered))
	if err != nil || !handled || code != 198 || len(body) != 0 {
		t.Fatalf("save selection: code=%d handled=%v body=%x error=%v", code, handled, body, err)
	}
	reopen := func() {
		t.Helper()
		collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		service.collection = collection
	}
	reopen()
	if got := selectionInfo(t, service, ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("restarted banner choices=%v, want %v", got, want)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]roster.GachaSelection) []roster.GachaSelection
	}{
		{"another banner pool", func(v []roster.GachaSelection) []roster.GachaSelection { v[0].ItemID = 999; return v }},
		{"non five star", func(v []roster.GachaSelection) []roster.GachaSelection { v[0].ItemID = 700; return v }},
		{"duplicate target", func(v []roster.GachaSelection) []roster.GachaSelection { v[0].ItemID = v[1].ItemID; return v }},
		{"duplicate slot", func(v []roster.GachaSelection) []roster.GachaSelection { v[0].Slot = 1; return v }},
		{"out of range slot", func(v []roster.GachaSelection) []roster.GachaSelection { v[0].Slot = 12; return v }},
		{"mixed group", func(v []roster.GachaSelection) []roster.GachaSelection { v[1].GroupID = 20; return v }},
		{"incomplete set", func(v []roster.GachaSelection) []roster.GachaSelection { return v[:11] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.mutate(append([]roster.GachaSelection(nil), want...))
			if _, _, _, err := service.Handle(ctx, "/GachaSelectionSave", selectionRequest(bad)); err == nil {
				t.Fatal("invalid replacement accepted")
			}
			reopen()
			if got := selectionInfo(t, service, ctx); !reflect.DeepEqual(got, want) {
				t.Fatalf("rejected request changed saved choices: %v", got)
			}
		})
	}
	code, _, handled, err = service.Handle(ctx, "/GachaSelectionSave", selectionRequest([]roster.GachaSelection{{GroupID: 10}}))
	if err != nil || !handled || code != 198 {
		t.Fatalf("clear selection: code=%d handled=%v error=%v", code, handled, err)
	}
	reopen()
	if got := selectionInfo(t, service, ctx); len(got) != 0 {
		t.Fatalf("cleared targets restored after restart: %v", got)
	}
}

func TestClearingSelectionPreservesExhaustedChangeLimit(t *testing.T) {
	service, ctx := selectionService(t)
	if err := service.regular.AddGroupDesign(gamedata.GachaGroupDesign{ID: 20, GachaType: 1, GachaSubType: 1, PointCount: 1, OneTimeGachaID: 1, SelectCount: 1, SelectionChangeCount: 1}, gamedata.GachaFixedDesign{}); err != nil {
		t.Fatal(err)
	}
	selected := selectionRequest([]roster.GachaSelection{{GroupID: 20, ItemID: 100}})
	if _, _, _, err := service.Handle(ctx, "/GachaSelectionSave", selected); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle(ctx, "/GachaSelectionSave", selectionRequest([]roster.GachaSelection{{GroupID: 20}})); err != nil {
		t.Fatal(err)
	}
	collection, err := roster.OpenCollectionStore(ctx, stateio.RootStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.collection = collection
	if len(collection.GachaSelections(20)) != 0 {
		t.Fatal("clear did not persist")
	}
	if got := collection.GachaSelectionChangeCounts(); !reflect.DeepEqual(got, []roster.GachaSelectionChangeCount{{GroupID: 20, Count: 1}}) {
		t.Fatalf("clear changed the spent allowance: %v", got)
	}
	if _, _, _, err := service.Handle(ctx, "/GachaSelectionSave", selected); err == nil {
		t.Fatal("clearing an exhausted selection reset its change allowance")
	}
}
