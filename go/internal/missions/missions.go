// Package missions owns local completion eligibility and claim state for the
// three regular (non-scheduled-event) mission endpoints. Static definitions
// come from GameData; this package never replays an HTTP capture.
package missions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
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
type Service struct {
	mu        sync.Mutex
	path      string
	design    *gamedata.MissionDesign
	inventory *player.Inventory
	wallet    *player.Wallet
	state     snapshot
}

func (s *Service) AttachWallet(wallet *player.Wallet) error {
	if wallet == nil {
		return errors.New("missions: nil wallet")
	}
	s.wallet = wallet
	return nil
}

type snapshot struct {
	Version   string            `json:"version"`
	Completed []string          `json:"completed"`
	Claimed   []string          `json:"claimed"`
	Progress  map[string]uint64 `json:"progress,omitempty"`
}

func Open(path string, design *gamedata.MissionDesign, inventory *player.Inventory) (*Service, error) {
	if path == "" || design == nil || inventory == nil {
		return nil, errors.New("missions: invalid service configuration")
	}
	s := &Service{path: filepath.Clean(path), design: design, inventory: inventory, state: snapshot{Version: "2.34.13", Progress: map[string]uint64{}}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("missions: read state: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != "2.34.13" {
		return nil, errors.New("missions: malformed state")
	}
	if s.state.Progress == nil {
		s.state.Progress = map[string]uint64{}
	}
	return s, nil
}

// CompleteMission is the only way normal mission eligibility enters this
// package. It is persistent and idempotent. Gameplay/event handlers should
// call it only after they have independently verified the table condition.
func (s *Service) CompleteMission(key gamedata.MissionKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.design.Missions[key]; !ok {
		return fmt.Errorf("missions: unknown mission %+v", key)
	}
	condition := s.design.Conditions[key]
	value := condition.TargetValue
	if value == 0 {
		value = 1
	}
	return s.setProgressLocked(key, value)
}

func (s *Service) SetProgress(key gamedata.MissionKey, value uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setProgressLocked(key, value)
}

func (s *Service) setProgressLocked(key gamedata.MissionKey, value uint64) error {
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
	return s.commit(next)
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
			next.Progress[name] = before + 1
			if next.Progress[name] > target {
				next.Progress[name] = target
			}
			if before < target && next.Progress[name] >= target {
				queue = append(queue, key)
			}
		}
	}
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch path {
	case "/MissionInfo":
		if err := requireSeq(request); err != nil {
			return 118, nil, true, err
		}
		return 118, s.missionInfo(), true, nil
	case "/MissionUpdate":
		if err := s.update(request); err != nil {
			return 119, nil, true, err
		}
		return 119, nil, true, nil
	case "/AchievementInfo":
		if err := requireSeq(request); err != nil {
			return 166, nil, true, err
		}
		return 166, s.achievementInfo(), true, nil
	case "/MissionClear":
		response, err := s.clearMission(request)
		return missionClearPacket, response, true, err
	case "/MissionSectionReward":
		response, err := s.clearSection(request)
		return missionSectionPacket, response, true, err
	case "/AchievementClear":
		response, err := s.clearAchievements(request)
		return achievementClearPacket, response, true, err
	default:
		return 0, nil, false, nil
	}
}

func (s *Service) missionInfo() []byte {
	var response []byte
	for _, key := range s.progressMissionKeys() {
		entry := wire.AppendVarint(nil, 1, key.GroupID)
		entry = wire.AppendVarint(entry, 2, key.ID)
		if key.GroupType != 0 {
			entry = wire.AppendVarint(entry, 3, key.GroupType)
		}
		entry = wire.AppendVarint(entry, 4, s.state.Progress[missionName(key)])
		if contains(s.state.Claimed, "mission:"+missionName(key)) {
			entry = wire.AppendVarint(entry, 5, 1)
		}
		response = wire.AppendBytes(response, 1, entry)
	}
	for _, identity := range s.state.Claimed {
		var groupType, id uint64
		if _, err := fmt.Sscanf(identity, "section:%d/%d", &groupType, &id); err != nil {
			continue
		}
		entry := wire.AppendVarint(nil, 1, groupType)
		entry = wire.AppendVarint(entry, 2, id)
		response = wire.AppendBytes(response, 2, entry)
	}
	now := time.Now().UTC()
	daily := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	daysUntilMonday := (8 - int(now.Weekday())) % 7
	if daysUntilMonday == 0 {
		daysUntilMonday = 7
	}
	weekly := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, time.UTC)
	response = wire.AppendVarint(response, 3, uint64(daily.UnixMilli()))
	response = wire.AppendVarint(response, 4, uint64(weekly.UnixMilli()))
	return response
}

