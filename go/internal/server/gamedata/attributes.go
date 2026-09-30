package gamedata

import "math"

// Stat is the small, protocol-independent subset of character statistics
// needed by the local server.  The client uses the same two kinds of values
// for every stat: a flat contribution and a percentage contribution.
type Stat int

const (
	StatHealth Stat = iota + 1
	StatAttack
	StatMagic
	StatDefencePercent
	StatMagicResistancePercent
	StatCriticalChance
	StatCriticalDamage
	StatElementDamage
	StatElementResistance
)

// StatContribution is one already-resolved contribution from an equipment
// option, costume, collection, awakening, or another account system. Percent
// is expressed as a fraction (0.08 means 8%), matching the client formula.
type StatContribution struct {
	Stat    Stat
	Flat    float64
	Percent float64
	// Option preserves the exact Define_CharStatOption for systems such as
	// awakening whose element-specific distinctions exceed BaseStats.
	Option uint64
}

// BaseStats contains the design value after the character level curve has
// been applied.  It is deliberately separate from CharacterStats so callers
// cannot accidentally persist a derived value as design data.
type BaseStats struct {
	Health float64
	Attack float64
	Magic  float64
}

// AggregateStats applies the final client-side aggregation order:
//
//	final = base + round(((base + flat) * (1 + percent)) - base)
//
// The game truncates the design value before applying account contributions;
// the final bonus is truncated toward zero as well.  Keeping this operation
// in one place prevents revival, CharInfo, and battle code from developing
// subtly different HP calculations.
func AggregateStats(base BaseStats, contributions []StatContribution) BaseStats {
	flat := map[Stat]float64{}
	percent := map[Stat]float64{}
	for _, contribution := range contributions {
		if contribution.Stat == 0 {
			continue
		}
		flat[contribution.Stat] += contribution.Flat
		percent[contribution.Stat] += contribution.Percent
	}
	return BaseStats{
		Health: aggregateOne(base.Health, flat[StatHealth], percent[StatHealth]),
		Attack: aggregateOne(base.Attack, flat[StatAttack], percent[StatAttack]),
		Magic:  aggregateOne(base.Magic, flat[StatMagic], percent[StatMagic]),
	}
}

func aggregateOne(base, flat, percent float64) float64 {
	base = math.Trunc(base)
	bonus := ((base + flat) * (1 + percent)) - base
	return base + math.Trunc(bonus)
}
