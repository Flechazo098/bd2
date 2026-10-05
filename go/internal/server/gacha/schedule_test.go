package gacha

import (
	"encoding/base64"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func fixtureSchedule(t *testing.T) (*ScheduleSeed, error) {
	golden, err := base64.StdEncoding.DecodeString("ChEIpgEQgMi/3Iw0GJiIvcaRNAoSCLrqARCA8PTEiDQYmIi9xpE0ChIIu+oBEIDw9MSINBiYiL3GkTQKEQjOARCA8PTEiDQYmIi9xpE0ChEIzQEQgPD0xIg0GJiIvcaRNAoRCNABEIDIv9yMNBiYiL3GkTQKEQiZARCAyL/cjDQYmIi9xpE0ChAISBCAyL/cjDQYmIi9xpE0ChAIRxCAyL/cjDQYmIi9xpE0ChEIzwEQgMi/3Iw0GJiIvcaRNAoRCPEHEIDo2beLNBiA+L34jzQSBAgCGAo6EAgdEIDw9MSINBiYiL3GkTQ6EAgeEIDIv9yMNBiYiL3GkTRKUAiTCUJLGgMQvR4aBRCFBzABGgUQ0ggwAhoGEOn7AzADGgYQzeMDMAQaBhDF7gMwBRoGEJnsAzAGGgYQvfkDMAcaBhDu6QMwCBoGEKnvAzAJ")
	if err != nil {
		t.Fatal(err)
	}
	return &ScheduleSeed{ClientVersion: "2.35.10", Schedules: collectScheduleWindows(t, golden, 1), StepUps: collectScheduleWindows(t, golden, 7)}, nil
}

func TestActivePickupCostumesUsesHalfOpenScheduleWindows(t *testing.T) {
	character := gamedata.CharacterDesign{ID: 1, HP: 1, CostumeMaxLevel: 5}
	catalog, err := fixtureRegularCatalog(map[uint64]gamedata.RegularGacha{
		11: {ID: 11, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 101, Weight: 1}}},
		12: {ID: 12, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 102, Weight: 1}}},
	}, map[uint64]gamedata.CharacterDesign{101: character, 102: character})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []gamedata.GachaGroupDesign{
		{ID: 1, GachaType: 1, PointCount: 1, PickUpCostumeID: 101, OneTimeGachaID: 11},
		{ID: 2, GachaType: 1, PointCount: 1, PickUpCostumeID: 102, OneTimeGachaID: 12},
	} {
		if err := catalog.AddGroupDesign(group, gamedata.GachaFixedDesign{}); err != nil {
			t.Fatal(err)
		}
	}
	seed := &ScheduleSeed{Schedules: []ScheduleWindow{
		{GroupID: 1, StartTime: 100, EndTime: 200},
		{GroupID: 2, StartTime: 200, EndTime: 300},
	}}
	if got := ActivePickupCostumes(catalog, seed, 100); !got[101] || got[102] || len(got) != 1 {
		t.Fatalf("at start active=%v", got)
	}
	if got := ActivePickupCostumes(catalog, seed, 200); got[101] || !got[102] || len(got) != 1 {
		t.Fatalf("at end active=%v", got)
	}
}

func TestGachaInfoUsesInjectedScheduleAndEmptyAccountHasNoPreview(t *testing.T) {
	seed, err := fixtureSchedule(t)
	if err != nil {
		t.Fatal(err)
	}
	character := gamedata.CharacterDesign{ID: 1, HP: 1, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 1}
	design, err := fixtureInfiniteGachaDesign(10, []uint64{11}, map[uint64]gamedata.CharacterDesign{11: character})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := fixtureRegularCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 11, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{11: character})
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(design, regular, collection, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachSchedule(seed); err != nil {
		t.Fatal(err)
	}
	code, response, handled, err := service.Handle("/GachaInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil || !handled || code != 145 {
		t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
	}
	regularWindows := collectScheduleWindows(t, response, 1)
	stepWindows := collectScheduleWindows(t, response, 7)
	if len(regularWindows) != 11 || len(stepWindows) != 2 || countFields(response, 9) != 0 {
		t.Fatalf("schedule=%d step=%d preview=%d", len(regularWindows), len(stepWindows), countFields(response, 9))
	}
	for i := range seed.Schedules {
		if regularWindows[i] != seed.Schedules[i] {
			t.Fatalf("schedule %d response=%+v seed=%+v", i, regularWindows[i], seed.Schedules[i])
		}
	}
}

func collectScheduleWindows(t *testing.T, proto []byte, number int) []ScheduleWindow {
	t.Helper()
	var result []ScheduleWindow
	if err := wire.Walk(proto, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		group, _, err := wire.Varint(field.Value, 1)
		if err != nil {
			return err
		}
		start, _, err := wire.Varint(field.Value, 2)
		if err != nil {
			return err
		}
		end, _, err := wire.Varint(field.Value, 3)
		if err != nil {
			return err
		}
		free, _, err := wire.Varint(field.Value, 4)
		if err != nil {
			return err
		}
		cash, _, err := wire.Varint(field.Value, 5)
		if err != nil {
			return err
		}
		result = append(result, ScheduleWindow{GroupID: group, StartTime: start, EndTime: end, FreeCountBonus: free != 0, CashCountBonus: cash != 0})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
