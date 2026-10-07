package eventtasks

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"
)

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
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

	seq, present, err := wire.Varint(req, 1)
	if err != nil || !present || seq == 0 {
		return code, nil, true, errors.New("eventtasks: invalid sequence")
	}
	if err = wire.Walk(req, func(wire.Field) error { return nil }); err != nil {
		return code, nil, true, err
	}
	digest := sha256.Sum256(append([]byte(path), req...))
	ds := hex.EncodeToString(digest[:])
	rk := ctx.SessionID + ":" + path + ":" + key(seq)
	if r, ok := s.state.Receipts[rk]; ok {
		if r.Digest != ds {
			return code, nil, true, errors.New("eventtasks: changed replay request")
		}
		return r.Code, append([]byte(nil), r.Reply...), true, nil
	}
	before, _ := json.Marshal(s.state)
	out, err := s.handle(ctx, path, req, rk)
	if err != nil {
		s.state = snapshot{}
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, err
	}
	s.state.Receipts[rk] = receipt{ds, code, out}
	if err = s.save(ctx); err != nil {
		s.state = snapshot{}
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, err
	}
	return code, out, true, nil
}

func scalar(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }

func (s *Service) handle(ctx command.Context, path string, b []byte, identity string) ([]byte, error) {
	switch path {
	case "/Attendance", "/LoginEvent":
		today := s.day()
		if _, ok := s.state.LoginDays[today]; !ok {
			s.state.LoginDays[today] = s.now().UnixMilli()
		}
		var out []byte
		var granted []gamedata.Reward
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
			group := a.Group
			var ids []uint64
			if v.Type == 0 {
				for _, r := range s.design.AttendanceRewards[group] {
					ids = append(ids, r.ID)
				}
			} else {
				group = v.ID
				for k := range s.design.LimitRewards {
					if k[0] == group {
						ids = append(ids, k[1])
					}
				}
			}
			slices.Sort(ids)
			for _, id := range ids {
				rewards := s.attendanceRewards(ctx, v, a, group, id)
				if len(rewards) == 0 {
					continue
				}
				granted = append(granted, rewards...)
				a.Obtained[key(group, id)] = true
				a.History[key(group, id)] = true
				x := wire.AppendVarint(nil, 1, v.UID)
				x = wire.AppendVarint(x, 2, group)
				x = wire.AppendVarint(x, 3, id)
				out = wire.AppendBytes(out, 5, x)
			}
		}
		if len(granted) > 0 {
			if err := s.mailAttendance(ctx, identity, granted); err != nil {
				return nil, err
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
				slices.Sort(ids)
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
		rewards := s.attendanceRewards(ctx, v, a, g, id)
		if len(rewards) == 0 {
			return nil, errors.New("eventtasks: attendance day unavailable")
		}
		if err := s.mailAttendance(ctx, identity, rewards); err != nil {
			return nil, err
		}
		a.Obtained[ck] = true
		a.History[ck] = true
		return nil, nil
	case "/EventMissionInfo":
		var out []byte
		for _, v := range s.taskSchedules() {
			if v.Type != 4 || !s.active(v) {
				continue
			}
			for _, t := range s.design.Missions {
				if !s.availableTask(ctx, v, t) {
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
			if !ok || t.Group != scalar(f.Value, 1) || !s.availableTask(ctx, v, t) {
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
			if t.Group != g || !s.availableTask(ctx, v, t) || !all && t.ID != id {
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
		bundle, e := s.economy.Apply(ctx, identity, nil, rewards)
		if e != nil {
			return nil, e
		}
		for _, id := range done {
			s.mission(v, id).Claimed = true
		}
		// Completion-of-mission conditions are derived from durable claims,
		// rather than a client-supplied total.
		for _, t := range s.design.Missions {
			if !s.availableTask(ctx, v, t) || (t.Type != 1001 && t.Type != 1002) {
				continue
			}
			var completed uint64
			for _, other := range s.design.Missions {
				if !s.availableTask(ctx, v, other) || !s.mission(v, other.ID).Claimed {
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
			bundle, e = s.economy.Apply(ctx, identity, nil, rewards)
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
					if s.availableTask(ctx, mv, t) && !s.mission(mv, t.ID).Claimed {
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
		if chosen.CashID != 0 && (s.authorizeCash == nil || !s.authorizeCash(ctx, v.ID, typ)) {
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
		if _, e = s.economy.Apply(ctx, identity, costs, chosen.Rewards); e != nil {
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

func (s *Service) Notify(ctx command.Context) ([]byte, error) {

	var out []byte
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		groups := map[uint64][]byte{}
		for _, t := range s.design.Missions {
			if !s.availableTask(ctx, v, t) {
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

func (s *Service) AfterDispatch(ctx command.Context, path string, request, response []byte) ([]byte, error) {
	if s.provider != nil {
		version := s.inventoryObservationVersion()
		if s.inventoryReady && version != "" && version == s.observationVersion {
			return s.notifyMissionChanges(ctx)
		}
		s.inventoryReady = false
		after, e := s.inventorySnapshot(ctx)
		if e != nil {
			return nil, e
		}
		rk := fmt.Sprintf("observer:%s:%s:%d", ctx.SessionID, path, scalar(request, 1))

		_, seen := s.state.Receipts[rk]

		if !seen {
			type delta struct{ condition, sub, count uint64 }
			var deltas []delta
			for kind, old := range s.before.Items {
				current := after.Items[kind]
				if current < old {
					condition := uint64(12)
					if kind[1] == 0 {
						condition = 11
					}
					deltas = append(deltas, delta{condition, kind[0], old - current})
				}
			}
			for kind, current := range after.Items {
				old := s.before.Items[kind]
				if current > old {
					deltas = append(deltas, delta{32, kind[0], current - old})
				}
			}
			for idx, current := range after.Equipment {
				old, ok := s.before.Equipment[idx]
				if ok && current.Level > old.Level {
					deltas = append(deltas, delta{14, 0, current.Level - old.Level})
				}
			}
			for idx, current := range after.Costumes {
				old, ok := s.before.Costumes[idx]
				if ok && current.Level > old.Level {
					deltas = append(deltas, delta{104, current.ID, current.Level - old.Level})
				}
			}
			if len(deltas) > 0 {

				before, e := json.Marshal(s.state)
				if e != nil {

					return nil, e
				}
				changed := false
				for _, d := range deltas {
					changed = s.recordEventLocked(ctx, d.condition, d.sub, d.count, s.unlocked) || changed
				}
				if changed {
					s.state.Receipts[rk] = receipt{Digest: "observer"}
					e = s.save(ctx)
				}
				if e != nil {
					s.state = snapshot{}
					_ = json.Unmarshal(before, &s.state)
				}

				if e != nil {
					return nil, e
				}
			}
		}
		s.before = after
		s.observationVersion = s.inventoryObservationVersion()
		s.inventoryReady = true
	}
	return s.notifyMissionChanges(ctx)
}

func (s *Service) notifyMissionChanges(ctx command.Context) ([]byte, error) {

	if s.beforeMissions == nil {
		return nil, nil
	}
	current := s.visibleMissionValues(ctx)
	var keys []missionNoticeKey
	for k, value := range current {
		if value != s.beforeMissions[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Event != b.Event {
			return a.Event < b.Event
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		return a.Schedule < b.Schedule
	})
	var out, groupMap, rows []byte
	var eventID, groupID uint64
	flushGroup := func() {
		if len(rows) == 0 {
			return
		}
		entry := wire.AppendVarint(nil, 1, groupID)
		entry = wire.AppendBytes(entry, 2, rows)
		groupMap = wire.AppendBytes(groupMap, 1, entry)
		rows = nil
	}
	flushEvent := func() {
		flushGroup()
		if len(groupMap) == 0 {
			return
		}
		entry := wire.AppendVarint(nil, 1, eventID)
		entry = wire.AppendBytes(entry, 2, groupMap)
		out = wire.AppendBytes(out, 4, entry)
		groupMap = nil
	}
	for _, k := range keys {
		if k.Event != eventID {
			flushEvent()
			eventID = k.Event
			groupID = k.Group
		}
		if k.Group != groupID {
			flushGroup()
			groupID = k.Group
		}
		entry := wire.AppendVarint(nil, 1, k.Task)
		entry = wire.AppendVarint(entry, 2, current[k])
		rows = wire.AppendBytes(rows, 1, entry)
	}
	flushEvent()
	// Consume only this request's baseline; the next BeforeDispatch takes a fresh
	// snapshot even when a transaction rolls back or the session changes.
	s.beforeMissions = nil
	return out, nil
}
