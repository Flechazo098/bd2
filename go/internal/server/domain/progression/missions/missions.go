// Package missions owns local completion eligibility and claim state for the
// three regular (non-scheduled-event) mission endpoints. Static definitions
// come from GameData; this package never replays an HTTP capture.
package missions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"time"
)

const (
	missionClearPacket     = 120
	missionSectionPacket   = 122
	achievementClearPacket = 168
)

var ErrInvalidRequest = errors.New("missions: invalid request")

// Service is deliberately given completion signals by authoritative gameplay
// code. A request cannot manufacture a mission completion merely by naming a
// table row. AchievementClear is different: its request carries the exact
// client-calculated completed achievement ids, as in the official protocol.
type AchievementProgressSource interface {
	AchievementValue(ctx command.Context, groupID uint64) (uint64, error)
}

func (s *Service) AttachAchievementProgress(ctx command.Context, source AchievementProgressSource) error {
	if source == nil {
		return errors.New("missions: nil achievement progress")
	}
	s.achievementProgress = source
	return nil
}

type Service struct {
	eventHandler interface {
		Handle(ctx command.Context, _ string, _ []byte) (int, []byte, bool, error)
	}
	levelRewards        *gamedata.AchievementLevelDesign
	levelReward         uint64
	achievementProgress AchievementProgressSource

	storage   stateio.Store
	design    *gamedata.MissionDesign
	inventory *assets.Inventory
	wallet    *assets.Wallet
	mail      *mail.Service
	state     snapshot
	now       func() time.Time
}

func (s *Service) AttachWallet(ctx command.Context, wallet *assets.Wallet) error {
	if wallet == nil {
		return errors.New("missions: nil wallet")
	}
	s.wallet = wallet
	return nil
}

type snapshot struct {
	Version      string            `json:"version"`
	DailyPeriod  string            `json:"daily_period"`
	WeeklyPeriod string            `json:"weekly_period"`
	Completed    []string          `json:"completed"`
	Claimed      []string          `json:"claimed"`
	Progress     map[string]uint64 `json:"progress"`
}

func Open(ctx command.Context, storage stateio.Store, design *gamedata.MissionDesign, inventory *assets.Inventory) (*Service, error) {
	if storage == nil || design == nil || inventory == nil {
		return nil, errors.New("missions: invalid service configuration")
	}
	now := time.Now().UTC()
	s := &Service{storage: storage, design: design, inventory: inventory, now: time.Now, state: snapshot{
		Version: versionconfig.State(), DailyPeriod: dailyPeriod(now), WeeklyPeriod: weeklyPeriod(now), Progress: map[string]uint64{},
	}}
	b, err := storage.Load(ctx.State, "missions")
	if err != nil {
		return nil, fmt.Errorf("missions: load state: %w", err)
	}
	if b == nil {
		return s, nil
	}
	if err := stateio.RequireExactJSONObject(b, "version", "daily_period", "weekly_period", "completed", "claimed", "progress"); err != nil {
		return nil, fmt.Errorf("missions: incompatible state layout: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != versionconfig.State() || s.state.DailyPeriod == "" || s.state.WeeklyPeriod == "" {
		return nil, errors.New("missions: malformed state")
	}
	if s.state.Progress == nil {
		return nil, errors.New("missions: progress must be an object")
	}
	return s, nil
}

func (s *Service) AttachMail(ctx command.Context, mailbox *mail.Service) error {
	if mailbox == nil {
		return errors.New("missions: nil compensation mailbox")
	}

	s.mail = mailbox
	return nil
}

func (s *Service) EnsurePersisted(ctx command.Context) error {

	b, err := s.storage.Load(ctx.State, "missions")
	if err != nil {
		return err
	}
	if b != nil {
		return nil
	}
	return s.persist(ctx, cloneSnapshot(s.state))
}

// CompleteMission is the only way normal mission eligibility enters this
// package. It is persistent and idempotent. Gameplay/event handlers should
// call it only after they have independently verified the table condition.
func (s *Service) CompleteMission(ctx command.Context, key gamedata.MissionKey) error {

	if err := s.rolloverLocked(ctx); err != nil {
		return err
	}
	if _, ok := s.design.Missions[key]; !ok {
		return fmt.Errorf("missions: unknown mission %+v", key)
	}
	condition := s.design.Conditions[key]
	value := condition.TargetValue
	if value == 0 {
		value = 1
	}
	return s.setProgressLocked(ctx, key, value)
}

func (s *Service) SetProgress(ctx command.Context, key gamedata.MissionKey, value uint64) error {

	if err := s.rolloverLocked(ctx); err != nil {
		return err
	}
	return s.setProgressLocked(ctx, key, value)
}

func (s *Service) setProgressLocked(ctx command.Context, key gamedata.MissionKey, value uint64) error {
	if _, ok := s.design.Missions[key]; !ok {
		return fmt.Errorf("missions: unknown mission %+v", key)
	}
	name := missionName(key)
	if s.state.Progress[name] >= value {
		return nil
	}
	next := cloneSnapshot(s.state)
	next.Progress[name] = value
	condition := s.design.Conditions[key]
	target := condition.TargetValue
	if target == 0 {
		target = 1
	}
	if s.state.Progress[name] < target && value >= target {
		s.applyCompletionDependencies(&next, key)
	}
	return s.commit(ctx, next)
}

func (s *Service) applyCompletionDependencies(next *snapshot, completed gamedata.MissionKey) {
	queue := []gamedata.MissionKey{completed}
	seen := map[string]bool{}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		currentName := missionName(current)
		if seen[currentName] {
			continue
		}
		seen[currentName] = true
		for key, condition := range s.design.Conditions {
			if key.GroupType == 2 {
				continue
			}
			matched := condition.Type == 30 && key.GroupType == current.GroupType
			matched = matched || (condition.Type == 31 && condition.SubType == current.GroupType)
			if !matched {
				continue
			}
			name := missionName(key)
			target := condition.TargetValue
			if target == 0 {
				target = 1
			}
			before := next.Progress[name]
			if before >= target {
				continue
			}
			next.Progress[name] = min(before+1, target)
			if before < target && next.Progress[name] >= target {
				queue = append(queue, key)
			}
		}
	}
}

