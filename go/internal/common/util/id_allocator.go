package util

import (
	"errors"
	"sync"
)

type InstanceIDSource interface {
	NewInstanceID() (uint64, error)
}

// IDAllocator leases committed ranges; unused IDs are discarded after a crash.
type IDAllocator struct {
	mu      sync.Mutex
	next    uint64
	end     uint64
	reserve func(uint64) (uint64, error)
}

func NewIDAllocator(reserve func(uint64) (uint64, error)) *IDAllocator {
	return &IDAllocator{reserve: reserve}
}

func (a *IDAllocator) NewInstanceID() (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next == a.end {
		if a.reserve == nil {
			return 0, errors.New("instance IDs: missing durable range source")
		}
		const count = 512
		first, err := a.reserve(count)
		if err != nil {
			return 0, err
		}
		if first == 0 || first > ^uint64(0)-count {
			return 0, errors.New("instance IDs: invalid reserved range")
		}
		a.next, a.end = first, first+count
	}
	id := a.next
	a.next++
	return id, nil
}
