package eventplay

import (
	"bd2server/internal/common/util"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/protocol/wire"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

func improvesFieldRecord(kind, current, previous uint64) bool {
	return current > 0 && (previous == 0 || kind == 3 && current < previous || kind != 3 && current > previous)
}

func selectFieldObjects(d *gamedata.EventField) (map[uint64]bool, error) {
	selected := map[uint64]bool{}
	groups := map[uint64][]uint64{}
	for id, object := range d.Objects {
		if object.RandomGroup == 0 {
			selected[id] = true
		} else {
			groups[object.RandomGroup] = append(groups[object.RandomGroup], id)
		}
	}
	for group, ids := range groups {
		count, configured := d.RandomCounts[group]
		if !configured || count > uint64(len(ids)) {
			return nil, fmt.Errorf("eventplay: field random count outside group %d", group)
		}
		slices.Sort(ids)
		for i := range count {
			n, err := util.RandomUint64N(uint64(len(ids)) - i)
			if err != nil {
				return nil, err
			}
			j := int(i) + int(n)
			ids[i], ids[j] = ids[j], ids[i]
			selected[ids[i]] = true
		}
	}
	for group, count := range d.RandomCounts {
		if count > uint64(len(groups[group])) {
			return nil, fmt.Errorf("eventplay: field random group %d unavailable", group)
		}
	}
	return selected, nil
}

func (s *Service) fieldRunGame(ctx command.Context, path string, req []byte, st *snapshot, key string) ([]byte, error) {
	f := family(path)
	if strings.HasSuffix(path, "Info") {
		uids, err := list(req, 2)
		if err != nil {
			return nil, err
		}
		var out []byte
		seen := map[uint64]bool{}
		for _, uid := range uids {
			c, err := s.calendar(uid, f)
			if err != nil {
				return nil, err
			}
			if seen[c.UID] {
				continue
			}
			seen[c.UID] = true
			game, err := s.design.Row("PackEventMiniGameTable", 8, c.ID)
			if err != nil {
				return nil, err
			}
			r := playRecord(st, c.UID, c.ID, 0, num(game, 3), f)
			body := s.recordWire(r)
			world, err := s.fieldWorldRecord(ctx, r, st)
			if err != nil {
				return nil, err
			}
			out = wire.AppendBytes(out, 1, append(body, world...))
		}
		return out, nil
	}
	uid := num(req, 2)
	a, active := st.Runs[f]
	if strings.HasSuffix(path, "End") || path == "/MiniGameFieldScore" {
		if !active {
			return nil, fmt.Errorf("eventplay: no active field run")
		}
		if path == "/MiniGameFieldScore" && uid != a.UID {
			return nil, fmt.Errorf("eventplay: field score schedule differs from run")
		}
		uid = a.UID
	}
	c, err := s.calendar(uid, f)
	if err != nil {
		return nil, err
	}
	if now := s.now().UnixMilli(); now < c.Start || now >= c.End {
		return nil, fmt.Errorf("eventplay: season closed")
	}
	game, err := s.design.Row("PackEventMiniGameTable", 8, c.ID)
	if err != nil {
		return nil, err
	}
	kind := num(game, 3)
	r := playRecord(st, c.UID, c.ID, 0, kind, f)
	if strings.HasSuffix(path, "Start") {
		a = Run{UID: c.UID, Game: c.ID, Family: f, Mode: kind, Started: uint64(s.now().UnixMilli()), Session: ctx.SessionID}
		var out []byte
		if f == "Field" {
			d, err := s.fieldRules(c.ID)
			if err != nil {
				return nil, err
			}
			a.FieldObjects, err = selectFieldObjects(d)
			if err != nil {
				return nil, err
			}
			a.HP = d.HP
			ids := make([]uint64, 0, len(a.FieldObjects))
			for id := range a.FieldObjects {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				out = wire.AppendVarint(out, 1, id)
			}
		}
		st.Runs[f] = a
		if s.onProgress != nil {
			if err := s.onProgress(ctx, conditions.Event{Type: conditions.MiniGamePlay, Sub: c.ID, Count: 1}); err != nil {
				return nil, err
			}
			if err := s.onProgress(ctx, conditions.Event{Type: conditions.EventFieldMiniGamePlay, Sub: c.ID, Count: 1}); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	if path == "/MiniGameFieldScore" {
		d, err := s.fieldRules(a.Game)
		if err != nil {
			return nil, err
		}
		ids, err := list(req, 3)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("eventplay: missing field objects")
		}
		if now := uint64(s.now().UnixMilli()); now < a.Started || now-a.Started > d.Limit*1000+5000 {
			return nil, fmt.Errorf("eventplay: field run time expired")
		}
		for _, id := range ids {
			object, exists := d.Objects[id]
			if !exists || !a.FieldObjects[id] || object.RandomGroup > 0 && has(a.Objects, id) {
				return nil, fmt.Errorf("eventplay: field object inactive or already collected")
			}
			if object.RandomGroup > 0 {
				a.Objects = append(a.Objects, id)
			}
			switch object.Type {
			case 1:
				a.Score = uint64(max(int64(0), int64(a.Score)+object.Point))
			case 2:
				a.HP = uint64(max(int64(0), min(int64(d.HP), int64(a.HP)-object.Point)))
			}
			if a.Score > math.MaxInt32 {
				return nil, fmt.Errorf("eventplay: field score exceeds protocol")
			}
		}
		st.Runs[f] = a
		return wire.AppendVarint(wire.AppendVarint(nil, 1, a.Score), 2, a.HP), nil
	}
	if strings.HasSuffix(path, "End") {
		score := a.Score
		var history string
		if f == "Run" {
			score, history, err = s.runResult(req, a, kind)
			if err != nil {
				return nil, err
			}
		}
		if improvesFieldRecord(kind, score, r.Best) {
			r.Best = score
			r.PlayInfo = history
			r.AchievedAt = uint64(s.now().UnixMilli())
		}
		bundle, err := s.payFieldRecord(ctx, &r, score, key)
		if err != nil {
			return nil, err
		}
		st.Records[recKey(r.UID, 0, kind, f)] = r
		delete(st.Runs, f)
		if f == "Run" {
			out := wire.AppendVarint(wire.AppendVarint(nil, 1, r.Best), 2, r.Paid)
			if len(bundle) > 0 {
				out = wire.AppendBytes(out, 3, bundle)
			}
			return wire.AppendVarint(out, 4, boolWire(s.fieldQuickAvailable(r))), nil
		}
		out := wire.AppendVarint(nil, 1, r.UID)
		out = wire.AppendVarint(out, 2, score)
		out = wire.AppendVarint(out, 3, r.Best)
		out = wire.AppendVarint(out, 4, r.Paid)
		if len(bundle) > 0 {
			out = wire.AppendBytes(out, 5, bundle)
		}
		return wire.AppendVarint(out, 6, boolWire(s.fieldQuickAvailable(r))), nil
	}
	if strings.HasSuffix(path, "QuickReward") || path == "/MiniGameFieldReward" {
		if !s.fieldQuickAvailable(r) {
			return nil, fmt.Errorf("eventplay: no unpaid field reward")
		}
		bundle, err := s.payFieldRecord(ctx, &r, r.Best, key)
		if err != nil {
			return nil, err
		}
		st.Records[recKey(r.UID, 0, kind, f)] = r
		if strings.HasSuffix(path, "QuickReward") {
			return wire.AppendBytes(nil, 1, bundle), nil
		}
		out := wire.AppendVarint(nil, 1, r.Paid)
		err = wire.Walk(bundle, func(field wire.Field) error {
			if field.Number == 1 {
				if field.Type != 2 {
					return wire.ErrMalformed
				}
				out = wire.AppendBytes(out, 2, field.Value)
			}
			return nil
		})
		return out, err
	}
	return nil, fmt.Errorf("eventplay: unsupported field run transition")
}

func (s *Service) payFieldRecord(ctx command.Context, r *record, score uint64, key string) ([]byte, error) {
	day := uint64(s.now().UnixMilli()) / 86400000
	if r.Date != day {
		r.Paid = 0
		r.Date = day
	}
	point, rewards, err := s.scoreRewards(r.Game, score)
	if err != nil {
		return nil, err
	}
	if !improvesFieldRecord(r.Mode, point, r.Paid) {
		return nil, nil
	}
	_, old, err := s.scoreRewards(r.Game, r.Paid)
	if err != nil {
		return nil, err
	}
	difference := rewardDifference(old, rewards)
	var bundle []byte
	if len(difference) > 0 {
		bundle, err = s.grant(ctx, "eventplay:"+key, difference)
		if err != nil {
			return nil, err
		}
	}
	r.Paid = point
	return bundle, nil
}

func (s *Service) fieldQuickAvailable(r record) bool {
	if r.Date != uint64(s.now().UnixMilli())/86400000 {
		r.Paid = 0
	}
	point, _, err := s.scoreRewards(r.Game, r.Best)
	return err == nil && improvesFieldRecord(r.Mode, point, r.Paid)
}

func (s *Service) fieldWorldRecord(ctx command.Context, r record, st *snapshot) ([]byte, error) {
	if s.arcadeRanks == nil || s.arcadeProfile == nil {
		return nil, fmt.Errorf("eventplay: arcade ranking unavailable")
	}
	profile, err := s.arcadeProfile(ctx)
	if err != nil {
		return nil, err
	}
	rows := s.arcadeRanks.Board(ranking.ArcadeScope{Family: r.Family, EventUID: r.UID, Mode: r.Mode}, profile, s.arcadeRecords(st), 1)
	if len(rows) == 0 {
		return nil, nil
	}
	best := rows[0]
	out := wire.AppendVarint(nil, 5, best.Profile.Owner)
	out = wire.AppendString(out, 6, best.Profile.Name)
	out = wire.AppendVarint(out, 7, uint64(best.Score))
	if best.PlayInfo != "" {
		out = wire.AppendString(out, 8, best.PlayInfo)
	}
	return out, nil
}

type runTimeline struct {
	TotalTime     float64
	TimelineDatas []struct {
		Time               float64
		Behaviour          int
		Position           struct{ X, Y, Z float64 }
		SpeedMagnification float64
	}
}

func (s *Service) runResult(req []byte, a Run, kind uint64) (uint64, string, error) {
	score := num(req, 3)
	if score > math.MaxInt32 {
		return 0, "", fmt.Errorf("eventplay: run time exceeds protocol")
	}
	if now := uint64(s.now().UnixMilli()); now < a.Started || score > now-a.Started+1000 {
		return 0, "", fmt.Errorf("eventplay: run time exceeds elapsed time")
	}
	raw, _, err := wire.Bytes(req, 4)
	if err != nil {
		return 0, "", err
	}
	if kind == 4 {
		d, err := s.fieldRules(a.Game)
		if err != nil {
			return 0, "", err
		}
		if score > d.Limit*1000 {
			return 0, "", fmt.Errorf("eventplay: dodge time exceeds limit")
		}
		if len(raw) != 0 {
			return 0, "", fmt.Errorf("eventplay: dodge has no run ghost")
		}
		return score, "", nil
	}
	if len(raw) == 0 {
		if num(req, 2) == 0 && score > 0 {
			return 0, "", fmt.Errorf("eventplay: completed run has no ghost")
		}
		return 0, "", nil
	}
	if num(req, 2) != 0 {
		return 0, "", fmt.Errorf("eventplay: abandoned run contains completed ghost")
	}
	if len(raw) > 512*1024 {
		return 0, "", fmt.Errorf("eventplay: run timeline too large")
	}
	var timeline runTimeline
	if err := json.Unmarshal(raw, &timeline); err != nil {
		return 0, "", fmt.Errorf("eventplay: invalid run timeline: %w", err)
	}
	if timeline.TotalTime <= 0 || math.Abs(timeline.TotalTime*1000-float64(score)) > 1000 || len(timeline.TimelineDatas) < 2 || len(timeline.TimelineDatas) > 20000 {
		return 0, "", fmt.Errorf("eventplay: run timeline time differs from result")
	}
	previous := float64(0)
	for _, sample := range timeline.TimelineDatas {
		if sample.Time < previous || sample.Time > timeline.TotalTime+0.001 || sample.Behaviour < 0 || sample.Behaviour > 7 || sample.SpeedMagnification < 0 || math.IsNaN(sample.SpeedMagnification) || math.IsInf(sample.SpeedMagnification, 0) {
			return 0, "", fmt.Errorf("eventplay: invalid run timeline sample")
		}
		previous = sample.Time
	}
	last := timeline.TimelineDatas[len(timeline.TimelineDatas)-1]
	if last.Behaviour != 7 || math.Abs(last.Time-timeline.TotalTime) > 0.001 {
		return 0, "", fmt.Errorf("eventplay: run timeline not completed")
	}
	return score, string(raw), nil
}