// AttachEventHandler delegates scheduled event operations before regular
// mission processing. The event service validates calendar and player state.
func (s *Service) AttachEventHandler(ctx command.Context, handler interface {
	Handle(ctx command.Context, _ string, _ []byte) (int, []byte, bool, error)
}) error {
	if handler == nil {
		return errors.New("missions: nil event handler")
	}
	s.eventHandler = handler
	return nil
}

func dailyPeriod(now time.Time) string { return now.UTC().Format("2006-01-02") }

func weeklyPeriod(now time.Time) string {
	now = now.UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -daysSinceMonday).Format("2006-01-02")
}

func (s *Service) rolloverLocked(ctx command.Context) error {
	if s.now == nil {
		return errors.New("missions: missing clock")
	}
	now := s.now().UTC()
	daily, weekly := dailyPeriod(now), weeklyPeriod(now)
	if daily == s.state.DailyPeriod && weekly == s.state.WeeklyPeriod {
		return nil
	}
	if s.mail == nil {
		return errors.New("missions: compensation mailbox is not attached")
	}
	next := cloneSnapshot(s.state)
	if daily != next.DailyPeriod {
		if err := s.compensatePeriodLocked(ctx, 0, next.DailyPeriod, now); err != nil {
			return err
		}
		clearMissionPeriod(&next, 0)
		next.DailyPeriod = daily
	}
	if weekly != next.WeeklyPeriod {
		if err := s.compensatePeriodLocked(ctx, 1, next.WeeklyPeriod, now); err != nil {
			return err
		}
		clearMissionPeriod(&next, 1)
		next.WeeklyPeriod = weekly
	}
	return s.commit(ctx, next)
}

func (s *Service) compensatePeriodLocked(ctx command.Context, groupType uint64, period string, now time.Time) error {
	label := "日常"
	if groupType == 1 {
		label = "周常"
	}
	for _, key := range s.completedUnclaimedForType(groupType) {
		rewards := s.design.Missions[key]
		if len(rewards) == 0 {
			continue
		}
		identity := fmt.Sprintf("expired-mission:%d:%s:%s", groupType, period, missionName(key))
		title := label + "任务到期补发"
		body := fmt.Sprintf("%s周期 %s 已结束。任务 %d/%d 已完成但未领取，奖励由系统自动补发。", label, period, key.GroupID, key.ID)
		if err := s.mail.EnqueueCompensation(ctx, identity, title, body, rewards, now); err != nil {
			return fmt.Errorf("missions: enqueue expired mission %+v: %w", key, err)
		}
	}
	for _, key := range s.eligibleUnclaimedSectionsForType(groupType) {
		rewards := s.design.Sections[key].Rewards
		if len(rewards) == 0 {
			continue
		}
		identity := fmt.Sprintf("expired-section:%d:%s:%d", groupType, period, key.ID)
		title := label + "阶段奖励到期补发"
		body := fmt.Sprintf("%s周期 %s 已结束。阶段奖励 %d 已达成但未领取，奖励由系统自动补发。", label, period, key.ID)
		if err := s.mail.EnqueueCompensation(ctx, identity, title, body, rewards, now); err != nil {
			return fmt.Errorf("missions: enqueue expired section %+v: %w", key, err)
		}
	}
	return nil
}

