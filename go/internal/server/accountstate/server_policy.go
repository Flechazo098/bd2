package accountstate

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const startingPackMetadataKey = "server_start_pack_id"

// LockStartingPack checks the permanent server starting-pack policy inside the
// active startup operation. Only an empty database may initialize the policy;
// the operation's commit makes the first successful startup's choice durable.
func (r *Repository) LockStartingPack(configured int, initialize bool) (int, error) {
	if configured != 1 && configured != 21 {
		return 0, fmt.Errorf("accountstate: invalid story.start_pack_id %d (want 1 or 21)", configured)
	}
	if err := r.Check(); err != nil {
		return 0, err
	}
	r.activeMu.RLock()
	defer r.activeMu.RUnlock()
	if r.active == nil {
		return 0, errors.New("accountstate: starting pack policy requires an active startup operation")
	}
	t := r.active
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return 0, ErrClosed
	}
	var raw string
	err := t.tx.QueryRow(`SELECT value FROM metadata WHERE key=?`, startingPackMetadataKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		var populated bool
		if err := t.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM domain_state) OR EXISTS(SELECT 1 FROM domain_entry)`).Scan(&populated); err != nil {
			return 0, fmt.Errorf("accountstate: inspect starting pack initialization: %w", err)
		}
		if !initialize || populated {
			return 0, errors.New("accountstate: server_start_pack_id is missing; existing state requires explicit repair")
		}
		if _, err := t.tx.Exec(`INSERT INTO metadata(key,value) VALUES(?,?)`, startingPackMetadataKey, strconv.Itoa(configured)); err != nil {
			return 0, fmt.Errorf("accountstate: initialize starting pack policy: %w", err)
		}
		// Metadata changes do not mutate domain memory, so a rollback need not
		// trigger the domain-memory recovery fence.
		return configured, nil
	}
	if err != nil {
		return 0, fmt.Errorf("accountstate: read starting pack policy: %w", err)
	}
	locked, err := strconv.Atoi(raw)
	if err != nil || (raw != "1" && raw != "21") {
		return 0, fmt.Errorf("accountstate: invalid server_start_pack_id %q; explicit repair required", raw)
	}
	if locked != configured {
		return 0, fmt.Errorf("accountstate: story.start_pack_id %d conflicts with permanent server_start_pack_id %d", configured, locked)
	}
	return locked, nil
}
