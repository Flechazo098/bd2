package gamedata

import (
	"errors"
	"math/big"
)

// Local pity policy: no separate official equipment pity pool is available.
// gradePool conditions the actual nested weighted distribution on one grade.
// Exact rational path weights preserve branches with different child totals.
func (g EquipmentGacha) gradePool(grade uint64) ([]WeightedEquipment, error) {
	weights := map[uint64]*big.Rat{}
	var walk func([]WeightedEquipment, *big.Rat) error
	walk = func(pool []WeightedEquipment, path *big.Rat) error {
		total := new(big.Int)
		for _, item := range pool {
			if item.Weight == 0 {
				return errors.New("gamedata: zero equipment weight")
			}
			total.Add(total, new(big.Int).SetUint64(item.Weight))
		}
		if total.Sign() == 0 {
			return errors.New("gamedata: empty equipment pool")
		}
		for _, item := range pool {
			prob := new(big.Rat).Mul(path, new(big.Rat).SetFrac(new(big.Int).SetUint64(item.Weight), total))
			if item.ID == 0 {
				if err := walk(item.Children, prob); err != nil {
					return err
				}
				continue
			}
			actual, ok := g.Grades[item.ID]
			if !ok || actual == 0 || actual > 4 {
				return errors.New("gamedata: equipment grade unavailable")
			}
			if actual == grade {
				if weights[item.ID] == nil {
					weights[item.ID] = new(big.Rat)
				}
				weights[item.ID].Add(weights[item.ID], prob)
			}
		}
		return nil
	}
	if err := walk(g.Pool, new(big.Rat).SetInt64(1)); err != nil {
		return nil, err
	}
	denom := big.NewInt(1)
	for _, weight := range weights {
		gcd := new(big.Int).GCD(nil, nil, denom, weight.Denom())
		denom.Mul(new(big.Int).Quo(denom, gcd), weight.Denom())
	}
	var ids []uint64
	for id := range weights {
		ids = append(ids, id)
	}
	// Stable table-independent ordering makes deterministic draw sources reproducible.
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	var out []WeightedEquipment
	for _, id := range ids {
		w := weights[id]
		count := new(big.Int).Mul(w.Num(), new(big.Int).Quo(new(big.Int).Set(denom), w.Denom()))
		if !count.IsUint64() {
			return nil, errors.New("gamedata: conditioned equipment weight overflow")
		}
		out = append(out, WeightedEquipment{ID: id, Weight: count.Uint64()})
	}
	return out, nil
}
