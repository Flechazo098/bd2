package roster

// GachaGrantSummary is a read-only projection of real durable purchase grants.
// It deliberately excludes grants without a schedule group; ordinary quest
// rewards also use ViewCostumeIDs and must not be counted as gacha draws.
func (s *CollectionStore) GachaGrantSummary() map[string]uint64 {

	out := map[string]uint64{}
	for identity, grant := range s.data.Grants {
		if grant.GachaGroupID != 0 && len(grant.ViewCostumeIDs) > 0 {
			out[identity] = uint64(len(grant.ViewCostumeIDs))
		}
	}
	return out
}
