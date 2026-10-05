package eventplay

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"fmt"
	"math"
	"strings"
)

func (s *Service) validateStage(f string, id, mode uint64) error {
	switch f {
	case "Rhythm":
		r, e := s.design.Row("RhythmGameMusicTable", 5, id)
		if e != nil {
			return e
		}
		if mode > 2 {
			return fmt.Errorf("eventplay: unknown rhythm mode")
		}
		_ = r
	case "Hopscotch":
		_, e := s.design.Row("HopscotchStageTable", 6, id)
		return e
	case "Sichuan":
		_, e := s.design.Row("SichuanStageTable", 2, id)
		return e
	case "Survival":
		if len(s.design.Tables["FieldMiniGameSurvivalTable"]) == 0 {
			return fmt.Errorf("eventplay: survival design missing")
		}
	case "Action":
		if len(s.design.Tables["ActionGameDefaultTable"]) == 0 {
			return fmt.Errorf("eventplay: action design missing")
		}
	}
	return nil
}
func (s *Service) endScore(f string, req []byte, a Run) (uint64, error) {
	var score uint64
	switch f {
	case "Run":
		score = num(req, 3)
	case "Field":
		score = a.Score
	case "Action":
		score = num(req, 7)
	case "Rhythm":
		score = num(req, 4)
		r, e := s.design.Row("RhythmGameMusicTable", 5, a.Stage)
		if e != nil {
			return 0, e
		}
		maxField := 8
		if a.Mode == 1 {
			maxField = 2
		} else if a.Mode == 2 {
			maxField = 1
		}
		if score > num(r, maxField) {
			return 0, fmt.Errorf("eventplay: rhythm score exceeds static maximum")
		}
		judgments := uint64(0)
		err := wire.Walk(req, func(f wire.Field) error {
			if f.Number == 3 {
				if f.Type != 2 {
					return wire.ErrMalformed
				}
				typ, count := num(f.Value, 1), num(f.Value, 2)
				if typ > 10 || count > math.MaxInt32 {
					return fmt.Errorf("eventplay: invalid note judgment")
				}
				judgments += count
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		noteField := 10
		if a.Mode > 0 {
			noteField = 4
		}
		if judgments > num(r, noteField) {
			return 0, fmt.Errorf("eventplay: judgment count exceeds designed notes")
		}
	case "Sichuan":
		score = num(req, 4)
	case "Hopscotch":
		score = num(req, 2)
		if score > 10000 {
			return 0, fmt.Errorf("eventplay: captured area exceeds 100 percent")
		}
	case "Survival":
		score = num(req, 3)
		d := s.design.Tables["FieldMiniGameSurvivalTable"][0]
		if score > num(d, 24) {
			return 0, fmt.Errorf("eventplay: survival time exceeds design")
		}
	case "Defense":
		score = num(req, 3)
	}
	if score > math.MaxInt32 {
		return 0, fmt.Errorf("eventplay: submitted score exceeds protocol range")
	}
	return score, nil
}
func (s *Service) scoreRewards(game, score uint64) (uint64, []gamedata.BattleReward, error) {
	d, e := s.design.Row("PackEventMiniGameTable", 8, game)
	if e != nil {
		return 0, nil, e
	}
	return s.design.ScoreRewards(num(d, 10), score)
}
func (s *Service) progress(path string, req []byte, rk string, next *snapshot, key string) ([]byte, error) {
	a, ok := next.Runs[rk]
	if !ok {
		return nil, fmt.Errorf("eventplay: no active run")
	}
	var out []byte
	switch path {
	case "/MiniGameFieldScore":
		d, e := s.design.Field(a.Game)
		if e != nil {
			return nil, e
		}
		ids, e := list(req, 3)
		if e != nil || len(ids) == 0 {
			return nil, fmt.Errorf("eventplay: missing field objects")
		}
		for _, id := range ids {
			v, ok := d.Objects[id]
			if !ok || has(a.Objects, id) {
				return nil, fmt.Errorf("eventplay: object invalid or already scored")
			}
			a.Objects = append(a.Objects, id)
			if v.Type == 1 {
				a.Score += v.Point
			} else if v.Type == 2 {
				a.HP += v.Point
			}
		}
		out = wire.AppendVarint(nil, 1, a.Score)
		out = wire.AppendVarint(out, 2, a.HP)
	case "/MiniGameSurvivalPlay":
		exp, coin, e := s.survivalProgress(req, &a)
		if e != nil {
			return nil, e
		}
		if s.onProgress != nil {
			total := uint64(0)
			for id, count := range a.Killed {
				previous := next.Runs[rk].Killed[id]
				if count > previous {
					total += count - previous
				}
			}
			if total > 0 {
				if e = s.onProgress(319, a.Game, total); e != nil {
					return nil, e
				}
			}
		}
		out = wire.AppendVarint(nil, 1, coin)
		out = wire.AppendVarint(out, 2, exp)
	case "/MiniGameSurvivalSkillSelectList":
		var e error
		out, e = s.survivalOffers(&a)
		if e != nil {
			return nil, e
		}
	case "/MiniGameSurvivalSkillUp":
		id := num(req, 2)
		if a.SkillCredits == 0 || !has(a.Offers, id) {
			return nil, fmt.Errorf("eventplay: skill not earned or offered")
		}
		group, err := s.design.Row("FieldMiniGameSkillGroupTable", 1, id)
		if err != nil {
			return nil, fmt.Errorf("eventplay: invalid skill")
		}
		if item := num(req, 3); item != 0 {
			if _, e := s.design.Row("FieldMiniGameSurvivalItemTable", 1, item); e != nil {
				return nil, e
			}
		}
		if a.SkillLevels == nil {
			a.SkillLevels = map[uint64]uint64{}
		}
		level := a.SkillLevels[id] + 1
		if level > num(group, 2) {
			return nil, fmt.Errorf("eventplay: survival skill at maximum")
		}
		capacity := uint64(0)
		for _, r := range s.design.Tables["FieldMiniGameSurvivalTable"] {
			if num(r, 21) > capacity {
				capacity = num(r, 21)
			}
		}
		if !has(a.Skills, id) && uint64(len(a.SkillLevels)) >= capacity {
			return nil, fmt.Errorf("eventplay: survival skill capacity reached")
		}
		a.SkillLevels[id] = level
		a.SkillCredits--
		a.Offers = nil
		if level == num(group, 2) && s.onProgress != nil {
			if e := s.onProgress(318, a.Game, 1); e != nil {
				return nil, e
			}
			if e := s.onProgress(321, id, 1); e != nil {
				return nil, e
			}
		}
		if !has(a.Skills, id) {
			a.Skills = append(a.Skills, id)
		}
		v := wire.AppendVarint(nil, 1, id)
		v = wire.AppendVarint(v, 2, level)
		out = wire.AppendBytes(nil, 1, v)
	default:
		if strings.Contains(path, "QuickReward") || strings.Contains(path, "Reward") {
			return nil, fmt.Errorf("eventplay: reward requires a completed unpaid score record")
		}
		return nil, fmt.Errorf("eventplay: unsupported run transition")
	}
	next.Runs[rk] = a
	_ = key
	return out, nil
}
func has(a []uint64, id uint64) bool {
	for _, x := range a {
		if x == id {
			return true
		}
	}
	return false
}
func seqSeed(n uint64) uint64 { return n & 2147483647 }
