package player

// CollectionRewardBundle encodes a persisted character/costume grant for
// any reward-bearing response. Its copy upgrades and post-max exchanges are
// identical to recruitment rewards and are replayed from the collection ledger.
func CollectionRewardBundle(c *CollectionStore, g CollectionGrant) []byte {
	return recruitRewardBundle(c, g)
}
