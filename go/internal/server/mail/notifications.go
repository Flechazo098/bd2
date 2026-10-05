package mail

import "bd2server/internal/server/wire"

// BeforeDispatch and AfterDispatch observe newly allocated mail inside the
// account request transaction, including mail issued by other domains.
func (s *Service) BeforeDispatch(string, []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforeMailID = s.state.NextDynamicMailID
	return nil
}

func (s *Service) AfterDispatch(string, []byte, []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.dynamic {
		if id >= s.beforeMailID && !containsID(s.state.Opened, id) && entry.ExpiresAt > uint64(s.now().UnixMilli()) {
			return wire.AppendVarint(nil, 1, 1), nil // Notify.IsNewMail
		}
	}
	return nil, nil
}
