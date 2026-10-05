package player

import (
	"fmt"
	"math"

	"bd2server/internal/server/gamedata"
)

// ExchangeOnce applies a server-calculated cost and reward in one wallet save.
// The caller's account transaction also owns inventory and gameplay receipts.
func (s *Wallet) ExchangeOnce(identity string, costs, rewards []gamedata.Reward) error {
	if identity == "" {
		return fmt.Errorf("player: missing wallet exchange identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Spent[identity] {
		return nil
	}
	next := cloneWallet(s.state)
	if err := exchangeCurrency(&next.Currency, costs, rewards); err != nil {
		return err
	}
	next.Spent[identity] = true
	return s.commit(next, entry("spent", identity, []byte("true"))...)
}

func (s *Wallet) CanExchange(costs, rewards []gamedata.Reward) error {
	c := s.Snapshot()
	return exchangeCurrency(&c, costs, rewards)
}

func exchangeCurrency(c *Currency, costs, rewards []gamedata.Reward) error {
	value := func(t uint64) *uint64 {
		switch t {
		case 2:
			return &c.Jewelry
		case 3:
			return &c.FreeJewelry
		case 4:
			return &c.Gold
		case 12:
			return &c.Catalyst
		case 20:
			return &c.Mileage
		case 22:
			return &c.HopePowder
		case 68:
			return &c.EquipMileage
		}
		return nil
	}
	for _, r := range costs {
		v := value(r.Type)
		if v == nil || r.ID != 0 || r.Count == 0 || *v < r.Count {
			return fmt.Errorf("player: invalid or insufficient currency %d", r.Type)
		}
		*v -= r.Count
	}
	for _, r := range rewards {
		v := value(r.Type)
		if v == nil || r.ID != 0 || r.Count == 0 || *v > math.MaxInt64-r.Count || r.Count > math.MaxInt64 {
			return fmt.Errorf("player: invalid or overflowing currency %d", r.Type)
		}
		*v += r.Count
	}
	return nil
}
