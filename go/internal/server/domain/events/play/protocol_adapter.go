package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func (s *Service) upgrade(ctx command.Context, path string, req []byte, uid uint64, next *snapshot, key string) ([]byte, error) {
	prefix := fmt.Sprintf("%d:", uid)
	var costs, rewards []gamedata.Reward
	if path == "/MiniGameSurvivalCharUpgrade" {
		id, target := num(req, 2), num(req, 3)
		k := prefix + strconv.FormatUint(id, 10)
		if target == 0 || target != next.Upgrades[k]+1 {
			return nil, fmt.Errorf("eventplay: upgrade level is not next")
		}
		var row []byte
		for _, r := range s.design.Rows("FieldMiniGameUpgradeTable", 1, id) {
			if num(r, 2) == target {
				row = r
				break
			}
		}
		if row == nil {
			return nil, fmt.Errorf("eventplay: upgrade design missing")
		}
		costs = []gamedata.Reward{{Type: 43, Count: num(row, 3)}}
		if _, e := s.economy.Apply(ctx, "eventplay:"+key, costs, nil); e != nil {
			return nil, e
		}
		next.Upgrades[k] = target
		return nil, nil
	}
	for _, r := range s.design.Tables["FieldMiniGameUpgradeTable"] {
		id, level := num(r, 1), num(r, 2)
		if next.Upgrades[prefix+strconv.FormatUint(id, 10)] >= level {
			rewards = append(rewards, gamedata.Reward{Type: 43, Count: num(r, 3)})
		}
	}
	bundle, e := s.economy.Apply(ctx, "eventplay:"+key, nil, rewards)
	if e != nil {
		return nil, e
	}
	for k := range next.Upgrades {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(next.Upgrades, k)
		}
	}
	return bundle, nil
}

func (s *Service) survivalProgress(req []byte, a *Run) (uint64, uint64, error) {
	char, e := s.design.Row("FieldMiniGameCharTable", 13, a.Char)
	if e != nil {
		return 0, 0, e
	}
	var mapRow []byte
	for _, r := range s.design.Rows("FieldMiniGameMapTable", 2, a.MapGroup) {
		if num(r, 3) == a.Stage {
			mapRow = r
		}
	}
	if mapRow == nil {
		return 0, 0, fmt.Errorf("eventplay: survival map missing")
	}
	monsters := s.design.Rows("FieldMiniGameMonsterTable", 2, num(mapRow, 4))
	if a.Killed == nil {
		a.Killed = map[uint64]uint64{}
	}
	if a.Picked == nil {
		a.Picked = map[uint64]uint64{}
	}
	if a.Available == nil {
		a.Available = map[uint64]uint64{}
	}
	elapsed := uint64(0)
	if uint64(s.now().UnixMilli()) > a.Started {
		elapsed = (uint64(s.now().UnixMilli()) - a.Started) / 1000
	}
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 6 {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 {
			return fmt.Errorf("eventplay: empty kill count")
		}
		var row []byte
		for _, r := range monsters {
			if num(r, 5) == id {
				row = r
				break
			}
		}
		if row == nil {
			return fmt.Errorf("eventplay: monster outside survival map")
		}
		max := num(row, 12)
		repeats := num(row, 11)
		start := num(row, 16)
		interval := num(row, 13)
		if elapsed < start {
			return fmt.Errorf("eventplay: monster not spawned yet")
		}
		waves := uint64(1)
		if interval > 0 {
			waves += (elapsed - start) / interval
		}
		if repeats > 0 && waves > repeats {
			waves = repeats
		}
		if max == 0 || a.Killed[id] > max*waves || count > max*waves-a.Killed[id] {
			return fmt.Errorf("eventplay: kill count exceeds designed spawns")
		}
		a.Killed[id] += count
		box := num(row, 6)
		if box != 0 {
			items, err := s.boxItems(box)
			if err != nil {
				return err
			}
			for _, item := range items {
				a.Available[item] += count
			}
		}
		return nil
	})
	if e != nil {
		return 0, 0, e
	}
	exp, coin := uint64(0), uint64(0)
	healing := uint64(0)
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 5 {
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		id, count := num(f.Value, 1), num(f.Value, 2)
		if count == 0 || count > a.Available[id] {
			return fmt.Errorf("eventplay: item count exceeds committed drops")
		}
		row, e := s.design.Row("FieldMiniGameSurvivalItemTable", 1, id)
		if e != nil {
			return e
		}
		v := num(row, 4)
		if count > math.MaxInt32 || v > math.MaxInt32/count {
			return fmt.Errorf("eventplay: item value overflow")
		}
		a.Available[id] -= count
		a.Picked[id] += count
		switch num(row, 3) {
		case 0:
			healing += v * count
		case 3:
			exp += uint64(math.Floor(float64(v*count) * doubleField(char, 8, 1)))
		case 4:
			coin += uint64(math.Floor(float64(v*count) * doubleField(char, 10, 1)))
		case 5:
			a.SkillCredits += count
		}
		return nil
	})
	if e != nil {
		return 0, 0, e
	}
	boxes, e := list(req, 7)
	if e != nil {
		return 0, 0, e
	}
	for _, id := range boxes {
		if _, e := s.design.Row("FieldMiniGameSurvivalBoxTable", 2, id); e != nil {
			return 0, 0, e
		}
	}
	hp := num(req, 2)
	if hp > a.MaxHP || hp > a.HP+healing {
		return 0, 0, fmt.Errorf("eventplay: invalid survival HP")
	}
	a.HP = hp
	a.Exp += exp
	a.Coin += coin
	a.LevelExp += exp
	if a.Exp > math.MaxInt32 || a.Coin > math.MaxInt32 {
		return 0, 0, fmt.Errorf("eventplay: survival progress exceeds protocol range")
	}
	growth := num(char, 2)
	for {
		var current, next []byte
		for _, r := range s.design.Rows("FieldMiniGameCharLevelTable", 1, growth) {
			if num(r, 2) == a.Level {
				current = r
			}
			if num(r, 2) == a.Level+1 {
				next = r
			}
		}
		if current == nil || next == nil || num(current, 3) == 0 || a.LevelExp < num(current, 3) {
			break
		}
		a.LevelExp -= num(current, 3)
		a.Level++
		a.SkillCredits++
	}
	return exp, coin, nil
}

func doubleField(b []byte, n int, def float64) float64 {
	v := def
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == n && f.Type == 1 {
			v = math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
		}
		return nil
	})
	return v
}

func (s *Service) boxItems(id uint64) ([]uint64, error) {
	r, e := s.design.Row("FieldMiniGameSurvivalBoxTable", 2, id)
	if e != nil {
		return nil, e
	}
	values, e := list(r, 3)
	if e != nil {
		return nil, e
	}
	return values, nil
}

