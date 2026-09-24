package gacha

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func TestVersionedScheduleMatchesOfficial23510GachaInfo(t *testing.T) {
	seed, err := LoadScheduleSeed(filepath.Join("..", "..", "seed", "v2_34_13", "gacha_schedule.json"), "2.35.10")
	if err != nil {
		t.Fatal(err)
	}
	// Official 2.35.10 GachaInfo, captured 2026-09-23. It is evidence used by
	// this test only; production loads the semantic JSON seed above.
	golden, err := base64.StdEncoding.DecodeString("ChEIpgEQgMi/3Iw0GJiIvcaRNAoSCLrqARCA8PTEiDQYmIi9xpE0ChIIu+oBEIDw9MSINBiYiL3GkTQKEQjOARCA8PTEiDQYmIi9xpE0ChEIzQEQgPD0xIg0GJiIvcaRNAoRCNABEIDIv9yMNBiYiL3GkTQKEQiZARCAyL/cjDQYmIi9xpE0ChAISBCAyL/cjDQYmIi9xpE0ChAIRxCAyL/cjDQYmIi9xpE0ChEIzwEQgMi/3Iw0GJiIvcaRNAoRCPEHEIDo2beLNBiA+L34jzQSBAgCGAo6EAgdEIDw9MSINBiYiL3GkTQ6EAgeEIDIv9yMNBiYiL3GkTRKUAiTCUJLGgMQvR4aBRCFBzABGgUQ0ggwAhoGEOn7AzADGgYQzeMDMAQaBhDF7gMwBRoGEJnsAzAGGgYQvfkDMAcaBhDu6QMwCBoGEKnvAzAJ")
	if err != nil {
		t.Fatal(err)
	}
	regular := collectScheduleWindows(t, golden, 1)
	steps := collectScheduleWindows(t, golden, 7)
	if len(regular) != len(seed.Schedules) || len(steps) != len(seed.StepUps) {
		t.Fatalf("golden schedules=%d/%d seed=%d/%d", len(regular), len(steps), len(seed.Schedules), len(seed.StepUps))
	}
	for i := range seed.Schedules {
		if regular[i] != seed.Schedules[i] {
			t.Fatalf("schedule %d golden=%+v seed=%+v", i, regular[i], seed.Schedules[i])
		}
	}
	for i := range seed.StepUps {
		if steps[i] != seed.StepUps[i] {
			t.Fatalf("step-up %d golden=%+v seed=%+v", i, steps[i], seed.StepUps[i])
		}
	}
	if countFields(golden, 9) != 1 {
		t.Fatal("official golden must retain the player preview used to verify field 9 separately")
	}
}

func TestScheduleSeedStrictValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gacha.json")
	bad := `{"client_version":"2.35.10","schedules":[{"group_id":1,"start_time":1,"end_time":2}],"step_ups":[{"group_id":2,"start_time":1,"end_time":2}],"unknown":true}`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadScheduleSeed(path, "2.35.10"); err == nil {
		t.Fatal("unknown schedule field was accepted")
	}
	if _, err := LoadScheduleSeed(filepath.Join("..", "..", "seed", "v2_34_13", "gacha_schedule.json"), "2.34.13"); err == nil {
		t.Fatal("wrong client version was accepted")
	}
}

func TestGachaInfoUsesInjectedScheduleAndEmptyAccountHasNoPreview(t *testing.T) {
	seed, err := LoadScheduleSeed(filepath.Join("..", "..", "seed", "v2_34_13", "gacha_schedule.json"), "2.35.10")
	if err != nil {
		t.Fatal(err)
	}
	character := gamedata.CharacterDesign{ID: 1, HP: 1, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 1}
	design, err := gamedata.NewInfiniteGachaDesign(10, []uint64{11}, map[uint64]gamedata.CharacterDesign{11: character})
	if err != nil {
		t.Fatal(err)
	}
	regular, err := gamedata.NewRegularGachaCatalog(map[uint64]gamedata.RegularGacha{1: {ID: 1, Count: 1, PriceType: 3, Price: 1, Pool: []gamedata.WeightedCostume{{ID: 11, Weight: 1}}}}, map[uint64]gamedata.CharacterDesign{11: character})
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
