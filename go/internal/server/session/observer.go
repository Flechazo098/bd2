package session

import "fmt"

func (s *Server) dispatchObserved(path string, request []byte) (int, []byte, []byte, error) {
	for _, observer := range s.observers {
		if err := observer.BeforeDispatch(path, request); err != nil {
			return 0, nil, nil, fmt.Errorf("%s before response observation: %w", path, err)
		}
	}
	code, response, err := s.dispatch(path, request)
	if err != nil {
		return 0, nil, nil, err
	}
	var notify []byte
	for _, observer := range s.observers {
		fields, err := observer.AfterDispatch(path, request, response)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("%s after response observation: %w", path, err)
		}
		notify = append(notify, fields...)
	}
	return code, response, notify, nil
}
