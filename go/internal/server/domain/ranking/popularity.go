package ranking

import "bd2server/internal/server/domain/battle/pvp"

func (p *PVP) PopularityRank(snapshot pvp.PublishedSnapshot) (uint64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if board := p.boards[pvpBoardKey(snapshot.Mode, snapshot.Season)]; board != nil {
		if row, ok := board.Members[snapshot.Profile.OwnerID]; ok {
			snapshot.VP = p.score(row)
			snapshot.Rank = p.rank(board, snapshot.Profile.OwnerID)
		}
	}
	rank, e := p.design.MirrorRank(snapshot.Season, snapshot.VP, snapshot.Rank)
	return rank.ID, e
}
