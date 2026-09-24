package player

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"bd2server/internal/stateio"
)

const collectionDomain = "collection"

var collectionEntryBuckets = [...]string{
	"characters", "costumes", "grants", "gacha_applied", "gacha_users",
	"gacha_fixed", "step_up_progress", "gacha_point_exchanges",
	"gacha_selections", "gacha_selection_changes", "costume_potential", "char_awake",
}

func rejectInlineCollectionEntries(raw []byte) error {
	if err := stateio.RequireExactJSONObject(raw, "version", "next_character_index", "next_costume_index", "latest_preview", "preview_event_index", "preview_locked", "base_costume_levels", "gacha_count_corrected"); err != nil {
		return fmt.Errorf("player: incompatible collection core: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("player: decode collection core: %w", err)
	}
	for _, bucket := range collectionEntryBuckets {
		if _, found := fields[bucket]; found {
			return fmt.Errorf("player: collection core contains entry bucket %q", bucket)
		}
	}
	return nil
}

func loadEntryMap[T any](store stateio.EntryStore, bucket string) (map[string]T, error) {
	entries, err := store.ListEntries(collectionDomain, bucket)
	if err != nil {
		return nil, fmt.Errorf("player: load collection %s: %w", bucket, err)
	}
	result := make(map[string]T, len(entries))
	for key, payload := range entries {
		if key == "" {
			return nil, fmt.Errorf("player: empty collection %s key", bucket)
		}
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, fmt.Errorf("player: decode collection %s entry %q: %w", bucket, key, err)
		}
		result[key] = value
	}
	return result, nil
}

func loadCollectionEntries(store stateio.EntryStore, data *collectionSnapshot) error {
	var err error
	if data.Characters, err = loadIndexedEntries[Character](store, "characters", func(c Character) uint64 { return c.InvenIndex }); err != nil {
		return err
	}
	if data.Costumes, err = loadIndexedEntries[Costume](store, "costumes", func(c Costume) uint64 { return c.InvenIndex }); err != nil {
		return err
	}
	if data.Grants, err = loadEntryMap[CollectionGrant](store, "grants"); err != nil {
		return err
	}
	if data.GachaApplied, err = loadEntryMap[bool](store, "gacha_applied"); err != nil {
		return err
	}
	if data.GachaUsers, err = loadEntryMap[GachaUserState](store, "gacha_users"); err != nil {
		return err
	}
	if data.GachaFixed, err = loadEntryMap[GachaFixedState](store, "gacha_fixed"); err != nil {
		return err
	}
	if data.StepUpProgress, err = loadEntryMap[uint64](store, "step_up_progress"); err != nil {
		return err
	}
	if data.GachaPointExchange, err = loadEntryMap[GachaPointExchange](store, "gacha_point_exchanges"); err != nil {
		return err
	}
	if data.GachaSelections, err = loadEntryMap[[]GachaSelection](store, "gacha_selections"); err != nil {
		return err
	}
	if data.GachaSelectionChanges, err = loadEntryMap[uint64](store, "gacha_selection_changes"); err != nil {
		return err
	}
	if data.CostumePotential, err = loadEntryMap[[]uint64](store, "costume_potential"); err != nil {
		return err
	}
	if data.CharAwake, err = loadEntryMap[CharAwakeProgress](store, "char_awake"); err != nil {
		return err
	}
	return nil
}

