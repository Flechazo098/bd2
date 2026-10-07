package calendaradapter

import (
	"bd2server/internal/server/domain/events/calendar"
	"bd2server/internal/server/protocol/staticdata"
	"fmt"
	"maps"
)

func scalar(n int, v uint64) readonly.Field { return readonly.Field{Number: n, Type: 0, Varint: v} }
func message(n int, f []readonly.Field) readonly.Field {
	return readonly.Field{Number: n, Type: 2, Fields: f}
}
func flag(n int, b bool) readonly.Field {
	if b {
		return scalar(n, 1)
	}
	return scalar(n, 0)
}
func seasonFields(s calendar.Season) []readonly.Field {
	a, _ := calendar.ParseTimestamp(s.Start)
	b, _ := calendar.ParseTimestamp(s.End)
	return []readonly.Field{scalar(1, s.ID), scalar(2, a), scalar(3, b), flag(4, s.Error), flag(5, s.Return), scalar(6, s.RankRewardGroupID)}
}

// ApplyReadonly returns a separate seed with every calendar endpoint encoded
// entirely from the loaded files. Commerce supplies reset times from its
// current reset rules and clock when serving CashShopInfo.
func ApplyStaticData(s *calendar.Set, seed *readonly.Seed) (*readonly.Seed, error) {
	if s == nil || seed == nil {
		return nil, fmt.Errorf("calendar: missing calendar/static seed")
	}
	result := &readonly.Seed{Version: seed.Version, Responses: make(map[string]readonly.Response, len(seed.Responses))}
	maps.Copy(result.Responses, seed.Responses)
	var hunt []readonly.Field
	if h := s.MonsterHunt; h != nil {
		for _, v := range h.Seasons {
			end, _ := calendar.ParseTimestamp(v.CalculateEndAt)
			f := []readonly.Field{message(1, seasonFields(v.Season)), scalar(2, v.HuntID), scalar(3, v.InfoOpenDay), scalar(4, end), flag(5, v.ErrorFlag), flag(6, v.IndependentFlag), scalar(7, v.RankRewardGroupID)}
			for _, id := range v.CostumeBanIDs {
				f = append(f, scalar(8, id))
			}
			for _, id := range v.BurstBanIDs {
				f = append(f, scalar(9, id))
			}
			hunt = append(hunt, message(1, f))
		}
		hunt = append(hunt, scalar(2, h.StartRegularSeason))
		for _, v := range h.History {
			hunt = append(hunt, message(3, []readonly.Field{scalar(1, v.Season), scalar(2, v.HuntID), flag(3, v.ErrorFlag), flag(4, v.Hidden)}))
		}
	}
	result.Responses["/MonsterHuntScheduleInfo"] = readonly.Response{PacketCode: 0, Fields: hunt}
	var cash []readonly.Field
	for _, v := range s.CashProducts {
		var a, b uint64
		if v.Start != "" {
			a, _ = calendar.ParseTimestamp(v.Start)
		}
		if v.End != "" {
			b, _ = calendar.ParseTimestamp(v.End)
		}
		cash = append(cash, message(1, []readonly.Field{scalar(1, v.GroupID), scalar(2, v.ProductID), scalar(3, v.SaleGroup), scalar(4, a), scalar(5, b), scalar(6, v.EndDelayMinutes), scalar(8, v.EventIndex)}))
	}
	result.Responses["/CashShopInfo"] = readonly.Response{PacketCode: 60, Fields: cash}
	var hubs []readonly.Field
	for _, v := range s.EventHubs {
		a, _ := calendar.ParseTimestamp(v.Start)
		b, _ := calendar.ParseTimestamp(v.PlayEnd)
		c, _ := calendar.ParseTimestamp(v.End)
		f := []readonly.Field{scalar(1, v.UID), scalar(2, v.HubID), scalar(3, a), scalar(4, b), scalar(5, c)}
		for _, setting := range v.Settings {
			sf := []readonly.Field{scalar(1, setting.Slot), scalar(2, setting.ProgressType)}
			for _, uid := range setting.EventUIDs {
				sf = append(sf, scalar(3, uid))
			}
			f = append(f, message(6, sf))
		}
		hubs = append(hubs, message(1, f))
	}
	result.Responses["/EventHubInfo"] = readonly.Response{PacketCode: 0, Fields: hubs}
	var mini []readonly.Field
	for _, v := range s.MiniGameHubs {
		mini = append(mini, message(1, []readonly.Field{scalar(1, v.Slot), scalar(2, v.EventUID), scalar(3, v.ProgressType)}))
	}
	result.Responses["/MiniGameHubInfo"] = readonly.Response{PacketCode: 390, Fields: mini}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}
