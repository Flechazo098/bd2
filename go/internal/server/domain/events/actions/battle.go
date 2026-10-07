package eventactions

import (
	"bd2server/internal/server/domain/events"
	"crypto/sha256"
	"encoding/hex"
)

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
