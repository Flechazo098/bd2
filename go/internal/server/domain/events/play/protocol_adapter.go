package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
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
		if target == 0 || target != max(uint64(1), next.Upgrades[k])+1 {
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

func (s *Service) endScore(f string, req []byte, a Run) (uint64, error) {
	var score uint64
	switch f {
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
		d, err := s.survivalRule(a.Game)
		if err != nil {
			return 0, err
		}
		if score > num(d, 24) || uint64(s.now().UnixMilli()) < a.Started || score > (uint64(s.now().UnixMilli())-a.Started)/1000+1 {
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
	return s.design.ScoreRewards(num(d, 10), score, num(d, 3))
}

func (s *Service) progress(ctx command.Context, path string, req []byte, rk string, next *snapshot, key string) ([]byte, error) {
	a, ok := next.Runs[rk]
	if !ok {
		return nil, fmt.Errorf("eventplay: no active run")
	}
	var out []byte
	switch path {
	case "/MiniGameSurvivalPlay":
		previousKills := uint64(0)
		for _, count := range a.Killed {
			previousKills += count
		}
		exp, coin, e := s.survivalProgress(req, &a)
		if e != nil {
			return nil, e
		}
		if s.onProgress != nil {
			total := uint64(0)
			for _, count := range a.Killed {
				total += count
			}
			total -= previousKills
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
		out, e = s.survivalSelect(&a)
		if e != nil {
			return nil, e
		}
	case "/MiniGameSurvivalSkillUp":
		var e error
		out, e = s.survivalSkillUp(ctx, req, &a, next)
		if e != nil {
			return nil, e
		}
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

func (s *Service) specializedInfo(st *snapshot, uid uint64, f string) []byte {
	out := wire.AppendVarint(nil, 1, uid)
	if f == "Survival" {
		best := uint64(0)
		cleared := map[uint64]uint64{}
		for _, r := range st.Records {
			if r.UID != uid || r.Family != f {
				continue
			}
			if row, e := s.survivalMap(r.Mode, r.Stage); e == nil && num(row, 11) == 1 {
				best = max(best, r.Best)
			}
			for key, value := range r.Clears {
				if !value {
					continue
				}
				var group, stage uint64
				if _, e := fmt.Sscanf(key, "map:%d:%d", &group, &stage); e == nil {
					cleared[group] = max(cleared[group], stage)
				}
			}
		}
		out = wire.AppendVarint(out, 4, best)
		groups := make([]uint64, 0, len(cleared))
		for group := range cleared {
			groups = append(groups, group)
		}
		slices.Sort(groups)
		for _, group := range groups {
			v := wire.AppendVarint(nil, 1, group)
			v = wire.AppendVarint(v, 2, cleared[group])
			out = wire.AppendBytes(out, 8, v)
		}
		out = append(out, s.survivalUnlockInfo(st, uid)...)
		for _, row := range s.design.Tables["FieldMiniGameUpgradeGroupTable"] {
			id := num(row, 1)
			level := max(uint64(1), st.Upgrades[fmt.Sprintf("%d:%d", uid, id)])
			v := wire.AppendVarint(nil, 1, id)
			v = wire.AppendVarint(v, 2, level)
			out = wire.AppendBytes(out, 10, v)
		}
		return out
	}
	for _, key := range sortedRecordKeys(st) {
		r := st.Records[key]
		if r.UID != uid || r.Family != f {
			continue
		}
		if f == "Action" {
			if r.Stage > 0 {
				var monster uint64
				for _, stage := range s.design.Rows("ActionGameStageTable", 3, r.Mode) {
					if num(stage, 4) == r.Stage {
						monster = num(stage, 1)
					}
				}
				v := wire.AppendVarint(nil, 1, monster)
				v = wire.AppendVarint(v, 2, r.Best)
				out = wire.AppendBytes(out, 2, v)
			}
			for _, row := range s.design.Tables["ActionGameMissionTable"] {
				id := num(row, 4)
				if r.Clears[fmt.Sprintf("mission:%d", id)] {
					out = wire.AppendVarint(out, 3, id)
				}
			}
		}
		if f == "Defense" {
			for _, row := range s.design.Tables["MGDRewardTable"] {
				id := num(row, 1)
				if r.Clears[fmt.Sprintf("wave:%d", id)] {
					out = wire.AppendVarint(out, 2, id)
				}
			}
		}
	}
	return out
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
	case 9:
		if len(s.design.Tables["RhythmGameDefaultTable"]) > 0 {
			return num(s.design.Tables["RhythmGameDefaultTable"][0], 2), nil
		}
	case 13:
		if len(s.design.Tables["PackManDefaultTable"]) > 0 {
			return num(s.design.Tables["PackManDefaultTable"][0], 14), nil
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
	if path == "/MiniGameActionRanking" || path == "/MiniGameActionUserRecordInfo" || path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo" {
		if _, ok := s.rooms.(NativeRankingSource); ok {
			b, e := s.nativeRanking(ctx, path, req)
			return code, b, true, e
		}
	}
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
	if strings.Contains(path, "Ranking") || strings.Contains(path, "UserRecord") {
		b, e := s.arcadeRanking(ctx, path, req)
		return code, b, true, e
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
	case "/QuickBattle":
		out, e = s.quickBattle(ctx, req, key)
	case "/MiniGameRelayServerInfo":
		provider, ok := s.rooms.(RelayEndpointSource)
		if !ok {
			return fail(fmt.Errorf("eventplay: native relay channels are not configured"))
		}
		for _, endpoint := range provider.RelayEndpoints() {
			b := wire.AppendString(nil, 1, endpoint.Region)
			b = wire.AppendString(b, 2, endpoint.Host)
			b = wire.AppendString(b, 3, endpoint.TCPPort)
			out = wire.AppendBytes(out, 1, b)
		}

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
	if strings.HasSuffix(path, "End") {
		s.achievementProgress = nil
		if path == "/MiniGameRhythmPlayEnd" {
			s.completedRhythmResults = nil
		}
	}
	return code, out, true, nil
}

func (s *Service) recordWire(r record) []byte {
	if r.Date != uint64(s.now().UnixMilli())/86400000 {
		r.Paid = 0
	}
	b := wire.AppendVarint(nil, 1, r.UID)
	b = wire.AppendVarint(b, 2, r.Paid)
	b = wire.AppendVarint(b, 3, r.Best)
	if s.fieldQuickAvailable(r) {
		b = wire.AppendVarint(b, 4, 1)
	}
	return b
}

func (s *Service) calendar(uid uint64, f string) (events.Schedule, error) {
	if uid == 0 {
		rows := append([]events.Schedule(nil), s.registry.List()...)
		sort.Slice(rows, func(i, j int) bool {
			now := s.now().UnixMilli()
			ai := rows[i].Start <= now && now < rows[i].End
			aj := rows[j].Start <= now && now < rows[j].End
			if ai != aj {
				return ai
			}
			if rows[i].Start != rows[j].Start {
				return rows[i].Start > rows[j].Start
			}
			return rows[i].UID < rows[j].UID
		})
		for _, c := range rows {
			if c.Type == 11 && c.Start <= s.now().UnixMilli() {
				d, e := s.design.Row("PackEventMiniGameTable", 8, c.ID)
				if e != nil {
					continue
				}
				kind := num(d, 3)
				expected := map[string]uint64{"Field": 1, "Run": 3, "Survival": 5, "Sichuan": 6, "Defense": 7, "Action": 8, "Rhythm": 9, "Hopscotch": 12, "PackMan": 13}[f]
				if f == "Run" && kind == 4 || f == "Field" && kind == 2 {
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
	expected := map[string]uint64{"Field": 1, "Run": 3, "Survival": 5, "Sichuan": 6, "Defense": 7, "Action": 8, "Rhythm": 9, "Hopscotch": 12, "PackMan": 13}[f]
	if f == "Run" && kind == 4 || f == "Field" && kind == 2 {
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
	if f == "Sichuan" || f == "Hopscotch" || f == "PackMan" || f == "Rhythm" {
		return s.singlePlayerGame(ctx, path, req, next, key)
	}
	if f == "Run" || f == "Field" {
		return s.fieldRunGame(ctx, path, req, next, key)
	}
	start := strings.HasSuffix(path, "Start")
	end := strings.HasSuffix(path, "End")
	runKey := f
	uid := num(req, 2)
	if path == "/MiniGameSurvivalCharUpgrade" || path == "/MiniGameSurvivalCharUpgradeReset" {
		uid = 0
	}
	var native *NativeRun
	if start && (f == "Action" || f == "Defense") {
		if s.rooms == nil {
			return nil, fmt.Errorf("eventplay: native room unavailable")
		}
		admission, err := s.rooms.StartRun(ctx, f)
		if err != nil {
			return nil, err
		}
		if admission.Family != f || admission.UID == 0 || admission.InstanceID == "" || admission.GUID == "" || admission.Started <= 0 {
			return nil, fmt.Errorf("eventplay: invalid native admission")
		}
		native = &admission
		uid = admission.UID
	}
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
	if !strings.HasSuffix(path, "Info") {
		if now := s.now().UnixMilli(); now < c.Start || now >= c.End {
			return nil, fmt.Errorf("eventplay: season closed")
		}
	}
	stage, mode := num(req, 3), num(req, 4)
	if native != nil {
		stage, mode = native.Stage, native.Group
	}
	if f == "Rhythm" {
		mode = num(req, 5)
	}
	if f == "Survival" {
		stage = num(req, 5)
		mode = num(req, 4)
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
		if matching < 1 || matching > 3 {
			return nil, fmt.Errorf("eventplay: unknown matching mode")
		}
		if s.rooms == nil {
			return nil, fmt.Errorf("eventplay: native room unavailable")
		}
		_, err := s.rooms.StartRun(ctx, f)
		return nil, err
	}
	if start {
		if s.now().UnixMilli() < c.Start || s.now().UnixMilli() >= c.End {
			return nil, fmt.Errorf("eventplay: season closed")
		}
		if _, active := next.Runs[runKey]; active {
			previous := next.Runs[runKey]
			if previous.Native != nil && s.rooms == nil {
				return nil, fmt.Errorf("eventplay: native room unavailable")
			}
			if previous.Native != nil {
				if _, err := s.rooms.Result(ctx, *previous.Native); !errors.Is(err, ErrRoomAbandoned) {
					return nil, fmt.Errorf("eventplay: run already active")
				}
			}
			delete(next.Runs, runKey)
		}
		if e = s.validateStage(f, stage, mode); e != nil {
			return nil, e
		}
		a := Run{UID: uid, Game: c.ID, Stage: stage, Mode: mode, Started: uint64(s.now().UnixMilli()), Family: f, Session: session, HP: 100}
		if f == "Action" || f == "Defense" {
			a.Stage, a.Mode, a.Char = native.Stage, native.Group, native.Monster
			a.Started, a.Native = uint64(native.Started), native
			payload, err := json.Marshal(native)
			if err != nil {
				return nil, err
			}
			if err = ctx.Publish(command.Event{ID: "room-start:" + native.InstanceID + ":" + ctx.AccountID, Topic: "room.run.accepted", Payload: payload}); err != nil {
				return nil, err
			}
		}
		if f == "Survival" {
			if e := s.startSurvival(req, &a, next); e != nil {
				return nil, e
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
		rk = recKey(uid, a.Stage, a.Mode, f)
		r = playRecord(next, uid, c.ID, a.Stage, a.Mode, f)
		next.Records[rk] = r
		if f == "Survival" {
			v := wire.AppendVarint(nil, 1, a.Skills[0])
			v = wire.AppendVarint(v, 2, 1)
			out := wire.AppendBytes(nil, 1, v)
			return wire.AppendVarint(out, 2, seqSeed(a.Started)), nil
		}
		return nil, nil
	}
	if end {
		a := next.Runs[runKey]
		var nativeResult *NativeResult
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
			if a.Native == nil || !bytes.Equal(guid, []byte(a.Native.GUID)) {
				return nil, fmt.Errorf("eventplay: native room guid mismatch")
			}
			if s.rooms == nil {
				return nil, fmt.Errorf("eventplay: native result unavailable")
			}
			result, err := s.rooms.Result(ctx, *a.Native)
			if err != nil {
				return nil, err
			}
			nativeResult = &result
		}
		var score uint64
		if nativeResult != nil {
			score = nativeResult.Score
			if f == "Action" && !nativeResult.Clear {
				score = 0
			}
			if score > math.MaxInt32 {
				return nil, fmt.Errorf("eventplay: native score exceeds protocol range")
			}
		} else {
			score, e = s.endScore(f, req, a)
		}
		if e != nil {
			return nil, e
		}
		if s.onProgress != nil {
			switch f {
			case "Survival":
				for condition, count := range map[uint64]uint64{316: a.Level, 317: score} {
					if count == 0 {
						continue
					}
					if e = s.onProgress(ctx, condition, c.ID, count); e != nil {
						return nil, e
					}
				}
				mapRow, err := s.survivalMap(a.MapGroup, a.Stage)
				if err != nil {
					return nil, err
				}
				if num(req, 2) == 0 && score >= num(mapRow, 9) {
					if e = s.onProgress(ctx, 320, a.Stage, 1); e != nil {
						return nil, e
					}
				}
			case "Defense":
				if e = s.onProgress(ctx, 322, c.ID, score); e != nil {
					return nil, e
				}
			case "Action":
				if nativeResult.Clear {
					if e = s.onProgress(ctx, 323, a.Char, 1); e != nil {
						return nil, e
					}
				}
			}
		}
		rk = recKey(uid, a.Stage, a.Mode, f)
		r = next.Records[rk]
		if r.Clears == nil {
			r.Clears = map[string]bool{}
		}
		if f == "Action" && score > 0 && (r.Best == 0 || score < r.Best) || f != "Action" && score > r.Best {
			r.Best = score
			r.AchievedAt = uint64(s.now().UnixMilli())
		}
		var out []byte
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
			out, e = s.endSurvivalRewards(ctx, req, a, next, key, &r)
			if e != nil {
				return nil, e
			}
		}
		if f == "Action" {
			missionReply, err := s.actionMissions(ctx, req, a, *nativeResult, next, key)
			if err != nil {
				return nil, err
			}
			out = append(out, missionReply...)
			out = wire.AppendVarint(out, 1, r.Best)
		}
		next.Records[rk] = r
		if a.Native != nil {
			payload, err := json.Marshal(a.Native)
			if err != nil {
				return nil, err
			}
			if err = ctx.Publish(command.Event{ID: "room-claim:" + a.Native.InstanceID + ":" + ctx.AccountID, Topic: "room.run.claimed", Payload: payload}); err != nil {
				return nil, err
			}
		}
		delete(next.Runs, runKey)
		return out, nil
	}
	if strings.HasSuffix(path, "Info") {
		if _, exists := next.Records[rk]; !exists {
			next.Records[rk] = r
		}
		return s.specializedInfo(next, uid, f), nil
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
		hub, e := s.battleHub(ctx, a.UID, false)
		if e != nil {
			return nil, e
		}
		rs, e = s.battleBonus(ctx, hub, rs)
		if e != nil {
			return nil, e
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
