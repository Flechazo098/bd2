package eventtasks

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
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

	if err := s.reconcileRewardLevelProgress(ctx); err != nil {
		return err
	}
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

// AttachProgress routes observed inventory deltas through the account-wide
// mission recorder, so regular daily/weekly missions see them as well.
func (s *Service) AttachProgress(record func(command.Context, conditions.Event) error) {
	s.progress = record
}

func (s *Service) record(ctx command.Context, event conditions.Event) error {
	if s.progress != nil {
		return s.progress(ctx, event)
	}
	return s.RecordEvent(ctx, event, s.unlocked)
}
