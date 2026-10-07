package eventtasks

import (
	"bd2server/internal/server/domain/command"
	"encoding/json"
)

func (s *Service) BeginLogin(ctx command.Context) {

	s.inventoryReady = false
}

func (s *Service) inventoryObservationVersion() string {
	if provider, ok := s.provider.(interface{ ObservationVersion() string }); ok {
		return provider.ObservationVersion()
	}
	return ""
}
func (s *Service) inventorySnapshot(ctx command.Context) (InventorySnapshot, error) {
	return s.provider.InventorySnapshot(ctx)
}
func (s *Service) BeforeDispatch(ctx command.Context, _ string, _ []byte) error {

	s.beforeMissions = s.visibleMissionValues(ctx)

	if s.provider == nil {
		return nil
	}
	version := s.inventoryObservationVersion()
	if s.inventoryReady && version != "" && version == s.observationVersion {
		return nil
	}
	s.inventoryReady = false
	var e error
	s.before, e = s.inventorySnapshot(ctx)
	if e == nil {
		s.observationVersion = s.inventoryObservationVersion()
		s.inventoryReady = true
	}
	return e
}
func (s *Service) AttachInventoryProvider(p InventoryProvider) { s.provider = p }

func (s *Service) CompleteSingleTargetEvent(ctx command.Context, condition uint64, unlocked func(command.Context, uint64, uint64) bool) error {

	before, _ := json.Marshal(s.state)
	for _, v := range s.taskSchedules() {
		if !s.active(v) {
			continue
		}
		for _, t := range s.design.Missions {
			if t.Type != condition || t.SubType != 0 || t.Target != 1 || len(t.Params) != 0 || !s.availableTask(ctx, v, t) {
				continue
			}
			if (t.UnlockPack > 0 || t.UnlockQuest > 0) && (unlocked == nil || !unlocked(ctx, t.UnlockPack, t.UnlockQuest)) {
				continue
			}
			s.mission(v, t.ID).Value = 1
		}
	}
	if e := s.save(ctx); e != nil {
		_ = json.Unmarshal(before, &s.state)
		return e
	}
	return nil
}
