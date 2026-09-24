package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
)

// Inventory holds player-owned rewards separately from the immutable starter
// seed. GrantOnce uses a battle identity to prevent double-credit on retries.
type Inventory struct {
	mu          sync.Mutex
	store       stateio.AtomicEntryStore
	starter     *Starter
	randomBoxes *gamedata.RandomBoxDesign
	owned       ownedSnapshot
	persisted   ownedSnapshot
	corePresent bool
}

type ownedSnapshot struct {
	Version    string              `json:"version"`
	NextIndex  uint64              `json:"next_index"`
	Items      []Item              `json:"-"`
	Granted    map[string]bool     `json:"-"`
	GrantItems map[string][]uint64 `json:"-"`
}

func OpenInventory(store stateio.Store, starter *Starter) (*Inventory, error) {
	entries, ok := store.(stateio.AtomicEntryStore)
	if starter == nil || starter.Validate() != nil || !ok {
		return nil, errors.New("player: invalid inventory configuration")
	}
	s := &Inventory{store: entries, starter: starter, owned: ownedSnapshot{Version: versionconfig.Protocol(), NextIndex: 900000001, Granted: map[string]bool{}, GrantItems: map[string][]uint64{}}}
	data, err := store.Load("items")
	if err != nil {
		return nil, err
	}
	if data != nil {
		s.corePresent = true
		if err := stateio.RequireExactJSONObject(data, "version", "next_index"); err != nil {
			return nil, fmt.Errorf("player: incompatible inventory layout: %w", err)
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(data, &shape); err != nil {
			return nil, fmt.Errorf("player: decode inventory shape: %w", err)
		}
		for _, name := range []string{"items", "granted", "grant_items"} {
			if _, exists := shape[name]; exists {
				return nil, fmt.Errorf("player: inventory %s must use entries", name)
			}
		}
		if err := json.Unmarshal(data, &s.owned); err != nil {
			return nil, fmt.Errorf("player: decode inventory: %w", err)
		}
	} else if err := stateio.RequireNoEntries(entries, "items", "items", "granted", "grant_items"); err != nil {
		return nil, fmt.Errorf("player: invalid inventory storage: %w", err)
	}
	if s.owned.Version != versionconfig.Protocol() || s.owned.NextIndex < 900000001 {
		return nil, errors.New("player: invalid saved inventory")
	}
	s.owned.Granted, err = loadBoolEntries(entries, "items", "granted")
	if err != nil {
		return nil, err
	}
	rawGrantItems, err := entries.ListEntries("items", "grant_items")
	if err != nil {
		return nil, err
	}
	s.owned.GrantItems = make(map[string][]uint64, len(rawGrantItems))
	for key, value := range rawGrantItems {
		var indices []uint64
		if key == "" || json.Unmarshal(value, &indices) != nil {
			return nil, fmt.Errorf("player: invalid items grant_items entry %q", key)
		}
		s.owned.GrantItems[key] = indices
	}
	rawItems, err := entries.ListEntries("items", "items")
	if err != nil {
		return nil, err
	}
	for key, value := range rawItems {
		var item Item
		index, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || json.Unmarshal(value, &item) != nil || item.InvenIndex != index {
			return nil, fmt.Errorf("player: invalid items entry %q", key)
		}
		s.owned.Items = append(s.owned.Items, item)
	}
	sort.Slice(s.owned.Items, func(i, j int) bool { return s.owned.Items[i].InvenIndex < s.owned.Items[j].InvenIndex })
	s.persisted = cloneOwnedSnapshot(s.owned)
	return s, nil
}

func (s *Inventory) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.store.Load("items")
	if err != nil {
		return err
	}
	if data != nil {
		return nil
	}
	return s.commitOwned(cloneOwnedSnapshot(s.owned))
}

func (s *Inventory) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/ItemInfo" && path != "/UseRandomBox" {
		return 0, nil, false, nil
	}
	if path == "/UseRandomBox" {
		return s.useRandomBox(request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Item, 0, len(s.starter.Items)+len(s.owned.Items))
	items = append(items, s.starter.Items...)
	items = append(items, s.owned.Items...)
	return (&Starter{Version: versionconfig.Protocol(), Items: items}).Handle(path, request)
}

// AttachRandomBoxes installs the version-validated deterministic RandomBox
// definitions.  It is supplied at process startup from real GameData rather
// than accepting a client-supplied reward.
func (s *Inventory) AttachRandomBoxes(design *gamedata.RandomBoxDesign) error {
	if s == nil || design == nil {
		return errors.New("player: nil random box design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.randomBoxes = design
	return nil
}

func (s *Inventory) useRandomBox(request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, errors.New("player: UseRandomBox invalid sequence")
	}
	index, present, err := wire.Varint(request, 2)
	if err != nil || !present || index == 0 {
		return 0, nil, true, errors.New("player: UseRandomBox invalid inventory index")
	}
	count, present, err := wire.Varint(request, 3)
	if err != nil || !present || count == 0 || count > uint64(^uint32(0)>>1) {
		return 0, nil, true, errors.New("player: UseRandomBox invalid use count")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.randomBoxes == nil {
		return 0, nil, true, errors.New("player: UseRandomBox design unavailable")
	}
	boxAt := -1
	for i, item := range s.owned.Items {
		if item.InvenIndex == index {
			boxAt = i
			break
		}
	}
	if boxAt < 0 {
		return 0, nil, true, fmt.Errorf("player: UseRandomBox unknown inventory index %d", index)
	}
	box := s.owned.Items[boxAt]
	if box.Type != 9 || box.Count < count {
		return 0, nil, true, errors.New("player: UseRandomBox item or count mismatch")
	}
	rewards, err := s.randomBoxes.Open(box.ID, count)
	if err != nil {
		return 0, nil, true, err
	}
	next := cloneOwnedSnapshot(s.owned)
	if box.Count == count {
		next.Items = append(next.Items[:boxAt], next.Items[boxAt+1:]...)
	} else {
		next.Items[boxAt].Count -= count
	}
	granted := make([]Item, 0, len(rewards))
	for _, reward := range rewards {
		if reward.Type == 0 || reward.ID == 0 || reward.Count == 0 {
			return 0, nil, true, errors.New("player: UseRandomBox invalid GameData reward")
		}
		item := Item{InvenIndex: next.NextIndex, ID: reward.ID, Type: reward.Type, Count: reward.Count, TimeValue: uint64(time.Now().UnixMilli())}
		next.NextIndex++
		next.Items = append(next.Items, item)
		granted = append(granted, item)
	}
	if err := s.commitOwned(next); err != nil {
		return 0, nil, true, err
	}
	s.owned = next
	var bundle []byte
	for _, item := range granted {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	return 143, wire.AppendBytes(nil, 1, bundle), true, nil
}

func (s *Inventory) All() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]Item, 0, len(s.starter.Items)+len(s.owned.Items))
	items = append(items, s.starter.Items...)
	items = append(items, s.owned.Items...)
	return items
}

