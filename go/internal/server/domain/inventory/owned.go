package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"

	"time"
)

// Inventory holds player-owned rewards separately from the immutable starter
// seed. GrantOnce uses a battle identity to prevent double-credit on retries.
type Inventory struct {
	store       stateio.ScopedEntryStore
	starter     []Item
	randomBoxes *gamedata.RandomBoxDesign
	itemStacks  *gamedata.ItemStackDesign
	owned       ownedSnapshot
	persisted   ownedSnapshot
	corePresent bool
}

type ownedSnapshot struct {
	Version      string              `json:"version"`
	NextIndex    uint64              `json:"next_index"`
	Items        []Item              `json:"-"`
	Granted      map[string]bool     `json:"-"`
	GrantItems   map[string][]uint64 `json:"-"`
	GrantRewards map[string][]Item   `json:"-"`
}

func OpenInventory(ctx command.Context, store stateio.Store, starter []Item) (*Inventory, error) {
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok {
		return nil, errors.New("player: invalid inventory configuration")
	}
	s := &Inventory{store: entries, starter: append([]Item(nil), starter...), owned: ownedSnapshot{Version: versionconfig.State(), NextIndex: 900000001, Granted: map[string]bool{}, GrantItems: map[string][]uint64{}, GrantRewards: map[string][]Item{}}}
	data, err := store.Load(ctx.State, "items")
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
	} else if err := stateio.RequireNoEntries(entries, ctx.State, "items", "items", "granted", "grant_items", "grant_rewards"); err != nil {
		return nil, fmt.Errorf("player: invalid inventory storage: %w", err)
	}
	if s.owned.Version != versionconfig.State() || s.owned.NextIndex < 900000001 {
		return nil, errors.New("player: invalid saved inventory")
	}
	s.owned.Granted, err = loadBoolEntries(ctx, entries, "items", "granted")
	if err != nil {
		return nil, err
	}
	rawGrantItems, err := entries.ListEntries(ctx.State, "items", "grant_items")
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
	rawRewards, err := entries.ListEntries(ctx.State, "items", "grant_rewards")
	if err != nil {
		return nil, err
	}
	for key, value := range rawRewards {
		var rewards []Item
		if key == "" || !s.owned.Granted[key] || json.Unmarshal(value, &rewards) != nil {
			return nil, fmt.Errorf("inventory: invalid grant reward entry %q", key)
		}
		s.owned.GrantRewards[key] = rewards
	}
	rawItems, err := entries.ListEntries(ctx.State, "items", "items")
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

func (s *Inventory) EnsurePersisted(ctx command.Context) error {

	data, err := s.store.Load(ctx.State, "items")
	if err != nil {
		return err
	}
	if data != nil {
		return nil
	}
	return s.commitOwned(ctx, cloneOwnedSnapshot(s.owned))
}

// AttachRandomBoxes installs the version-validated deterministic RandomBox
// definitions.  It is supplied at process startup from real GameData rather
// than accepting a client-supplied reward.
func (s *Inventory) AttachRandomBoxes(ctx command.Context, design *gamedata.RandomBoxDesign) error {
	if s == nil || design == nil {
		return errors.New("player: nil random box design")
	}

	s.randomBoxes = design
	return nil
}

func (s *Inventory) All(ctx command.Context) []Item {

	items := make([]Item, 0, len(s.starter)+len(s.owned.Items))
	items = append(items, s.starter...)
	items = append(items, s.owned.Items...)
	return items
}

