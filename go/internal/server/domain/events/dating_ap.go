package events

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// AttachDatingAP configures the daily free encounter allowance.
func (e *Economy) AttachDatingAP(max uint64) error {
	if max == 0 || max > math.MaxInt32 {
		return fmt.Errorf("events: invalid dating AP maximum")
	}
	e.datingCap = max
	if e.now == nil {
		e.now = time.Now
	}
	return nil
}
func (e *Economy) CurrencyBalance(ctx command.Context, typ uint64) (uint64, error) {
	if !extraCurrency(typ) {
		return 0, fmt.Errorf("events: unsupported additional balance")
	}
	s, err := e.load(ctx)
	if err != nil {
		return 0, err
	}
	if err = e.refreshAP(ctx, &s); err != nil {
		return 0, err
	}
	return s.Balances[typ], nil
}

func (e *Economy) refreshDatingAP(ctx command.Context, s *economySnapshot) error {
	if e.datingCap == 0 {
		return nil
	}
	now := e.now().UnixMilli()
	offset := (e.resetSeconds - 9*3600) * 1000
	next := ((now-offset)/86400000+1)*86400000 + offset
	raw, err := e.store.Load(ctx.State, "datingapclock")
	if err != nil {
		return err
	}
	clock := economyAPClock{Next: next}
	if raw != nil {
		if err = stateio.RequireExactJSONObject(raw, "next"); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &clock); err != nil {
			return err
		}
		if now < clock.Next {
			return nil
		}
	}
	s.Balances[41] = e.datingCap
	clock.Next = next
	raw, err = json.Marshal(clock)
	if err != nil {
		return err
	}
	if err = e.store.Save(ctx.State, "datingapclock", raw); err != nil {
		return err
	}
	raw, err = json.Marshal(s)
	if err != nil {
		return err
	}
	return e.store.Save(ctx.State, "event_economy", raw)
}
