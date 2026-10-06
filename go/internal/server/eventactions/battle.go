package eventactions

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Tactics uses the client's custom battle stage fields 8/9 and has fixed
// tutorial decks. Character health and ordinary pack rewards do not apply.
func (s *Service) HandlesBattle(mode uint64) bool { return mode == 29 }
func (s *Service) EnterBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := battleDigest(req)
	if r, ok := s.state.Receipts[s.session+":enter:"+receipt]; ok {
		if r.Digest != digest {
			return nil, errors.New("eventactions: changed battle enter replay")
		}
		return r.Reply, nil
	}
	before, _ := json.Marshal(s.state)
	if val(req, 5) != 29 {
		return nil, errors.New("eventactions: wrong tactics battle mode")
	}
	group, stage, deck := val(req, 8), val(req, 9), val(req, 4)
	row, ok := s.row("TacticsBingoTable", 4, 5, group, stage)
	if !ok || row.V(2) != deck {
		return nil, errors.New("eventactions: invalid tactics stage/deck")
	}
	var uid uint64
	for _, v := range s.registry.List() {
		if v.Type != 20 || !s.active(v) {
			continue
		}
		g, ok := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
		if ok && g.V(1) == group {
			uid = v.UID
			break
		}
	}
	if uid == 0 {
		return nil, errors.New("eventactions: tactics event inactive")
	}
	if _, err := s.tacticsBlueParty(row.V(1)); err != nil {
		return nil, err
	}
	// The client sends BattleEnter before the new scene's DeckSave. Its
	// battle reset clears the prior stage's virtual party; do the same here.
	s.state.Deck = nil
	s.state.BattleUID, s.state.BattleStage, s.state.BattleDeck = uid, stage, deck
	s.state.Receipts[s.session+":enter:"+receipt] = receiptRecord(nil, digest)
	if e := s.save(); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return nil, e
	}
	return nil, nil
}
func (s *Service) CompleteBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.state.Receipts[s.session+":battle:"+receipt]; ok {
		if r.Digest != battleDigest(req) {
			return nil, errors.New("eventactions: changed battle completion replay")
		}
		return r.Reply, nil
	}
	before, _ := json.Marshal(s.state)
	uid, stage := s.state.BattleUID, s.state.BattleStage
	if uid == 0 || stage == 0 {
		return nil, errors.New("eventactions: tactics battle not entered")
	}
	if _, e := s.resolve(uid, 20); e != nil {
		return nil, e
	}
	if val(req, 2) == 1 && !contains(s.state.Tactics[uid], stage) {
		priorLines := s.tacticsLines(uid)
		s.state.Tactics[uid] = append(s.state.Tactics[uid], stage)
		if s.progress != nil {
			if e := s.progress(346, stage, 1); e != nil {
				_ = json.Unmarshal(before, &s.state)
				return nil, e
			}
			lines := s.tacticsLines(uid)
			if lines > priorLines {
				if e := s.progress(347, 0, lines-priorLines); e != nil {
					_ = json.Unmarshal(before, &s.state)
					return nil, e
				}
			}
			v, _ := s.registry.Resolve(uid)
			g, _ := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
			n := uint64(5) - g.V(4)
			if uint64(len(s.state.Tactics[uid])) == n*n {
				if e := s.progress(348, 0, 1); e != nil {
					_ = json.Unmarshal(before, &s.state)
					return nil, e
				}
			}
		}
	}
	var out []byte
	for _, id := range s.state.Tactics[uid] {
		out = wire.AppendVarint(out, 29, id)
	}
	s.state.BattleUID, s.state.BattleStage, s.state.BattleDeck = 0, 0, 0
	s.state.Receipts[s.session+":battle:"+receipt] = receiptRecord(out, battleDigest(req))
	if e := s.save(); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return nil, e
	}
	return out, nil
}

func (s *Service) tacticsLines(uid uint64) uint64 {
	v, _ := s.registry.Resolve(uid)
	g, ok := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
	if !ok || g.V(4) > 2 {
		return 0
	}
	n := uint64(5) - g.V(4)
	clear := s.state.Tactics[uid]
	var lines uint64
	for i := range n {
		row, col := true, true
		for j := range n {
			row = row && contains(clear, i*n+j+1)
			col = col && contains(clear, j*n+i+1)
		}
		if row {
			lines++
		}
		if col {
			lines++
		}
	}
	a, b := true, true
	for i := range n {
		a = a && contains(clear, i*n+i+1)
		b = b && contains(clear, i*n+(n-i))
	}
	if a {
		lines++
	}
	if b {
		lines++
	}
	return lines
}

func (s *Service) AssociatedMissionGroup(v events.Schedule) uint64 {
	if v.Type == 20 {
		r, ok := s.design.Row("TacticsBingoGroupTable", 3, v.ID)
		if ok {
			return r.V(2)
		}
	}
	if v.Type == 23 {
		r, ok := s.design.Row("VotingEventTable", 6, v.ID)
		if ok {
			return r.V(12)
		}
	}
	return 0
}
func receiptRecord(b []byte, digest string) receipt { return receipt{Digest: digest, Reply: b} }
func battleDigest(b []byte) string                  { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
