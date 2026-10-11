package pvp

import (
	"bd2server/internal/server/domain/command"
	"fmt"
)

// CloseSeason is an actor command barrier: its event follows all accepted
// results in that account's ordered transactional outbox.
func (s *Service) CloseSeason(ctx command.Context, mode Mode, seasonID uint64) error {
	if ctx.AccountID != s.profile.OwnerID || ctx.State == nil {
		return fmt.Errorf("pvp: season closure ownership mismatch")
	}
	var season Season
	for _, candidate := range s.rules.Seasons {
		if candidate.Mode == mode && candidate.ID == seasonID {
			season = candidate
			break
		}
	}
	if season.ID == 0 || uint64(s.now().UnixMilli()) < season.End {
		return fmt.Errorf("pvp: season cannot close before its deadline")
	}
	key := fmt.Sprintf("%s:%d", mode, seasonID)
	if _, exists, err := ctx.State.LoadEntry("pvp", "closed", key); err != nil {
		return err
	} else if exists {
		return nil
	}
	m := s.state.Modes[mode]
	if m.Active != nil && m.Active.Season == seasonID {
		m.Active = nil
	}
	if err := publish(ctx, "pvp:closed:"+s.profile.OwnerID+":"+key, "pvp.season.closed", SeasonClosed{Version: 1, OwnerID: s.profile.OwnerID, Mode: mode, Season: seasonID}); err != nil {
		return err
	}
	if err := ctx.State.PutEntry("pvp", "closed", key, []byte("true")); err != nil {
		return err
	}
	return s.save(ctx)
}
