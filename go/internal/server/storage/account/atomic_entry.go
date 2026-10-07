package accountstate

import "bd2server/internal/server/storage/stateio"

func (t *CommandStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	for _, change := range changes {
		if err := validEntryKey(domain, change.Bucket, change.Key); err != nil {
			return err
		}
	}
	if core != nil {
		if err := t.Save(domain, core); err != nil {
			return err
		}
	}
	for _, change := range changes {
		if change.Delete {
			if _, err := t.DeleteEntry(domain, change.Bucket, change.Key); err != nil {
				return err
			}
		} else if err := t.PutEntry(domain, change.Bucket, change.Key, change.Payload); err != nil {
			return err
		}
	}
	return nil
}
