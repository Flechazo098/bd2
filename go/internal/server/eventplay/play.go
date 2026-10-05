// Package eventplay owns event stories and client-simulated minigame runs.
package eventplay

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type Rewards interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type Run struct {
	UID, Game, Stage, Mode, Started, Score, HP uint64
	Family, Session                            string
	Objects, Skills                            []uint64
	Coin, Exp, Char, MapGroup                  uint64
	SkillLevels                                map[uint64]uint64
	MaxHP, Rerolls, Level, SkillCredits        uint64
	Offers                                     []uint64
	LevelExp                                   uint64
	Killed, Picked, Available                  map[uint64]uint64
}
type record struct {
	UID, Game, Stage, Mode, Best, Paid, Date uint64
	Family                                   string
	Clears                                   map[string]bool
	PendingBundle                            []byte
}
type reply struct{ Request, Body []byte }
type snapshot struct {
	Version  int               `json:"version"`
	Records  map[string]record `json:"records"`
	Runs     map[string]Run    `json:"runs"`
	Replies  map[string]reply  `json:"replies"`
	Stories  map[string]bool   `json:"stories"`
	Upgrades map[string]uint64 `json:"upgrades"`
}
type Service struct {
	battleChallenges gamedata.EventBattleChallenges
	mu               sync.Mutex
	store            stateio.Store
	registry         events.Resolver
	economy          Rewards
	design           *gamedata.EventPlayCatalog
	state            snapshot
	now              func() time.Time
	rooms            RoomRuntime
	onProgress       func(uint64, uint64, uint64) error
	hubCalendars     *readonly.Seed
	fieldBindings    []FieldBinding
}

func Open(store stateio.Store, root, version string, registry events.Resolver, economy Rewards) (*Service, error) {
	if store == nil || registry == nil || economy == nil {
		return nil, fmt.Errorf("eventplay: incomplete configuration")
	}
	d, e := gamedata.LoadEventPlayCatalog(root, version)
	if e != nil {
		return nil, e
	}
	s := &Service{store: store, registry: registry, economy: economy, design: d, now: time.Now, state: snapshot{1, map[string]record{}, map[string]Run{}, map[string]reply{}, map[string]bool{}, map[string]uint64{}}}
	raw, e := store.Load("eventplay")
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "version", "records", "runs", "replies", "stories", "upgrades"); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.state); e != nil {
			return nil, e
		}
		if s.state.Version != 1 || s.state.Records == nil || s.state.Runs == nil || s.state.Replies == nil || s.state.Stories == nil || s.state.Upgrades == nil {
			return nil, fmt.Errorf("eventplay: incompatible save")
		}
	}
	for _, r := range s.state.Records {
		if r.UID == 0 || r.Game == 0 || r.Best > math.MaxInt32 || r.Clears == nil {
			return nil, fmt.Errorf("eventplay: invalid saved record")
		}
		if _, e = s.design.Row("PackEventMiniGameTable", 8, r.Game); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func num(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }

// AttachHubCalendars installs project-maintained hub layouts, including the
// separate play/end windows and slots referencing multiple domain identities.
func (s *Service) AttachHubCalendars(seed *readonly.Seed) {
	s.hubCalendars = seed
}
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
func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, req, "local")
}
func (s *Service) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
			return s.hubCalendars.Handle(path, req)
		}
		if s.hubCalendars != nil && path == "/EventHubInfo" {
			out, err := s.publicEventHubs(req)
			return code, out, true, err
		}
		if path == "/MiniEventHubInfo" {
			out, err := s.miniEventHubs(req)
			return code, out, true, err
		}
		return code, nil, true, nil
	}
	key := fmt.Sprintf("%s:%s:%d", session, path, seq)
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
			out, e = s.grant("eventplay:story:"+storyKey, rs)
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
		out, e = s.game(path, req, session, &next, key)
	}
	if e != nil {
		return fail(e)
	}
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	b, e := json.Marshal(next)
	if e != nil {
		return fail(e)
	}
	if e = s.store.Save("eventplay", b); e != nil {
		return fail(e)
	}
	s.state = next
	return code, out, true, nil
}
func (s *Service) grant(identity string, rewards []gamedata.BattleReward) ([]byte, error) {
	rs := make([]gamedata.Reward, 0, len(rewards))
	for _, r := range rewards {
		rs = append(rs, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
	}
	return s.economy.Apply(identity, nil, rs)
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
func family(path string) string {
	p := strings.TrimPrefix(path, "/MiniGame")
	for _, f := range []string{"Hopscotch", "Survival", "Defense", "Rhythm", "Sichuan", "Action", "Field", "Run"} {
		if strings.HasPrefix(p, f) {
			return f
		}
	}
	return ""
}
func recKey(uid, stage, mode uint64, f string) string {
	return fmt.Sprintf("%d:%s:%d:%d", uid, f, stage, mode)
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
func (s *Service) game(path string, req []byte, session string, next *snapshot, key string) ([]byte, error) {
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
		return s.upgrade(path, req, uid, next, key)
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
			d, e := s.design.Field(c.ID)
			if e != nil {
				return nil, e
			}
			a.HP = d.HP
			ids := make([]uint64, 0, len(d.Objects))
			for id := range d.Objects {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
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
			if e = s.onProgress(343, c.ID, 1); e != nil {
				return nil, e
			}
			if e = s.onProgress(315, c.ID, 1); e != nil {
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
					if e = s.onProgress(condition, c.ID, count); e != nil {
						return nil, e
					}
				}
				if num(req, 2) == 0 {
					if e = s.onProgress(320, a.Stage, 1); e != nil {
						return nil, e
					}
				}
			case "Defense":
				if e = s.onProgress(322, c.ID, score); e != nil {
					return nil, e
				}
			case "Action":
				if e = s.onProgress(323, a.Char, 1); e != nil {
					return nil, e
				}
				for condition, field := range map[uint64]int{326: 9, 327: 10, 329: 12, 330: 13, 333: 14, 334: 15, 335: 16, 336: 17} {
					if count := num(req, field); count > 0 {
						if e = s.onProgress(condition, a.Char, count); e != nil {
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
					b, err := s.grant("eventplay:"+key+":"+ck, rr)
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
						b, err := s.grant(fmt.Sprintf("eventplay:map:%d:%d:%d", uid, a.MapGroup, a.Stage), rr)
						if err != nil {
							return nil, err
						}
						out = wire.AppendBytes(out, 1, b)
					}
				}
			}
			if a.Coin > 0 {
				b, err := s.grant("eventplay:"+key+":coin", []gamedata.BattleReward{{Type: 43, Count: a.Coin}})
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
			bundle, e := s.grant("eventplay:"+key, rewardDifference(previous, rewards))
			if e != nil {
				return nil, e
			}
			field := 1
			if f == "Run" {
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
		return s.claimScore(path, uid, f, next, key)
	}
	return s.progress(path, req, runKey, next, key)
}