func (s *Service) survivalOffers(a *Run) ([]byte, error) {
	if a.SkillCredits == 0 {
		return nil, fmt.Errorf("eventplay: no earned skill selection")
	}
	if len(a.Offers) > 0 {
		if a.Rerolls == 0 {
			return nil, fmt.Errorf("eventplay: no remaining rerolls")
		}
		a.Rerolls--
	}
	a.Offers = nil
	rows := append([][]byte(nil), s.design.Tables["FieldMiniGameSkillGroupTable"]...)
	sort.Slice(rows, func(i, j int) bool { return num(rows[i], 1) < num(rows[j], 1) })
	for _, r := range rows {
		id := num(r, 1)
		if a.SkillLevels[id] < num(r, 2) {
			a.Offers = append(a.Offers, id)
			if len(a.Offers) == 3 {
				break
			}
		}
	}
	out := wire.AppendVarint(nil, 1, a.Rerolls)
	for _, id := range a.Offers {
		out = wire.AppendVarint(out, 2, id)
	}
	return out, nil
}

func summaryWire(st *snapshot, record bool) []byte {
	keys := []string{}
	for k, v := range st.Records {
		if v.Best > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		v := st.Records[k]
		field := map[string]int{"Survival": 1, "Sichuan": 2, "Action": 3, "Rhythm": 5, "Hopscotch": 6}[v.Family]
		if field == 0 {
			continue
		}
		var b []byte
		switch v.Family {
		case "Survival":
			b = wire.AppendVarint(nil, 2, v.Best)
		case "Sichuan":
			b = wire.AppendDouble(nil, 2, float64(v.Best))
		case "Action":
			b = wire.AppendVarint(nil, 2, v.Best)
			b = wire.AppendVarint(b, 3, v.Stage)
		case "Rhythm":
			b = wire.AppendVarint(nil, 2, v.Best)
			b = wire.AppendVarint(b, 3, v.Stage)
		case "Hopscotch":
			b = wire.AppendVarint(nil, 1, v.Stage)
			b = wire.AppendVarint(b, 3, v.Best)
		}
		if record {
			switch v.Family {
			case "Survival":
				b = wire.AppendVarint(nil, 1, v.Best)
				b = wire.AppendDouble(b, 2, 100)
			case "Sichuan":
				b = wire.AppendDouble(nil, 1, float64(v.Best))
				b = wire.AppendDouble(b, 2, 100)
			case "Action":
				b = wire.AppendVarint(nil, 1, v.Stage)
				b = wire.AppendVarint(b, 2, v.Best)
				b = wire.AppendDouble(b, 3, 100)
			case "Rhythm":
				b = wire.AppendVarint(nil, 1, v.Best)
				b = wire.AppendVarint(b, 2, v.Stage)
				b = wire.AppendDouble(b, 3, 100)
			case "Hopscotch":
				b = wire.AppendVarint(nil, 1, v.Stage)
				b = wire.AppendVarint(b, 2, v.Best)
				b = wire.AppendDouble(b, 4, 100)
			}
		}
		out = wire.AppendBytes(out, field, b)
	}
	return out
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
		if a.Mode == 1 { //nolint:staticcheck // QF1003
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

func (s *Service) progress(ctx command.Context, path string, req []byte, rk string, next *snapshot, key string) ([]byte, error) {
	a, ok := next.Runs[rk]
	if !ok {
		return nil, fmt.Errorf("eventplay: no active run")
	}
	var out []byte
	switch path {
	case "/MiniGameFieldScore":
		d, e := s.fieldRules(a.Game)
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
			if v.Type == 1 { //nolint:staticcheck // QF1003
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
				if e = s.onProgress(ctx, 319, a.Game, total); e != nil {
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
			if e := s.onProgress(ctx, 318, a.Game, 1); e != nil {
				return nil, e
			}
			if e := s.onProgress(ctx, 321, id, 1); e != nil {
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

// LockRoomRun installs the stage selected by native room matching before the
// client's HTTP Start. It does not award rewards or trust HTTP stage changes.
func (s *Service) LockRoomRun(ctx command.Context, session, family, guid string, uid, group, stage, monster uint64) error {

	if family != "Defense" && family != "Action" {
		return fmt.Errorf("eventplay: invalid room family")
	}
	c, e := s.calendar(uid, family)
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(raw, &next)
	next.Runs[session+":"+family+":native"] = Run{UID: uid, Game: c.ID, Stage: stage, Mode: group, Char: monster, Family: family, Session: guid}
	raw, _ = json.Marshal(next)
	if e = s.store.Save(ctx.State, "eventplay", raw); e != nil {
		return e
	}
	s.state = next
	return nil
}

func specializedInfo(st *snapshot, uid uint64, f string) []byte {
	var out []byte
	if f == "Action" || f == "Survival" || f == "Defense" {
		out = wire.AppendVarint(out, 1, uid)
	}
	for _, r := range st.Records {
		if r.UID != uid || r.Family != f {
			continue
		}
		switch f {
		case "Sichuan":
			v := wire.AppendVarint(nil, 1, uid)
			v = wire.AppendVarint(v, 6, r.Best)
			out = wire.AppendBytes(out, 1, v)
		case "Rhythm":
			v := wire.AppendVarint(nil, 1, r.Stage)
			v = wire.AppendVarint(v, 2, r.Mode)
			v = wire.AppendVarint(v, 3, r.Best)
			out = wire.AppendBytes(out, 1, v)
		case "Action":
			v := wire.AppendVarint(nil, 1, r.Stage)
			v = wire.AppendVarint(v, 2, r.Best)
			out = wire.AppendBytes(out, 2, v)
		case "Hopscotch":
			v := wire.AppendVarint(nil, 1, r.Stage)
			if r.Best > 0 {
				v = wire.AppendVarint(v, 2, 1)
			}
			out = wire.AppendBytes(out, 2, v)
		case "Survival":
			out = wire.AppendVarint(out, 4, r.Best)
			for stage, clear := range r.Clears {
				if !clear {
					continue
				}
				var group, id uint64
				if _, e := fmt.Sscanf(stage, "map:%d:%d", &group, &id); e == nil {
					v := wire.AppendVarint(nil, 1, group)
					v = wire.AppendVarint(v, 2, id)
					out = wire.AppendBytes(out, 8, v)
				}
			}
		}
	}
	if f == "Survival" {
		prefix := fmt.Sprintf("%d:", uid)
		for key, level := range st.Upgrades {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			var id uint64
			_, _ = fmt.Sscanf(key[len(prefix):], "%d", &id)
			v := wire.AppendVarint(nil, 1, id)
			v = wire.AppendVarint(v, 2, level)
			out = wire.AppendBytes(out, 10, v)
		}
	}
	return out
}

func rankingWire(st *snapshot, uid uint64, f string, record bool) []byte {
	keys := []string{}
	for k, r := range st.Records {
		if r.UID == uid && r.Family == f && r.Best > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		r := st.Records[k]
		var b []byte
		switch f {
		case "Survival":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendDouble(b, 2, 100)
			}
		case "Hopscotch":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Stage)
				b = wire.AppendVarint(b, 2, r.Best)
				b = wire.AppendDouble(b, 4, 100)
			}
		case "Action":
			b = wire.AppendVarint(nil, 3, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Stage)
				b = wire.AppendVarint(b, 2, r.Best)
				b = wire.AppendDouble(b, 3, 100)
			}
		case "Sichuan":
			b = wire.AppendVarint(nil, 7, 1)
			b = wire.AppendVarint(b, 8, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendDouble(b, 2, 100)
			}
		case "Rhythm":
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
			if record {
				b = wire.AppendVarint(nil, 1, r.Best)
				b = wire.AppendVarint(b, 2, r.Stage)
				b = wire.AppendDouble(b, 3, 100)
			}
		default:
			b = wire.AppendVarint(nil, 1, 1)
			b = wire.AppendVarint(b, 4, r.Best)
		}
		out = wire.AppendBytes(out, 1, b)
	}
	return out
}

func (s *Service) claimScore(ctx command.Context, path string, uid uint64, f string, next *snapshot, key string) ([]byte, error) {
	for k, r := range next.Records {
		if r.UID != uid || r.Family != f || r.Best == 0 {
			continue
		}
		point, rewards, e := s.scoreRewards(r.Game, r.Best)
		if e != nil {
			return nil, e
		}
		day := uint64(s.now().UnixMilli()) / 86400000
		if r.Date != day {
			r.Paid = 0
		}
		if point <= r.Paid {
			return nil, fmt.Errorf("eventplay: daily best reward already paid")
		}
		_, old, e := s.scoreRewards(r.Game, r.Paid)
		if e != nil {
			return nil, e
		}
		bundle, e := s.grant(ctx, "eventplay:"+key, rewardDifference(old, rewards))
		if e != nil {
			return nil, e
		}
		r.Paid = point
		r.Date = day
		next.Records[k] = r
		if strings.HasSuffix(path, "QuickReward") {
			return wire.AppendBytes(nil, 1, bundle), nil
		}
		out := wire.AppendVarint(nil, 1, point)
		return wire.AppendBytes(out, 2, bundle), nil
	}
	return nil, fmt.Errorf("eventplay: no completed score to reward")
}

func (s *Service) AssociatedMissionGroup(c events.Schedule) (uint64, error) {
	if c.Type != 11 {
		return 0, nil
	}
	game, e := s.design.Row("PackEventMiniGameTable", 8, c.ID)
	if e != nil {
		return 0, e
	}
	switch num(game, 3) {
	case 5:
		row, e := s.design.Row("FieldMiniGameSurvivalTable", 9, num(game, 4))
		if e != nil {
			return 0, e
		}
		return num(row, 8), nil
	case 7:
		if len(s.design.Tables["MGDDefaultTable"]) > 0 {
			return num(s.design.Tables["MGDDefaultTable"][0], 5), nil
		}
	case 8:
		if len(s.design.Tables["ActionGameDefaultTable"]) > 0 {
			return num(s.design.Tables["ActionGameDefaultTable"][0], 8), nil
		}
	case 12:
		if len(s.design.Tables["HopscotchDefaultTable"]) > 0 {
			return num(s.design.Tables["HopscotchDefaultTable"][0], 20), nil
		}
	}
	return 0, nil
}

func num(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }

func list(b []byte, n int) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		for p := f.Value; len(p) > 0; {
			v, k := binary.Uvarint(p)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			p = p[k:]
		}
		return nil
	})
	return out, e
}

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}

	fail := func(e error) (int, []byte, bool, error) { return code, nil, true, e }
	seq := num(req, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return fail(fmt.Errorf("eventplay: invalid sequence"))
	}
	if e := wire.Walk(req, func(f wire.Field) error {
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt64 {
				return wire.ErrMalformed
			}
		}
		return nil
	}); e != nil {
		return fail(e)
	}
	// Public hub calendars are derived afresh, never stored as account replies.
	if path == "/EventHubInfo" || path == "/MiniEventHubInfo" || path == "/MiniGameHubInfo" {
		if s.hubCalendars != nil && path == "/MiniGameHubInfo" {
			return s.hubCalendars.Handle(ctx, path, req)
		}
		if s.hubCalendars != nil && path == "/EventHubInfo" {
			out, err := s.publicEventHubs(ctx, req)
			return code, out, true, err
		}
		if path == "/MiniEventHubInfo" {
			out, err := s.miniEventHubs(ctx, req)
			return code, out, true, err
		}
		return code, nil, true, nil
	}
	key := fmt.Sprintf("%s:%s:%d", commandSession, path, seq)
	if r, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return fail(fmt.Errorf("eventplay: changed request retry"))
		}
		return code, r.Body, true, nil
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(raw, &next)
	var out []byte
	var e error
	switch path {
	case "/MiniGameUserRecordInfo", "/MiniGameRanking":
		out = summaryWire(&next, path == "/MiniGameUserRecordInfo")
	case "/MiniGameRelayServerInfo":
		return fail(fmt.Errorf("eventplay: native relay channels are not configured"))

	case "/PackEventStoryInfo", "/PackEventBattleInfo":
		uids, err := list(req, 2)
		if err != nil {
			return fail(err)
		}
		for _, uid := range uids {
			c, err := s.registry.Resolve(uid)
			if err != nil {
				return fail(err)
			}
			if path == "/PackEventStoryInfo" {
				for _, r := range s.design.Rows("PackEventStoryTable", 1, c.ID) {
					id := num(r, 2)
					if next.Stories[fmt.Sprintf("%d:%d:%d", uid, c.ID, id)] {
						v := wire.AppendVarint(nil, 1, uid)
						v = wire.AppendVarint(v, 2, c.ID)
						v = wire.AppendVarint(v, 3, id)
						out = wire.AppendBytes(out, 1, v)
					}
				}
			} else {
				for _, r := range s.design.Rows("PackEventBattleTable", 4, c.ID) {
					id := num(r, 5)
					if !next.Stories[fmt.Sprintf("battle:%d:%d:%d", uid, c.ID, id)] {
						continue
					}
					v := wire.AppendVarint(nil, 1, uid)
					v = wire.AppendVarint(v, 2, c.ID)
					v = wire.AppendVarint(v, 3, id)
					for _, idx := range s.battleChallengeIndexesLocked(uid, c.ID, id) {
						v = wire.AppendVarint(v, 4, idx)
					}
					out = wire.AppendBytes(out, 1, v)
				}
			}
		}
	case "/PackEventStoryClear", "/PackEventStoryReplayClear":
		uid, group, id := num(req, 2), num(req, 3), num(req, 4)
		if path == "/PackEventStoryReplayClear" {
			group, id = num(req, 2), num(req, 3)
			uid = 0
		}
		if uid != 0 {
			c, err := s.registry.Resolve(uid)
			if err != nil {
				return fail(err)
			}
			if c.ID != group || s.now().UnixMilli() < c.Start || s.now().UnixMilli() >= c.End {
				return fail(fmt.Errorf("eventplay: story schedule unavailable"))
			}
		}
		r, err := s.design.Story(group, id)
		if err != nil {
			return fail(err)
		}
		storyKey := fmt.Sprintf("%d:%d:%d", uid, group, id)
		if !next.Stories[storyKey] {
			rs, err := gamedata.EventPlayRewards(r, 11, 10, 9)
			if err != nil {
				return fail(err)
			}
			out, e = s.grant(ctx, "eventplay:story:"+storyKey, rs)
			if e != nil {
				return fail(e)
			}
			out = wire.AppendBytes(nil, 1, out)
			next.Stories[storyKey] = true
		}
		if path == "/PackEventStoryClear" {
			v := wire.AppendVarint(nil, 1, uid)
			v = wire.AppendVarint(v, 2, group)
			v = wire.AppendVarint(v, 3, id)
			out = wire.AppendBytes(out, 2, v)
		}
	default:
		out, e = s.game(ctx, path, req, commandSession, &next, key)
	}
	if e != nil {
		return fail(e)
	}
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	b, e := json.Marshal(next)
	if e != nil {
		return fail(e)
	}
	if e = s.store.Save(ctx.State, "eventplay", b); e != nil {
		return fail(e)
	}
	s.state = next
	return code, out, true, nil
}

