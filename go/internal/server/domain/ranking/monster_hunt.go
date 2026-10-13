package ranking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"

	"bd2server/internal/server/domain/battle/monsterhunt"
)

type MonsterHunt struct {
	store   Store
	writer  sync.Mutex
	mu      sync.RWMutex
	players map[uint64]monsterhunt.RankPublication
	boards  map[uint64][]monsterhunt.RankEntry
}

func NewMonsterHunt(ctx context.Context, store Store) (*MonsterHunt, error) {
	if store == nil {
		return nil, errors.New("hunt ranking requires persistence")
	}
	r := &MonsterHunt{store: store, players: map[uint64]monsterhunt.RankPublication{}, boards: map[uint64][]monsterhunt.RankEntry{}}
	err := store.View(ctx, func(tx Tx) error {
		for cursor := ""; ; {
			rows, err := tx.Scan("monster_hunt.players", "", cursor, 256)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				var publication monsterhunt.RankPublication
				if err := json.Unmarshal(row.Value, &publication); err != nil {
					return err
				}
				if err := validHuntPublication(publication); err != nil {
					return err
				}
				if _, exists := r.players[publication.Profile.Owner]; exists {
					return errors.New("duplicate hunt ranking owner")
				}
				r.players[publication.Profile.Owner] = publication
				for _, record := range publication.Records {
					r.boards[record.Season] = append(r.boards[record.Season], monsterhunt.RankEntry{RankRecord: record, Profile: publication.Profile})
				}
				cursor = row.Key
			}
		}
	})
	for season, board := range r.boards {
		slices.SortFunc(board, compareHunt)
		r.boards[season] = board
	}
	return r, err
}

func validHuntPublication(p monsterhunt.RankPublication) error {
	if p.Account == "" || p.Profile.Owner == 0 || p.Version == 0 {
		return errors.New("invalid hunt ranking identity")
	}
	seen := map[uint64]bool{}
	for _, row := range p.Records {
		if row.Season == 0 || row.Level == 0 || row.Version > 1 || row.Score <= 0 || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) || seen[row.Season] {
			return errors.New("invalid hunt ranking result")
		}
		seen[row.Season] = true
	}
	return nil
}

func compareHunt(a, b monsterhunt.RankEntry) int {
	if a.Version == 0 && a.Score != b.Score {
		if a.Score > b.Score {
			return -1
		}
		return 1
	}
	if a.Level != b.Level {
		if a.Level > b.Level {
			return -1
		}
		return 1
	}
	if a.Version == 1 && a.Damage != b.Damage {
		if a.Damage > b.Damage {
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

func (r *MonsterHunt) apply(p monsterhunt.RankPublication) {
	for _, record := range p.Records {
		board := r.boards[record.Season]
		board = slices.DeleteFunc(board, func(row monsterhunt.RankEntry) bool { return row.Profile.Owner == p.Profile.Owner })
		entry := monsterhunt.RankEntry{RankRecord: record, Profile: p.Profile}
		i := sort.Search(len(board), func(i int) bool { return compareHunt(board[i], entry) >= 0 })
		board = slices.Insert(board, i, entry)
		r.boards[record.Season] = board
	}
	r.players[p.Profile.Owner] = p
}

func (r *MonsterHunt) Publish(ctx context.Context, p monsterhunt.RankPublication) error {
	if err := validHuntPublication(p); err != nil {
		return err
	}
	r.writer.Lock()
	defer r.writer.Unlock()
	r.mu.RLock()
	old := r.players[p.Profile.Owner]
	r.mu.RUnlock()
	if old.Account != "" && old.Account != p.Account {
		return errors.New("hunt ranking owner conflict")
	}
	if old.Version >= p.Version {
		return nil
	}
	for _, previous := range old.Records {
		found := false
		for _, next := range p.Records {
			if next.Season != previous.Season {
				continue
			}
			found = true
			if compareHunt(monsterhunt.RankEntry{RankRecord: next}, monsterhunt.RankEntry{RankRecord: previous}) > 0 {
				return errors.New("hunt ranking result regressed")
			}
		}
		if !found {
			return errors.New("hunt ranking publication lost season")
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var owned monsterhunt.RankPublication
	if err := json.Unmarshal(raw, &owned); err != nil {
		return err
	}
	if err := r.store.Update(ctx, func(tx Tx) error {
		return tx.Put("monster_hunt.players", fmt.Sprintf("%020d", p.Profile.Owner), raw)
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.apply(owned)
	r.mu.Unlock()
	return nil
}

func huntPlaced(entry monsterhunt.RankEntry, rank, count int) monsterhunt.RankEntry {
	entry.Profile.Guild = slices.Clone(entry.Profile.Guild)
	entry.Rank = uint64(rank)
	entry.TopPercent = float64(rank) * 100 / float64(count)
	return entry
}

func (r *MonsterHunt) Top(season uint64, limit int) []monsterhunt.RankEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	board := r.boards[season]
	out := make([]monsterhunt.RankEntry, min(max(limit, 0), len(board)))
	for i := range out {
		out[i] = huntPlaced(board[i], i+1, len(board))
	}
	return out
}

func (r *MonsterHunt) Rank(owner uint64, record monsterhunt.RankRecord) monsterhunt.RankEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile := r.players[owner].Profile
	profile.Owner = owner
	entry := monsterhunt.RankEntry{RankRecord: record, Profile: profile}
	board := r.boards[record.Season]
	count := len(board) + 1
	position := sort.Search(len(board), func(i int) bool { return compareHunt(board[i], entry) >= 0 })
	for _, old := range r.players[owner].Records {
		if old.Season != record.Season {
			continue
		}
		count--
		previous := monsterhunt.RankEntry{RankRecord: old, Profile: profile}
		oldPosition := sort.Search(len(board), func(i int) bool { return compareHunt(board[i], previous) >= 0 })
		if oldPosition < position {
			position--
		}
		break
	}
	return huntPlaced(entry, position+1, count)
}

func (r *MonsterHunt) OwnerRank(season, owner uint64) (monsterhunt.RankEntry, bool) {
	r.mu.RLock()
	p := r.players[owner]
	var record monsterhunt.RankRecord
	for _, row := range p.Records {
		if row.Season == season {
			record = row
			break
		}
	}
	r.mu.RUnlock()
	if record.Season == 0 {
		return monsterhunt.RankEntry{}, false
	}
	return r.Rank(owner, record), true
}
