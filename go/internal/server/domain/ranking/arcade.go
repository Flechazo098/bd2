package ranking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
)

type ArcadeScope struct {
	Family                string
	EventUID, Stage, Mode uint64
}

type ArcadeRecord struct {
	ArcadeScope
	Score                   float64
	CapturedArea, ClearTime uint64
	AchievedAt              uint64
	PlayInfo                string
}

type ArcadeProfile struct {
	Owner                               uint64
	Name                                string
	XP, Portrait, PortraitDesign, Title uint64
	Guild                               []byte
}

type ArcadePublication struct {
	Account string
	Version uint64
	Profile ArcadeProfile
	Records []ArcadeRecord
}

type ArcadeEntry struct {
	ArcadeRecord
	Profile    ArcadeProfile
	Rank       uint64
	TopPercent float64
	Own        bool
}

type Arcade struct {
	store   Store
	writer  sync.Mutex
	mu      sync.RWMutex
	players map[uint64]ArcadePublication
	best    map[uint64]map[ArcadeScope]ArcadeEntry
	boards  map[ArcadeScope][]ArcadeEntry
}

func NewArcade(ctx context.Context, store Store) (*Arcade, error) {
	if store == nil {
		return nil, errors.New("arcade ranking requires persistence")
	}
	r := &Arcade{store: store, players: map[uint64]ArcadePublication{}, best: map[uint64]map[ArcadeScope]ArcadeEntry{}, boards: map[ArcadeScope][]ArcadeEntry{}}
	err := store.View(ctx, func(tx Tx) error {
		for cursor := ""; ; {
			rows, err := tx.Scan("arcade.players", "", cursor, 256)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				var publication ArcadePublication
				if err := json.Unmarshal(row.Value, &publication); err != nil {
					return err
				}
				if err := validArcadePublication(publication); err != nil {
					return err
				}
				if row.Key != arcadeOwnerKey(publication.Profile.Owner) || row.Key <= cursor {
					return errors.New("invalid saved arcade identity")
				}
				r.players[publication.Profile.Owner] = publication
				r.best[publication.Profile.Owner] = arcadeBest(publication)
				for scope, entry := range r.best[publication.Profile.Owner] {
					r.boards[scope] = append(r.boards[scope], entry)
				}
				cursor = row.Key
			}
		}
	})
	if err != nil {
		return nil, err
	}
	for _, board := range r.boards {
		slices.SortFunc(board, compareArcade)
	}
	return r, nil
}

func arcadeOwnerKey(owner uint64) string { return fmt.Sprintf("%020d", owner) }

func (r *Arcade) Account(owner uint64) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.players[owner].Account
}

func validArcadeRecord(row ArcadeRecord) error {
	if row.EventUID == 0 || row.EventUID > math.MaxInt32 || row.Stage > math.MaxInt32 || row.Mode > math.MaxInt32 || row.Score <= 0 || row.AchievedAt == 0 || row.AchievedAt > math.MaxInt64 || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) {
		return errors.New("invalid arcade result")
	}
	switch row.Family {
	case "Survival", "Rhythm", "PackMan", "Field", "Run":
		if row.Score >= 1<<63 || math.Trunc(row.Score) != row.Score {
			return errors.New("invalid arcade point")
		}
		if row.Family == "Rhythm" && (row.Mode < 1 || row.Mode > 2) {
			return errors.New("invalid ranked rhythm mode")
		}
		if row.Family == "Field" && (row.Mode < 1 || row.Mode > 2) || row.Family == "Run" && (row.Mode < 3 || row.Mode > 4) {
			return errors.New("invalid field run ranking")
		}
	case "Sichuan":
		if row.Mode != 2 {
			return errors.New("invalid ranked sichuan mode")
		}
	case "Hopscotch":
		if row.ClearTime == 0 || row.ClearTime > math.MaxInt32 || row.CapturedArea > 100 || row.Score != float64(row.ClearTime) {
			return errors.New("invalid arcade area record")
		}
	default:
		return errors.New("invalid arcade family")
	}
	return nil
}

func validArcadePublication(p ArcadePublication) error {
	if p.Account == "" || p.Profile.Owner == 0 || p.Profile.Owner > math.MaxInt64 || p.Profile.Name == "" || p.Version == 0 {
		return errors.New("invalid arcade ranking identity")
	}
	seen := map[ArcadeScope]bool{}
	for _, row := range p.Records {
		if err := validArcadeRecord(row); err != nil {
			return err
		}
		if seen[row.ArcadeScope] {
			return errors.New("duplicate arcade record")
		}
		seen[row.ArcadeScope] = true
	}
	return nil
}

func arcadeBoardScope(scope ArcadeScope) ArcadeScope {
	if scope.Family == "Survival" || scope.Family == "Sichuan" {
		scope.Stage, scope.Mode = 0, 0
	}
	return scope
}

// Sichuan challenge ties retain the first achievement; other ties use stable owner identity.
func compareArcade(a, b ArcadeEntry) int {
	var comparison int
	if a.Family == "Hopscotch" {
		comparison = cmpUint(a.ClearTime, b.ClearTime)
	} else if a.Score < b.Score {
		comparison = -1
	} else if a.Score > b.Score {
		comparison = 1
	}
	if a.Family != "Sichuan" && a.Family != "Hopscotch" && (a.Family != "Run" || a.Mode != 3) {
		comparison = -comparison
	}
	if comparison != 0 {
		return comparison
	}
	if a.Family == "Sichuan" {
		if c := cmpUint(a.AchievedAt, b.AchievedAt); c != 0 {
			return c
		}
	}
	return cmpUint(a.Profile.Owner, b.Profile.Owner)
}