func recordWire(r record) []byte {
	b := wire.AppendVarint(nil, 1, r.UID)
	b = wire.AppendVarint(b, 2, r.Paid)
	b = wire.AppendVarint(b, 3, r.Best)
	if r.Best > 0 && r.Paid < r.Best {
		b = wire.AppendVarint(b, 4, 1)
	}
	return b
}

func (s *Service) calendar(uid uint64, f string) (events.Schedule, error) {
	if uid == 0 {
		for _, c := range s.registry.List() {
			if c.Type == 11 {
				d, e := s.design.Row("PackEventMiniGameTable", 8, c.ID)
				if e != nil {
					continue
				}
				kind := num(d, 3)
				expected := map[string]uint64{"Field": 1, "Run": 3, "Survival": 5, "Sichuan": 6, "Defense": 7, "Action": 8, "Rhythm": 9, "Hopscotch": 12}[f]
				if f == "Run" && (kind == 2 || kind == 4) {
					expected = kind
				}
				if kind != expected {
					continue
				}
				uid = c.UID
				break
			}
		}
	}
	c, e := s.registry.Resolve(uid)
	if e != nil {
		return c, e
	}
	if c.Type != 11 {
		return c, fmt.Errorf("eventplay: minigame schedule mismatch")
	}
	if _, e = s.design.Row("PackEventMiniGameTable", 8, c.ID); e != nil {
		return c, e
	}
	d, _ := s.design.Row("PackEventMiniGameTable", 8, c.ID)
	kind := num(d, 3)
	expected := map[string]uint64{"Field": 1, "Run": 3, "Survival": 5, "Sichuan": 6, "Defense": 7, "Action": 8, "Rhythm": 9, "Hopscotch": 12}[f]
	if f == "Run" && (kind == 2 || kind == 4) {
		expected = kind
	}
	if kind != expected {
		return c, fmt.Errorf("eventplay: configured minigame family mismatch")
	}
	return c, nil
}

