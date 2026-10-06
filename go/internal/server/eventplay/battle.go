package eventplay

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Service) HandlesBattle(mode uint64) bool { return mode == 17 }
func (s *Service) EnterBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if num(req, 5) != 17 || receipt == "" {
		return nil, fmt.Errorf("eventplay: invalid event battle")
	}
	group, id, deck := num(req, 8), num(req, 9), num(req, 4)
	var row []byte
	for _, r := range s.design.Rows("PackEventBattleTable", 4, group) {
		if num(r, 5) == id && num(r, 1) == deck {
			row = r
			break
		}
	}
	if row == nil {
		return nil, fmt.Errorf("eventplay: event battle deck/stage mismatch")
	}
	var uid uint64
	for _, c := range s.registry.List() {
		if c.ID == group && (c.Type == 9 || c.Type == 8) && s.now().UnixMilli() >= c.Start && s.now().UnixMilli() < c.End {
			uid = c.UID
			break
		}
	}
	if uid == 0 {
		return nil, fmt.Errorf("eventplay: event battle schedule unavailable")
	}
	previous := uint64(0)
	for _, r := range s.design.Rows("PackEventBattleTable", 4, group) {
		stage := num(r, 5)
		if stage < id && stage > previous {
			previous = stage
		}
	}
	if previous != 0 && !s.state.Stories[fmt.Sprintf("battle:%d:%d:%d", uid, group, previous)] {
		return nil, fmt.Errorf("eventplay: previous event battle not cleared")
	}
	if validator, ok := s.economy.(interface{ CanApply([]gamedata.Reward) error }); ok {
		cost := num(row, 3)
		if cost > 0 {
			if e := validator.CanApply([]gamedata.Reward{{Type: 30, Count: cost}}); e != nil {
				return nil, e
			}
		}
	}
	payload, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(payload, &next)
	next.Runs["battle:"+receipt] = Run{UID: uid, Game: group, Stage: id, Mode: 17, Started: uint64(s.now().UnixMilli()), Family: "Battle", Session: strings.Split(receipt, ":")[0]}
	payload, _ = json.Marshal(next)
	if e := s.store.Save("eventplay", payload); e != nil {
		return nil, e
	}
	s.state = next
	return nil, nil
}
func (s *Service) CompleteBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "eventbattle:" + receipt
	if r, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return nil, fmt.Errorf("eventplay: changed battle retry")
		}
		return r.Body, nil
	}
	a, ok := s.state.Runs["battle:"+receipt]
	if !ok {
		return nil, fmt.Errorf("eventplay: no entered event battle")
	}
	var row []byte
	for _, r := range s.design.Rows("PackEventBattleTable", 4, a.Game) {
		if num(r, 5) == a.Stage {
			row = r
			break
		}
	}
	if row == nil {
		return nil, fmt.Errorf("eventplay: saved battle design missing")
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(raw, &next)
	var out []byte
	if num(req, 2) == 1 {
		rs, e := gamedata.EventPlayRewards(row, 9, 10, 8)
		if e != nil {
			return nil, e
		}
		definitions := s.battleChallenges[num(row, 1)]
		claims, e := gamedata.VerifySubmittedChallenges(req, definitions)
		if e != nil {
			return nil, e
		}
		for _, index := range claims {
			marker := fmt.Sprintf("challenge:%d:%d:%d:%d", a.UID, a.Game, a.Stage, index)
			if !next.Stories[marker] {
				rs = append(rs, definitions[index].Reward)
				next.Stories[marker] = true
			}
		}
		rewards := []gamedata.Reward{}
		for _, r := range rs {
			rewards = append(rewards, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		}
		cost := num(row, 3)
		var costs []gamedata.Reward
		if cost > 0 {
			costs = []gamedata.Reward{{Type: 30, Count: cost}}
		}
		bundle, e := s.economy.Apply(key, costs, rewards)
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 5, bundle)
		info := wire.AppendVarint(nil, 1, a.UID)
		info = wire.AppendVarint(info, 2, a.Game)
		info = wire.AppendVarint(info, 3, a.Stage)
		for i := range definitions {
			if next.Stories[fmt.Sprintf("challenge:%d:%d:%d:%d", a.UID, a.Game, a.Stage, i)] {
				info = wire.AppendVarint(info, 4, uint64(i))
			}
		}
		out = wire.AppendBytes(out, 16, info)
		next.Stories[fmt.Sprintf("battle:%d:%d:%d", a.UID, a.Game, a.Stage)] = true
	}
	delete(next.Runs, "battle:"+receipt)
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	raw, _ = json.Marshal(next)
	if e := s.store.Save("eventplay", raw); e != nil {
		return nil, e
	}
	s.state = next
	return out, nil
}

func (s *Service) AttachBattleChallenges(d gamedata.EventBattleChallenges) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.battleChallenges = d
}

func (s *Service) BattleChallengeIndexes(uid, group, stage uint64) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.battleChallengeIndexesLocked(uid, group, stage)
}
func (s *Service) battleChallengeIndexesLocked(uid, group, stage uint64) []uint64 {
	var deck uint64
	for _, row := range s.design.Rows("PackEventBattleTable", 4, group) {
		if num(row, 5) == stage {
			deck = num(row, 1)
			break
		}
	}
	var out []uint64
	for i := range s.battleChallenges[deck] {
		if s.state.Stories[fmt.Sprintf("challenge:%d:%d:%d:%d", uid, group, stage, i)] {
			out = append(out, uint64(i))
		}
	}
	return out
}
