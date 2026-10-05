// Package eventtasks owns attendance, scheduled missions and pass progress.
// Calendar policy is supplied by events.Resolver; requests never create dates
// or manufacture progress. The parent request transaction includes all saves.
package eventtasks

import (
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"bd2server/internal/server/world"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type attendance struct {
	Group, Count uint64
	LastDay      string
	Obtained     map[string]bool
	History      map[string]bool
}
type mission struct {
	Value   uint64
	Claimed bool
	Period  string
}
type pass struct {
	Exp     uint64
	Premium bool
	Claimed map[string]bool
}
type receipt struct {
	Digest string
	Code   int
	Reply  []byte
}
type snapshot struct {
	Version    string
	NewbieStep uint64
	Attendance map[string]*attendance
	Missions   map[string]*mission
	Passes     map[string]*pass
	LoginDays  map[string]int64
	Receipts   map[string]receipt
}
type Service struct {
	provider          world.GameplayAchievementProvider
	before            world.GameplayAchievementSnapshot
	beforeMissions    map[missionNoticeKey]uint64
	mu                sync.Mutex
	store             stateio.Store
	design            *gamedata.EventTasksDesign
	registry          events.Resolver
	economy           Economy
	state             snapshot
	now               func() time.Time
	session           string
	unlocked          func(uint64, uint64) bool
	authorizeCash     func(uint64, uint64) bool
	attendancePremium func(uint64) bool
	associated        func(events.Schedule) uint64
}

func Open(store stateio.Store, d *gamedata.EventTasksDesign, r events.Resolver, e Economy) (*Service, error) {
	if store == nil || d == nil || r == nil || e == nil {
		return nil, errors.New("eventtasks: missing dependency")
	}
	s := &Service{store: store, design: d, registry: r, economy: e, now: time.Now}
	b, err := store.Load("eventtasks")
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Version", "NewbieStep", "Attendance", "Missions", "Passes", "LoginDays", "Receipts"); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != versionconfig.State() || s.state.Attendance == nil || s.state.Missions == nil || s.state.Passes == nil || s.state.LoginDays == nil || s.state.Receipts == nil {
			return nil, errors.New("eventtasks: invalid state layout")
		}
		for _, a := range s.state.Attendance {
			if a == nil || a.Obtained == nil || a.History == nil {
				return nil, errors.New("eventtasks: invalid attendance")
			}
		}
		for _, m := range s.state.Missions {
			if m == nil {
				return nil, errors.New("eventtasks: invalid mission")
			}
		}
		for _, p := range s.state.Passes {
			if p == nil || p.Claimed == nil {
				return nil, errors.New("eventtasks: invalid pass")
			}
		}
	} else {
		s.state.Version = versionconfig.State()
	}
	s.init()
	return s, nil
}
func (s *Service) init() {
	if s.state.Attendance == nil {
		s.state.Attendance = map[string]*attendance{}
	}
	if s.state.Missions == nil {
		s.state.Missions = map[string]*mission{}
	}
	if s.state.Passes == nil {
		s.state.Passes = map[string]*pass{}
	}
	if s.state.LoginDays == nil {
		s.state.LoginDays = map[string]int64{}
	}
	if s.state.Receipts == nil {
		s.state.Receipts = map[string]receipt{}
	}
}
func (s *Service) SetSession(v string)                                         { s.mu.Lock(); defer s.mu.Unlock(); s.session = v }
func (s *Service) AttachUnlockResolver(f func(uint64, uint64) bool)            { s.unlocked = f }
func (s *Service) AttachCashAuthorization(f func(uint64, uint64) bool)         { s.authorizeCash = f }
func (s *Service) AttachAttendancePremium(f func(uint64) bool)                 { s.attendancePremium = f }
func (s *Service) AttachAssociatedMissionGroup(f func(events.Schedule) uint64) { s.associated = f }

func (s *Service) SetNewbieStep(step uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if step > s.state.NewbieStep {
		s.state.NewbieStep = step
	}
	return s.save()
}
func (s *Service) NewbieStep() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.state.NewbieStep }
func (s *Service) save() error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save("eventtasks", b)
}
func scalar(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }
func key(v ...uint64) string {
	var k string
	for i, n := range v {
		if i > 0 {
			k += "/"
		}
		k += strconv.FormatUint(n, 10)
	}
	return k
}

