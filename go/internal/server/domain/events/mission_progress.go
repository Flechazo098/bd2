package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/progression/conditions"
)

// AttachProgress reports currency spends and gains to mission conditions.
func (e *Economy) AttachProgress(record func(command.Context, conditions.Event) error) {
	e.progress = record
}

// MissionCurrency folds the currencies the client spends as one balance into
// the EElementType mission conditions name: a stacked AP into its free AP,
// and BonusHuntingAP (23) into FreeHuntingAP (21).
func MissionCurrency(t uint64) uint64 {
	if pair, ok := freeStackPairs[t]; ok {
		return pair[0]
	}
	if t == 23 {
		return 21
	}
	return t
}

func currency(t uint64) bool { return walletCurrency(t) || extraCurrency(t) || apCurrency(t) }

// recordCurrencies reports one applied exchange. Items are observed by the
// event mission inventory projection instead.
func (e *Economy) recordCurrencies(ctx command.Context, consumed []assets.Item, granted []gamedata.BattleReward) error {
	if e.progress == nil {
		return nil
	}
	var events []conditions.Event
	for _, c := range consumed {
		if currency(c.Type) && c.Count != 0 {
			events = append(events, conditions.Event{Type: conditions.ItemCurrencyUse, Sub: MissionCurrency(c.Type), Count: c.Count})
		}
	}
	for _, r := range granted {
		if currency(r.Type) && r.Count != 0 {
			events = append(events, conditions.Event{Type: conditions.ItemCurrencyGet, Sub: MissionCurrency(r.Type), Count: r.Count})
		}
	}
	for _, event := range events {
		if err := e.progress(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
