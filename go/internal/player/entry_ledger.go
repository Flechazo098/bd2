package player

import (
	"encoding/json"
	"fmt"

	"bd2server/internal/stateio"
)

func loadBoolEntries(store stateio.AtomicEntryStore, domain, bucket string) (map[string]bool, error) {
	entries, err := store.ListEntries(domain, bucket)
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(entries))
	for key, payload := range entries {
		if key == "" || string(payload) != "true" {
			return nil, fmt.Errorf("player: invalid %s %s entry %q", domain, bucket, key)
		}
		result[key] = true
	}
	return result, nil
}

func loadUintEntries(store stateio.AtomicEntryStore, domain, bucket string) (map[string]uint64, error) {
	entries, err := store.ListEntries(domain, bucket)
	if err != nil {
		return nil, err
	}
	result := make(map[string]uint64, len(entries))
	for key, payload := range entries {
		var value uint64
		if key == "" || json.Unmarshal(payload, &value) != nil || value == 0 {
			return nil, fmt.Errorf("player: invalid %s %s entry %q", domain, bucket, key)
		}
		result[key] = value
	}
	return result, nil
}

func entry(bucket, key string, payload []byte) []stateio.EntryMutation {
	return []stateio.EntryMutation{{Bucket: bucket, Key: key, Payload: payload}}
}
