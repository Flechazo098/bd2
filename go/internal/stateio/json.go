package stateio

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RequireExactJSONObject makes the persisted layout part of the current
// storage contract. It rejects both missing and unknown fields rather than
// allowing encoding/json to silently interpret an older or newer format.
func RequireExactJSONObject(payload []byte, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("state payload is not an object")
	}
	want := make(map[string]bool, len(required))
	for _, name := range required {
		if name == "" || want[name] {
			return fmt.Errorf("invalid required field %q", name)
		}
		want[name] = true
	}
	var missing, unknown []string
	for name := range want {
		if _, exists := fields[name]; !exists {
			missing = append(missing, name)
		}
	}
	for name := range fields {
		if !want[name] {
			unknown = append(unknown, name)
		}
	}
	if len(missing) == 0 && len(unknown) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	return fmt.Errorf("state fields mismatch: missing=[%s] unknown=[%s]", strings.Join(missing, ","), strings.Join(unknown, ","))
}
