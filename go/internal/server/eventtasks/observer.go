package eventtasks

import (
	"bd2server/internal/server/world"
	"encoding/json"
	"fmt"
)

func (s *Service) BeginSession(id string) { s.SetSession(id) }
func (s *Service) inventorySnapshot() (world.GameplayAchievementSnapshot, error) {
	if provider, ok := s.provider.(interface {
		InventorySnapshot() (world.GameplayAchievementSnapshot, error)
	}); ok {
		return provider.InventorySnapshot()
	}
	return s.provider.Snapshot()
}
func (s *Service) BeforeDispatch(string, []byte) error {
	s.mu.Lock()
	s.beforeMissions = s.visibleMissionValues()
	s.mu.Unlock()
	if s.provider == nil {
		return nil
	}
	var e error
	s.before, e = s.inventorySnapshot()
	return e
}
func (s *Service) AttachGameplayProvider(p world.GameplayAchievementProvider) { s.provider = p }
func (s *Service) AfterDispatch(path string, request, response []byte) ([]byte, error) {
	if s.provider != nil {
		after, e := s.inventorySnapshot()
		if e != nil {
			return nil, e
		}
		rk := fmt.Sprintf("observer:%s:%s:%d", s.session, path, scalar(request, 1))
		s.mu.Lock()
		_, seen := s.state.Receipts[rk]
		s.mu.Unlock()
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
				s.mu.Lock()
				before, e := json.Marshal(s.state)
				if e != nil {
					s.mu.Unlock()
					return nil, e
				}
				for _, d := range deltas {
					s.recordEventLocked(d.condition, d.sub, d.count, s.unlocked)
				}
				s.state.Receipts[rk] = receipt{Digest: "observer"}
				e = s.save()
				if e != nil {
					s.state = snapshot{}
					_ = json.Unmarshal(before, &s.state)
				}
				s.mu.Unlock()
				if e != nil {
					return nil, e
				}
			}
		}
	}
	return s.notifyMissionChanges()
}

func (s *Service) CompleteSingleTargetEvent(condition uint64, unlocked func(uint64, uint64) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, _ := json.Marshal(s.state)
	for _, v := range s.taskSchedules() {
		if !s.active(v) {
			continue
		}
		for _, t := range s.design.Missions {
			if t.Type != condition || t.SubType != 0 || t.Target != 1 || len(t.Params) != 0 || !s.availableTask(v, t) {
				continue
			}
			if (t.UnlockPack > 0 || t.UnlockQuest > 0) && (unlocked == nil || !unlocked(t.UnlockPack, t.UnlockQuest)) {
				continue
			}
			s.mission(v, t.ID).Value = 1
		}
	}
	if e := s.save(); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return e
	}
	return nil
}
