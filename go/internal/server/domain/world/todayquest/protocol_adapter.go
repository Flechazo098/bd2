package todayquest

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"sort"
)

func (s *Service) questWire(a Active) []byte {
	q := s.design.Quests[a.ID]
	b := wire.AppendVarint(nil, 1, uint64(a.ID))
	b = wire.AppendVarint(b, 2, uint64(a.Value))
	for _, id := range a.Objects {
		b = wire.AppendVarint(b, 3, id)
	}
	return wire.AppendVarint(b, 6, uint64(q.PackID))
}

func (s *Service) Info(ctx command.Context, pack int) ([][]byte, []int, error) {

	st, e := s.load(ctx)
	if e != nil {
		return nil, nil, e
	}
	if e = s.save(ctx, st); e != nil {
		return nil, nil, e
	}
	var rows [][]byte
	var cleared []int
	ids := []int{}
	for id := range st.Active {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if s.design.Quests[id].PackID == pack {
			rows = append(rows, s.questWire(st.Active[id]))
		}
	}
	for id := range st.Cleared {
		if s.design.Quests[id].PackID == pack {
			cleared = append(cleared, id)
		}
	}
	sort.Ints(cleared)
	return rows, cleared, nil
}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/TodayQuestInfo" && path != "/QuestAccept" && path != "/QuestUpdate" && path != "/QuestClear" && path != "/QuestGiveUp" {
		return 0, nil, false, nil
	}
	id := uint64(0)
	var e error
	if path != "/TodayQuestInfo" {
		id, _, e = wire.Varint(request, 2)
		if e != nil {
			return 0, nil, true, e
		}
		if _, ok := s.design.Quests[int(id)]; !ok {
			return 0, nil, false, nil
		}
	}

	seq, found, e := wire.Varint(request, 1)
	if e != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("todayquest: missing sequence")
	}
	st, e := s.load(ctx)
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/TodayQuestInfo" {
		var b []byte
		ids := []int{}
		for id := range st.Active {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			b = wire.AppendBytes(b, 1, s.questWire(st.Active[id]))
		}
		ids = nil
		for id := range st.Cleared {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			b = wire.AppendVarint(b, 2, uint64(id))
		}
		b = wire.AppendVarint(b, 3, s.secondsLeft(st))
		for _, id := range st.Offered {
			b = wire.AppendVarint(b, 4, uint64(id))
		}
		return 64, b, true, s.save(ctx, st)
	}
	q := s.design.Quests[int(id)]
	pack, _, e := wire.Varint(request, 3)
	if e != nil || int(pack) != q.PackID || !s.unlocked(ctx, q.PackID) {
		return 0, nil, true, fmt.Errorf("todayquest: unavailable pack")
	}
	a, active := st.Active[q.ID]
	switch path {
	case "/QuestAccept":
		level, _, e := wire.Varint(request, 4)
		opt, _, optErr := wire.Varint(request, 5)
		if e != nil || level != 0 || optErr != nil || opt != 0 {
			return 0, nil, true, fmt.Errorf("todayquest: invalid difficulty")
		}
		if !active {
			offered := false
			for _, x := range st.Offered {
				if x == q.ID {
					offered = true
				}
			}
			completed := 0
			for x := range st.Cleared {
				if s.design.Quests[x].NextID == 0 {
					completed++
				}
			}
			if q.PriorID != 0 || !offered || st.Cleared[q.ID] || completed+len(st.Active) >= s.design.Limit {
				return 0, nil, true, fmt.Errorf("todayquest: root not available or limit reached")
			}
			a = Active{ID: q.ID}
			st.Active[q.ID] = a
		}
		items, e := s.give(ctx, st, q.ID)
		if e != nil {
			return 0, nil, true, e
		}
		b := wire.AppendBytes(nil, 1, s.questWire(a))
		for _, it := range items {
			b = wire.AppendBytes(b, 4, assets.ItemWire(it))
		}
		return 17, b, true, s.save(ctx, st)
	case "/QuestGiveUp":
		if !active {
			return 0, nil, true, fmt.Errorf("todayquest: quest is not active")
		}
		delete(st.Active, q.ID)
		root := s.root(q.ID)
		for x := range st.Cleared {
			if s.root(x) == root {
				delete(st.Cleared, x)
			}
		}
		return 20, wire.AppendVarint(nil, 1, id), true, s.save(ctx, st)
	case "/QuestUpdate":
		if !active {
			return 0, nil, true, fmt.Errorf("todayquest: quest is not active")
		}
		vs, e := values(request, 4)
		if e != nil || len(vs) == 0 {
			return 0, nil, true, fmt.Errorf("todayquest: missing progress")
		}
		if q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18 {
			for _, v := range vs {
				allowed := false
				for _, x := range q.MagicValues {
					if x == v {
						allowed = true
					}
				}
				if !allowed {
					return 0, nil, true, fmt.Errorf("todayquest: foreign quest object")
				}
				seen := false
				for _, x := range a.Objects {
					if x == v {
						seen = true
					}
				}
				if !seen {
					a.Objects = append(a.Objects, v)
				}
			}
		} else {
			if len(vs) != 1 || vs[0] > uint64(q.ConditionCount) {
				return 0, nil, true, fmt.Errorf("todayquest: invalid progress")
			}
			if int(vs[0]) > a.Value {
				a.Value = int(vs[0])
			}
		}
		st.Active[q.ID] = a
		return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, id), 2, nil), true, s.save(ctx, st)
	case "/QuestClear":
		if b, ok := st.Responses[q.ID]; ok && st.Cleared[q.ID] {
			return 18, b, true, nil
		}
		if !active || (q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18) && len(a.Objects) < q.ConditionCount || !(q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18) && a.Value < q.ConditionCount { //nolint:staticcheck // QF1001
			return 0, nil, true, fmt.Errorf("todayquest: incomplete quest")
		}
		if q.ReputationCompleteID != 0 && s.CompleteReputation == nil {
			return 0, nil, true, fmt.Errorf("todayquest: reputation provider unavailable")
		}
		if q.NextID == 0 && s.CompleteAchievement == nil {
			return 0, nil, true, fmt.Errorf("todayquest: achievement provider unavailable")
		}
		bundle, e := s.economy.Apply(ctx, s.identity(st, q.ID, "reward"), nil, q.Rewards)
		if e != nil {
			return 0, nil, true, e
		}
		b := wire.AppendBytes(nil, 1, bundle)
		b = wire.AppendVarint(b, 3, id)
		delete(st.Active, q.ID)
		st.Cleared[q.ID] = true
		if q.ReputationCompleteID != 0 {
			rep, e := s.CompleteReputation(ctx, s.identity(st, q.ID, "reputation"), q.PackID, uint64(q.ReputationCompleteID))
			if e != nil {
				return 0, nil, true, e
			}
			b = wire.AppendBytes(b, 8, rep)
		}
		if q.NextID != 0 {
			next := Active{ID: q.NextID}
			st.Active[next.ID] = next
			b = wire.AppendBytes(b, 2, s.questWire(next))
			items, e := s.give(ctx, st, next.ID)
			if e != nil {
				return 0, nil, true, e
			}
			for _, it := range items {
				b = wire.AppendBytes(b, 6, assets.ItemWire(it))
			}
		} else {
			b = wire.AppendBytes(b, 2, nil)
			if e = s.CompleteAchievement(ctx, s.identity(st, q.ID, "achievement")); e != nil {
				return 0, nil, true, e
			}
			key := s.identity(st, q.ID, "score")
			if _, ok := st.ScoreAwards[key]; !ok {
				st.ScoreAwards[key] = uint64(s.design.AchievementScore)
			}
		}
		st.Responses[q.ID] = b
		return 18, b, true, s.save(ctx, st)
	}
	return 0, nil, false, nil
}

func values(raw []byte, number int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("todayquest: invalid progress type")
		}
		b := f.Value
		for len(b) > 0 {
			n, k := binary.Uvarint(b)
			if k <= 0 {
				return fmt.Errorf("todayquest: invalid packed progress")
			}
			out = append(out, n)
			b = b[k:]
			if f.Type == 0 && len(b) > 0 {
				return fmt.Errorf("todayquest: invalid scalar progress")
			}
		}
		return nil
	})
	return out, err
}
