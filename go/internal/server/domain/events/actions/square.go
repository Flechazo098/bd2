package eventactions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type squareReceipt struct {
	Digest [32]byte
	Body   []byte
}
type squareReceipts struct {
	Session string
	High    uint64
	Recent  map[uint64]squareReceipt
}

func (s *Service) handleSquare(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	code := 0
	switch path {
	case "/SquareRewardInfo":
		code = 525
	case "/SquareReward":
		code = 526
	case "/StatueHitInfo":
		code = 556
	case "/StatueHit":
		code = 557
	default:
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return fail(fmt.Errorf("eventactions: invalid square sequence"))
	}
	if err = wire.Walk(req, func(wire.Field) error { return nil }); err != nil {
		return fail(err)
	}
	if path == "/SquareRewardInfo" || path == "/StatueHitInfo" {
		out, e := s.square(ctx, path, req, "")
		return code, out, true, e
	}
	ledger := squareReceipts{Session: ctx.SessionID, Recent: map[uint64]squareReceipt{}}
	raw, found, err := ctx.State.LoadEntry("missions", "square_requests", path)
	if err != nil {
		return fail(err)
	}
	if found {
		if err = json.Unmarshal(raw, &ledger); err != nil {
			return fail(err)
		}
		if ledger.Recent == nil {
			return fail(fmt.Errorf("eventactions: invalid square receipt ledger"))
		}
	}
	if ledger.Session != ctx.SessionID {
		ledger = squareReceipts{Session: ctx.SessionID, Recent: map[uint64]squareReceipt{}}
	}
	digest := sha256.Sum256(req)
	if prior, ok := ledger.Recent[seq]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("eventactions: square request changed"))
		}
		return code, prior.Body, true, nil
	}
	if seq <= ledger.High {
		return fail(fmt.Errorf("eventactions: stale square request"))
	}
	before, err := json.Marshal(s.state)
	if err != nil {
		return fail(err)
	}
	out, err := s.square(ctx, path, req, ctx.SessionID+":"+strconv.FormatUint(seq, 10))
	if err != nil {
		_ = json.Unmarshal(before, &s.state)
		return fail(err)
	}
	ledger.High = seq
	ledger.Recent[seq] = squareReceipt{Digest: digest, Body: out}
	for old := range ledger.Recent {
		if old < seq && seq-old >= 128 {
			delete(ledger.Recent, old)
		}
	}
	raw, err = json.Marshal(ledger)
	if err == nil {
		err = ctx.State.PutEntry("missions", "square_requests", path, raw)
	}
	if err == nil && path == "/SquareReward" {
		err = s.save(ctx)
	}
	if err != nil {
		_ = json.Unmarshal(before, &s.state)
		return fail(err)
	}
	return code, out, true, nil
}

type StatueHits struct{ ID, Count uint64 }

func (s *Service) AttachStatueHits(view func(command.Context, uint64) (uint64, uint64, error)) {
	s.hitTotals = view
}

func squareSample(limit uint64) (uint64, error) {
	if limit == 0 {
		return 0, fmt.Errorf("eventactions: empty square reward pool")
	}
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64(limit))
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}