// scheduleKey preserves the public UID where it is present. UID zero is a
// protocol default shared by permanent groups, so its local identity also
// includes the event type and design group.
func scheduleKey(v events.Schedule) string {
	if v.UID != 0 {
		return key(v.UID)
	}
	return key(0, v.Type, v.ID, v.SubID)
}
func (s *Service) active(v events.Schedule) bool {
	n := s.now().UnixMilli()
	return n >= v.Start && n < v.End
}
func (s *Service) find(t, id uint64) (events.Schedule, error) {
	rows := s.registry.List()
	if t == 4 {
		rows = s.taskSchedules()
	}
	for _, v := range rows {
		if v.Type == t && v.ID == id && s.active(v) {
			return v, nil
		}
	}
	return events.Schedule{}, fmt.Errorf("eventtasks: event %d/%d inactive", t, id)
}

func (s *Service) taskSchedules() []events.Schedule {
	rows := s.registry.List()
	var out []events.Schedule
	seen := map[uint64]bool{}
	for _, v := range rows {
		if v.Type == 4 {
			out = append(out, v)
			if s.active(v) {
				seen[v.ID] = true
			}
		}
	}
	for _, v := range rows {
		if v.Type == 5 && s.active(v) {
			g := s.design.Passes[v.ID].MissionGroup
			if g > 0 && !seen[g] {
				v.Type = 4
				v.ID = g
				out = append(out, v)
				seen[g] = true
			}
		}
	}
	if s.associated != nil {
		for _, v := range rows {
			if !s.active(v) {
				continue
			}
			g := s.associated(v)
			if g > 0 && !seen[g] {
				v.Type = 4
				v.ID = g
				out = append(out, v)
				seen[g] = true
			}
		}
	}
	return out
}
func (s *Service) resolve(uid uint64) (events.Schedule, error) {
	v, e := s.registry.Resolve(uid)
	if e != nil {
		return v, e
	}
	if !s.active(v) {
		return v, errors.New("eventtasks: event expired")
	}
	return v, nil
}
func (s *Service) day() string { return s.now().UTC().Format("2006-01-02") }
func (s *Service) attendance(v events.Schedule) *attendance {
	k := scheduleKey(v)
	a := s.state.Attendance[k]
	if a == nil {
		g := s.design.Attendance[v.ID].Group
		var first uint64
		for _, x := range s.design.AttendanceGroups {
			if x.Group == g && (first == 0 || x.ID < first) {
				first = x.ID
			}
		}
		if first > 0 {
			g = first
		}
		a = &attendance{Group: g, Obtained: map[string]bool{}, History: map[string]bool{}}
		s.state.Attendance[k] = a
	}
	return a
}
func (s *Service) pass(v events.Schedule) *pass {
	k := scheduleKey(v)
	p := s.state.Passes[k]
	if p == nil {
		p = &pass{Claimed: map[string]bool{}}
		s.state.Passes[k] = p
	}
	return p
}
func (s *Service) mission(v events.Schedule, id uint64) *mission {
	k := scheduleKey(v) + "/" + key(id)
	m := s.state.Missions[k]
	if m == nil {
		m = &mission{}
		s.state.Missions[k] = m
	}
	g := s.design.MissionGroups[v.ID]
	period := ""
	if g.Type == 1 {
		period = s.day()
	}
	if g.Type == 2 {
		y, w := s.now().UTC().ISOWeek()
		period = fmt.Sprintf("%d-%d", y, w)
	}
	if m.Period != period {
		*m = mission{Period: period}
	}
	return m
}
func (s *Service) availableTask(v events.Schedule, t gamedata.EventTask) bool {
	g := s.design.MissionGroups[v.ID]
	for i, n := range g.Groups {
		if n == t.Group {
			if g.Type == 3 && int64(i) > (s.now().UnixMilli()-v.Start)/86400000 {
				return false
			}
			return t.UnlockPack == 0 && t.UnlockQuest == 0 || s.unlocked != nil && s.unlocked(t.UnlockPack, t.UnlockQuest)
		}
	}
	return false
}