func loadIndexedEntries[T any](store stateio.EntryStore, bucket string, index func(T) uint64) ([]T, error) {
	values, err := loadEntryMap[T](store, bucket)
	if err != nil {
		return nil, err
	}
	indices := make([]uint64, 0, len(values))
	for key, value := range values {
		parsed, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || parsed == 0 || key != strconv.FormatUint(parsed, 10) || index(value) != parsed {
			return nil, fmt.Errorf("player: invalid collection %s entry key %q", bucket, key)
		}
		indices = append(indices, parsed)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	result := make([]T, 0, len(indices))
	for _, id := range indices {
		result = append(result, values[strconv.FormatUint(id, 10)])
	}
	return result, nil
}

func collectionCore(in collectionSnapshot) collectionSnapshot {
	in.Characters = nil
	in.Costumes = nil
	in.Grants = nil
	in.GachaApplied = nil
	in.GachaUsers = nil
	in.GachaFixed = nil
	in.StepUpProgress = nil
	in.GachaPointExchange = nil
	in.GachaSelections = nil
	in.GachaSelectionChanges = nil
	in.CostumePotential = nil
	in.CharAwake = nil
	return in
}

func sameCollectionCore(a, b collectionSnapshot) bool {
	return reflect.DeepEqual(collectionCore(a), collectionCore(b))
}

func diffEntryMap[T any](bucket string, before, after map[string]T, changes *[]stateio.EntryMutation) error {
	for key, value := range after {
		old, found := before[key]
		if found && reflect.DeepEqual(old, value) {
			continue
		}
		if key == "" {
			return fmt.Errorf("player: empty collection %s key", bucket)
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("player: encode collection %s entry %q: %w", bucket, key, err)
		}
		*changes = append(*changes, stateio.EntryMutation{Bucket: bucket, Key: key, Payload: payload})
	}
	for key := range before {
		if _, found := after[key]; !found {
			*changes = append(*changes, stateio.EntryMutation{Bucket: bucket, Key: key, Delete: true})
		}
	}
	return nil
}

func indexedMap[T any](bucket string, values []T, index func(T) uint64) (map[string]T, error) {
	result := make(map[string]T, len(values))
	for _, value := range values {
		id := index(value)
		if id == 0 {
			return nil, fmt.Errorf("player: zero collection %s index", bucket)
		}
		key := strconv.FormatUint(id, 10)
		if _, found := result[key]; found {
			return nil, fmt.Errorf("player: duplicate collection %s index %d", bucket, id)
		}
		result[key] = value
	}
	return result, nil
}

func diffIndexedEntries[T any](bucket string, before, after []T, index func(T) uint64, changes *[]stateio.EntryMutation) error {
	old, err := indexedMap(bucket, before, index)
	if err != nil {
		return err
	}
	next, err := indexedMap(bucket, after, index)
	if err != nil {
		return err
	}
	return diffEntryMap(bucket, old, next, changes)
}

func diffCollectionEntries(before, after collectionSnapshot) ([]stateio.EntryMutation, error) {
	changes := make([]stateio.EntryMutation, 0)
	for _, item := range []struct {
		bucket    string
		old, next map[string]bool
	}{{"gacha_applied", before.GachaApplied, after.GachaApplied}} {
		if err := diffEntryMap(item.bucket, item.old, item.next, &changes); err != nil {
			return nil, err
		}
	}
	if err := diffIndexedEntries("characters", before.Characters, after.Characters, func(c Character) uint64 { return c.InvenIndex }, &changes); err != nil {
		return nil, err
	}
	if err := diffIndexedEntries("costumes", before.Costumes, after.Costumes, func(c Costume) uint64 { return c.InvenIndex }, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("grants", before.Grants, after.Grants, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("gacha_users", before.GachaUsers, after.GachaUsers, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("gacha_fixed", before.GachaFixed, after.GachaFixed, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("step_up_progress", before.StepUpProgress, after.StepUpProgress, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("gacha_point_exchanges", before.GachaPointExchange, after.GachaPointExchange, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("gacha_selections", before.GachaSelections, after.GachaSelections, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("gacha_selection_changes", before.GachaSelectionChanges, after.GachaSelectionChanges, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("costume_potential", before.CostumePotential, after.CostumePotential, &changes); err != nil {
		return nil, err
	}
	if err := diffEntryMap("char_awake", before.CharAwake, after.CharAwake, &changes); err != nil {
		return nil, err
	}
	if len(changes) > 1 {
		sort.Slice(changes, func(i, j int) bool {
			if changes[i].Bucket != changes[j].Bucket {
				return changes[i].Bucket < changes[j].Bucket
			}
			return changes[i].Key < changes[j].Key
		})
	}
	return changes, nil
}
