package accountstate

import (
	"context"
	"fmt"

	"bd2server/internal/stateio"
)

var _ stateio.AtomicEntryStore = (*Repository)(nil)

// SaveWithEntries writes a domain's bounded core and its changed entry rows
// together. Calls inside a request join that transaction; direct calls create
// their own transaction so neither half can become visible alone.
func (r *Repository) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if domain == "" {
		return fmt.Errorf("accountstate: empty domain name")
	}
	r.activeMu.RLock()
	if r.active != nil {
		err := saveWithEntries(r.active, domain, core, changes)
		r.activeMu.RUnlock()
		return err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveWithEntries(tx, domain, core, changes); err != nil {
		return err
	}
	return tx.Commit()
}

func saveWithEntries(tx *Tx, domain string, core []byte, changes []stateio.EntryMutation) error {
	if core != nil {
		if _, err := tx.Save(domain, core); err != nil {
			return err
		}
	}
	for _, change := range changes {
		if change.Delete {
			if _, err := tx.DeleteEntry(domain, change.Bucket, change.Key); err != nil {
				return err
			}
		} else if err := tx.PutEntry(domain, change.Bucket, change.Key, change.Payload); err != nil {
			return err
		}
	}
	return nil
}
