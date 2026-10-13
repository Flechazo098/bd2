package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
)

func (s *Service) actionMissions(ctx command.Context, req []byte, a Run, result NativeResult, st *snapshot, key string) ([]byte, error) {
	if a.Native == nil {
		return nil, fmt.Errorf("eventplay: action mission lacks authoritative room run")
	}
	for field := 8; field <= 18; field++ {
		if num(req, field) > math.MaxInt32 {
			return nil, fmt.Errorf("eventplay: invalid action mission counter")
		}
	}
	if num(req, 24) > math.MaxInt32 {
		return nil, fmt.Errorf("eventplay: invalid received damage")
	}
	monster, err := s.design.Row("ActionGameCharTable", 14, a.Char)
	if err != nil {
		return nil, err
	}
	if num(req, 17) > num(req, 10) || num(req, 17) > num(req, 18) {
		return nil, fmt.Errorf("eventplay: rage groggy count contradicts report")
	}
	profile, err := s.arcadeProfile(ctx)
	if err != nil {
		return nil, err
	}
	character := uint64(0)
	err = wire.Walk(result.Evidence, func(f wire.Field) error {
		if f.Number == 2 && f.Type == 2 && num(f.Value, 1) == profile.Owner {
			if character != 0 {
				return fmt.Errorf("eventplay: duplicate native action member")
			}
			character = num(f.Value, 2)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ledger := playRecord(st, a.UID, a.Game, 0, 0, a.Family)
	var out []byte
	var rewards []gamedata.BattleReward
	for _, row := range s.design.Tables["ActionGameMissionTable"] {
		id, condition, threshold, target := num(row, 4), num(row, 3), num(row, 1), num(row, 2)
		identity := fmt.Sprintf("mission:%d", id)
		if ledger.Clears[identity] || num(row, 7) != num(monster, 16) || !result.Clear {
			continue
		}
		if condition != 1 && condition != 11 && target != a.Char {
			continue
		}
		if condition >= 6 && condition <= 8 {
			binding := s.actionMissionCharacters[condition]
			if binding == 0 {
				return nil, fmt.Errorf("eventplay: action mission character binding missing")
			}
			if character != binding {
				continue
			}
		}
		earned := false
		switch condition {
		case 1:
			earned = threshold == a.Char
		case 2:
			earned = threshold <= math.MaxUint64/1000 && result.Score <= threshold*1000
		case 3:
			earned = num(req, 8) <= threshold
		case 4:
			earned = num(req, 9) >= threshold
		case 5:
			earned = num(req, 10) >= threshold
		case 6:
			earned = num(req, 11) >= threshold
		case 7:
			earned = num(req, 12) >= threshold
		case 8:
			earned = num(req, 13) >= threshold
		case 9:
			earned = num(req, 14) >= threshold
		case 10:
			earned = num(req, 15) >= threshold
		case 11:
			earned = threshold == a.Char && num(req, 16) <= target
		case 12:
			earned = num(req, 17) >= threshold
		default:
			return nil, fmt.Errorf("eventplay: unsupported action mission condition %d", condition)
		}
		if !earned {
			continue
		}
		ledger.Clears[identity] = true
		out = wire.AppendVarint(out, 2, id)
		rr, e := gamedata.EventPlayRewards(row, 10, 9, 8)
		if e != nil {
			return nil, e
		}
		rewards = append(rewards, rr...)
	}
	st.Records[recKey(a.UID, 0, 0, a.Family)] = ledger
	if len(rewards) > 0 {
		bundle, err := s.grant(ctx, "eventplay:"+key+":missions", rewards)
		if err != nil {
			return nil, err
		}
		out = wire.AppendBytes(out, 3, bundle)
	}

	return out, nil
}
