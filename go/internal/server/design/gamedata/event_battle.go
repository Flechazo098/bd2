package gamedata

import (
	"encoding/binary"
	"fmt"
	"math"

	"bd2server/internal/server/protocol/wire"
)

type EventBattleRule struct {
	Group, Stage, Deck, APCost uint64
	Quick                      bool
	Rewards                    []BattleReward
}

func (c *EventPlayCatalog) EventBattle(group, stage, deck uint64) (EventBattleRule, error) {
	for _, raw := range c.Rows("PackEventBattleTable", 4, group) {
		id, err := optionalScalar(raw, 5)
		if err != nil {
			return EventBattleRule{}, err
		}
		if id != stage {
			continue
		}
		actualDeck, err := optionalScalar(raw, 1)
		if err != nil {
			return EventBattleRule{}, err
		}
		if deck != 0 && actualDeck != deck {
			return EventBattleRule{}, fmt.Errorf("gamedata: event battle deck mismatch")
		}
		ap, err := optionalScalar(raw, 3)
		if err != nil {
			return EventBattleRule{}, err
		}
		quick, err := optionalScalar(raw, 7)
		if err != nil {
			return EventBattleRule{}, err
		}
		rewards, err := eventGameRewards(raw, 9, 10, 8)
		if err != nil {
			return EventBattleRule{}, err
		}
		return EventBattleRule{Group: group, Stage: stage, Deck: actualDeck, APCost: ap, Quick: quick == 1, Rewards: rewards}, nil
	}
	return EventBattleRule{}, fmt.Errorf("gamedata: event battle unavailable")
}

func (c *EventPlayCatalog) EventBattleBonus(hub uint64, owned map[uint64]bool, rewards []BattleReward) ([]BattleReward, error) {
	rates := map[[2]uint64]float32{}
	for _, raw := range c.Rows("PickUpCostumeEventTable", 3, hub) {
		costume, err := optionalScalar(raw, 2)
		if err != nil {
			return nil, err
		}
		if !owned[costume] {
			continue
		}
		ids, err := packedInts(raw, 5)
		if err != nil {
			return nil, err
		}
		types, err := packedInts(raw, 6)
		if err != nil {
			return nil, err
		}
		var bonus []float32
		if err := wire.Walk(raw, func(f wire.Field) error {
			if f.Number != 1 {
				return nil
			}
			if f.Type != 2 && f.Type != 5 || len(f.Value)%4 != 0 {
				return fmt.Errorf("gamedata: invalid event bonus")
			}
			for at := 0; at < len(f.Value); at += 4 {
				bonus = append(bonus, math.Float32frombits(binary.LittleEndian.Uint32(f.Value[at:])))
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if len(ids) != len(types) || len(ids) != len(bonus) {
			return nil, fmt.Errorf("gamedata: event bonus arrays differ")
		}
		for i, rate := range bonus {
			if math.IsNaN(float64(rate)) || math.IsInf(float64(rate), 0) || rate < 0 {
				return nil, fmt.Errorf("gamedata: invalid event bonus rate")
			}
			rates[[2]uint64{types[i], ids[i]}] += rate
		}
	}
	out := append([]BattleReward(nil), rewards...)
	for i, r := range out {
		count := math.Floor(float64(float32(r.Count) * (1 + rates[[2]uint64{r.Type, r.ID}])))
		if count > math.MaxInt32 || count < 0 {
			return nil, fmt.Errorf("gamedata: event bonus overflow")
		}
		out[i].Count = uint64(count)
	}
	return out, nil
}
