package stateio

// EntrySnapshotStore keeps optional gameplay snapshots within an existing
// account domain. It participates in the parent's SQLite request transaction
// without adding cores to the nine-domain account initialization contract.
type EntrySnapshotStore struct {
	Domain, Bucket string
}

func (s EntrySnapshotStore) Load(command AtomicEntryStore, name string) ([]byte, error) {
	payload, _, err := command.LoadEntry(s.Domain, s.Bucket, name)
	return payload, err
}
func (s EntrySnapshotStore) Save(command AtomicEntryStore, name string, payload []byte) error {
	return command.PutEntry(s.Domain, s.Bucket, name, payload)
}
