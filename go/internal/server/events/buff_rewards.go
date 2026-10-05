package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
)

type buffRewardState struct {
	Counts   map[uint64]uint64
	Receipts map[string]string
}
type BuffRewards struct {
	mu     sync.Mutex
	store  stateio.Store
	design map[uint64]gamedata.PictorialBuffStat
}

func OpenBuffRewards(store stateio.Store, design map[uint64]gamedata.PictorialBuffStat) (*BuffRewards, error) {
	if store == nil || len(design) == 0 {
		return nil, fmt.Errorf("events: buff reward dependencies unavailable")
	}
	s := &BuffRewards{store: store, design: design}
	_, err := s.load()
	return s, err
}
func (s *BuffRewards) load() (buffRewardState, error) {
	v := buffRewardState{Counts: map[uint64]uint64{}, Receipts: map[string]string{}}
	b, err := s.store.Load("buff_item_rewards")
	if err != nil || b == nil {
		return v, err
	}
	if err := stateio.RequireExactJSONObject(b, "Counts", "Receipts"); err != nil {
		return v, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	if v.Counts == nil || v.Receipts == nil {
		return v, fmt.Errorf("events: invalid buff reward state")
	}
	for id, count := range v.Counts {
		if _, ok := s.design[id]; !ok || count == 0 || count > math.MaxInt32 {
			return v, fmt.Errorf("events: invalid permanent buff ownership")
		}
	}
	return v, nil
}
func (s *BuffRewards) Validate(rewards []gamedata.Reward) error {
	for _, r := range rewards {
		if _, ok := s.design[r.ID]; !ok || r.Type != 63 || r.Count == 0 || r.Count > math.MaxInt32 {
			return fmt.Errorf("events: unknown or invalid buff reward %d:%d:%d", r.Type, r.ID, r.Count)
		}
	}
	return nil
}
func (s *BuffRewards) GrantOnce(identity string, rewards []gamedata.Reward) error {
	if identity == "" {
		return fmt.Errorf("events: empty buff reward identity")
	}
	if err := s.Validate(rewards); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(rewards)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if previous, ok := v.Receipts[identity]; ok {
		if previous != digest {
			return fmt.Errorf("events: buff reward identity reused")
		}
		return nil
	}
	for _, r := range rewards {
		if v.Counts[r.ID] > math.MaxInt32-r.Count {
			return fmt.Errorf("events: permanent buff reward overflow")
		}
		v.Counts[r.ID] += r.Count
	}
	v.Receipts[identity] = digest
	raw, err = json.Marshal(v)
	if err != nil {
		return err
	}
	return s.store.Save("buff_item_rewards", raw)
}
func (s *BuffRewards) SnapshotBuffs() ([]gamedata.PictorialBuffStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return nil, err
	}
	type key struct{ category, stat uint64 }
	totals := map[key]float64{}
	for id, count := range v.Counts {
		d := s.design[id]
		totals[key{d.Category, d.StatType}] += d.Value * float64(count)
	}
	var out []gamedata.PictorialBuffStat
	for k, value := range totals {
		out = append(out, gamedata.PictorialBuffStat{Category: k.category, StatType: k.stat, Value: math.Round(value*1e8) / 1e8})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].StatType < out[j].StatType
	})
	return out, nil
}
func (e *Economy) AttachBuffRewards(service *BuffRewards) { e.buffRewards = service }
