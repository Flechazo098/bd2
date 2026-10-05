// Package events holds the public event calendar and shared account rewards.
package events

import (
	"fmt"
	"sync"
	"time"

	"bd2server/internal/server/wire"
)

type Schedule struct {
	UID, Type, ID, SubID uint64
	Start, End           int64
}

// Resolver separates gameplay from the project's published calendar.
type Resolver interface {
	Resolve(uid uint64) (Schedule, error)
	List() []Schedule
}

type Registry struct {
	mu   sync.RWMutex
	rows []Schedule
	now  func() time.Time
}

func NewRegistry() *Registry { return &Registry{now: time.Now} }

// Replace atomically installs an explicitly configured calendar. An empty
// calendar is valid; gameplay code does not invent official event dates.
func (r *Registry) Replace(rows []Schedule) error {
	seen := map[string]bool{}
	for _, s := range rows {
		identity := fmt.Sprintf("uid:%d", s.UID)
		if s.UID == 0 {
			identity = fmt.Sprintf("public:%d:%d:%d", s.Type, s.ID, s.SubID)
		}
		if s.ID == 0 || s.Type > 25 || s.Type == 2 || s.End <= s.Start || seen[identity] {
			return fmt.Errorf("events: invalid or duplicate schedule %d", s.UID)
		}
		seen[identity] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append([]Schedule(nil), rows...)
	return nil
}

func (r *Registry) List() []Schedule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Schedule(nil), r.rows...)
}

func (r *Registry) Resolve(uid uint64) (Schedule, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result Schedule
	found := false
	for _, s := range r.rows {
		if s.UID == uid {
			if found {
				return Schedule{}, fmt.Errorf("events: ambiguous public schedule %d requires design identity", uid)
			}
			result, found = s, true
		}
	}
	if found {
		return result, nil
	}
	return Schedule{}, fmt.Errorf("events: unknown schedule %d", uid)
}

func (r *Registry) Handle(path string, req []byte) (int, []byte, bool, error) {
	if path != "/EventScheduleInfo" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return 163, nil, true, fmt.Errorf("events: invalid sequence")
	}
	var out []byte
	for _, s := range r.List() {
		b := wire.AppendVarint(nil, 1, s.UID)
		b = wire.AppendVarint(b, 2, s.Type)
		b = wire.AppendVarint(b, 3, s.ID)
		if s.SubID != 0 {
			b = wire.AppendVarint(b, 4, s.SubID)
		}
		b = wire.AppendVarint(b, 5, uint64(s.Start))
		b = wire.AppendVarint(b, 6, uint64(s.End))
		if now := r.now().UnixMilli(); now >= s.Start && now < s.End {
			b = wire.AppendVarint(b, 7, 1)
		}
		out = wire.AppendBytes(out, 1, b)
	}
	return 163, out, true, nil
}
