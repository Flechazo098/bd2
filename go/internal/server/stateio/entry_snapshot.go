package stateio

// EntrySnapshotStore keeps optional gameplay snapshots within an existing
// account domain. It participates in the parent's SQLite request transaction
// without adding cores to the nine-domain account initialization contract.
type EntrySnapshotStore struct {
	Entries        EntryStore
	Domain, Bucket string
}

func (s EntrySnapshotStore) Load(name string) ([]byte, error) {
	payload, _, err := s.Entries.LoadEntry(s.Domain, s.Bucket, name)
	return payload, err
}
func (s EntrySnapshotStore) Save(name string, payload []byte) error {
	return s.Entries.PutEntry(s.Domain, s.Bucket, name, payload)
}