// SelectMutable returns concrete owned stacks for a server-calculated cost.
// Starter seed items are immutable and deliberately excluded, matching
// Consume. Results preserve inventory order and split the final stack exactly.
func (s *Inventory) SelectMutable(itemType, id, count uint64) ([]Item, error) {
	if itemType == 0 || id == 0 || count == 0 {
		return nil, errors.New("player: invalid mutable item selection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := count
	var selected []Item
	for _, item := range s.owned.Items {
		if item.Type != itemType || item.ID != id || item.Count == 0 {
			continue
		}
		use := min(item.Count, remaining)
		copy := item
		copy.Count = use
		selected = append(selected, copy)
		remaining -= use
		if remaining == 0 {
			return selected, nil
		}
	}
	return nil, fmt.Errorf("player: insufficient mutable item %d/%d: have %d want %d", itemType, id, count-remaining, count)
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
	next := cloneOwnedSnapshot(s.owned)
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
	if err := s.commitOwned(next); err != nil {
		return nil, err
	}
	s.owned = next
	return newItems, nil
}

func cloneOwnedSnapshot(current ownedSnapshot) ownedSnapshot {
	next := ownedSnapshot{Version: current.Version, NextIndex: current.NextIndex,
		Items: append([]Item(nil), current.Items...), Granted: make(map[string]bool, len(current.Granted)+1), GrantItems: make(map[string][]uint64, len(current.GrantItems)+1)}
	for k, v := range current.Granted {
		next.Granted[k] = v
	}
	for k, v := range current.GrantItems {
		next.GrantItems[k] = append([]uint64(nil), v...)
	}
	return next
}

// commitOwned persists a fully validated next snapshot. Caller holds s.mu.
func (s *Inventory) commitOwned(next ownedSnapshot) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	changes := make([]stateio.EntryMutation, 0)
	for key, value := range next.Granted {
		if value && !s.persisted.Granted[key] {
			changes = append(changes, stateio.EntryMutation{Bucket: "granted", Key: key, Payload: []byte("true")})
		}
	}
	for key, value := range next.GrantItems {
		if !equalIndices(value, s.persisted.GrantItems[key]) {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "grant_items", Key: key, Payload: payload})
		}
	}
	before := make(map[uint64]Item, len(s.persisted.Items))
	for _, item := range s.persisted.Items {
		before[item.InvenIndex] = item
	}
	for _, item := range next.Items {
		old, exists := before[item.InvenIndex]
		if !exists || !equalItem(old, item) {
			payload, err := json.Marshal(item)
			if err != nil {
				return err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "items", Key: strconv.FormatUint(item.InvenIndex, 10), Payload: payload})
		}
		delete(before, item.InvenIndex)
	}
	for index := range before {
		changes = append(changes, stateio.EntryMutation{Bucket: "items", Key: strconv.FormatUint(index, 10), Delete: true})
	}
	if s.corePresent && next.Version == s.persisted.Version && next.NextIndex == s.persisted.NextIndex {
		data = nil
	}
	if err := s.store.SaveWithEntries("items", data, changes); err != nil {
		return err
	}
	s.corePresent = true
	s.persisted = cloneOwnedSnapshot(next)
	return nil
}

func equalIndices(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalItem(a, b Item) bool { return reflect.DeepEqual(a, b) }

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

// WasGranted reports whether GrantOnce committed the identity even when all
// granted item stacks were later consumed. Cash-product purchase counts use
// this durable marker rather than the current inventory contents.
func (s *Inventory) WasGranted(identity string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owned.Granted[identity]
}

// Consume atomically removes the requested counts from mutable owned items.
// Starter seed entries are immutable and are deliberately not accepted here.
func (s *Inventory) CanConsume(requested []Item) error {
	if len(requested) == 0 {
		return errors.New("player: no items to consume")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := make(map[uint64]Item, len(s.owned.Items))
	for _, item := range s.owned.Items {
		remaining[item.InvenIndex] = item
	}
	for _, want := range requested {
		if want.InvenIndex == 0 || want.ID == 0 || want.Type == 0 || want.Count == 0 {
			return errors.New("player: invalid item consumption")
		}
		item, ok := remaining[want.InvenIndex]
		if !ok || item.ID != want.ID || item.Type != want.Type || item.Count < want.Count {
			return fmt.Errorf("player: item %d consumption mismatch", want.InvenIndex)
		}
		item.Count -= want.Count
		remaining[want.InvenIndex] = item
	}
	return nil
}

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
	if err := s.commitOwned(next); err != nil {
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
