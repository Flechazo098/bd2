package hunting

import (
	"bd2server/internal/server/domain/command"
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
)

type achievementProgressSnapshot struct {
	History, Highest []byte
	Counts           map[uint64]uint64
}

// RecordedClearCounts uses retained boss settlements. Highest stage also
// proves at least one completion when old settlement receipts are absent.
func (s *Service) RecordedClearCounts(ctx command.Context) (map[uint64]uint64, error) {
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	highest := map[string]uint64{}
	for key, state := range s.state.Packs {
		highest[key] = state.Highest
	}
	highestRaw, err := json.Marshal(highest)
	if err != nil {
		return nil, err
	}
	historyRaw, err := s.storage.Load(ctx.State, "huntingbattle")
	if err != nil {
		return nil, err
	}
	if cached := s.achievementProgress; cached != nil && bytes.Equal(historyRaw, cached.History) && bytes.Equal(highestRaw, cached.Highest) {
		return maps.Clone(cached.Counts), nil
	}
	ledger, err := s.loadBattleReceipts(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[uint64]uint64{}
	byPack := map[int]map[uint64]uint64{}
	for _, receipt := range ledger {
		var pack int
		var mode, monster, deck uint64
		if n, err := fmt.Sscanf(receipt.Fingerprint, "%d:%d:%d:%d", &pack, &mode, &monster, &deck); err != nil || n != 4 || mode != BattleMode {
			continue
		}
		d, err := s.load(pack)
		if err != nil {
			return nil, err
		}
		for _, ground := range d.Grounds {
			if ground.BossID != monster {
				continue
			}
			if byPack[pack] == nil {
				byPack[pack] = map[uint64]uint64{}
			}
			byPack[pack][ground.Difficulty]++
		}
	}
	for key, state := range s.state.Packs {
		pack, _ := strconv.Atoi(key)
		if state.Highest == 0 {
			continue
		}
		d, err := s.load(pack)
		if err != nil {
			return nil, err
		}
		highest, known := ground(d, state.Highest)
		if !known {
			return nil, fmt.Errorf("hunting: completed stage missing")
		}
		if byPack[pack] == nil {
			byPack[pack] = map[uint64]uint64{}
		}
		byPack[pack][highest.Difficulty] = max(1, byPack[pack][highest.Difficulty])
	}
	for _, pack := range byPack {
		for difficulty, count := range pack {
			counts[difficulty] += count
		}
	}
	s.achievementProgress = &achievementProgressSnapshot{History: append([]byte(nil), historyRaw...), Highest: highestRaw, Counts: maps.Clone(counts)}
	return counts, nil
}
