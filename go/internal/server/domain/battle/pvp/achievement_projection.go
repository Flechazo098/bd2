package pvp

// Retained season records prove actual victories and defeats; reset deck
// statistics and the current rank do not substitute for battle history.
func (s *Service) RecordedBattleCounts() map[[2]uint64]uint64 {
	out := map[[2]uint64]uint64{}
	for mode, state := range s.state.Modes {
		win, loss := uint64(37), uint64(38)
		if mode == Colosseum {
			win, loss = 138, 139
		}
		for _, progress := range state.Progress {
			out[[2]uint64{win, 0}] += progress.Wins + progress.DefenseWins
			out[[2]uint64{loss, 0}] += progress.Losses + progress.DefenseLosses
		}
	}
	return out
}
