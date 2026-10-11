package ranking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"
	"unicode/utf8"
)

type TeamMember struct {
	AccountID   string
	OwnerIndex  uint64
	Name        string
	CharacterID uint64
}

type CompletedTeamRun struct {
	ResultID, InstanceID         string
	EventUID, Monster, StageType uint64
	Clear                        bool
	Milliseconds                 int64
	Members                      []TeamMember
}

type TeamQuery struct {
	OwnerID                      string
	EventUID, Monster, StageType uint64
	Limit                        int
}

type TeamRanking struct {
	ResultID, InstanceID               string
	EventUID, Monster, StageType, Rank uint64
	Milliseconds                       int64
	TopPercent                         float64
	Own                                bool
	Members                            []TeamMember
}

type teamBoard struct {
	event, monster, stage uint64
	best                  map[string]CompletedTeamRun
	order                 []string
	own                   map[string]string
}

type Teams struct {
	store  Store
	writer sync.Mutex
	mu     sync.RWMutex
	boards map[string]*teamBoard
}

func NewTeams(ctx context.Context, store Store) (*Teams, error) {
	if store == nil {
		return nil, errors.New("ranking: missing team store")
	}
	t := &Teams{store: store, boards: map[string]*teamBoard{}}
	err := store.View(ctx, func(tx Tx) error {
		for after := ""; ; {
			page, err := tx.Scan("team.best", "", after, 256)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for _, entry := range page {
				var run CompletedTeamRun
				if err = json.Unmarshal(entry.Value, &run); err != nil {
					return err
				}
				run, key, err := canonicalTeamRun(run)
				if err != nil || !run.Clear {
					return errors.New("ranking: invalid saved team result")
				}
				boardKey := teamBoardKey(run.EventUID, run.Monster, run.StageType)
				if entry.Key != boardKey+":"+key {
					return errors.New("ranking: saved team identity mismatch")
				}
				board := t.boards[boardKey]
				if board == nil {
					board = newTeamBoard(run)
					t.boards[boardKey] = board
				}
				if _, exists := board.best[key]; exists {
					return errors.New("ranking: duplicate saved team")
				}
				board.best[key] = run
				after = entry.Key
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, board := range t.boards {
		for key := range board.best {
			board.order = append(board.order, key)
		}
		sort.Slice(board.order, func(i, j int) bool { return lessTeam(board, board.order[i], board.order[j]) })
		for _, key := range board.order {
			for _, member := range board.best[key].Members {
				if board.own[member.AccountID] == "" {
					board.own[member.AccountID] = key
				}
			}
		}
	}
	return t, nil
}

func newTeamBoard(run CompletedTeamRun) *teamBoard {
	return &teamBoard{event: run.EventUID, monster: run.Monster, stage: run.StageType, best: map[string]CompletedTeamRun{}, own: map[string]string{}}
}
func teamBoardKey(event, monster, stage uint64) string {
	return fmt.Sprintf("%020d:%020d:%d", event, monster, stage)
}
func canonicalTeamRun(run CompletedTeamRun) (CompletedTeamRun, string, error) {
	if run.ResultID == "" || len(run.ResultID) > 512 || run.InstanceID == "" || len(run.InstanceID) > 512 || !utf8.ValidString(run.ResultID) || !utf8.ValidString(run.InstanceID) || run.EventUID == 0 || run.Monster == 0 || run.StageType < 1 || run.StageType > 2 || run.Milliseconds < 0 || run.Milliseconds > math.MaxInt32 || run.Clear && run.Milliseconds == 0 || len(run.Members) == 0 || len(run.Members) > 32 || run.StageType == 1 && len(run.Members) != 1 {
		return run, "", errors.New("ranking: invalid completed team run")
	}
	run.Members = slices.Clone(run.Members)
	sort.Slice(run.Members, func(i, j int) bool { return run.Members[i].OwnerIndex < run.Members[j].OwnerIndex })
	accounts := map[string]bool{}
	ids := make([]uint64, len(run.Members))
	for i, m := range run.Members {
		if m.AccountID == "" || len(m.AccountID) > 256 || !utf8.ValidString(m.AccountID) || m.OwnerIndex == 0 || m.OwnerIndex > math.MaxInt64 || m.Name == "" || len(m.Name) > 256 || !utf8.ValidString(m.Name) || m.CharacterID == 0 || m.CharacterID > math.MaxInt32 || accounts[m.AccountID] || i > 0 && ids[i-1] == m.OwnerIndex {
			return run, "", errors.New("ranking: invalid team member")
		}
		accounts[m.AccountID] = true
		ids[i] = m.OwnerIndex
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return run, "", err
	}
	key := sha256.Sum256(raw)
	return run, hex.EncodeToString(key[:]), nil
}
func lessTeam(b *teamBoard, a, c string) bool {
	if b.best[a].Milliseconds != b.best[c].Milliseconds {
		return b.best[a].Milliseconds < b.best[c].Milliseconds
	}
	return a < c
}

func (t *Teams) Record(ctx context.Context, input CompletedTeamRun) error {
	run, teamKey, err := canonicalTeamRun(input)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	fingerprint := sha256.Sum256(raw)
	identity := sha256.Sum256([]byte(run.ResultID))
	receiptKey := hex.EncodeToString(identity[:])
	boardKey := teamBoardKey(run.EventUID, run.Monster, run.StageType)
	t.writer.Lock()
	defer t.writer.Unlock()
	board := t.boards[boardKey]
	improved := run.Clear && (board == nil || board.best[teamKey].ResultID == "" || run.Milliseconds < board.best[teamKey].Milliseconds)
	var publish bool
	err = t.store.Update(ctx, func(tx Tx) error {
		prior, exists, err := tx.Get("team.results", receiptKey)
		if err != nil {
			return err
		}
		if exists {
			if !bytes.Equal(prior, fingerprint[:]) {
				return errors.New("ranking: result identity reused with different run")
			}
			return nil
		}
		if improved {
			if err = tx.Put("team.best", boardKey+":"+teamKey, raw); err != nil {
				return err
			}
			publish = true
		}
		return tx.Put("team.results", receiptKey, fingerprint[:])
	})
	if err != nil || !publish {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if board == nil {
		board = newTeamBoard(run)
		t.boards[boardKey] = board
	}
	if _, exists := board.best[teamKey]; exists {
		at := sort.Search(len(board.order), func(i int) bool { return !lessTeam(board, board.order[i], teamKey) })
		if at == len(board.order) || board.order[at] != teamKey {
			return errors.New("ranking: team index inconsistent")
		}
		board.order = slices.Delete(board.order, at, at+1)
	}
	board.best[teamKey] = run
	at := sort.Search(len(board.order), func(i int) bool { return !lessTeam(board, board.order[i], teamKey) })
	board.order = slices.Insert(board.order, at, teamKey)
	for _, member := range run.Members {
		old := board.own[member.AccountID]
		if old == "" || lessTeam(board, teamKey, old) {
			board.own[member.AccountID] = teamKey
		}
	}
	return nil
}

func (t *Teams) Rankings(query TeamQuery) ([]TeamRanking, error) {
	if query.Limit < 1 || query.Limit > 256 || query.StageType > 2 {
		return nil, errors.New("ranking: invalid team query")
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	var keys []string
	for key, b := range t.boards {
		if query.EventUID != 0 && query.EventUID != b.event || query.Monster != 0 && query.Monster != b.monster || query.StageType != 0 && query.StageType != b.stage {
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out []TeamRanking
	for _, key := range keys {
		board := t.boards[key]
		own := board.own[query.OwnerID]
		selected := slices.Clone(board.order[:min(query.Limit, len(board.order))])
		if own != "" && !slices.Contains(selected, own) {
			selected = append(selected, own)
		}
		for _, team := range selected {
			run := board.best[team]
			position := sort.Search(len(board.order), func(i int) bool { return !lessTeam(board, board.order[i], team) })
			rank := uint64(position + 1)
			out = append(out, TeamRanking{ResultID: run.ResultID, InstanceID: run.InstanceID, EventUID: run.EventUID, Monster: run.Monster, StageType: run.StageType, Rank: rank, Milliseconds: run.Milliseconds, TopPercent: float64(rank) * 100 / float64(len(board.order)), Own: own != "" && own == team, Members: slices.Clone(run.Members)})
		}
	}
	return out, nil
}
