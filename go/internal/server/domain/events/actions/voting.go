package eventactions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/events"
	"slices"
	"sort"
)

func (s *Service) voteEvent() (events.Schedule, gamedata.EventActionRow, bool) {
	v, ok := s.find(23)
	if !ok {
		return v, gamedata.EventActionRow{}, false
	}
	r, ok := s.design.Row("VotingEventTable", 6, v.ID)
	return v, r, ok
}
func (s *Service) rounds(v events.Schedule, r gamedata.EventActionRow) (uint64, []gamedata.EventActionRow) {
	var rows []gamedata.EventActionRow
	current := uint64(0)
	for _, x := range s.design.Tables["VotingRoundTable"] {
		if x.V(2) == r.V(14) {
			rows = append(rows, x)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].V(3) < rows[j].V(3) })
	for _, x := range rows {
		if s.now().UnixMilli() >= v.Start+int64(x.V(4))*86400000 {
			current = x.V(3)
		}
	}
	return current, rows
}
func (s *Service) candidates(r gamedata.EventActionRow) []uint64 {
	var ids []uint64
	for _, x := range s.design.Tables["VotingCandidateTable"] {
		if x.V(1) == r.V(10) {
			ids = append(ids, x.V(2))
		}
	}
	slices.Sort(ids)
	return ids
}
func contains(ids []uint64, id uint64) bool {
	return slices.Contains(ids, id)
}
