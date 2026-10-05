package eventtasks

import (
	"bd2server/internal/server/world"
	"encoding/json"
	"fmt"
)

func (s *Service) BeginSession(id string) { s.SetSession(id) }
func (s *Service) BeforeDispatch(string, []byte) error {
	if s.provider == nil {
		return nil
	}
	var e error
	s.before, e = s.provider.Snapshot()
	return e
}
func (s *Service) AttachGameplayProvider(p world.GameplayAchievementProvider) { s.provider = p }
func (s *Service) AfterDispatch(path string, request, response []byte) ([]byte, error) {
	if s.provider != nil {
		after, e := s.provider.Snapshot()
		if e != nil {
			return nil, e
		}
		rk := fmt.Sprintf("observer:%s:%s:%d", s.session, path, scalar(request, 1))
		s.mu.Lock()
		_, seen := s.state.Receipts[rk]
		s.mu.Unlock()
		if !seen {
			for kind, old := range s.before.Items {
				current := after.Items[kind]
				if current < old {
					condition := uint64(12)
					if kind[1] == 0 {
						condition = 11
					}
					if e = s.RecordEvent(condition, kind[0], old-current, s.unlocked); e != nil {
						return nil, e
					}
				}
			}
			for kind, current := range after.Items {
				old := s.before.Items[kind]
				if current > old {
					if e = s.RecordEvent(32, kind[0], current-old, s.unlocked); e != nil {
						return nil, e
					}
				}
			}
			for idx, current := range after.Equipment {
				old, ok := s.before.Equipment[idx]
				if ok && current.Level > old.Level {
					if e = s.RecordEvent(14, 0, current.Level-old.Level, s.unlocked); e != nil {
						return nil, e
					}
				}
			}
			for idx, current := range after.Costumes {
				old, ok := s.before.Costumes[idx]
				if ok && current.Level > old.Level {
					if e = s.RecordEvent(104, current.ID, current.Level-old.Level, s.unlocked); e != nil {
						return nil, e
					}
				}
			}
			s.mu.Lock()
			s.state.Receipts[rk] = receipt{Digest: "observer"}
			e = s.save()
			s.mu.Unlock()
			if e != nil {
				return nil, e
			}
		}
	}
	return s.Notify()
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