func (s *Service) update(request []byte) error {
	if err := requireSeq(request); err != nil {
		return err
	}
	var updates int
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return ErrInvalidRequest
		}
		groupID, err := scalar(field.Value, 1)
		if err != nil || groupID == 0 {
			return ErrInvalidRequest
		}
		id, err := scalar(field.Value, 2)
		if err != nil || id == 0 {
			return ErrInvalidRequest
		}
		value, err := scalar(field.Value, 3)
		if err != nil {
			return ErrInvalidRequest
		}
		var key gamedata.MissionKey
		found := false
		for candidate := range s.design.Missions {
			if candidate.GroupID == groupID && candidate.ID == id && candidate.GroupType != 2 {
				if found {
					return ErrInvalidRequest
				}
				key, found = candidate, true
			}
		}
		if !found {
			return ErrInvalidRequest
		}
		if err := s.setProgressLocked(key, value); err != nil {
			return err
		}
		updates++
		return nil
	})
	if err != nil {
		return err
	}
	if updates == 0 {
		return ErrInvalidRequest
	}
	return nil
}

func (s *Service) achievementInfo() []byte {
	// AchievementInfo contains progress and last completed IDs. This local
	// account currently records acknowledgement/claim state through the
	// inventory idempotency key, but deliberately does not fabricate progress.
	return nil
}

func (s *Service) clearMission(request []byte) ([]byte, error) {
	if err := requireSeq(request); err != nil {
		return nil, err
	}
	all, err := boolean(request, 2)
	if err != nil {
		return nil, err
	}
	groupType, err := scalar(request, 3)
	if err != nil {
		return nil, err
	}
	groupID, err := scalar(request, 4)
	if err != nil {
		return nil, err
	}
	id, err := scalar(request, 5)
	if err != nil {
		return nil, err
	}
	if groupType == 2 {
		return nil, errors.New("missions: scheduled event missions require an event service")
	}
	var keys []gamedata.MissionKey
	if all {
		if groupType != 0 || groupID != 0 || id != 0 {
			return nil, fmt.Errorf("%w: bulk MissionClear has identifiers", ErrInvalidRequest)
		}
		keys = s.completedMissionKeys()
	} else {
		if groupID == 0 || id == 0 {
			return nil, fmt.Errorf("%w: MissionClear identity", ErrInvalidRequest)
		}
		keys = []gamedata.MissionKey{{GroupType: groupType, GroupID: groupID, ID: id}}
	}
	items, err := s.claimMissions(keys)
	if err != nil {
		return nil, err
	}
	bundle := rewardBundle(items)
	for _, key := range keys {
		for _, reward := range s.design.Missions[key] {
			if reward.Type == 3 || reward.Type == 4 {
				entry := wire.AppendVarint(nil, 3, reward.Type)
				entry = wire.AppendVarint(entry, 4, reward.Count)
				bundle = wire.AppendBytes(bundle, 1, entry)
			}
		}
	}
	return bundle, nil
}

func (s *Service) clearSection(request []byte) ([]byte, error) {
	if err := requireSeq(request); err != nil {
		return nil, err
	}
	groupType, err := scalar(request, 2)
	if err != nil {
		return nil, fmt.Errorf("%w: section group type", ErrInvalidRequest)
	}
	all, err := boolean(request, 3)
	if err != nil {
		return nil, err
	}
	id, err := scalar(request, 4)
	if err != nil {
		return nil, err
	}
	var keys []gamedata.SectionRewardKey
	if all {
		if id != 0 {
			return nil, fmt.Errorf("%w: bulk section id", ErrInvalidRequest)
		}
		keys = s.eligibleSections(groupType)
	} else {
		if id == 0 {
			return nil, fmt.Errorf("%w: section id", ErrInvalidRequest)
		}
		key := gamedata.SectionRewardKey{GroupType: groupType, ID: id}
		if !s.sectionEligible(key) {
			return nil, fmt.Errorf("%w: section is not complete", ErrInvalidRequest)
		}
		keys = []gamedata.SectionRewardKey{key}
	}
	items, err := s.claimSections(keys)
	if err != nil {
		return nil, err
	}
	var response []byte
	for _, item := range items {
		response = wire.AppendBytes(response, 1, player.ItemWire(item))
	}
	for _, key := range keys {
		for _, reward := range s.design.Sections[key].Rewards {
			if reward.Type == 3 || reward.Type == 4 {
				entry := wire.AppendVarint(nil, 3, reward.Type)
				entry = wire.AppendVarint(entry, 4, reward.Count)
				response = wire.AppendBytes(response, 1, entry)
			}
		}
	}
	return response, nil
}

