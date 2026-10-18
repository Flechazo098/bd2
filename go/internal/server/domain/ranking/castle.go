package ranking

import (
	"bd2server/internal/server/domain/battle/castle"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
)

type Castle struct {
	store   Store
	writer  sync.Mutex
	mu      sync.RWMutex
	players map[uint64]castle.RankPublication
	boards  map[[2]uint64][]castle.RankEntry
}

func NewCastle(ctx context.Context, store Store) (*Castle, error) {
	if store == nil {
		return nil, errors.New("castle ranking requires persistence")
	}
	r := &Castle{store: store, players: map[uint64]castle.RankPublication{}, boards: map[[2]uint64][]castle.RankEntry{}}
	e := store.View(ctx, func(tx Tx) error {
		for cursor := ""; ; {
			rows, e := tx.Scan("castle.players", "", cursor, 256)
			if e != nil {
				return e
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				var p castle.RankPublication
				if e = json.Unmarshal(row.Value, &p); e != nil {
					return e
				}
				if e = validCastlePublication(p); e != nil {
					return e
				}
				if _, ok := r.players[p.Profile.Owner]; ok {
					return errors.New("duplicate castle owner")
				}
				r.apply(p)
				cursor = row.Key
			}
		}
	})
	return r, e
}
func validCastlePublication(p castle.RankPublication) error {
	if p.Account == "" || p.Version == 0 || p.Profile.Owner == 0 {
		return errors.New("invalid castle identity")
	}
	seen := map[[2]uint64]bool{}
	for _, record := range p.Records {
		key := [2]uint64{record.Season, record.Stage}
		if record.Season == 0 || record.Point == 0 || seen[key] {
			return errors.New("invalid castle record")
		}
		seen[key] = true
	}
	return nil
}
func compareCastle(a, b castle.RankEntry) int {
	if a.Point != b.Point {
		if a.Point > b.Point {
			return -1
		}
		return 1
	}
	if a.Date != b.Date {
		if a.Date < b.Date {
			return -1
		}
		return 1
	}
	if a.Profile.Owner < b.Profile.Owner {
		return -1
	}
	if a.Profile.Owner > b.Profile.Owner {
		return 1
	}
	return 0
}
func (r *Castle) apply(p castle.RankPublication) {
	for _, record := range p.Records {
		key := [2]uint64{record.Season, record.Stage}
		board := slices.DeleteFunc(r.boards[key], func(e castle.RankEntry) bool { return e.Profile.Owner == p.Profile.Owner })
		entry := castle.RankEntry{RankRecord: record, Profile: p.Profile}
		i := sort.Search(len(board), func(i int) bool { return compareCastle(board[i], entry) >= 0 })
		r.boards[key] = slices.Insert(board, i, entry)
	}
	r.players[p.Profile.Owner] = p
}
func (r *Castle) Publish(ctx context.Context, p castle.RankPublication) error {
	if e := validCastlePublication(p); e != nil {
		return e
	}
	r.writer.Lock()
	defer r.writer.Unlock()
	r.mu.RLock()
	old := r.players[p.Profile.Owner]
	r.mu.RUnlock()
	if old.Account != "" && old.Account != p.Account {
		return errors.New("castle owner conflict")
	}
	if old.Version >= p.Version {
		return nil
	}
	for _, previous := range old.Records {
		found := false
		for _, next := range p.Records {
			if next.Season != previous.Season || next.Stage != previous.Stage {
				continue
			}
			found = true
			if next.Point < previous.Point {
				return errors.New("castle score regressed")
			}
		}
		if !found {
			return errors.New("castle season lost")
		}
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	var owned castle.RankPublication
	if e = json.Unmarshal(raw, &owned); e != nil {
		return e
	}
	if e = r.store.Update(ctx, func(tx Tx) error { return tx.Put("castle.players", fmt.Sprintf("%020d", p.Profile.Owner), raw) }); e != nil {
		return e
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apply(owned)
	return nil
}
func (r *Castle) Top(season, stage uint64, limit int) []castle.RankEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	board := r.boards[[2]uint64{season, stage}]
	out := make([]castle.RankEntry, min(max(limit, 0), len(board)))
	for i := range out {
		out[i] = board[i]
		out[i].Rank = uint64(i + 1)
		out[i].Profile.Guild = slices.Clone(out[i].Profile.Guild)
	}
	return out
}
func (r *Castle) Rank(owner uint64, record castle.RankRecord) castle.RankEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile := r.players[owner].Profile
	profile.Owner = owner
	profile.Guild = slices.Clone(profile.Guild)
	entry := castle.RankEntry{RankRecord: record, Profile: profile}
	board := r.boards[[2]uint64{record.Season, record.Stage}]
	rank := uint64(1)
	for _, other := range board {
		if other.Profile.Owner != owner && compareCastle(other, entry) < 0 {
			rank++
		}
	}
	entry.Rank = rank
	return entry
}