func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/Attendance": 0, "/AttendanceInfo": 0, "/EventRewardHistory": 0, "/EventReward": 151, "/EventMissionInfo": 127, "/MissionClear": 120, "/MissionUpdate": 119, "/PassInfo": 124, "/PassReward": 126, "/PassBuy": 129, "/LoginEvent": 0}
	code, found := codes[path]
	if !found {
		return 0, nil, false, nil
	}
	if path == "/MissionClear" && scalar(req, 3) != 2 {
		return 0, nil, false, nil
	}
	if path == "/MissionUpdate" {
		handled := false
		_ = wire.Walk(req, func(f wire.Field) error {
			if f.Number == 2 && f.Type == 2 && scalar(f.Value, 4) > 0 {
				handled = true
			}
			return nil
		})
		if !handled {
			return 0, nil, false, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, present, err := wire.Varint(req, 1)
	if err != nil || !present || seq == 0 {
		return code, nil, true, errors.New("eventtasks: invalid sequence")
	}
	if err = wire.Walk(req, func(wire.Field) error { return nil }); err != nil {
		return code, nil, true, err
	}
	digest := sha256.Sum256(append([]byte(path), req...))
	ds := hex.EncodeToString(digest[:])
	rk := s.session + ":" + path + ":" + key(seq)
	if r, ok := s.state.Receipts[rk]; ok {
		if r.Digest != ds {
			return code, nil, true, errors.New("eventtasks: changed replay request")
		}
		return r.Code, append([]byte(nil), r.Reply...), true, nil
	}
	before, _ := json.Marshal(s.state)
	out, err := s.handle(path, req, rk)
	if err != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, err
	}
	s.state.Receipts[rk] = receipt{ds, code, out}
	if err = s.save(); err != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, err
	}
	return code, out, true, nil
}

