package gamedata

import (
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
)

type EventBattleChallenge struct {
	Type, Value1, Value2 uint64
	Reward               BattleReward
}
type EventBattleChallenges map[uint64][]EventBattleChallenge

func LoadEventBattleChallenges(root, version string) (EventBattleChallenges, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	rows, e := db.Query("SELECT id,ProtoBuf FROM BattleDeckTable")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := EventBattleChallenges{}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		fields := map[int][]uint64{}
		for _, n := range []int{6, 7, 8, 9, 10, 11} {
			v, e := packedInts(raw, n)
			if e != nil {
				return nil, e
			}
			fields[n] = v
		}
		n := len(fields[6])
		if n == 0 {
			continue
		}
		for _, f := range []int{7, 8, 9, 10, 11} {
			if len(fields[f]) != n {
				return nil, fmt.Errorf("gamedata: event challenge arrays mismatch deck%d", id)
			}
		}
		for i, t := range fields[6] {
			if t < 1 || t > 8 {
				return nil, fmt.Errorf("gamedata: unsupported challenge type%d", t)
			}
			out[id] = append(out[id], EventBattleChallenge{Type: t, Value1: fields[7][i], Value2: fields[8][i], Reward: BattleReward{Type: fields[11][i], ID: fields[10][i], Count: fields[9][i]}})
		}
	}
	return out, rows.Err()
}

// VerifySubmittedChallenges uses the client's simulation reports. Conditions
// absent from BattleEnd (turn count/used skill/team elements) are accepted as
// explicit local simulation attestations after static index/type validation.
func VerifySubmittedChallenges(req []byte, definitions []EventBattleChallenge) ([]uint64, error) {
	var indexes []uint64
	seen := map[uint64]bool{}
	e := wire.Walk(req, func(f wire.Field) error {
		if f.Number != 5 {
			return nil
		}
		var v []uint64
		var e error
		if f.Type == 0 { //nolint:staticcheck // QF1003
			n, k := binary.Uvarint(f.Value)
			if k <= 0 {
				return wire.ErrMalformed
			}
			v = []uint64{n}
		} else if f.Type == 2 {
			v, e = packedInts(wire.AppendBytes(nil, 5, f.Value), 5)
		} else {
			return wire.ErrMalformed
		}
		if e != nil {
			return e
		}
		for _, i := range v {
			if i >= uint64(len(definitions)) || seen[i] {
				return fmt.Errorf("gamedata: invalid challenge index")
			}
			seen[i] = true
			indexes = append(indexes, i)
		}
		return nil
	})
	return indexes, e
}
