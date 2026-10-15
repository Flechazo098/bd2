package ranking

import (
	"errors"
	"slices"
	"sort"

	"bd2server/internal/server/domain/battle/pvp"
)

func copyPublished(snapshot pvp.PublishedSnapshot) pvp.PublishedSnapshot {
	snapshot.Profile.Guild = slices.Clone(snapshot.Profile.Guild)
	snapshot.Snapshot.FullDeck = slices.Clone(snapshot.Snapshot.FullDeck)
	snapshot.Snapshot.Contents = slices.Clone(snapshot.Snapshot.Contents)
	for i := range snapshot.Snapshot.Contents {
		snapshot.Snapshot.Contents[i] = slices.Clone(snapshot.Snapshot.Contents[i])
	}
	return snapshot
}

func (p *PVP) publishedRank(board *pvpBoard, owner string) pvp.PublishedSnapshot {
	row := board.Members[owner]
	snapshot := row.Defense
	snapshot.VP = p.score(row)
	snapshot.Rank = 0
	snapshot.TopPercent = 0
	if rank := p.rank(board, owner); rank != 0 {
		snapshot.Rank = rank
		snapshot.TopPercent = float64(rank) * 100 / float64(len(board.Order))
	}
	return snapshot
}

func (p *PVP) Candidates(query pvp.CandidateQuery) ([]pvp.PublishedSnapshot, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if query.Owner == "" || query.Season == 0 || query.Limit < 1 || query.Limit > 256 {
		return nil, errors.New("invalid PVP candidate query")
	}
	board := p.board(query.Mode, query.Season)
	if board.Frozen {
		return nil, nil
	}
	excluded := map[string]bool{query.Owner: true}
	for _, id := range query.Exclude {
		excluded[id] = true
	}
	// Walk the sorted score index from its insertion point; only excluded or
	// selected rows are visited, and snapshots are copied only after selection.
	at := sort.Search(len(board.Candidates), func(i int) bool { return p.score(board.Members[board.Candidates[i]]) <= query.VP })
	left, right := at-1, at
	var candidates []pvp.PublishedSnapshot
	distance := func(owner string) uint64 {
		vp := p.score(board.Members[owner])
		if vp > query.VP {
			return vp - query.VP
		}
		return query.VP - vp
	}
	for len(candidates) < query.Limit && (left >= 0 || right < len(board.Candidates)) {
		var owner string
		if right >= len(board.Candidates) || left >= 0 && (distance(board.Candidates[left]) < distance(board.Candidates[right]) || distance(board.Candidates[left]) == distance(board.Candidates[right]) && board.Members[board.Candidates[left]].Defense.Profile.OwnerIndex < board.Members[board.Candidates[right]].Defense.Profile.OwnerIndex) {
			owner = board.Candidates[left]
			left--
		} else {
			owner = board.Candidates[right]
			right++
		}
		if excluded[owner] {
			continue
		}
		candidates = append(candidates, copyPublished(p.publishedRank(board, owner)))
	}
	return candidates, nil
}

func (p *PVP) Ranking(query pvp.RankQuery) ([]pvp.RankEntry, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if query.Season == 0 || query.Start < 0 || query.Limit < 1 || query.Limit > 256 {
		return nil, errors.New("invalid PVP ranking query")
	}
	board := p.board(query.Mode, query.Season)
	var owners []string
	if query.Owner != "" {
		if _, ok := board.Members[query.Owner]; ok {
			owners = []string{query.Owner}
		}
	} else if query.OwnerIndex != 0 {
		if owner := board.Owners[query.OwnerIndex]; owner != "" {
			owners = []string{owner}
		}
	} else if query.Start < len(board.Order) {
		owners = board.Order[query.Start:min(len(board.Order), query.Start+query.Limit)]
	}
	out := make([]pvp.RankEntry, 0, len(owners))
	for _, owner := range owners {
		row := board.Members[owner]
		out = append(out, pvp.RankEntry{PublishedSnapshot: copyPublished(p.publishedRank(board, owner)), Wins: row.Progress.Wins + row.Progress.DefenseWins, Losses: row.Progress.Losses + row.Progress.DefenseLosses})
	}
	return out, nil
}

func (p *PVP) StatueRanking(mode pvp.Mode) (uint64, []pvp.RankEntry, error) {
	p.mu.RLock()
	var season uint64
	for _, configured := range p.seasons {
		if configured.Mode == mode && p.board(mode, configured.ID).Frozen && configured.ID > season {
			season = configured.ID
		}
	}
	p.mu.RUnlock()
	if season == 0 {
		return 0, nil, nil
	}
	rows, err := p.Ranking(pvp.RankQuery{Mode: mode, Season: season, Limit: 3})
	return season, rows, err
}

func (p *PVP) Personal(query pvp.ProgressQuery) (pvp.PersonalProgress, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if query.Owner == "" || query.Season == 0 || len(query.PendingIDs) > 256 {
		return pvp.PersonalProgress{}, errors.New("invalid PVP personal query")
	}
	row := p.board(query.Mode, query.Season).Members[query.Owner]
	progress := row.Progress
	progress.AppliedIDs = nil
	for _, id := range query.PendingIDs {
		if row.Applied[id] {
			progress.AppliedIDs = append(progress.AppliedIDs, id)
		}
	}
	return progress, nil
}

func (p *PVP) History(query pvp.HistoryQuery) ([]pvp.BattleSettled, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if query.Owner == "" || query.Season == 0 && query.BattleIndex == 0 || query.Start < 0 || query.Limit < 1 || query.Limit > 256 {
		return nil, errors.New("invalid PVP history query")
	}
	var rows []pvp.BattleSettled
	if query.Season != 0 {
		rows = p.board(query.Mode, query.Season).Members[query.Owner].History
	} else {
		for _, board := range p.boards {
			for _, row := range board.Members[query.Owner].History {
				if row.Mode == query.Mode {
					rows = append(rows, row)
				}
			}
		}
	}
	var out []pvp.BattleSettled
	for _, row := range slices.Backward(rows) {

		if query.BattleIndex != 0 && row.BattleIndex != query.BattleIndex {
			continue
		}
		if query.Start > 0 {
			query.Start--
			continue
		}
		row.Attacker = copyPublished(row.Attacker)
		row.Defender = copyPublished(row.Defender)
		row.Seeds = slices.Clone(row.Seeds)
		out = append(out, row)
		if len(out) == query.Limit {
			break
		}
	}
	return out, nil
}

func (p *PVP) Entitlements(mode pvp.Mode, owner string, limit int) ([]pvp.SeasonEntitlement, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if owner == "" || limit < 1 || limit > 256 {
		return nil, errors.New("invalid PVP entitlement query")
	}
	var out []pvp.SeasonEntitlement
	for _, board := range p.boards {
		if !board.Frozen {
			continue
		}
		for _, entitlement := range board.Entitlements[owner] {
			if entitlement.Mode == mode && !board.Claimed[entitlement.ID] {
				entitlement.Rewards = slices.Clone(entitlement.Rewards)
				out = append(out, entitlement)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Season < out[j].Season })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