func (s *Service) handle(path string, b []byte, identity string) ([]byte, error) {
	switch path {
	case "/Attendance", "/LoginEvent":
		today := s.day()
		if _, ok := s.state.LoginDays[today]; !ok {
			s.state.LoginDays[today] = s.now().UnixMilli()
		}
		var out []byte
		for _, v := range s.registry.List() {
			if !s.active(v) {
				continue
			}
			if v.Type > 1 {
				continue
			}
			a := s.attendance(v)
			if a.LastDay != today {
				if v.Type == 0 {
					complete, maxDay := true, uint64(0)
					for _, r := range s.design.AttendanceRewards[a.Group] {
						if r.Day > maxDay {
							maxDay = r.Day
						}
						if !a.Obtained[key(a.Group, r.ID)] {
							complete = false
						}
					}
					if complete && maxDay > 0 && a.Count >= maxDay {
						group := s.design.Attendance[v.ID].Group
						if next := s.design.AttendanceGroups[[2]uint64{group, a.Group}].Next; next > 0 {
							a.Group, a.Count = next, 0
							for _, r := range s.design.AttendanceRewards[next] {
								delete(a.Obtained, key(next, r.ID))
							}
						}
					}
				}
				a.LastDay = today
				a.Count++
				if v.Type == 0 {
					maxDay := uint64(0)
					for _, r := range s.design.AttendanceRewards[a.Group] {
						if r.Day > maxDay {
							maxDay = r.Day
						}
					}
					if maxDay > 0 && a.Count > maxDay {
						a.Count = maxDay
					}
				}
			}
			if path == "/LoginEvent" {
				x := wire.AppendVarint(nil, 1, v.UID)
				x = wire.AppendVarint(x, 2, uint64(s.now().UTC().Truncate(24*time.Hour).Add(24*time.Hour).UnixMilli()))
				out = wire.AppendBytes(out, 1, x)
				continue
			}
			if v.Type == 0 {
				x := wire.AppendVarint(nil, 1, v.UID)
				x = wire.AppendVarint(x, 2, a.Group)
				x = wire.AppendVarint(x, 3, a.Count)
				out = wire.AppendBytes(out, 1, x)
			} else {
				x := wire.AppendVarint(nil, 1, v.UID)
				for k, box := range s.design.LimitRewards {
					if k[0] != v.ID {
						continue
					}
					entry := wire.AppendVarint(nil, 1, k[1])
					entry = wire.AppendVarint(entry, 2, box)
					date := time.UnixMilli(v.Start).UTC().AddDate(0, 0, int(k[1])-1).Format("2006-01-02")
					entry = wire.AppendBytes(entry, 3, []byte(date))
					x = wire.AppendBytes(x, 2, entry)
				}
				out = wire.AppendBytes(out, 2, x)
			}
			for claim := range a.Obtained {
				var group, id uint64
				_, _ = fmt.Sscanf(claim, "%d/%d", &group, &id)
				x := wire.AppendVarint(nil, 1, v.UID)
				x = wire.AppendVarint(x, 2, group)
				x = wire.AppendVarint(x, 3, id)
				out = wire.AppendBytes(out, 5, x)
			}
		}
		return out, nil
	case "/AttendanceInfo":
		var out []byte
		start, end := scalar(b, 2), scalar(b, 3)
		days := make([]string, 0, len(s.state.LoginDays))
		for d := range s.state.LoginDays {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days {
			if len(d) != 10 {
				continue
			}
			n, _ := strconv.ParseUint(d[0:4]+d[5:7]+d[8:10], 10, 64)
			if (start == 0 || n >= start) && (end == 0 || n <= end) {
				out = wire.AppendVarint(out, 1, uint64(s.state.LoginDays[d]))
			}
		}
		return out, nil
	case "/EventRewardHistory":
		var out []byte
		for uid, a := range s.state.Attendance {
			groups := map[uint64][]uint64{}
			for k := range a.History {
				var g, id uint64
				_, _ = fmt.Sscanf(k, "%d/%d", &g, &id)
				groups[g] = append(groups[g], id)
			}
			var u uint64
			_, _ = fmt.Sscanf(uid, "%d", &u)
			for g, ids := range groups {
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				x := wire.AppendVarint(nil, 1, u)
				x = wire.AppendVarint(x, 2, g)
				for _, id := range ids {
					x = wire.AppendVarint(x, 3, id)
				}
				out = wire.AppendBytes(out, 1, x)
			}
		}
		return out, nil
	case "/EventReward":
		v, e := s.resolve(scalar(b, 2))
		if e != nil {
			return nil, e
		}
		if v.Type > 1 {
			return nil, errors.New("eventtasks: reward is not attendance")
		}
		a := s.attendance(v)
		g, id := scalar(b, 3), scalar(b, 4)
		ck := key(g, id)
		if a.Obtained[ck] {
			return nil, errors.New("eventtasks: attendance already received")
		}
		var rewards []gamedata.Reward
		if v.Type == 0 {
			if g != a.Group {
				return nil, errors.New("eventtasks: wrong attendance group")
			}
			for _, r := range s.design.AttendanceRewards[g] {
				if r.ID == id && r.Day <= a.Count {
					rewards = append(rewards, r.Basic)
					if ticket := s.design.Attendance[v.ID].Ticket; ticket != 0 && s.attendancePremium != nil && s.attendancePremium(ticket) && r.Premium.Count > 0 {
						rewards = append(rewards, r.Premium)
					}
				}
			}
		} else {
			box, ok := s.design.LimitRewards[[2]uint64{g, id}]
			date := time.UnixMilli(v.Start).UTC().AddDate(0, 0, int(id)-1).Format("2006-01-02")
			_, logged := s.state.LoginDays[date]
			if ok && g == v.ID && logged && date == s.day() {
				rewards = []gamedata.Reward{{Type: 9, ID: box, Count: 1}}
			}
		}
		if len(rewards) == 0 {
			return nil, errors.New("eventtasks: attendance day unavailable")
		}
		bundle, e := s.economy.Apply(identity, nil, rewards)
		if e != nil {
			return nil, e
		}
		a.Obtained[ck] = true
		a.History[ck] = true
		// Frozen EventRewardResponse has no known fields. The owned-client plugin
		// applies this actual grant once using the durable request receipt; Handle
		// persists the complete response for replay before the account commits.
		out := wire.AppendBytes(nil, 1001, bundle)
		out = wire.AppendString(out, 1002, identity)
		return out, nil
	case "/EventMissionInfo":
		var out []byte
		for _, v := range s.taskSchedules() {
			if v.Type != 4 || !s.active(v) {
				continue
			}
			for _, t := range s.design.Missions {
				if !s.availableTask(v, t) {
					continue
				}
				m := s.mission(v, t.ID)
				x := wire.AppendVarint(nil, 1, v.ID)
				x = wire.AppendVarint(x, 2, t.Group)
				x = wire.AppendVarint(x, 3, t.ID)
				x = wire.AppendVarint(x, 4, m.Value)
				if m.Claimed {
					x = wire.AppendVarint(x, 5, 1)
				}
				out = wire.AppendBytes(out, 1, x)
			}
		}
		return out, nil
	case "/MissionUpdate":
		err := wire.Walk(b, func(f wire.Field) error {
			if f.Number != 2 || f.Type != 2 {
				return nil
			}
			id, value, event := scalar(f.Value, 2), scalar(f.Value, 3), scalar(f.Value, 4)
			v, e := s.find(4, event)
			if e != nil {
				return e
			}
			t, ok := s.design.Missions[id]
			if !ok || t.Group != scalar(f.Value, 1) || !s.availableTask(v, t) {
				return errors.New("eventtasks: invalid mission update")
			}
			if value > s.mission(v, id).Value {
				return errors.New("eventtasks: client progress exceeds authoritative gameplay")
			}
			return nil
		})
		return nil, err
	case "/MissionClear":
		v, e := s.find(4, scalar(b, 6))
		if e != nil {
			return nil, e
		}
		all := scalar(b, 2) != 0
		g, id := scalar(b, 4), scalar(b, 5)
		var rewards []gamedata.Reward
		var done []uint64
		var exp uint64
		for _, t := range s.design.Missions {
			if t.Group != g || !s.availableTask(v, t) || !all && t.ID != id {
				continue
			}
			m := s.mission(v, t.ID)
			if !m.Claimed && m.Value >= t.Target {
				rewards = append(rewards, t.Rewards...)
				done = append(done, t.ID)
				exp += t.PassExp
			}
		}
		if len(done) == 0 {
			return nil, errors.New("eventtasks: no completed unclaimed mission")
		}
		bundle, e := s.economy.Apply(identity, nil, rewards)
		if e != nil {
			return nil, e
		}
		for _, id := range done {
			s.mission(v, id).Claimed = true
		}
		// Completion-of-mission conditions are derived from durable claims,
		// rather than a client-supplied total.
		for _, t := range s.design.Missions {
			if !s.availableTask(v, t) || (t.Type != 1001 && t.Type != 1002) {
				continue
			}
			var completed uint64
			for _, other := range s.design.Missions {
				if !s.availableTask(v, other) || !s.mission(v, other.ID).Claimed {
					continue
				}
				match := other.Group == t.Group
				if len(t.Params) > 0 {
					match = false
					for _, n := range t.Params {
						if n == other.Group || n == other.ID {
							match = true
						}
					}
				}
				if match {
					completed++
				}
			}
			m := s.mission(v, t.ID)
			if completed > t.Target {
				completed = t.Target
			}
			if completed > m.Value {
				m.Value = completed
			}
		}
		for _, pv := range s.registry.List() {
			if pv.Type == 5 && s.active(pv) && s.design.Passes[pv.ID].MissionGroup == v.ID {
				s.pass(pv).Exp += exp
			}
		}
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/PassInfo":
		var out []byte
		for _, v := range s.registry.List() {
			if v.Type != 5 || !s.active(v) {
				continue
			}
			p := s.pass(v)
			x := wire.AppendVarint(nil, 1, v.ID)
			x = wire.AppendVarint(x, 2, p.Exp)
			if p.Premium {
				x = wire.AppendVarint(x, 3, 1)
			}
			out = wire.AppendBytes(out, 1, x)
			d := s.design.Passes[v.ID]
			for _, lv := range s.design.PassLevels[d.LevelGroup] {
				basic, premium := p.Claimed[key(lv.ID, 0)], p.Claimed[key(lv.ID, 1)]
				if !basic && !premium {
					continue
				}
				x = wire.AppendVarint(nil, 1, v.ID)
				x = wire.AppendVarint(x, 2, lv.ID)
				if basic {
					x = wire.AppendVarint(x, 3, 1)
				}
				if premium {
					x = wire.AppendVarint(x, 4, 1)
				}
				out = wire.AppendBytes(out, 2, x)
			}
		}
		return out, nil
	case "/PassReward":
		v, e := s.find(5, scalar(b, 3))
		if e != nil {
			return nil, e
		}
		d, ok := s.design.Passes[v.ID]
		if !ok {
			return nil, errors.New("eventtasks: unknown pass")
		}
		p := s.pass(v)
		if d.NewbieStep > s.state.NewbieStep {
			return nil, errors.New("eventtasks: guide pass step locked")
		}
		all, id, rt := scalar(b, 2) != 0, scalar(b, 4), scalar(b, 5)
		if rt > 1 {
			return nil, fmt.Errorf("eventtasks: invalid pass reward kind pass=%d level=%d reward_type=%d all=%t", v.ID, id, rt, all)
		}
		if rt == 1 && !p.Premium {
			return nil, fmt.Errorf("eventtasks: premium pass required pass=%d level=%d reward_type=%d all=%t", v.ID, id, rt, all)
		}
		var rewards []gamedata.Reward
		var claims []string
		var threshold uint64
		for _, lv := range s.design.PassLevels[d.LevelGroup] {
			eligible := p.Exp >= threshold
			// NextNeedExp advances from this level to the next; level 1 starts at 0.
			threshold += lv.NeedExp
			if !eligible || !all && lv.ID != id {
				continue
			}
			// IsAll selects levels within RewardType. The native one-click flow
			// sends an all-basic request followed by a separate all-premium one.
			ck := key(lv.ID, rt)
			if p.Claimed[ck] {
				continue
			}
			r := lv.Basic
			if rt == 1 {
				r = lv.Premium
			}
			if r.Type > 0 && r.Count > 0 {
				rewards = append(rewards, r)
			}
			claims = append(claims, ck)
		}
		if len(claims) == 0 && !all {
			return nil, fmt.Errorf("eventtasks: pass reward not eligible pass=%d level=%d reward_type=%d all=%t exp=%d premium=%t", v.ID, id, rt, all, p.Exp, p.Premium)
		}
		var bundle []byte
		if len(claims) > 0 {
			bundle, e = s.economy.Apply(identity, nil, rewards)
			if e != nil {
				return nil, fmt.Errorf("eventtasks: pass reward grant failed pass=%d level=%d reward_type=%d all=%t: %w", v.ID, id, rt, all, e)
			}
		}
		for _, k := range claims {
			p.Claimed[k] = true
		}
		// An already-claimed all-basic phase must still succeed so the client
		// can proceed to all-premium. Include a non-null empty reward bundle.
		out := wire.AppendBytes(nil, 1, bundle)
		if d.NewbieStep > 0 {
			complete := true
			for _, lv := range s.design.PassLevels[d.LevelGroup] {
				if !p.Claimed[key(lv.ID, 0)] {
					complete = false
				}
			}
			mv, err := s.find(4, d.MissionGroup)
			if err != nil {
				complete = false
			} else {
				for _, t := range s.design.Missions {
					if s.availableTask(mv, t) && !s.mission(mv, t.ID).Claimed {
						complete = false
					}
				}
			}
			if complete && s.state.NewbieStep == d.NewbieStep {
				s.state.NewbieStep++
			}
			// The native receiver assigns this field even on an incomplete step.
			out = wire.AppendVarint(out, 2, s.state.NewbieStep)
		}
		return out, nil
	case "/PassBuy":
		v, e := s.find(5, scalar(b, 2))
		if e != nil {
			return nil, e
		}
		p := s.pass(v)
		typ := scalar(b, 3)
		var chosen *gamedata.EventPassBuy
		for _, b := range s.design.PassBuys[v.ID] {
			if b.Type == typ {
				copy := b
				chosen = &copy
				break
			}
		}
		if chosen == nil {
			return nil, errors.New("eventtasks: unknown pass purchase")
		}
		d := s.design.Passes[v.ID]
		if d.NewbieStep > s.state.NewbieStep {
			return nil, fmt.Errorf("eventtasks: guide pass step locked pass=%d type=%d", v.ID, typ)
		}
		levels := s.design.PassLevels[d.LevelGroup]
		if len(levels) == 0 {
			return nil, fmt.Errorf("eventtasks: missing pass levels pass=%d type=%d", v.ID, typ)
		}
		starts := make([]uint64, len(levels))
		levelIndex := 0
		for i := 1; i < len(levels); i++ {
			starts[i] = starts[i-1] + levels[i-1].NeedExp
			if p.Exp >= starts[i] {
				levelIndex = i
			}
		}
		if typ == 0 && levelIndex == len(levels)-1 {
			return nil, fmt.Errorf("eventtasks: pass already at max level pass=%d type=%d", v.ID, typ)
		}
		if p.Premium && (typ == 1 || typ == 3) {
			return nil, fmt.Errorf("eventtasks: premium already active pass=%d type=%d", v.ID, typ)
		}
		// CashShopBuy charges paid diamonds and grants the pass ticket first.
		// Activation consumes that purchase entitlement and the design ticket.
		if chosen.CashID != 0 && (s.authorizeCash == nil || !s.authorizeCash(v.ID, typ)) {
			return nil, fmt.Errorf("eventtasks: cash entitlement required pass=%d type=%d sku=%d/%d/%d", v.ID, typ, chosen.CashGroup, chosen.CashID, chosen.CashSales)
		}
		var costs []gamedata.Reward
		if chosen.Cost.Count > 0 {
			cost := chosen.Cost
			if typ == 0 {
				// PassRootUI.GetLevelPassBuyWeight uses the current level.
				cost.Count *= levels[levelIndex].ID
			}
			costs = append(costs, cost)
		}
		if _, e = s.economy.Apply(identity, costs, chosen.Rewards); e != nil {
			return nil, fmt.Errorf("eventtasks: pass payment failed pass=%d type=%d: %w", v.ID, typ, e)
		}
		if chosen.LevelsGranted > 0 {
			target := uint64(levelIndex) + min(chosen.LevelsGranted, uint64(len(levels)-1-levelIndex))
			// Preserve progress within the current level and discard surplus at MAX.
			p.Exp = min(starts[target]+p.Exp-starts[levelIndex], starts[len(levels)-1])
		}
		if typ == 1 || typ == 3 {
			p.Premium = true
		}
		return wire.AppendVarint(nil, 1, p.Exp), nil
	}
	return nil, errors.New("eventtasks: unsupported operation")
}

// RecordEvent is called by authoritative gameplay. It returns no rewards and
// caps every matching task at its design threshold.
func (s *Service) RecordEvent(condition, sub, count uint64, unlocked func(uint64, uint64) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if count == 0 {
		return nil
	}
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		for _, t := range s.design.Missions {
			if t.Type != condition || t.SubType != 0 && t.SubType != sub || !s.availableTask(v, t) {
				continue
			}
			if (t.UnlockPack > 0 || t.UnlockQuest > 0) && (unlocked == nil || !unlocked(t.UnlockPack, t.UnlockQuest)) {
				continue
			}
			match := len(t.Params) == 0
			for _, n := range t.Params {
				if n == sub {
					match = true
				}
			}
			if !match {
				continue
			}
			m := s.mission(v, t.ID)
			if m.Claimed {
				continue
			}
			if m.Value >= t.Target {
				continue
			}
			if count >= t.Target-m.Value {
				m.Value = t.Target
			} else {
				m.Value += count
			}
		}
	}
	return s.save()
}
func (s *Service) Notify() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		groups := map[uint64][]byte{}
		for _, t := range s.design.Missions {
			if !s.availableTask(v, t) {
				continue
			}
			m := s.mission(v, t.ID)
			if m.Value == 0 {
				continue
			}
			entry := wire.AppendVarint(nil, 1, t.ID)
			entry = wire.AppendVarint(entry, 2, m.Value)
			groups[t.Group] = wire.AppendBytes(groups[t.Group], 1, entry)
		}
		var groupMap []byte
		for g, rows := range groups {
			entry := wire.AppendVarint(nil, 1, g)
			entry = wire.AppendBytes(entry, 2, rows)
			groupMap = wire.AppendBytes(groupMap, 1, entry)
		}
		entry := wire.AppendVarint(nil, 1, v.ID)
		entry = wire.AppendBytes(entry, 2, groupMap)
		out = wire.AppendBytes(out, 4, entry)
	}
	return out, nil
}

// RecordLogin shares the same daily marker as Attendance, preventing repeated
// reconnects from inflating event login tasks. Main should invoke this once
// through the regular mission observer before serving attendance queries.
func (s *Service) RecordLogin(unlocked func(uint64, uint64) bool) error {
	s.mu.Lock()
	marker := "mission-login:" + s.day()
	if _, ok := s.state.LoginDays[marker]; ok {
		s.mu.Unlock()
		return nil
	}
	s.state.LoginDays[marker] = s.now().UnixMilli()
	s.mu.Unlock()
	return s.RecordEvent(0, 0, 1, unlocked)
}
