package roster

// ObservationVersion covers earned costumes attached without a storage write.
func (s *CollectionStore) ObservationVersion() uint64 {

	return s.observationVersion
}
