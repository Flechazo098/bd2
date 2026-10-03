package accountstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"bd2server/internal/server/stateio"
)

const bootstrapDomain = "bootstrap"

type InitializationStatus uint8

const (
	InitializationCorrupt InitializationStatus = iota
	InitializationPending
	InitializationComplete
)

type bootstrapSnapshot struct {
	Version uint64 `json:"version"`
}

// InitializationState distinguishes a schema-only database left by an
// interrupted first start from a complete account. Partial domain state is
// rejected instead of being guessed or repaired at runtime.
func (r *Repository) InitializationState(accountDomains ...string) (InitializationStatus, error) {
	if r == nil {
		return InitializationCorrupt, errors.New("accountstate: nil repository")
	}
	rows, err := r.db.Query(`SELECT name FROM domain_state ORDER BY name`)
	if err != nil {
		return InitializationCorrupt, fmt.Errorf("accountstate: list initialization domains: %w", err)
	}
	var found []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return InitializationCorrupt, err
		}
		found = append(found, name)
	}
	if err := rows.Close(); err != nil {
		return InitializationCorrupt, err
	}
	var entryCount uint64
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM domain_entry`).Scan(&entryCount); err != nil {
		return InitializationCorrupt, fmt.Errorf("accountstate: count initialization entries: %w", err)
	}
	if len(found) == 0 && entryCount == 0 {
		return InitializationPending, nil
	}
	want := append([]string(nil), accountDomains...)
	want = append(want, bootstrapDomain)
	sort.Strings(want)
	if !slices.Equal(found, want) {
		return InitializationCorrupt, fmt.Errorf("accountstate: domains %v, want %v", found, want)
	}
	payload, err := r.Load(bootstrapDomain)
	if err != nil {
		return InitializationCorrupt, err
	}
	if err := validateBootstrap(payload); err != nil {
		return InitializationCorrupt, err
	}
	return InitializationComplete, nil
}

// MarkInitializationComplete joins the active startup transaction. It must be
// written only after every account domain and first-login entitlement exists;
// a rollback therefore leaves a schema-only database that can be retried.
func (r *Repository) MarkInitializationComplete() error {
	payload, err := json.Marshal(bootstrapSnapshot{Version: 1})
	if err != nil {
		return err
	}
	return r.Save(bootstrapDomain, payload)
}

func validateBootstrap(payload []byte) error {
	if len(payload) == 0 {
		return errors.New("accountstate: missing bootstrap completion marker")
	}
	if err := stateio.RequireExactJSONObject(payload, "version"); err != nil {
		return fmt.Errorf("accountstate: invalid bootstrap marker: %w", err)
	}
	var snapshot bootstrapSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil || snapshot.Version != 1 {
		return errors.New("accountstate: malformed bootstrap completion marker")
	}
	canonical, _ := json.Marshal(snapshot)
	if !bytes.Equal(payload, canonical) {
		return errors.New("accountstate: non-canonical bootstrap completion marker")
	}
	return nil
}
