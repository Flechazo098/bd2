package pvp

import (
	"fmt"
	"strconv"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) ticketReset() int64 {
	offset := (s.rules.Design.DailyResetSeconds - 9*3600) * 1000
	return (s.now().UnixMilli()-offset)/86400000*86400000 + offset
}
func (s *Service) RefreshTickets(ctx command.Context) error {
	if s.balance == nil {
		return fmt.Errorf("pvp: missing ticket balance source")
	}
	changed := false
	for _, mode := range []Mode{Mirror, Colosseum} {
		season, err := s.current(mode)
		if err != nil {
			return err
		}
		capacity := s.rules.Design.FreeMirrorTickets
		typ := uint64(15)
		reset := s.ticketReset()
		period := "day:" + strconv.FormatInt(reset, 10)
		if mode == Colosseum {
			if uint64(s.now().UnixMilli()) < season.Start || uint64(s.now().UnixMilli()) >= season.End {
				continue
			}
			typ = 52
			period = "season:" + strconv.FormatUint(season.ID, 10) + ":" + period
			values, err := s.rules.Design.ColosseumRule(season.ID, 9)
			if err != nil || len(values) != 1 {
				return fmt.Errorf("pvp: invalid arena ticket amount")
			}
			capacity = values[0]
			kind, err := s.rules.Design.ColosseumRule(season.ID, 10)
			if err != nil || len(kind) != 1 || kind[0] > 1 {
				return fmt.Errorf("pvp: invalid arena ticket reset")
			}
			if kind[0] == 1 {
				reset = int64(season.Start)
				period = "season:" + strconv.FormatUint(season.ID, 10)
			}
		}
		m := s.state.Modes[mode]
		if m.TicketPeriod == period {
			continue
		}
		balance, err := s.balance(ctx, typ)
		if err != nil {
			return err
		}
		id := fmt.Sprintf("pvp:tickets:%s:%s:%s", s.profile.OwnerID, mode, period)
		if balance > capacity {
			if err = s.spend(ctx, id+":remove", []gamedata.BattleReward{{Type: typ, Count: balance - capacity}}); err != nil {
				return err
			}
		} else if balance < capacity {
			if _, err = s.grant(ctx, id+":grant", []gamedata.BattleReward{{Type: typ, Count: capacity - balance}}); err != nil {
				return err
			}
		}
		m.TicketPeriod, m.TicketReset = period, reset
		changed = true
	}
	if changed {
		return s.save(ctx)
	}
	return nil
}
func (s *Service) spendTickets(ctx command.Context, mode Mode, season uint64, id string, multiplier uint64) error {
	free, paid, count := uint64(15), uint64(24), s.rules.Design.Mirror.TicketCost*multiplier
	if mode == Colosseum {
		values, err := s.rules.Design.ColosseumRule(season, 9)
		if err != nil || len(values) != 1 {
			return fmt.Errorf("pvp: invalid arena ticket amount")
		}
		if values[0] == 0 {
			return nil
		}
		free, paid, count = 52, 53, 1
	}
	have, err := s.balance(ctx, free)
	if err != nil {
		return err
	}
	fromFree := min(have, count)
	var costs []gamedata.BattleReward
	if fromFree > 0 {
		costs = append(costs, gamedata.BattleReward{Type: free, Count: fromFree})
	}
	if fromFree < count {
		costs = append(costs, gamedata.BattleReward{Type: paid, Count: count - fromFree})
	}
	return s.spend(ctx, id, costs)
}
func (s *Service) ChargeInfo(ctx command.Context) ([]byte, error) {
	if err := s.RefreshTickets(ctx); err != nil {
		return nil, err
	}
	var out []byte
	for _, mode := range []Mode{Mirror, Colosseum} {
		m := s.state.Modes[mode]
		if m.TicketPeriod == "" {
			continue
		}
		typ := uint64(15)
		if mode == Colosseum {
			typ = 52
		}
		count, err := s.balance(ctx, typ)
		if err != nil {
			return nil, err
		}
		item := wire.AppendVarint(nil, 3, typ)
		item = wire.AppendVarint(item, 4, count)
		info := wire.AppendVarint(nil, 1, uint64(m.TicketReset))
		info = wire.AppendBytes(info, 2, item)
		out = wire.AppendBytes(out, 1, info)
	}
	return out, nil
}