func cmpUint(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func arcadeBest(p ArcadePublication) map[ArcadeScope]ArcadeEntry {
	best := map[ArcadeScope]ArcadeEntry{}
	for _, record := range p.Records {
		scope := arcadeBoardScope(record.ArcadeScope)
		entry := ArcadeEntry{ArcadeRecord: record, Profile: p.Profile}
		old, exists := best[scope]
		if !exists || compareArcade(entry, old) < 0 {
			best[scope] = entry
		}
	}
	return best
}

func arcadePosition(board []ArcadeEntry, entry ArcadeEntry) int {
	return sort.Search(len(board), func(i int) bool { return compareArcade(board[i], entry) >= 0 })
}

func (r *Arcade) apply(p ArcadePublication) {
	for scope, previous := range r.best[p.Profile.Owner] {
		board := r.boards[scope]
		position := arcadePosition(board, previous)
		if position < len(board) && board[position].Profile.Owner == p.Profile.Owner {
			r.boards[scope] = slices.Delete(board, position, position+1)
		}
	}
	r.best[p.Profile.Owner] = arcadeBest(p)
	for scope, entry := range r.best[p.Profile.Owner] {
		board := r.boards[scope]
		position := arcadePosition(board, entry)
		r.boards[scope] = slices.Insert(board, position, entry)
	}
	r.players[p.Profile.Owner] = p
}

func (r *Arcade) Publish(ctx context.Context, p ArcadePublication) error {
	if err := validArcadePublication(p); err != nil {
		return err
	}
	r.writer.Lock()
	defer r.writer.Unlock()
	r.mu.RLock()
	old := r.players[p.Profile.Owner]
	r.mu.RUnlock()
	if old.Account != "" && old.Account != p.Account {
		return errors.New("arcade ranking owner conflict")
	}
	if old.Version > p.Version {
		return nil
	}
	if old.Version == p.Version {
		oldRaw, err := json.Marshal(old)
		if err != nil {
			return err
		}
		nextRaw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(oldRaw, nextRaw) {
			return errors.New("arcade publication version conflict")
		}
		return nil
	}
	nextRecords := map[ArcadeScope]ArcadeRecord{}
	for _, row := range p.Records {
		nextRecords[row.ArcadeScope] = row
	}
	for _, previous := range old.Records {
		next, exists := nextRecords[previous.ArcadeScope]
		if !exists || compareArcade(ArcadeEntry{ArcadeRecord: next}, ArcadeEntry{ArcadeRecord: previous}) > 0 {
			return errors.New("arcade publication lost or regressed record")
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var owned ArcadePublication
	if err := json.Unmarshal(raw, &owned); err != nil {
		return err
	}
	if err := r.store.Update(ctx, func(tx Tx) error { return tx.Put("arcade.players", arcadeOwnerKey(p.Profile.Owner), raw) }); err != nil {
		return err
	}
	r.mu.Lock()
	r.apply(owned)
	r.mu.Unlock()
	return nil
}

// Board overlays the requesting player's committed state while its outbox is pending.
func (r *Arcade) Board(scope ArcadeScope, own ArcadeProfile, recent []ArcadeRecord, limit int) []ArcadeEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	scope = arcadeBoardScope(scope)
	board := r.boards[scope]
	ownPosition := -1
	if previous, found := r.best[own.Owner][scope]; found {
		position := arcadePosition(board, previous)
		if position < len(board) && board[position].Profile.Owner == own.Owner {
			ownPosition = position
		}
	}
	var latest ArcadeEntry
	for _, record := range recent {
		if arcadeBoardScope(record.ArcadeScope) != scope {
			continue
		}
		entry := ArcadeEntry{ArcadeRecord: record, Profile: own}
		if latest.Profile.Owner == 0 || compareArcade(entry, latest) < 0 {
			latest = entry
		}
	}
	count, insertion := len(board), -1
	if latest.Profile.Owner != 0 {
		insertion = arcadePosition(board, latest)
		if ownPosition >= 0 {
			count--
			if ownPosition < insertion {
				insertion--
			}
		}
		count++
	}
	entryAt := func(position int) ArcadeEntry {
		if insertion >= 0 && position == insertion {
			return latest
		}
		index := position
		if insertion >= 0 {
			if position > insertion {
				index--
			}
			if ownPosition >= 0 && index >= ownPosition {
				index++
			}
		}
		return board[index]
	}
	placed := func(position int) ArcadeEntry {
		entry := entryAt(position)
		entry.Own = own.Owner != 0 && entry.Profile.Owner == own.Owner
		entry.Profile.Guild = slices.Clone(entry.Profile.Guild)
		entry.Rank, entry.TopPercent = uint64(position+1), float64(position+1)*100/float64(count)
		return entry
	}
	topCount := min(max(limit, 0), count)
	out := make([]ArcadeEntry, 0, min(topCount+1, count))
	for i := range topCount {
		out = append(out, placed(i))
	}
	if insertion >= 0 {
		ownPosition = insertion
	}
	if ownPosition >= topCount {
		out = append(out, placed(ownPosition))
	}
	return out
}

func (r *Arcade) Scopes() []ArcadeScope {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var scopes []ArcadeScope
	for scope, rows := range r.boards {
		if len(rows) > 0 {
			scopes = append(scopes, scope)
		}
	}
	slices.SortFunc(scopes, func(a, b ArcadeScope) int {
		if a.Family < b.Family {
			return -1
		}
		if a.Family > b.Family {
			return 1
		}
		if c := cmpUint(a.EventUID, b.EventUID); c != 0 {
			return c
		}
		if c := cmpUint(a.Stage, b.Stage); c != 0 {
			return c
		}
		return cmpUint(a.Mode, b.Mode)
	})
	return scopes
}
