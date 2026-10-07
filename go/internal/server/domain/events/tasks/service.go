// Package eventtasks owns attendance, scheduled missions and pass progress.
// Calendar policy is supplied by events.Resolver; requests never create dates
// or manufacture progress. The parent request transaction includes all saves.
package eventtasks

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"time"
)

type Economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}

// AttendanceMailIssuer delivers attendance attachments inside the parent
// account transaction. Its durable identity prevents duplicate mail on replay.
type AttendanceMailIssuer interface {
	IssueAttachmentsOnce(ctx command.Context, identity, title, body string, rewards []gamedata.Reward, sentAt time.Time) error
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
	provider           InventoryProvider
	before             InventorySnapshot
	observationVersion string
	inventoryReady     bool
	beforeMissions     map[missionNoticeKey]uint64
	visibleMissions    map[missionNoticeKey]uint64
	visibleVersion     string

	store    stateio.Store
	design   *gamedata.EventTasksDesign
	registry events.Resolver
	economy  Economy
	state    snapshot
	now      func() time.Time

	unlocked          func(command.Context, uint64, uint64) bool
	authorizeCash     func(command.Context, uint64, uint64) bool
	attendancePremium func(command.Context, uint64) bool
	attendanceMail    AttendanceMailIssuer
	associated        func(events.Schedule) uint64
}

func Open(ctx command.Context, store stateio.Store, d *gamedata.EventTasksDesign, r events.Resolver, e Economy) (*Service, error) {
	if store == nil || d == nil || r == nil || e == nil {
		return nil, errors.New("eventtasks: missing dependency")
	}
	s := &Service{store: store, design: d, registry: r, economy: e, now: time.Now}
	b, err := store.Load(ctx.State, "eventtasks")
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

func (s *Service) AttachUnlockResolver(f func(command.Context, uint64, uint64) bool) { s.unlocked = f }
func (s *Service) AttachCashAuthorization(f func(command.Context, uint64, uint64) bool) {
	s.authorizeCash = f
}
func (s *Service) AttachAttendancePremium(f func(command.Context, uint64) bool) {
	s.attendancePremium = f
}
func (s *Service) AttachAssociatedMissionGroup(f func(events.Schedule) uint64) { s.associated = f }

func (s *Service) AttachAttendanceMail(issuer AttendanceMailIssuer) { s.attendanceMail = issuer }

func (s *Service) mailAttendance(ctx command.Context, identity string, rewards []gamedata.Reward) error {
	if s.attendanceMail == nil {
		return errors.New("eventtasks: attendance mailbox unavailable")
	}
	return s.attendanceMail.IssueAttachmentsOnce(ctx, identity, "Attendance Rewards", "Your attendance rewards are ready. Please claim the attachments in your mailbox.", rewards, s.now())
}

func (s *Service) SetNewbieStep(ctx command.Context, step uint64) error {

	if step > s.state.NewbieStep {
		s.state.NewbieStep = step
	}
	return s.save(ctx)
}
func (s *Service) NewbieStep() uint64 { return s.state.NewbieStep }
func (s *Service) save(ctx command.Context) error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "eventtasks", b)
}

func key(v ...uint64) string {
	var k string
	for i, n := range v {
		if i > 0 {
			k += "/" //nolint:modernize // stringsbuilder
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

// attendanceRewards is shared by automatic and explicit claims. Obtained is
// the current cycle ledger; History is the client-visible historical ledger.
func (s *Service) attendanceRewards(ctx command.Context, v events.Schedule, a *attendance, group, id uint64) []gamedata.Reward {
	if a.Obtained[key(group, id)] {
		return nil
	}
	if v.Type == 0 {
		if group != a.Group {
			return nil
		}
		for _, r := range s.design.AttendanceRewards[group] {
			if r.ID != id || r.Day > a.Count {
				continue
			}
			var rewards []gamedata.Reward
			if r.Basic.Count > 0 {
				rewards = append(rewards, r.Basic)
			}
			if ticket := s.design.Attendance[v.ID].Ticket; ticket != 0 && s.attendancePremium != nil && s.attendancePremium(ctx, ticket) && r.Premium.Count > 0 {
				rewards = append(rewards, r.Premium)
			}
			return rewards
		}
	} else if v.Type == 1 && group == v.ID && id > 0 {
		box, ok := s.design.LimitRewards[[2]uint64{group, id}]
		date := time.UnixMilli(v.Start).UTC().AddDate(0, 0, int(id)-1).Format("2006-01-02")
		_, logged := s.state.LoginDays[date]
		if ok && box > 0 && logged && date == s.day() {
			return []gamedata.Reward{{Type: 9, ID: box, Count: 1}}
		}
	}
	return nil
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
func (s *Service) availableTask(ctx command.Context, v events.Schedule, t gamedata.EventTask) bool {
	g := s.design.MissionGroups[v.ID]
	for i, n := range g.Groups {
		if n == t.Group {
			if g.Type == 3 && int64(i) > (s.now().UnixMilli()-v.Start)/86400000 {
				return false
			}
			return t.UnlockPack == 0 && t.UnlockQuest == 0 || s.unlocked != nil && s.unlocked(ctx, t.UnlockPack, t.UnlockQuest)
		}
	}
	return false
}

// RecordEvent is called by authoritative gameplay. It returns no rewards and
// caps every matching task at its design threshold.
func (s *Service) RecordEvent(ctx command.Context, condition, sub, count uint64, unlocked func(command.Context, uint64, uint64) bool) error {

	if !s.recordEventLocked(ctx, condition, sub, count, unlocked) {
		return nil
	}
	return s.save(ctx)
}

// recordEventLocked reports every persistent mutation, including initializing
// a matching task or rolling its period. Irrelevant and already capped events
// leave the account snapshot untouched.
func (s *Service) recordEventLocked(ctx command.Context, condition, sub, count uint64, unlocked func(command.Context, uint64, uint64) bool) bool {
	changed := false
	if count == 0 {
		return false
	}
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		for _, t := range s.design.Missions {
			if t.Type != condition || t.SubType != 0 && t.SubType != sub || condition == 349 && t.SubType != sub || !s.availableTask(ctx, v, t) {
				continue
			}
			if (t.UnlockPack > 0 || t.UnlockQuest > 0) && (unlocked == nil || !unlocked(ctx, t.UnlockPack, t.UnlockQuest)) {
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
			previous := s.state.Missions[scheduleKey(v)+"/"+key(t.ID)]
			var prior mission
			if previous != nil {
				prior = *previous
			}
			m := s.mission(v, t.ID)
			if previous == nil || prior != *m {
				changed = true
			}
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
			changed = true
		}
	}
	return changed
}

// RecordLogin shares the same daily marker as Attendance, preventing repeated
// reconnects from inflating event login tasks. Main should invoke this once
// through the regular mission observer before serving attendance queries.
func (s *Service) RecordLogin(ctx command.Context, unlocked func(command.Context, uint64, uint64) bool) error {

	marker := "mission-login:" + s.day()
	if _, ok := s.state.LoginDays[marker]; ok {

		return nil
	}
	s.state.LoginDays[marker] = s.now().UnixMilli()

	return s.RecordEvent(ctx, 0, 0, 1, unlocked)
}
