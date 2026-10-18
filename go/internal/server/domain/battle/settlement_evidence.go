package battle

import "bd2server/internal/server/domain/command"

type SettlementEvidence struct {
	Receipt                             string
	Mode, Deck, Monster, Round, Attempt uint64
	InitialBlue                         [][]byte
}

func (s *Service) SettlementEvidence(ctx command.Context) SettlementEvidence {
	v := s.stateLocked(ctx)
	if !v.Entered {
		return SettlementEvidence{}
	}
	return SettlementEvidence{v.EnterReceipt, v.Mode, v.Deck, v.Monster, v.Round, v.Attempt, v.InitialBlue}
}
