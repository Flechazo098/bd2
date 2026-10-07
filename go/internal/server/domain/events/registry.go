// Package events holds the public event calendar and shared account rewards.
package events

import (
	"fmt"

	"time"
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

	r.rows = append([]Schedule(nil), rows...)
	return nil
}

func (r *Registry) List() []Schedule {

	return append([]Schedule(nil), r.rows...)
}

func (r *Registry) Resolve(uid uint64) (Schedule, error) {

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
