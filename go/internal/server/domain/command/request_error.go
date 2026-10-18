package command

import "fmt"

// RequestError identifies the item that failed inside an atomic command.
type RequestError struct {
	Path     string
	Sequence uint64
	Cause    error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("%s seq=%d: %v", e.Path, e.Sequence, e.Cause)
}

func (e *RequestError) Unwrap() error { return e.Cause }