func (s *Service) game(ctx command.Context, path string, req []byte, session string, next *snapshot, key string) ([]byte, error) {
	f := family(path)
	if f == "" {
		return nil, fmt.Errorf("eventplay: unknown family")
	}
	start := strings.HasSuffix(path, "Start")
	end := strings.HasSuffix(path, "End")
	runKey := session + ":" + f
	uid := num(req, 2)
	if end || path == "/MiniGameFieldScore" || path == "/MiniGameSurvivalPlay" || strings.Contains(path, "Skill") {
		if a, ok := next.Runs[runKey]; ok {
			uid = a.UID
		} else {
			return nil, fmt.Errorf("eventplay: no active run")
		}
	}
	c, e := s.calendar(uid, f)
	if e != nil {
		return nil, e
	}
	uid = c.UID
	stage, mode := num(req, 3), num(req, 4)
	if f == "Rhythm" {
		mode = num(req, 5)
	}
	if f == "Survival" {
		stage = num(req, 5)
	}
	if f == "Sichuan" {
		stage = num(req, 4)
	}
	rk := recKey(uid, stage, mode, f)
	r := next.Records[rk]
	if r.Clears == nil {
		r = record{UID: uid, Game: c.ID, Stage: stage, Mode: mode, Family: f, Clears: map[string]bool{}}
	}
	if path == "/MiniGameSurvivalCharUpgrade" || path == "/MiniGameSurvivalCharUpgradeReset" {
		return s.upgrade(ctx, path, req, uid, next, key)
	}
	if path == "/MiniGameDefenseMatching" {
		matching := num(req, 3)
		if matching > 2 {
			return nil, fmt.Errorf("eventplay: unknown matching mode")
		}
		next.Runs[runKey+":matching"] = Run{UID: uid, Game: c.ID, Family: f, Session: session}
		return nil, nil
	}
	if start {
		if s.now().UnixMilli() < c.Start || s.now().UnixMilli() >= c.End {
			return nil, fmt.Errorf("eventplay: season closed")
		}
		if _, active := next.Runs[runKey]; active {
			return nil, fmt.Errorf("eventplay: run already active")
		}
		if e = s.validateStage(f, stage, mode); e != nil {
			return nil, e
		}
		a := Run{UID: uid, Game: c.ID, Stage: stage, Mode: mode, Started: uint64(s.now().UnixMilli()), Family: f, Session: session, HP: 100}
		if f == "Action" {
			game, _ := s.design.Row("PackEventMiniGameTable", 8, c.ID)
			group := num(game, 4)
			rows := s.design.Rows("ActionGameStageTable", 3, group)
			if len(rows) == 0 {
				return nil, fmt.Errorf("eventplay: action stage group missing")
			}
			a.Stage = num(rows[0], 4)
			a.Mode = group
			a.Char = num(rows[0], 1)
		}
		if f == "Action" || f == "Defense" {
			if locked, ok := next.Runs[runKey+":native"]; ok {
				a.Stage, a.Mode, a.Char = locked.Stage, locked.Mode, locked.Char
				a.Session = locked.Session
				if s.rooms != nil {
					if e = s.rooms.ValidateRoom(session, a.Session, uid); e != nil {
						return nil, e
					}
				}
			}
		}
		var startReply []byte
		if f == "Field" {
			d, e := s.fieldRules(c.ID)
			if e != nil {
				return nil, e
			}
			a.HP = d.HP
			ids := make([]uint64, 0, len(d.Objects))
			for id := range d.Objects {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				startReply = wire.AppendVarint(startReply, 1, id)
			}
		}
		if f == "Survival" {
			a.Char = num(req, 3)
			a.MapGroup = num(req, 4)
			char, e := s.design.Row("FieldMiniGameCharTable", 13, a.Char)
			if e != nil {
				return nil, e
			}
			a.HP = num(char, 20)
			a.MaxHP = a.HP
			a.Rerolls = num(char, 19)
			a.Level = 1
			a.SkillCredits = 0
			a.Killed = map[uint64]uint64{}
			a.Picked = map[uint64]uint64{}
			a.Available = map[uint64]uint64{}
			skillGroup := num(char, 3)
			skills := s.design.Rows("FieldMiniGameSkillTable", 7, skillGroup)
			if len(skills) == 0 {
				return nil, fmt.Errorf("eventplay: character starting skill missing")
			}
			a.Skills = []uint64{num(skills[0], 8)}
			a.SkillLevels = map[uint64]uint64{skillGroup: 1}
			found := false
			for _, m := range s.design.Rows("FieldMiniGameMapTable", 2, a.MapGroup) {
				if num(m, 3) == a.Stage {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("eventplay: survival map missing")
			}
		}
		next.Runs[runKey] = a
		if s.onProgress != nil {
			if e = s.onProgress(ctx, 343, c.ID, 1); e != nil {
				return nil, e
			}
			if e = s.onProgress(ctx, 315, c.ID, 1); e != nil {
				return nil, e
			}
		}
		next.Records[rk] = r
		if f == "Survival" {
			v := wire.AppendVarint(nil, 1, a.Skills[0])
			v = wire.AppendVarint(v, 2, 1)
			out := wire.AppendBytes(nil, 1, v)
			return wire.AppendVarint(out, 2, seqSeed(a.Started)), nil
		}
		return startReply, nil
	}
	if end {
		a := next.Runs[runKey]
		if a.UID != uid {
			return nil, fmt.Errorf("eventplay: run schedule changed")
		}
		if f == "Action" && (num(req, 3) != a.Mode || num(req, 4) != a.Stage || num(req, 6) != a.Char) {
			return nil, fmt.Errorf("eventplay: action submitted stage/monster differs from configured start")
		}
		if f == "Action" || f == "Defense" {
			guid, _, err := wire.Bytes(req, 2)
			if err != nil {
				return nil, err
			}
			if a.Session != session && !bytes.Equal(guid, []byte(a.Session)) {
				return nil, fmt.Errorf("eventplay: native room guid mismatch")
			}
			if s.rooms != nil {
				if e = s.rooms.ValidateRoom(session, string(guid), uid); e != nil {
					return nil, e
				}
			}
		}
		score, e := s.endScore(f, req, a)
		if e != nil {
			return nil, e
		}
		if s.onProgress != nil {
			switch f {
			case "Survival":
				for condition, count := range map[uint64]uint64{316: a.Level, 317: score} {
					if e = s.onProgress(ctx, condition, c.ID, count); e != nil {
						return nil, e
					}
				}
				if num(req, 2) == 0 {
					if e = s.onProgress(ctx, 320, a.Stage, 1); e != nil {
						return nil, e
					}
				}
			case "Defense":
				if e = s.onProgress(ctx, 322, c.ID, score); e != nil {
					return nil, e
				}
			case "Action":
				if e = s.onProgress(ctx, 323, a.Char, 1); e != nil {
					return nil, e
				}
				for condition, field := range map[uint64]int{326: 9, 327: 10, 329: 12, 330: 13, 333: 14, 334: 15, 335: 16, 336: 17} {
					if count := num(req, field); count > 0 {
						if e = s.onProgress(ctx, condition, a.Char, count); e != nil {
							return nil, e
						}
					}
				}
			}
		}
		rk = recKey(uid, a.Stage, a.Mode, f)
		r = next.Records[rk]
		if r.Clears == nil {
			r.Clears = map[string]bool{}
		}
		if score > r.Best {
			r.Best = score
		}
		var out []byte
		point, rewards, e := s.scoreRewards(c.ID, score)
		if e != nil {
			return nil, e
		}
		if f == "Defense" {
			for _, row := range s.design.Tables["MGDRewardTable"] {
				id, wave := num(row, 1), num(row, 6)
				ck := fmt.Sprintf("wave:%d", id)
				if wave <= score && !r.Clears[ck] {
					rr, err := gamedata.EventPlayRewards(row, 5, 3, 2)
					if err != nil {
						return nil, err
					}
					b, err := s.grant(ctx, "eventplay:"+key+":"+ck, rr)
					if err != nil {
						return nil, err
					}
					out = wire.AppendBytes(out, 1, b)
					out = wire.AppendVarint(out, 2, id)
					r.Clears[ck] = true
				}
			}
		}
		if f == "Survival" {
			if num(req, 5) > a.Coin || num(req, 6) > a.Exp {
				return nil, fmt.Errorf("eventplay: end coin/exp exceeds committed play progress")
			}
			out = wire.AppendVarint(out, 2, a.Coin)
			out = wire.AppendVarint(out, 3, a.Exp)
			if num(req, 2) == 0 {
				r.Clears[fmt.Sprintf("map:%d:%d", a.MapGroup, a.Stage)] = true
				for _, m := range s.design.Rows("FieldMiniGameMapTable", 2, a.MapGroup) {
					if num(m, 3) != a.Stage {
						continue
					}
					rr, err := gamedata.EventPlayRewards(m, 8, 7, 6)
					if err != nil {
						return nil, err
					}
					if len(rr) > 0 {
						b, err := s.grant(ctx, fmt.Sprintf("eventplay:map:%d:%d:%d", uid, a.MapGroup, a.Stage), rr)
						if err != nil {
							return nil, err
						}
						out = wire.AppendBytes(out, 1, b)
					}
				}
			}
			if a.Coin > 0 {
				b, err := s.grant(ctx, "eventplay:"+key+":coin", []gamedata.BattleReward{{Type: 43, Count: a.Coin}})
				if err != nil {
					return nil, err
				}
				out = wire.AppendBytes(out, 1, b)
			}
		}
		day := uint64(s.now().UnixMilli()) / 86400000
		if r.Date != day {
			r.Paid = 0
		}
		if point > r.Paid {
			_, previous, e := s.scoreRewards(c.ID, r.Paid)
			if e != nil {
				return nil, e
			}
			bundle, e := s.grant(ctx, "eventplay:"+key, rewardDifference(previous, rewards))
			if e != nil {
				return nil, e
			}
			field := 1
			if f == "Run" { //nolint:staticcheck // QF1003
				field = 3
			} else if f == "Field" {
				field = 5
			} else if f == "Action" {
				field = 3
			} else if f == "Hopscotch" {
				field = 4
			}
			if f == "Rhythm" {
				r.PendingBundle = append(r.PendingBundle, bundle...)
			} else {
				out = wire.AppendBytes(out, field, bundle)
			}
			r.Paid = point
			r.Date = day
		}
		switch f {
		case "Run":
			out = wire.AppendVarint(out, 1, r.Best)
			out = wire.AppendVarint(out, 2, r.Paid)
		case "Field":
			out = wire.AppendVarint(out, 1, uid)
			out = wire.AppendVarint(out, 2, score)
			out = wire.AppendVarint(out, 3, r.Best)
			out = wire.AppendVarint(out, 4, r.Paid)
		case "Sichuan":
			out = wire.AppendDouble(out, 4, float64(r.Best))
		case "Rhythm":
			v := wire.AppendVarint(nil, 1, a.Stage)
			v = wire.AppendVarint(v, 2, a.Mode)
			v = wire.AppendVarint(v, 3, r.Best)
			v = wire.AppendVarint(v, 4, num(req, 2))
			v = wire.AppendVarint(v, 5, num(req, 6))
			out = wire.AppendBytes(out, 1, v)
		case "Action":
			out = wire.AppendVarint(out, 1, r.Best)
		case "Hopscotch":
			v := wire.AppendVarint(nil, 1, a.Stage)
			if score > 0 {
				v = wire.AppendVarint(v, 2, 1)
			}
			out = wire.AppendBytes(out, 2, v)
		}
		next.Records[rk] = r
		delete(next.Runs, runKey)
		if s.rooms != nil && (f == "Action" || f == "Defense") {
			if e = s.rooms.CompleteRoom(session, a.Session, score); e != nil {
				return nil, e
			}
		}
		return out, nil
	}
	if strings.Contains(path, "Ranking") || strings.Contains(path, "RecordInfo") {
		return rankingWire(next, uid, f, strings.Contains(path, "RecordInfo")), nil
	}
	if strings.HasSuffix(path, "Info") {
		var out []byte
		if f == "Rhythm" {
			for k, v := range next.Records {
				if v.UID == uid && v.Family == f && len(v.PendingBundle) > 0 {
					out = wire.AppendBytes(out, 2, v.PendingBundle)
					v.PendingBundle = nil
					next.Records[k] = v
				}
			}
		}
		if f != "Run" && f != "Field" {
			if _, exists := next.Records[rk]; !exists {
				next.Records[rk] = r
			}
			return append(out, specializedInfo(next, uid, f)...), nil
		}
		for _, v := range next.Records {
			if v.UID == uid && v.Family == f {
				out = wire.AppendBytes(out, 1, recordWire(v))
			}
		}
		if len(out) == 0 {
			next.Records[rk] = r
			out = wire.AppendBytes(out, 1, recordWire(r))
		}
		return out, nil
	}
	if strings.Contains(path, "QuickReward") || path == "/MiniGameFieldReward" {
		return s.claimScore(ctx, path, uid, f, next, key)
	}
	return s.progress(ctx, path, req, runKey, next, key)
}

// ListMiniContentRoutes exposes the same validated hub-scoped calendar used
// for presentation, including routes whose play period has already ended.
func (s *Service) ListMiniContentRoutes(ctx command.Context) ([]gamedata.MiniContentRoute, error) {
	body, err := s.miniEventHubs(ctx, wire.AppendVarint(nil, 1, 1))
	if err != nil {
		return nil, err
	}
	var routes []gamedata.MiniContentRoute
	seen := map[uint64]bool{}
	err = wire.Walk(body, func(hub wire.Field) error {
		if hub.Number != 1 || hub.Type != 2 {
			return nil
		}
		return wire.Walk(hub.Value, func(slot wire.Field) error {
			if slot.Number != 6 || slot.Type != 2 {
				return nil
			}
			typ := num(slot.Value, 2)
			if typ != 13 && typ != 14 {
				return nil
			}
			uid := num(slot.Value, 4)
			if seen[uid] {
				return fmt.Errorf("eventplay: ambiguous mini content UID %d", uid)
			}
			seen[uid] = true
			routes = append(routes, gamedata.MiniContentRoute{UID: uid, ContentType: typ, ContentID: num(slot.Value, 3), Start: int64(num(slot.Value, 5)), End: int64(num(slot.Value, 6))})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].UID < routes[j].UID })
	return routes, nil
}

// ResolveMiniContentUID resolves hub-scoped story/quiz identities without
// manufacturing EventScheduleInfo rows. Callers enforce the returned window.
func (s *Service) ResolveMiniContentUID(ctx command.Context, uid uint64) (contentType, contentID uint64, start, end int64, err error) {
	if uid == 0 {
		return 0, 0, 0, 0, fmt.Errorf("eventplay: missing mini content UID")
	}
	body, err := s.miniEventHubs(ctx, wire.AppendVarint(nil, 1, 1))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	found := false
	err = wire.Walk(body, func(hub wire.Field) error {
		if hub.Number != 1 || hub.Type != 2 {
			return nil
		}
		return wire.Walk(hub.Value, func(slot wire.Field) error {
			if slot.Number != 6 || slot.Type != 2 || num(slot.Value, 4) != uid {
				return nil
			}
			typ := num(slot.Value, 2)
			if typ != 13 && typ != 14 {
				return nil
			}
			if found {
				return fmt.Errorf("eventplay: ambiguous mini content UID %d", uid)
			}
			found = true
			contentType, contentID = typ, num(slot.Value, 3)
			start, end = int64(num(slot.Value, 5)), int64(num(slot.Value, 6))
			return nil
		})
	})
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if !found {
		return 0, 0, 0, 0, fmt.Errorf("eventplay: mini content UID %d unavailable", uid)
	}
	return contentType, contentID, start, end, nil
}

func (s *Service) miniEventHubs(ctx command.Context, req []byte) ([]byte, error) {
	var hubs []miniHubSchedule
	if s.hubCalendars == nil {
		return nil, nil
	}
	_, body, handled, err := s.hubCalendars.Handle(ctx, "/EventHubInfo", req)
	if err != nil {
		return nil, err
	}
	if handled {
		err = wire.Walk(body, func(f wire.Field) error {
			if f.Number != 1 || f.Type != 2 {
				return nil
			}
			hub := miniHubSchedule{UID: num(f.Value, 1), HubID: num(f.Value, 2), Start: int64(num(f.Value, 3)), PlayEnd: int64(num(f.Value, 4)), End: int64(num(f.Value, 5))}
			if err := wire.Walk(f.Value, func(setting wire.Field) error {
				if setting.Number != 6 || setting.Type != 2 {
					return nil
				}
				uids, err := list(setting.Value, 3)
				if err != nil {
					return err
				}
				hub.Bindings = append(hub.Bindings, miniHubBinding{num(setting.Value, 1), num(setting.Value, 2), uids})
				return nil
			}); err != nil {
				return err
			}
			hubs = append(hubs, hub)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].UID < hubs[j].UID })
	rows := s.registry.List()
	var out []byte
	for _, hub := range hubs {
		table, err := s.design.Row("PackEventHubTable", 14, hub.HubID)
		if err != nil || num(table, 13) != 1 {
			continue
		}
		v := wire.AppendVarint(nil, 1, hub.UID)
		v = wire.AppendVarint(v, 2, hub.HubID)
		v = wire.AppendVarint(v, 3, uint64(hub.Start))
		v = wire.AppendVarint(v, 4, uint64(hub.PlayEnd))
		v = wire.AppendVarint(v, 5, uint64(hub.End))
		slots := s.design.Rows("PackEventListTable", 6, hub.HubID)
		sort.Slice(slots, func(i, j int) bool { return num(slots[i], 10) < num(slots[j], 10) })
		for _, binding := range hub.Bindings {
			found := false
			for _, slot := range slots {
				if binding.Slot == num(slot, 11) {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("eventplay: mini hub %d static slot index %d missing", hub.UID, binding.Slot)
			}
		}
		for _, slot := range slots {
			contentType, contentID := num(slot, 9), num(slot, 7)
			bound := map[uint64]bool{}
			for _, binding := range hub.Bindings {
				if binding.Slot == num(slot, 11) {
					if binding.ContentType != contentType {
						return nil, fmt.Errorf("eventplay: mini hub %d slot %d content type mismatch", hub.UID, num(slot, 10))
					}
					for _, uid := range binding.UIDs {
						bound[uid] = true
					}
				}
			}
			if len(bound) == 0 {
				continue
			}
			// Mini hub stories and NPC quizzes have hub-scoped UIDs, rather
			// than entries in EventScheduleInfo. Their explicit binding and
			// static EndDateType supply the calendar; never invent a global
			// event type for these separate client protocols.
			if contentType == 13 || contentType == 14 {
				if len(bound) != 1 || bound[0] {
					return nil, fmt.Errorf("eventplay: mini hub %d slot %d requires one nonzero hub-scoped UID", hub.UID, num(slot, 10))
				}
				var uid uint64
				for id := range bound {
					uid = id
				}
				for _, row := range rows {
					if row.UID == uid {
						return nil, fmt.Errorf("eventplay: mini hub %d slot %d hub-scoped UID collides with global schedule", hub.UID, num(slot, 10))
					}
				}
				end := hub.End
				if num(slot, 4) == 0 {
					end = hub.PlayEnd
				}
				if end <= hub.Start {
					continue
				}
				b := wire.AppendVarint(nil, 1, num(slot, 10))
				b = wire.AppendVarint(b, 2, contentType)
				b = wire.AppendVarint(b, 3, contentID)
				b = wire.AppendVarint(b, 4, uid)
				b = wire.AppendVarint(b, 5, uint64(hub.Start))
				b = wire.AppendVarint(b, 6, uint64(end))
				v = wire.AppendBytes(v, 6, b)
				continue
			}
			eventType, subID, known := s.miniSlotSchedule(contentType, contentID)
			if !known {
				return nil, fmt.Errorf("eventplay: mini hub %d slot %d content type %d unsupported", hub.UID, num(slot, 10), contentType)
			}
			for uid := range bound {
				found := false
				for _, row := range rows {
					if row.UID == uid && row.Type == eventType && row.ID == contentID && row.SubID == subID {
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("eventplay: mini hub %d slot %d schedule %d identity mismatch", hub.UID, num(slot, 10), uid)
				}
			}
			var matches []events.Schedule
			for _, row := range rows {
				if bound[row.UID] && row.Type == eventType && row.ID == contentID && row.SubID == subID && row.UID != 0 && row.Start < hub.End && hub.Start < row.End {
					matches = append(matches, row)
				}
			}
			if len(matches) == 0 {
				continue
			}
			if len(matches) > 1 {
				return nil, fmt.Errorf("eventplay: mini hub %d slot %d has ambiguous schedules", hub.UID, num(slot, 10))
			}
			child := matches[0]
			start, end := child.Start, child.End
			if start < hub.Start {
				start = hub.Start
			}
			if end > hub.End {
				end = hub.End
			}
			// Project policy follows static EndDateType: play content closes at
			// PlayEnd; exchange content can remain through the declared End.
			if num(slot, 4) == 0 && end > hub.PlayEnd {
				end = hub.PlayEnd
			}
			if end <= start {
				continue
			}
			b := wire.AppendVarint(nil, 1, num(slot, 10))
			b = wire.AppendVarint(b, 2, contentType)
			b = wire.AppendVarint(b, 3, contentID)
			b = wire.AppendVarint(b, 4, child.UID)
			b = wire.AppendVarint(b, 5, uint64(start))
			b = wire.AppendVarint(b, 6, uint64(end))
			v = wire.AppendBytes(v, 6, b)
		}
		out = wire.AppendBytes(out, 1, v)
	}
	return out, nil
}

// Public event packs retain their declared wire settings and both end windows.
// Mini hub prefabs are served exclusively by MiniEventHubInfo.
func (s *Service) publicEventHubs(ctx command.Context, req []byte) ([]byte, error) {
	_, body, _, err := s.hubCalendars.Handle(ctx, "/EventHubInfo", req)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = wire.Walk(body, func(f wire.Field) error {
		if f.Number != 1 || f.Type != 2 {
			return nil
		}
		row, err := s.design.Row("PackEventHubTable", 14, num(f.Value, 2))
		if err != nil {
			return nil
		}
		if num(row, 13) == 0 {
			out = wire.AppendBytes(out, 1, f.Value)
		}
		return nil
	})
	return out, err
}

// ListEventFieldPacks authorizes hidden fields through installed pack rules
// and the live project calendar. A hub field and a minigame field are separate
// routes: some public hubs have no PackEventMiniGameTable row at all.
func (s *Service) ListEventFieldPacks(ctx command.Context) ([]gamedata.EventFieldPack, error) {

	return s.eventFieldPacks(ctx)
}

func (s *Service) ResolveEventFieldPack(ctx command.Context, id int) (gamedata.EventFieldPack, bool, error) {

	packs, err := s.eventFieldPacks(ctx)
	if err != nil {
		return gamedata.EventFieldPack{}, false, err
	}
	for _, p := range packs {
		if p.ID == id {
			return p, true, nil
		}
	}
	return gamedata.EventFieldPack{}, false, nil
}

func (s *Service) eventFieldPacks(ctx command.Context) ([]gamedata.EventFieldPack, error) {
	now := s.now().UnixMilli()
	result := map[int]gamedata.EventFieldPack{}
	// Bound minigames use the hub play window even when their global schedule
	// has a longer archive window. Zero means a known, currently closed binding.
	boundEnds := map[uint64]int64{}
	add := func(id int, uid, game, hub, mapID, point uint64, end int64) error {
		base, ok := s.design.FieldPacks[id]
		if !ok {
			return nil
		}
		if len(base.MapIDs) == 0 {
			return fmt.Errorf("eventplay: hidden pack %d has no installed map", id)
		}
		if mapID != 0 {
			found := false
			for _, m := range base.MapIDs {
				if uint64(m) == mapID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("eventplay: pack %d calendar map %d mismatch", id, mapID)
			}
		}
		if old, exists := result[id]; exists && old.End >= end {
			return nil
		}
		base.MapIDs = append([]int(nil), base.MapIDs...)
		base.BuyRewards = append([]gamedata.Reward(nil), base.BuyRewards...)
		base.ScheduleUID, base.GameID, base.HubID, base.End = uid, game, hub, end
		base.InitialMapID, base.PointPositionID = mapID, point
		if base.InitialMapID == 0 {
			base.InitialMapID = uint64(base.MapIDs[0])
		}
		result[id] = base
		return nil
	}
	if s.hubCalendars != nil {
		_, body, handled, err := s.hubCalendars.Handle(ctx, "/EventHubInfo", wire.AppendVarint(nil, 1, 1))
		if err != nil {
			return nil, err
		}
		if handled {
			err = wire.Walk(body, func(f wire.Field) error {
				if f.Number != 1 || f.Type != 2 {
					return nil
				}
				start, end := int64(num(f.Value, 3)), int64(num(f.Value, 4))
				if err := wire.Walk(f.Value, func(setting wire.Field) error {
					if setting.Number != 6 || setting.Type != 2 || num(setting.Value, 2) != 6 {
						return nil
					}
					uids, err := list(setting.Value, 3)
					if err != nil {
						return err
					}
					for _, uid := range uids {
						if _, exists := boundEnds[uid]; !exists {
							boundEnds[uid] = 0
						}
						if now >= start && now < end && boundEnds[uid] < end {
							boundEnds[uid] = end
						}
					}
					return nil
				}); err != nil {
					return err
				}
				if now < start || now >= end {
					return nil
				}
				hubID := num(f.Value, 2)
				hub, e := s.design.Row("PackEventHubTable", 14, hubID)
				if e != nil {
					return e
				}
				return add(int(num(hub, 20)), num(f.Value, 1), 0, hubID, 0, 0, end)
			})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, c := range s.registry.List() {
		for _, binding := range s.fieldBindings {
			if c.Type == binding.EventType && now >= c.Start && now < c.End {
				if err := add(binding.PackID, c.UID, c.ID, 0, 0, 0, c.End); err != nil {
					return nil, err
				}
				p := result[binding.PackID]
				p.ContentOpenType = binding.ContentOpenType
				result[binding.PackID] = p
			}
		}
		if c.Type != 11 || now < c.Start || now >= c.End {
			continue
		}
		end := c.End
		if hubEnd, bound := boundEnds[c.UID]; bound {
			if hubEnd == 0 {
				continue
			}
			if hubEnd < end {
				end = hubEnd
			}
		}
		game, err := s.design.Row("PackEventMiniGameTable", 8, c.ID)
		if err != nil {
			return nil, err
		}
		if err = add(int(num(game, 12)), c.UID, c.ID, 0, num(game, 9), num(game, 13), end); err != nil {
			return nil, err
		}
	}
	var packs []gamedata.EventFieldPack
	for _, p := range result {
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].ID < packs[j].ID })
	return packs, nil
}

// FieldObjectEventPeriod follows EventLostCoinInfo: the exact public calendar
// must be active, and the installed event must explicitly contain this pack.
func (s *Service) FieldObjectEventPeriod(pack int) (string, int64, error) {

	now := s.now().UnixMilli()
	period := ""
	var end int64
	for _, schedule := range s.registry.List() {
		if schedule.Type != 14 || now < schedule.Start || now >= schedule.End {
			continue
		}
		row, err := s.design.Row("EventLostCoinTable", 4, schedule.ID)
		if err != nil {
			return "", 0, err
		}
		packs, err := list(row, 1)
		if err != nil {
			return "", 0, err
		}
		matches := false
		for _, id := range packs {
			matches = matches || id == uint64(pack)
		}
		if !matches {
			continue
		}
		if period != "" {
			return "", 0, fmt.Errorf("eventplay: ambiguous active lost coin calendar for pack %d", pack)
		}
		period = fmt.Sprintf("event:%d:%d:%d:%d", schedule.UID, schedule.ID, schedule.Start, schedule.End)
		end = schedule.End
	}
	if period == "" {
		return "", 0, fmt.Errorf("eventplay: no active lost coin calendar for pack %d", pack)
	}
	return period, end, nil
}

func (s *Service) EnterBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

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
	if validator, ok := s.economy.(interface {
		CanApply(ctx command.Context, _ []gamedata.Reward) error
	}); ok {
		cost := num(row, 3)
		if cost > 0 {
			if e := validator.CanApply(ctx, []gamedata.Reward{{Type: 30, Count: cost}}); e != nil {
				return nil, e
			}
		}
	}
	payload, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(payload, &next)
	next.Runs["battle:"+receipt] = Run{UID: uid, Game: group, Stage: id, Mode: 17, Started: uint64(s.now().UnixMilli()), Family: "Battle", Session: strings.Split(receipt, ":")[0]}
	payload, _ = json.Marshal(next)
	if e := s.store.Save(ctx.State, "eventplay", payload); e != nil {
		return nil, e
	}
	s.state = next
	return nil, nil
}

func (s *Service) CompleteBattle(ctx command.Context, req []byte, receipt string) ([]byte, error) {

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
		bundle, e := s.economy.Apply(ctx, key, costs, rewards)
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
	if e := s.store.Save(ctx.State, "eventplay", raw); e != nil {
		return nil, e
	}
	s.state = next
	return out, nil
}

func (s *Service) BattleChallengeIndexes(uid, group, stage uint64) []uint64 {

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
