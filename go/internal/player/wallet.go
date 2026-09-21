package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"bd2server/internal/gamedata"
)

// Currency uses the UserDBInfo currency fields: type 3 is free jewelry and
// type 4 is gold. Paid jewelry is persisted as well, although quest rewards
// in the audited tutorial range do not grant it.
type Currency struct {
	Gold        uint64 `json:"gold"`
	FreeJewelry uint64 `json:"free_jewelry"`
	Jewelry     uint64 `json:"jewelry"`
	Mileage     uint64 `json:"mileage,omitempty"`
	HopePowder  uint64 `json:"hope_powder,omitempty"`
}

type walletSnapshot struct {
	Version string `json:"version"`
	Currency
	Granted map[string]bool `json:"granted"`
	Spent   map[string]bool `json:"spent,omitempty"`
}

type Wallet struct {
	mu    sync.Mutex
	path  string
	state walletSnapshot
}

func OpenWallet(path string, initial Currency) (*Wallet, error) {
	if path == "" {
		return nil, errors.New("player: wallet path is empty")
	}
	s := &Wallet{path: filepath.Clean(path), state: walletSnapshot{
		Version: "2.34.13", Currency: initial, Granted: map[string]bool{}, Spent: map[string]bool{},
	}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("player: read wallet: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != "2.34.13" || s.state.Granted == nil {
		return nil, errors.New("player: malformed wallet state")
	}
	if s.state.Spent == nil {
		s.state.Spent = map[string]bool{}
	}
	return s, nil
}

func (s *Wallet) Snapshot() Currency {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Currency
}

func (s *Wallet) WasGranted(identity string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Granted[identity]
}

func (s *Wallet) CanSpendFreeJewelry(amount uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return amount > 0 && s.state.FreeJewelry >= amount
}

func (s *Wallet) CanSpendGold(amount uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return amount > 0 && s.state.Gold >= amount
}

// SpendGoldOnce covers the currency part of a GameData-defined class-up.
// Its identity is the old character instance/stage so an interrupted request
// cannot charge the same promotion a second time.
func (s *Wallet) SpendGoldOnce(identity string, amount uint64) (Currency, error) {
	if identity == "" || amount == 0 {
		return Currency{}, errors.New("player: invalid gold spend")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Spent[identity] {
		return s.state.Currency, nil
	}
	if s.state.Gold < amount {
		return Currency{}, errors.New("player: insufficient gold")
	}
	next := cloneWallet(s.state)
	next.Gold -= amount
	next.Spent[identity] = true
	if err := s.commit(next); err != nil {
		return Currency{}, err
	}
	return next.Currency, nil
}

func (s *Wallet) WasSpent(identity string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Spent[identity]
}

func (s *Wallet) SpendFreeJewelryOnce(identity string, amount uint64) (Currency, error) {
	return s.spendJewelryOnce(identity, amount, false)
}

func (s *Wallet) SpendJewelryOnce(identity string, amount uint64) (Currency, error) {
	return s.spendJewelryOnce(identity, amount, true)
}

func (s *Wallet) spendJewelryOnce(identity string, amount uint64, paid bool) (Currency, error) {
	if identity == "" || amount == 0 {
		return Currency{}, errors.New("player: invalid wallet spend")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Spent[identity] {
		return s.state.Currency, nil
	}
	next := cloneWallet(s.state)
	if paid {
		if next.Jewelry < amount {
			return Currency{}, errors.New("player: insufficient paid jewelry")
		}
		next.Jewelry -= amount
	} else {
		if next.FreeJewelry < amount {
			return Currency{}, errors.New("player: insufficient free jewelry")
		}
		next.FreeJewelry -= amount
	}
	next.Spent[identity] = true
	if err := s.commit(next); err != nil {
		return Currency{}, err
	}
	return next.Currency, nil
}

// Currencies implements account.CurrencyProvider.
func (s *Wallet) Currencies() (gold, freeJewelry, jewelry, mileage uint64) {
	c := s.Snapshot()
	return c.Gold, c.FreeJewelry, c.Jewelry, c.Mileage
}

func (s *Wallet) HopePowderBalance() uint64 { return s.Snapshot().HopePowder }

// GrantMileageOnce persists the type-20 currency produced when a duplicate
// costume is drawn after +5. The gacha grant identity makes recovery after a
// collection/wallet split commit safe and idempotent.
func (s *Wallet) GrantMileageOnce(identity string, amount uint64) (Currency, error) {
	if identity == "" || amount == 0 {
		return Currency{}, errors.New("player: invalid mileage grant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Granted[identity] {
		return s.state.Currency, nil
	}
	next := cloneWallet(s.state)
	if math.MaxUint64-next.Mileage < amount {
		return Currency{}, errors.New("player: mileage overflow")
	}
	next.Mileage += amount
	next.Granted[identity] = true
	if err := s.commit(next); err != nil {
		return Currency{}, err
	}
	return next.Currency, nil
}

func (s *Wallet) GrantHopePowderOnce(identity string, amount uint64) (Currency, error) {
	if identity == "" || amount == 0 {
		return Currency{}, errors.New("player: invalid hope powder grant")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Granted[identity] {
		return s.state.Currency, nil
	}
	next := cloneWallet(s.state)
	if math.MaxUint64-next.HopePowder < amount {
		return Currency{}, errors.New("player: hope powder overflow")
	}
	next.HopePowder += amount
	next.Granted[identity] = true
	if err := s.commit(next); err != nil {
		return Currency{}, err
	}
	return next.Currency, nil
}

// GrantQuestOnce applies only currency rewards and is idempotent by the same
// quest identity used by the entity stores. Unknown reward types are ignored;
// callers dispatch those to their owning inventory domain.
func (s *Wallet) GrantQuestOnce(identity string, rewards []gamedata.Reward) (Currency, error) {
	if identity == "" {
		return Currency{}, errors.New("player: missing wallet grant identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Granted[identity] {
		return s.state.Currency, nil
	}
	next := cloneWallet(s.state)
	for _, reward := range rewards {
		switch reward.Type {
		case 3:
			if math.MaxUint64-next.FreeJewelry < reward.Count {
				return Currency{}, errors.New("player: free jewelry overflow")
			}
			next.FreeJewelry += reward.Count
		case 4:
			if math.MaxUint64-next.Gold < reward.Count {
				return Currency{}, errors.New("player: gold overflow")
			}
			next.Gold += reward.Count
		}
	}
	next.Granted[identity] = true
	if err := s.commit(next); err != nil {
		return Currency{}, err
	}
	return next.Currency, nil
}

func cloneWallet(in walletSnapshot) walletSnapshot {
	out := walletSnapshot{Version: in.Version, Currency: in.Currency, Granted: make(map[string]bool, len(in.Granted)), Spent: make(map[string]bool, len(in.Spent))}
	for key, value := range in.Granted {
		out.Granted[key] = value
	}
	for key, value := range in.Spent {
		out.Spent[key] = value
	}
	return out
}

func (s *Wallet) commit(next walletSnapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".wallet-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("player: persist wallet: %w", err)
	}
	s.state = next
	return nil
}
