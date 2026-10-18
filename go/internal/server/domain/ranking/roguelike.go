package ranking

import (
	"bd2server/internal/server/domain/battle/roguelike"
	"bd2server/internal/server/domain/command"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
)

type RogueLike struct {
	store   Store
	writer  sync.Mutex
	mu      sync.RWMutex
	records map[[2]uint64]roguelike.RankPublication
	boards  map[uint64][]roguelike.RankEntry
	totals  map[uint64]uint64
}

func NewRogueLike(ctx context.Context, store Store) (*RogueLike, error) {
	if store == nil {
		return nil, fmt.Errorf("roguelike ranking: missing store")
	}
	r := &RogueLike{store: store, records: map[[2]uint64]roguelike.RankPublication{}, boards: map[uint64][]roguelike.RankEntry{}, totals: map[uint64]uint64{}}
	e := store.View(ctx, func(tx Tx) error {
		for cursor := ""; ; {
			rows, e := tx.Scan("roguelike.players", "", cursor, 256)
			if e != nil {
				return e
			}
			for _, row := range rows {
				var p roguelike.RankPublication
				if e = json.Unmarshal(row.Value, &p); e != nil {
					return e
				}
				if e = validateRogue(p); e != nil {
					return e
				}
				r.apply(p)
				cursor = row.Key
			}
			if len(rows) < 256 {
				return nil
			}
		}
	})
	return r, e
}
func validateRogue(p roguelike.RankPublication) error {
	if p.Account == "" || p.Season == 0 || p.Version == 0 || p.Entry.Owner == 0 || p.Entry.Score > math.MaxInt64 || p.Entry.Damage > math.MaxInt64 {
		return fmt.Errorf("roguelike ranking: invalid publication")
	}
	return nil
}
func compareRogue(a, b roguelike.RankEntry) int {
	if a.Damage > b.Damage {
		return -1
	}
	if a.Damage < b.Damage {
		return 1
	}
	if a.Score > b.Score {
		return -1
	}
	if a.Score < b.Score {
		return 1
	}
	if a.Owner < b.Owner {
		return -1
	}
	if a.Owner > b.Owner {
		return 1
	}
	return 0
}
func (r *RogueLike) apply(p roguelike.RankPublication) {
	key := [2]uint64{p.Season, p.Entry.Owner}
	prior := r.records[key]
	r.totals[p.Season] = r.totals[p.Season] - prior.Entry.Score + p.Entry.Score
	board := slices.DeleteFunc(r.boards[p.Season], func(e roguelike.RankEntry) bool { return e.Owner == p.Entry.Owner })
	at := sort.Search(len(board), func(i int) bool { return compareRogue(board[i], p.Entry) >= 0 })
	r.boards[p.Season] = slices.Insert(board, at, p.Entry)
	r.records[key] = p
}
func (r *RogueLike) Publish(ctx context.Context, p roguelike.RankPublication) error {
	if e := validateRogue(p); e != nil {
		return e
	}
	r.writer.Lock()
	defer r.writer.Unlock()
	r.mu.RLock()
	old := r.records[[2]uint64{p.Season, p.Entry.Owner}]
	total := r.totals[p.Season]
	r.mu.RUnlock()
	if old.Account != "" && old.Account != p.Account {
		return fmt.Errorf("roguelike ranking: owner conflict")
	}
	if old.Version >= p.Version {
		return nil
	}
	if p.Entry.Score < old.Entry.Score || p.Entry.Damage < old.Entry.Damage || p.Entry.Score-old.Entry.Score > math.MaxInt64-total {
		return fmt.Errorf("roguelike ranking: regressed or overflowed score")
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	var owned roguelike.RankPublication
	if e = json.Unmarshal(raw, &owned); e != nil {
		return e
	}
	if e = r.store.Update(ctx, func(tx Tx) error {
		return tx.Put("roguelike.players", fmt.Sprintf("%020d:%020d", p.Season, p.Entry.Owner), raw)
	}); e != nil {
		return e
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply(owned)
	return nil
}
func (r *RogueLike) Total(_ command.Context, season uint64) (uint64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.totals[season], nil
}
func (r *RogueLike) Snapshot(_ command.Context, season uint64, local roguelike.RankEntry) ([]roguelike.RankEntry, roguelike.RankEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	board := r.boards[season]
	prior := r.records[[2]uint64{season, local.Owner}].Entry
	if prior.Score > local.Score {
		local.Score = prior.Score
	}
	if prior.Damage > local.Damage {
		local.Damage = prior.Damage
	}
	local.Profile = slices.Clone(prior.Profile)
	at := sort.Search(len(board), func(i int) bool { return compareRogue(board[i], local) >= 0 })
	rank := at + 1
	if compareRogue(prior, local) < 0 {
		rank--
	}
	local.Rank = uint64(max(rank, 1))
	if local.Score == 0 {
		local.Rank = 0
	}
	top := make([]roguelike.RankEntry, 0, min(100, len(board)+1))
	inserted := false
	for _, e := range board {
		if e.Owner == local.Owner {
			continue
		}
		if !inserted && local.Score > 0 && compareRogue(local, e) < 0 {
			top = append(top, local)
			inserted = true
		}
		if len(top) >= 100 {
			break
		}
		e.Profile = slices.Clone(e.Profile)
		top = append(top, e)
	}
	if !inserted && local.Score > 0 && len(top) < 100 {
		top = append(top, local)
	}
	for i := range top {
		top[i].Rank = uint64(i + 1)
	}
	return top, local, nil
}

func (r *RogueLike) PendingTotal(_ command.Context, season uint64, local roguelike.RankEntry) (uint64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := r.totals[season]
	previous := r.records[[2]uint64{season, local.Owner}].Entry.Score
	if local.Score <= previous {
		return total, nil
	}
	delta := local.Score - previous
	if delta > math.MaxInt64-total {
		return 0, fmt.Errorf("roguelike ranking: pending total overflow")
	}
	return total + delta, nil
}
