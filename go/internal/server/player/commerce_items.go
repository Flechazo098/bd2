package player

import (
	"fmt"
	"math"
)

// GrantCommerceOnce preserves the exact item expiry supplied by the commerce
// entitlement policy. It shares the existing atomic inventory layout.
func (s *Inventory) GrantCommerceOnce(identity string, rewards []Item) ([]Item, error) {
	if identity == "" {
		return nil, fmt.Errorf("player: missing commerce reward identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owned.Granted[identity] {
		var out []Item
		for _, idx := range s.owned.GrantItems[identity] {
			for _, item := range s.owned.Items {
				if item.InvenIndex == idx {
					out = append(out, item)
				}
			}
		}
		return out, nil
	}
	next := cloneOwnedSnapshot(s.owned)
	var out []Item
	for _, r := range rewards {
		if r.ID == 0 || (r.Type != 62 && r.Type != 19) || r.Count == 0 || r.Count > math.MaxInt32 || r.Type == 19 && r.ExpiryTime == 0 || next.NextIndex == math.MaxUint64 {
			return nil, fmt.Errorf("player: invalid commerce item")
		}
		if r.Type == 19 {
			found := false
			for i, current := range next.Items {
				if current.Type == 19 && current.ID == r.ID {
					r.InvenIndex = current.InvenIndex
					r.Count = 1
					next.Items[i] = r
					next.GrantItems[identity] = append(next.GrantItems[identity], r.InvenIndex)
					out = append(out, r)
					found = true
					break
				}
			}
			if found {
				continue
			}
			r.Count = 1
		}
		r.InvenIndex = next.NextIndex
		next.NextIndex++
		next.Items = append(next.Items, r)
		next.GrantItems[identity] = append(next.GrantItems[identity], r.InvenIndex)
		out = append(out, r)
	}
	next.Granted[identity] = true
	if err := s.commitOwned(next); err != nil {
		return nil, err
	}
	s.owned = next
	return out, nil
}
func (s *Inventory) ContentTicketExpiry(id uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expiry uint64
	for _, items := range [][]Item{s.starter.Items, s.owned.Items} {
		for _, i := range items {
			if i.Type == 19 && i.ID == id && i.Count > 0 && i.ExpiryTime > expiry {
				expiry = i.ExpiryTime
			}
		}
	}
	return expiry
}
