package roster

// AutoRecoveryResult uses Define_AutoReviveDisabledType's protocol values.
type AutoRecoveryResult struct {
	Caster, Experience, Catalyst, Disabled uint64
	Characters                             []Character
}
