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
	// Allocation is monotonic. Without a new ID, no existing row can match
	// the notification window, so read-only packets need not scan the inbox.
	if s.state.NextDynamicMailID == s.beforeMailID {
		return nil, nil
	}
	now := uint64(s.now().UnixMilli())
	for id, entry := range s.dynamic {
		if id >= s.beforeMailID && !containsID(s.state.Opened, id) && entry.ExpiresAt > now {
			return wire.AppendVarint(nil, 1, 1), nil // Notify.IsNewMail
		}
	}
	return nil, nil
}
