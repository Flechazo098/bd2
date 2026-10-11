package ranking

import "context"

type Entry struct {
	Key   string
	Value []byte
}
type Tx interface {
	Get(string, string) ([]byte, bool, error)
	Put(string, string, []byte) error
	Scan(string, string, string, int) ([]Entry, error)
}
type Store interface {
	View(context.Context, func(Tx) error) error
	Update(context.Context, func(Tx) error) error
}