func (s *Service) square(ctx command.Context, path string, req []byte, identity string) ([]byte, error) {
	if path == "/SquareRewardInfo" || path == "/SquareReward" {
		claim := "square:" + s.now().UTC().Format("2006-01-02")
		if path == "/SquareRewardInfo" {
			if s.state.Claims[claim] {
				return nil, nil
			}
			return wire.AppendVarint(nil, 1, 1), nil
		}
		if s.state.Claims[claim] {
			return nil, fmt.Errorf("eventactions: square daily reward already claimed")
		}
		rows := s.design.Tables["SquareRewardTable"]
		var total uint64
		for _, row := range rows {
			if row.V(7) > math.MaxUint64-total || row.V(4) > row.V(3) {
				return nil, fmt.Errorf("eventactions: invalid square reward design")
			}
			total += row.V(7)
		}
		pick, err := squareSample(total)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if pick >= row.V(7) {
				pick -= row.V(7)
				continue
			}
			amount, e := squareSample(row.V(3) - row.V(4) + 1)
			if e != nil {
				return nil, e
			}
			amount += row.V(4)
			bundle, e := s.economy.Apply(ctx, "square:"+claim, nil, []gamedata.Reward{{Type: row.V(6), ID: row.V(5), Count: amount}})
			if e != nil {
				return nil, e
			}
			for k := range s.state.Claims {
				if len(k) > 7 && k[:7] == "square:" {
					delete(s.state.Claims, k)
				}
			}
			s.state.Claims[claim] = true
			out := wire.AppendVarint(nil, 1, row.V(1))
			return wire.AppendBytes(out, 2, bundle), nil
		}
		return nil, fmt.Errorf("eventactions: square reward selection missing")
	}
	valid := map[uint64]bool{}
	for _, row := range s.design.Tables["FieldStatueEventTable"] {
		valid[row.V(3)] = true
	}
	if path == "/StatueHit" {
		counts := map[uint64]uint64{}
		err := wire.Walk(req, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			id, _, e := wire.Varint(f.Value, 1)
			if e != nil {
				return e
			}
			count, _, e := wire.Varint(f.Value, 2)
			if e != nil || !valid[id] || count == 0 || count > math.MaxInt32 || counts[id] != 0 {
				return fmt.Errorf("eventactions: invalid statue hit increment")
			}
			counts[id] = count
			return nil
		})
		if err != nil {
			return nil, err
		}
		if len(counts) == 0 {
			return nil, fmt.Errorf("eventactions: empty statue hit request")
		}
		for id, count := range counts {
			personal, e := personalHits(ctx, id)
			if e != nil {
				return nil, e
			}
			if count > math.MaxInt64-personal {
				return nil, fmt.Errorf("eventactions: statue hit overflow")
			}
			personal += count
			b, e := json.Marshal(personal)
			if e != nil {
				return nil, e
			}
			if e = ctx.State.PutEntry("missions", "statue_hits", strconv.FormatUint(id, 10), b); e != nil {
				return nil, e
			}
			b, e = json.Marshal(StatueHits{ID: id, Count: personal})
			if e != nil {
				return nil, e
			}
			if e = ctx.Publish(command.Event{ID: "statue:" + ctx.AccountID + ":" + identity + ":" + strconv.FormatUint(id, 10), Topic: "square.statue_hits", Payload: b}); e != nil {
				return nil, e
			}
		}
		return nil, nil
	}
	var out []byte
	for _, row := range s.design.Tables["FieldStatueEventTable"] {
		id := row.V(3)
		personal, err := personalHits(ctx, id)
		if err != nil {
			return nil, err
		}
		total := personal
		if s.hitTotals != nil {
			shared, ack, e := s.hitTotals(ctx, id)
			if e != nil {
				return nil, e
			}
			if ack > personal || shared < ack || shared-ack > math.MaxInt64-personal {
				return nil, fmt.Errorf("eventactions: invalid shared statue progress")
			}
			total = shared - ack + personal
		}
		b := wire.AppendVarint(nil, 1, id)
		b = wire.AppendVarint(b, 2, total)
		b = wire.AppendVarint(b, 3, personal)
		out = wire.AppendBytes(out, 1, b)
	}
	return out, nil
}

func personalHits(ctx command.Context, id uint64) (uint64, error) {
	b, found, err := ctx.State.LoadEntry("missions", "statue_hits", strconv.FormatUint(id, 10))
	if err != nil || !found {
		return 0, err
	}
	var count uint64
	err = json.Unmarshal(b, &count)
	if err == nil && count > math.MaxInt64 {
		err = fmt.Errorf("eventactions: invalid saved statue hits")
	}
	return count, err
}