func (s *Service) clearAchievements(request []byte) ([]byte, error) {
	if err := requireSeq(request); err != nil {
		return nil, err
	}
	contents, err := scalar(request, 2)
	if err != nil {
		return nil, fmt.Errorf("%w: achievement contents group", ErrInvalidRequest)
	}
	claims, err := achievementClaims(request)
	if err != nil || len(claims) == 0 {
		return nil, fmt.Errorf("%w: achievement clear info", ErrInvalidRequest)
	}
	var allItems []player.Item
	var addExp uint64
	next := cloneSnapshot(s.state)
	for _, claim := range claims {
		for _, id := range claim.IDs {
			key, design, ok := s.achievementDesign(contents, claim.GroupID, id)
			if !ok {
				return nil, fmt.Errorf("%w: unknown achievement %+v", ErrInvalidRequest, key)
			}
			identity := "achievement:" + achievementName(key)
			if contains(next.Claimed, identity) {
				continue
			}
			items, err := s.inventory.GrantOnce(identity, battleRewards(design.Rewards))
			if err != nil {
				return nil, err
			}
			addExp += design.AddExp
			allItems = append(allItems, items...)
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if addExp > uint64(^uint(0)>>1) {
		return nil, errors.New("missions: achievement exp overflow")
	}
	if err := s.commit(next); err != nil {
		return nil, err
	}
	response := wire.AppendVarint(nil, 1, addExp)
	return wire.AppendBytes(response, 2, rewardBundle(allItems)), nil
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

func (s *Service) claimMissions(keys []gamedata.MissionKey) ([]player.Item, error) {
	next := cloneSnapshot(s.state)
	var result []player.Item
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
		items, err := s.grantRewards(identity, rewards)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if !contains(next.Claimed, identity) {
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if err := s.commit(next); err != nil {
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

func (s *Service) claimSections(keys []gamedata.SectionRewardKey) ([]player.Item, error) {
	next := cloneSnapshot(s.state)
	var result []player.Item
	for _, key := range keys {
		section, ok := s.design.Sections[key]
		if !ok || !s.sectionEligible(key) {
			return nil, fmt.Errorf("%w: section is not complete", ErrInvalidRequest)
		}
		identity := fmt.Sprintf("section:%d/%d", key.GroupType, key.ID)
		items, err := s.grantRewards(identity, section.Rewards)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if !contains(next.Claimed, identity) {
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if err := s.commit(next); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) grantRewards(identity string, rewards []gamedata.Reward) ([]player.Item, error) {
	if s.wallet != nil {
		if _, err := s.wallet.GrantQuestOnce(identity+":currency", rewards); err != nil {
			return nil, err
		}
	}
	var stack []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Type == 3 || reward.Type == 4 {
			continue
		}
		if reward.ID == 0 || reward.Count == 0 {
			// Type 12 is an account resource whose full/overflow conversion is
			// server-dynamic. It is intentionally not fabricated as ItemDBInfo.
			continue
		}
		stack = append(stack, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
	}
	if len(stack) == 0 {
		return nil, nil
	}
	items, err := s.inventory.GrantOnce(identity+":items", stack)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	return items, nil
}

func rewardBundle(items []player.Item) []byte {
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, view)
	}
	return bundle
}

func battleRewards(rewards []gamedata.Reward) []gamedata.BattleReward {
	result := make([]gamedata.BattleReward, len(rewards))
	for i, reward := range rewards {
		result[i] = gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}
	}
	return result
}

func (s *Service) commit(next snapshot) error {
	sort.Strings(next.Completed)
	sort.Strings(next.Claimed)
	if equalStrings(next.Completed, s.state.Completed) && equalStrings(next.Claimed, s.state.Claimed) && equalProgress(next.Progress, s.state.Progress) {
		return nil
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".missions-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
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
	for key, value := range in.Progress {
		progress[key] = value
	}
	return snapshot{Version: in.Version, Completed: append([]string(nil), in.Completed...), Claimed: append([]string(nil), in.Claimed...), Progress: progress}
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func requireSeq(b []byte) error {
	seq, err := scalar(b, 1)
	if err != nil || seq == 0 {
		return ErrInvalidRequest
	}
	return nil
}
func scalar(b []byte, number int) (uint64, error) {
	value, found, err := wire.Varint(b, number)
	if err != nil {
		return 0, ErrInvalidRequest
	}
	if !found {
		return 0, nil
	}
	return value, nil
}
func boolean(b []byte, number int) (bool, error) {
	value, err := scalar(b, number)
	if err != nil || value > 1 {
		return false, ErrInvalidRequest
	}
	return value == 1, nil
}

type achievementClaim struct {
	GroupID uint64
	IDs     []uint64
}

func achievementClaims(b []byte) ([]achievementClaim, error) {
	var claims []achievementClaim
	err := wire.Walk(b, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return ErrInvalidRequest
		}
		group, err := scalar(field.Value, 1)
		if err != nil || group == 0 {
			return ErrInvalidRequest
		}
		ids, err := packed(field.Value, 2)
		if err != nil || len(ids) == 0 {
			return ErrInvalidRequest
		}
		claims = append(claims, achievementClaim{group, ids})
		return nil
	})
	return claims, err
}
func packed(b []byte, number int) ([]uint64, error) {
	var values []uint64
	err := wire.Walk(b, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if field.Type == 0 {
			value, _ := decode(field.Value)
			values = append(values, value)
			return nil
		}
		if field.Type != 2 {
			return ErrInvalidRequest
		}
		for remaining := field.Value; len(remaining) > 0; {
			value, n := decode(remaining)
			if n == 0 {
				return ErrInvalidRequest
			}
			values = append(values, value)
			remaining = remaining[n:]
		}
		return nil
	})
	return values, err
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