// SelectMutable returns concrete owned stacks for a server-calculated cost.
// Starter seed items are immutable and deliberately excluded, matching
// Consume. Results preserve inventory order and split the final stack exactly.
func (s *Inventory) SelectMutable(ctx command.Context, itemType, id, count uint64) ([]Item, error) {
	if itemType == 0 || id == 0 || count == 0 {
		return nil, errors.New("player: invalid mutable item selection")
	}

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

func (s *Inventory) GrantOnce(ctx command.Context, identity string, rewards []gamedata.BattleReward) ([]Item, error) {
	if identity == "" {
		return nil, errors.New("player: missing reward identity")
	}

	if s.owned.Granted[identity] {
		return nil, nil
	}
	next := cloneOwnedSnapshot(s.owned)
	newItems := make([]Item, 0, len(rewards))
	// Skin rewards represent ownership; returning an existing skin would also
	// replay the client's acquisition UI. Include the immutable starter seed.
	ownedSkins := make(map[uint64]bool)
	for _, items := range [][]Item{s.starter, next.Items} {
		for _, item := range items {
			if item.Type == 45 && item.Count > 0 {
				ownedSkins[item.ID] = true
			}
		}
	}
	for _, r := range rewards {
		if r.ID == 0 || r.Type == 0 || r.Count == 0 {
			return nil, errors.New("player: invalid battle reward")
		}
		if r.Type == 45 {
			if ownedSkins[r.ID] {
				continue
			}
			ownedSkins[r.ID] = true
		}
		items, err := s.addItems(&next, Item{ID: r.ID, Type: r.Type, Count: r.Count, TimeValue: uint64(time.Now().UnixMilli())})
		if err != nil {
			return nil, err
		}
		newItems = append(newItems, items...)
	}
	newItems = recordItemGrant(&next, identity, newItems)
	next.Granted[identity] = true
	if err := s.commitOwned(ctx, next); err != nil {
		return nil, err
	}
	s.owned = next
	return newItems, nil
}

func cloneOwnedSnapshot(current ownedSnapshot) ownedSnapshot {
	next := ownedSnapshot{Version: current.Version, NextIndex: current.NextIndex,
		Items: append([]Item(nil), current.Items...), Granted: make(map[string]bool, len(current.Granted)+1), GrantItems: make(map[string][]uint64, len(current.GrantItems)+1), GrantRewards: make(map[string][]Item, len(current.GrantRewards)+1)}
	maps.Copy(next.Granted, current.Granted)
	for k, v := range current.GrantItems {
		next.GrantItems[k] = append([]uint64(nil), v...)
	}
	for k, v := range current.GrantRewards {
		next.GrantRewards[k] = append([]Item(nil), v...)
	}
	return next
}

// commitOwned persists a fully validated next snapshot.
func (s *Inventory) commitOwned(ctx command.Context, next ownedSnapshot) error {
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
	for key, value := range next.GrantRewards {
		previous, found := s.persisted.GrantRewards[key]
		if !found || !reflect.DeepEqual(value, previous) {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "grant_rewards", Key: key, Payload: payload})
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
	if err := s.store.SaveWithEntries(ctx.State, "items", data, changes); err != nil {
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
	if rewards, found := s.owned.GrantRewards[identity]; found {
		return append([]Item(nil), rewards...)
	}
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

	return s.owned.Granted[identity]
}

// Consume atomically removes the requested counts from mutable owned items.
// Starter seed entries are immutable and are deliberately not accepted here.
func (s *Inventory) CanConsume(ctx command.Context, requested []Item) error {
	if len(requested) == 0 {
		return errors.New("player: no items to consume")
	}

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

func (s *Inventory) Consume(ctx command.Context, requested []Item) error {
	_, err := s.ConsumeAndRefund(ctx, requested, nil)
	return err
}

// ConsumeAndRefund changes the spent stack and returned growth resources in a
// single inventory save, keeping ItemInfo and RewardInfoBundle consistent.
func (s *Inventory) ConsumeAndRefund(ctx command.Context, requested []Item, refunds []gamedata.GrowthMaterial) ([]Item, error) {
	if len(requested) == 0 {
		return nil, errors.New("player: no items to consume")
	}

	next := cloneOwnedSnapshot(s.owned)
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
		items, err := s.addItems(&next, Item{ID: refund.ID, Type: 8, Count: refund.Count, TimeValue: uint64(time.Now().UnixMilli())})
		if err != nil {
			return nil, err
		}
		granted = append(granted, items...)
	}
	if err := s.commitOwned(ctx, next); err != nil {
		return nil, err
	}
	s.owned = next
	return mergeRewardDeltas(granted), nil
}

// ItemWire builds ItemDBInfo for RewardDBInfoBundle and ItemInfoResponse.
