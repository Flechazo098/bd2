package fishing

import (
	"bd2server/internal/server/protocol/wire"
	"math"
	"strings"
)

// Catch replies and retained fish instances are deduplicated by inventory
// index. The codex proves one catch of species whose fish was already sold.
func (s *Service) AchievementProgress() (map[[2]uint64]uint64, error) {
	values := map[[2]uint64]uint64{{141, 0}: s.state.Level, {142, 0}: uint64(len(s.state.Collection))}
	fish := map[uint64]Item{}
	for _, item := range s.state.Fish {
		fish[item.Index] = item
	}
	for key, reply := range s.state.Replies {
		if !strings.Contains(key, ":/FishingBiteEnd:") && !strings.Contains(key, ":/FishingFishAuto:") {
			continue
		}
		bundle, _, err := wire.Bytes(reply.Body, 1)
		if err != nil {
			return nil, err
		}
		if err := wire.Walk(bundle, func(field wire.Field) error {
			if field.Number != 11 || field.Type != 2 {
				return nil
			}
			index, _, err := wire.Varint(field.Value, 1)
			if err != nil {
				return err
			}
			id, _, err := wire.Varint(field.Value, 2)
			if err != nil {
				return err
			}
			size, _, err := wire.Varint(field.Value, 3)
			if err != nil {
				return err
			}
			fish[index] = Item{Index: index, ID: id, Size: size, Count: 1}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	counts, maxSizes := map[uint64]uint64{}, map[uint64]uint64{}
	for _, item := range fish {
		counts[item.ID] += item.Count
		row, err := s.design.Row("FishingFishTable", 12, item.ID)
		if err != nil {
			return nil, err
		}
		if item.Size >= uint64(math.Round(dbl(row, 14))) {
			maxSizes[item.ID] += item.Count
		}
	}
	for id, collection := range s.state.Collection {
		counts[id] = max(1, counts[id])
		row, err := s.design.Row("FishingFishTable", 12, id)
		if err != nil {
			return nil, err
		}
		if collection.Max >= uint64(math.Round(dbl(row, 14))) {
			maxSizes[id] = max(1, maxSizes[id])
		}
	}
	for id, count := range counts {
		values[[2]uint64{144, id}] = count
		values[[2]uint64{146, 0}] += count
	}
	for _, count := range maxSizes {
		values[[2]uint64{143, 0}] += count
	}
	return values, nil
}