func (s *Service) completedUnclaimedForType(groupType uint64) []gamedata.MissionKey {
	var result []gamedata.MissionKey
	for key, condition := range s.design.Conditions {
		if key.GroupType != groupType {
			continue
		}
		target := condition.TargetValue
		if target == 0 {
			target = 1
		}
		if s.state.Progress[missionName(key)] >= target && !contains(s.state.Claimed, "mission:"+missionName(key)) {
			result = append(result, key)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].GroupID != result[j].GroupID {
			return result[i].GroupID < result[j].GroupID
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func (s *Service) eligibleUnclaimedSectionsForType(groupType uint64) []gamedata.SectionRewardKey {
	completed := uint64(len(s.completedForType(groupType)))
	var result []gamedata.SectionRewardKey
	for key, section := range s.design.Sections {
		identity := fmt.Sprintf("section:%d/%d", key.GroupType, key.ID)
		if key.GroupType == groupType && section.SectionValue != 0 && completed >= section.SectionValue && !contains(s.state.Claimed, identity) {
			result = append(result, key)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Service) completedForType(groupType uint64) []gamedata.MissionKey {
	var result []gamedata.MissionKey
	for key, condition := range s.design.Conditions {
		if key.GroupType != groupType {
			continue
		}
		target := condition.TargetValue
		if target == 0 {
			target = 1
		}
		if s.state.Progress[missionName(key)] >= target {
			result = append(result, key)
		}
	}
	return result
}

func clearMissionPeriod(state *snapshot, groupType uint64) {
	prefix := strconv.FormatUint(groupType, 10) + "/"
	for name := range state.Progress {
		if strings.HasPrefix(name, prefix) {
			delete(state.Progress, name)
		}
	}
	state.Completed = filterPeriodIdentities(state.Completed, groupType)
	state.Claimed = filterPeriodIdentities(state.Claimed, groupType)
}

func filterPeriodIdentities(values []string, groupType uint64) []string {
	missionPrefix := "mission:" + strconv.FormatUint(groupType, 10) + "/"
	sectionPrefix := "section:" + strconv.FormatUint(groupType, 10) + "/"
	result := values[:0]
	for _, value := range values {
		if !strings.HasPrefix(value, missionPrefix) && !strings.HasPrefix(value, sectionPrefix) {
			result = append(result, value)
		}
	}
	return result
}

func (s *Service) achievementInfo() []byte {
	// AchievementInfo contains progress and last completed IDs. This local
	// account currently records acknowledgement/claim state through the
	// inventory idempotency key, but deliberately does not fabricate progress.
	return nil
}

// The official all-clear request omits ContentsGroup (protobuf value zero).
// In that form GroupID+ID must identify exactly one static row; ambiguity is
// rejected rather than resolved by map iteration order.
func (s *Service) achievementDesign(contents, groupID, id uint64) (gamedata.AchievementKey, gamedata.AchievementDesign, bool) {
	if contents != 0 {
		key := gamedata.AchievementKey{ContentsGroup: contents, GroupID: groupID, ID: id}
		design, ok := s.design.Achievements[key]
		return key, design, ok
	}
	var foundKey gamedata.AchievementKey
	var found gamedata.AchievementDesign
	matched := false
	for key, design := range s.design.Achievements {
		if key.GroupID != groupID || key.ID != id {
			continue
		}
		if matched {
			return gamedata.AchievementKey{}, gamedata.AchievementDesign{}, false
		}
		foundKey, found, matched = key, design, true
	}
	return foundKey, found, matched
}

func (s *Service) progressMissionKeys() []gamedata.MissionKey {
	result := make([]gamedata.MissionKey, 0, len(s.state.Progress))
	for name, value := range s.state.Progress {
		if value == 0 {
			continue
		}
		for key := range s.design.Missions {
			if missionName(key) == name {
				result = append(result, key)
				break
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.GroupType != b.GroupType {
			return a.GroupType < b.GroupType
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		return a.ID < b.ID
	})
	return result
}

func (s *Service) completedMissionKeys() []gamedata.MissionKey {
	var result []gamedata.MissionKey
	for key, condition := range s.design.Conditions {
		target := condition.TargetValue
		if target == 0 {
			target = 1
		}
		name := missionName(key)
		if s.state.Progress[name] >= target && !contains(s.state.Claimed, "mission:"+name) {
			result = append(result, key)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.GroupType != b.GroupType {
			return a.GroupType < b.GroupType
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		return a.ID < b.ID
	})
	return result
}

func (s *Service) claimMissions(ctx command.Context, keys []gamedata.MissionKey) ([]assets.Item, error) {
	next := cloneSnapshot(s.state)
	var result []assets.Item
	for _, key := range keys {
		condition := s.design.Conditions[key]
		target := condition.TargetValue
		if target == 0 {
			target = 1
		}
		if s.state.Progress[missionName(key)] < target {
			return nil, fmt.Errorf("%w: mission is not complete", ErrInvalidRequest)
		}
		rewards, ok := s.design.Missions[key]
		if !ok {
			return nil, fmt.Errorf("%w: unknown mission %+v", ErrInvalidRequest, key)
		}
		identity := "mission:" + missionName(key)
		items, err := s.grantRewards(ctx, identity, rewards)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if !contains(next.Claimed, identity) {
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if err := s.commit(ctx, next); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) eligibleSections(groupType uint64) []gamedata.SectionRewardKey {
	var result []gamedata.SectionRewardKey
	for key := range s.design.Sections {
		// Proto3 omits group_type for the official bulk request.  Zero means
		// all mission groups there, not an invalid group.
		if (groupType == 0 || key.GroupType == groupType) && s.sectionEligible(key) {
			result = append(result, key)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Section reward thresholds are authoritative static values. The completed
// set, rather than the claimed set, is deliberately counted as the client does.
func (s *Service) sectionEligible(key gamedata.SectionRewardKey) bool {
	section, ok := s.design.Sections[key]
	if !ok {
		return false
	}
	if section.SectionValue == 0 {
		return false
	}
	var count uint64
	for _, identity := range s.state.Claimed {
		var group, missionGroup, missionID uint64
		if _, err := fmt.Sscanf(identity, "mission:%d/%d/%d", &group, &missionGroup, &missionID); err == nil && group == key.GroupType {
			count++
		}
	}
	return count >= section.SectionValue
}

func (s *Service) claimSections(ctx command.Context, keys []gamedata.SectionRewardKey) ([]assets.Item, error) {
	next := cloneSnapshot(s.state)
	var result []assets.Item
	for _, key := range keys {
		section, ok := s.design.Sections[key]
		if !ok || !s.sectionEligible(key) {
			return nil, fmt.Errorf("%w: section is not complete", ErrInvalidRequest)
		}
		identity := fmt.Sprintf("section:%d/%d", key.GroupType, key.ID)
		items, err := s.grantRewards(ctx, identity, section.Rewards)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if !contains(next.Claimed, identity) {
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if err := s.commit(ctx, next); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) grantRewards(ctx command.Context, identity string, rewards []gamedata.Reward) ([]assets.Item, error) {
	if s.wallet != nil {
		if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", rewards); err != nil {
			return nil, err
		}
	}
	var stack []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Type == 2 || reward.Type == 3 || reward.Type == 4 || reward.Type == 12 || reward.Type == 20 {
			if s.wallet == nil {
				return nil, errors.New("missions: currency reward wallet unavailable")
			}
			continue
		}
		if reward.ID == 0 || reward.Count == 0 {
			// Type 12 is an account resource whose full/overflow conversion is
			// server-dynamic. It is intentionally not fabricated as ItemDBInfo.
			continue
		}
		stack = append(stack, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
	}
	if len(stack) == 0 {
		return nil, nil
	}
	items, err := s.inventory.GrantOnce(ctx, identity+":items", stack)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	return items, nil
}

func (s *Service) commit(ctx command.Context, next snapshot) error {
	sort.Strings(next.Completed)
	sort.Strings(next.Claimed)
	if next.DailyPeriod == s.state.DailyPeriod && next.WeeklyPeriod == s.state.WeeklyPeriod && equalStrings(next.Completed, s.state.Completed) && equalStrings(next.Claimed, s.state.Claimed) && equalProgress(next.Progress, s.state.Progress) {
		return nil
	}
	return s.persist(ctx, next)
}

func (s *Service) persist(ctx command.Context, next snapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.Save(ctx.State, "missions", b); err != nil {
		return err
	}
	s.state = next
	return nil
}
func equalProgress(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func cloneSnapshot(in snapshot) snapshot {
	progress := make(map[string]uint64, len(in.Progress))
	maps.Copy(progress, in.Progress)
	return snapshot{Version: in.Version, DailyPeriod: in.DailyPeriod, WeeklyPeriod: in.WeeklyPeriod, Completed: append([]string(nil), in.Completed...), Claimed: append([]string(nil), in.Claimed...), Progress: progress}
}
func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func missionName(k gamedata.MissionKey) string {
	return fmt.Sprintf("%d/%d/%d", k.GroupType, k.GroupID, k.ID)
}
func achievementName(k gamedata.AchievementKey) string {
	return fmt.Sprintf("%d/%d/%d", k.ContentsGroup, k.GroupID, k.ID)
}

type achievementClaim struct {
	GroupID uint64
	IDs     []uint64
}

func decode(b []byte) (uint64, int) {
	var value uint64
	for i, x := range b {
		value |= uint64(x&127) << (7 * i)
		if x < 128 {
			return value, i + 1
		}
		if i == 9 {
			return 0, 0
		}
	}
	return 0, 0
}
