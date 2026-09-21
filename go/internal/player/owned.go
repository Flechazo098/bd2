package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

// Inventory holds player-owned rewards separately from the immutable starter
// seed. GrantOnce uses a battle identity to prevent double-credit on retries.
type Inventory struct {
	mu      sync.Mutex
	path    string
	starter *Starter
	owned   ownedSnapshot
}

type ownedSnapshot struct {
	Version    string              `json:"version"`
	NextIndex  uint64              `json:"next_index"`
	Items      []Item              `json:"items"`
	Granted    map[string]bool     `json:"granted"`
	GrantItems map[string][]uint64 `json:"grant_items,omitempty"`
}

func OpenInventory(path string, starter *Starter) (*Inventory, error) {
	if starter == nil || starter.Validate() != nil || path == "" {
		return nil, errors.New("player: invalid inventory configuration")
	}
	s := &Inventory{path: filepath.Clean(path), starter: starter, owned: ownedSnapshot{Version: "2.34.13", NextIndex: 900000001, Granted: map[string]bool{}, GrantItems: map[string][]uint64{}}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.owned); err != nil {
		return nil, fmt.Errorf("player: decode inventory: %w", err)
	}
	if s.owned.Version != "2.34.13" || s.owned.NextIndex < 900000001 || s.owned.Granted == nil {
		return nil, errors.New("player: invalid saved inventory")
	}
	if s.owned.GrantItems == nil {
		s.owned.GrantItems = map[string][]uint64{}
	}
	return s, nil
}

func (s *Inventory) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/ItemInfo" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Item, 0, len(s.starter.Items)+len(s.owned.Items))
	items = append(items, s.starter.Items...)
	items = append(items, s.owned.Items...)
	return (&Starter{Version: "2.34.13", Items: items}).Handle(path, request)
}

func (s *Inventory) All() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Item, 0, len(s.starter.Items)+len(s.owned.Items))
	items = append(items, s.starter.Items...)
	items = append(items, s.owned.Items...)
	return items
}

func (s *Inventory) GrantOnce(identity string, rewards []gamedata.BattleReward) ([]Item, error) {
	if identity == "" {
		return nil, errors.New("player: missing reward identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owned.Granted[identity] {
		return nil, nil
	}
	next := ownedSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex,
		Items: append([]Item(nil), s.owned.Items...), Granted: make(map[string]bool, len(s.owned.Granted)+1), GrantItems: make(map[string][]uint64, len(s.owned.GrantItems)+1)}
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	for k, v := range s.owned.GrantItems {
		next.GrantItems[k] = append([]uint64(nil), v...)
	}
	newItems := make([]Item, 0, len(rewards))
	for _, r := range rewards {
		if r.ID == 0 || r.Type == 0 || r.Count == 0 {
			return nil, errors.New("player: invalid battle reward")
		}
		item := Item{InvenIndex: next.NextIndex, ID: r.ID, Type: r.Type, Count: r.Count, TimeValue: uint64(time.Now().UnixMilli())}
		next.NextIndex++
		next.Items = append(next.Items, item)
		newItems = append(newItems, item)
		next.GrantItems[identity] = append(next.GrantItems[identity], item.InvenIndex)
	}
	next.Granted[identity] = true
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".inventory-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return nil, err
	}
	s.owned = next
	return newItems, nil
}

// GrantedItems returns the stable instances created by a previous GrantOnce.
// Legacy grants made before instance tracking return an empty slice.
func (s *Inventory) GrantedItems(identity string) []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	indexes := s.owned.GrantItems[identity]
	result := make([]Item, 0, len(indexes))
	for _, index := range indexes {
		for _, item := range s.owned.Items {
			if item.InvenIndex == index {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

// Consume atomically removes the requested counts from mutable owned items.
// Starter seed entries are immutable and are deliberately not accepted here.
func (s *Inventory) Consume(requested []Item) error {
	_, err := s.ConsumeAndRefund(requested, nil)
	return err
}

// ConsumeAndRefund changes the spent stack and returned growth resources in a
// single inventory save, keeping ItemInfo and RewardInfoBundle consistent.
func (s *Inventory) ConsumeAndRefund(requested []Item, refunds []gamedata.GrowthMaterial) ([]Item, error) {
	if len(requested) == 0 {
		return nil, errors.New("player: no items to consume")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := ownedSnapshot{Version: s.owned.Version, NextIndex: s.owned.NextIndex,
		Items: append([]Item(nil), s.owned.Items...), Granted: make(map[string]bool, len(s.owned.Granted)), GrantItems: make(map[string][]uint64, len(s.owned.GrantItems))}
	for k, v := range s.owned.Granted {
		next.Granted[k] = v
	}
	for k, v := range s.owned.GrantItems {
		next.GrantItems[k] = append([]uint64(nil), v...)
	}
	for _, want := range requested {
		if want.InvenIndex == 0 || want.ID == 0 || want.Type == 0 || want.Count == 0 {
			return nil, errors.New("player: invalid item consumption")
		}
		found := -1
		for i, current := range next.Items {
			if current.InvenIndex == want.InvenIndex {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("player: item %d is not mutable-owned", want.InvenIndex)
		}
		current := next.Items[found]
		if current.ID != want.ID || current.Type != want.Type || current.Count < want.Count {
			return nil, fmt.Errorf("player: item %d consumption mismatch", want.InvenIndex)
		}
		current.Count -= want.Count
		if current.Count == 0 {
			next.Items = append(next.Items[:found], next.Items[found+1:]...)
		} else {
			next.Items[found] = current
		}
	}
	granted := make([]Item, 0, len(refunds))
	for _, refund := range refunds {
		if refund.ID == 0 || refund.Count == 0 {
			return nil, errors.New("player: invalid growth refund")
		}
		item := Item{InvenIndex: next.NextIndex, ID: refund.ID, Type: 8, Count: refund.Count, TimeValue: uint64(time.Now().UnixMilli())}
		next.NextIndex++
		next.Items = append(next.Items, item)
		granted = append(granted, item)
	}
	data, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".inventory-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return nil, err
	}
	s.owned = next
	return granted, nil
}

// ItemWire builds ItemDBInfo for RewardDBInfoBundle and ItemInfoResponse.
func ItemWire(item Item) []byte {
	var b []byte
	for _, f := range []struct {
		n int
		v uint64
	}{{1, item.InvenIndex}, {2, item.ID}, {3, item.Type}, {4, item.Count}, {5, item.KeepFlag}, {6, item.TimeValue}} {
		if f.v != 0 {
			b = wire.AppendVarint(b, f.n, f.v)
		}
	}
	if item.SortID != 0 {
		b = wire.AppendVarint(b, 9, item.SortID)
	}
	if item.UseCount != 0 {
		b = wire.AppendVarint(b, 10, item.UseCount)
	}
	return b
}
