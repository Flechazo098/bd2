package mail

import

// BeforeDispatch and AfterDispatch observe newly allocated mail inside the
// account request transaction, including mail issued by other domains.
"bd2server/internal/server/domain/command"

func (s *Service) BeforeDispatch(ctx command.Context, _ string, _ []byte) error {

	s.beforeMailID = s.state.NextDynamicMailID
	return nil
}
