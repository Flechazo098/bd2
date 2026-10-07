package hunting

import (
	"bd2server/internal/server/design/gamedata"
	"time"
)

type dispatchReceipt struct {
	Request, Response []byte
	Code              int
}
type dispatchJob struct {
	Group, ID, Count, Start, End, Free, Bonus uint64
	Rewards                                   []gamedata.BattleReward
	Runs                                      [][]gamedata.BattleReward
}
type dispatchState struct {
	Version  string                     `json:"version"`
	Jobs     map[string]dispatchJob     `json:"jobs"`
	Receipts map[string]dispatchReceipt `json:"receipts"`
}

func (s *Service) AttachDispatchEligibility(check func(*gamedata.DispatchDesign) error) {

	s.dispatchEligibility = check
}

func dispatchPlayed(j dispatchJob, seconds uint64) uint64 {
	if seconds == 0 {
		return j.Count
	}
	now := uint64(time.Now().UnixMilli())
	if now <= j.Start {
		return 0
	}
	n := min((now-j.Start)/(seconds*1000), j.Count)
	return n
}

func dispatchCompletedRewards(j dispatchJob, n uint64) []gamedata.BattleReward {
	var out []gamedata.BattleReward
	for i := uint64(0); i < n && i < uint64(len(j.Runs)); i++ {
		out = append(out, j.Runs[i]...)
	}
	return out
}
