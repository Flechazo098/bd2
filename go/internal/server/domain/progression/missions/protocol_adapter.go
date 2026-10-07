package missions

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (s *Service) userLevelReward(ctx command.Context, request []byte) ([]byte, error) {
	if s.levelRewards == nil {
		return nil, fmt.Errorf("missions: user level rewards unavailable")
	}
	if err := requireSeq(request); err != nil {
		return nil, err
	}
	ids, err := packed(request, 2)
	if err != nil || len(ids) == 0 {
		return nil, ErrInvalidRequest
	}
	exp, err := s.achievementExperienceLocked()
	if err != nil {
		return nil, err
	}
	level := s.levelRewards.Level(exp)
	next := s.levelReward
	var fresh []gamedata.AchievementLevel
	var previous uint64
	for _, id := range ids {
		if id == 0 || id <= previous {
			return nil, ErrInvalidRequest
		}
		previous = id
		index := -1
		for i, l := range s.levelRewards.Levels {
			if l.ID == id {
				index = i
				break
			}
		}
		if index < 0 || id > level {
			return nil, ErrInvalidRequest
		}
		if id <= s.levelReward {
			continue
		}
		expected := uint64(0)
		for _, l := range s.levelRewards.Levels {
			if l.ID > next {
				expected = l.ID
				break
			}
		}
		if id != expected {
			return nil, fmt.Errorf("%w: user level reward requires preceding claims", ErrInvalidRequest)
		}
		fresh = append(fresh, s.levelRewards.Levels[index])
		next = id
	}
	var items []assets.Item
	var currencies []gamedata.Reward
	for _, l := range fresh {
		granted, err := s.grantRewards(ctx, fmt.Sprintf("user-level:%d", l.ID), l.Rewards)
		if err != nil {
			return nil, err
		}
		items = append(items, granted...)
		for _, r := range l.Rewards {
			if r.Type == 2 || r.Type == 3 || r.Type == 4 || r.Type == 12 || r.Type == 20 {
				currencies = append(currencies, r)
			}
		}
	}
	if next != s.levelReward {
		raw, err := json.Marshal(levelRewardSnapshot{versionconfig.State(), next})
		if err != nil {
			return nil, err
		}
		if err := s.storage.(stateio.ScopedEntryStore).PutEntry(ctx.State, "missions", "user_level_rewards", "state", raw); err != nil {
			return nil, err
		}
		s.levelReward = next
	}
	bundle := rewardBundle(items)
	for _, r := range currencies {
		item := wire.AppendVarint(nil, 3, r.Type)
		item = wire.AppendVarint(item, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, item)
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if s.eventHandler != nil && path == "/MissionUpdate" {
		regular, event, err := splitMissionUpdates(request)
		if err != nil {
			return 119, nil, true, err
		}
		if len(event) > 0 {
			if len(regular) > 0 {

				err = s.validateRegularUpdates(regular)

				if err != nil {
					return 119, nil, true, err
				}
			}
			if _, _, handled, err := s.eventHandler.Handle(ctx, path, event); err != nil || !handled {
				if err == nil {
					err = ErrInvalidRequest
				}
				return 119, nil, true, err
			}
			if len(regular) == 0 {
				return 119, nil, true, nil
			}
			request = regular
		}
	}
	if s.eventHandler != nil && path == "/MissionClear" {
		if code, response, handled, err := s.eventHandler.Handle(ctx, path, request); handled {
			return code, response, handled, err
		}
	}

	if err := s.rolloverLocked(ctx); err != nil {
		return 0, nil, true, err
	}
	switch path {
	case "/UserLevelReward":
		response, err := s.userLevelReward(ctx, request)
		return 73, response, true, err
	case "/MissionInfo":
		if err := requireSeq(request); err != nil {
			return 118, nil, true, err
		}
		return 118, s.missionInfo(), true, nil
	case "/MissionUpdate":
		if err := s.update(ctx, request); err != nil {
			return 119, nil, true, err
		}
		return 119, nil, true, nil
	case "/AchievementInfo":
		if err := requireSeq(request); err != nil {
			return 166, nil, true, err
		}
		return 166, s.achievementInfo(), true, nil
	case "/MissionClear":
		response, err := s.clearMission(ctx, request)
		return missionClearPacket, response, true, err
	case "/MissionSectionReward":
		response, err := s.clearSection(ctx, request)
		return missionSectionPacket, response, true, err
	case "/AchievementClear":
		response, err := s.clearAchievements(ctx, request)
		return achievementClearPacket, response, true, err
	default:
		return 0, nil, false, nil
	}
}

// Each update carries its own event identity; a client can acknowledge regular
// and scheduled missions together in one request.
func splitMissionUpdates(request []byte) (regular, event []byte, err error) {
	if err = requireSeq(request); err != nil {
		return
	}
	seq, _, _ := wire.Varint(request, 1)
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return ErrInvalidRequest
		}
		uid, e := scalar(f.Value, 4)
		if e != nil {
			return e
		}
		if uid == 0 {
			if len(regular) == 0 {
				regular = wire.AppendVarint(nil, 1, seq)
			}
			regular = wire.AppendBytes(regular, 2, f.Value)
		} else {
			if len(event) == 0 {
				event = wire.AppendVarint(nil, 1, seq)
			}
			event = wire.AppendBytes(event, 2, f.Value)
		}
		return nil
	})
	return
}

func (s *Service) validateRegularUpdates(request []byte) error {
	return wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return ErrInvalidRequest
		}
		group, err := scalar(f.Value, 1)
		if err != nil || group == 0 {
			return ErrInvalidRequest
		}
		id, err := scalar(f.Value, 2)
		if err != nil || id == 0 {
			return ErrInvalidRequest
		}
		if _, err = scalar(f.Value, 3); err != nil {
			return ErrInvalidRequest
		}
		matches := 0
		for key := range s.design.Missions {
			if key.GroupType != 2 && key.GroupID == group && key.ID == id {
				matches++
			}
		}
		if matches != 1 {
			return ErrInvalidRequest
		}
		return nil
	})
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
	now := s.now().UTC()
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

func (s *Service) update(ctx command.Context, request []byte) error {
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
		if err := s.setProgressLocked(ctx, key, value); err != nil {
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

func (s *Service) clearMission(ctx command.Context, request []byte) ([]byte, error) {
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
	items, err := s.claimMissions(ctx, keys)
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

func (s *Service) clearSection(ctx command.Context, request []byte) ([]byte, error) {
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
	items, err := s.claimSections(ctx, keys)
	if err != nil {
		return nil, err
	}
	var response []byte
	for _, item := range items {
		response = wire.AppendBytes(response, 1, assets.ItemWire(item))
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

func (s *Service) clearAchievements(ctx command.Context, request []byte) ([]byte, error) {
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
	requested := map[gamedata.AchievementKey]bool{}
	for _, claim := range claims {
		for _, id := range claim.IDs {
			key, _, ok := s.achievementDesign(contents, claim.GroupID, id)
			if !ok {
				return nil, fmt.Errorf("%w: unknown achievement", ErrInvalidRequest)
			}
			requested[key] = true
		}
	}
	for key := range requested {
		if contains(s.state.Claimed, "achievement:"+achievementName(key)) {
			continue
		}
		for earlier := range s.design.Achievements {
			if earlier.ContentsGroup != key.ContentsGroup || earlier.GroupID != key.GroupID || earlier.ID >= key.ID {
				continue
			}
			if !requested[earlier] && !contains(s.state.Claimed, "achievement:"+achievementName(earlier)) {
				return nil, fmt.Errorf("missions: achievement tier %v requires earlier tier %v to be claimed", key, earlier)
			}
		}
	}
	// Validate the whole batch before granting any reward.
	for _, claim := range claims {
		for _, id := range claim.IDs {
			key, d, ok := s.achievementDesign(contents, claim.GroupID, id)
			if !ok {
				return nil, fmt.Errorf("%w: unknown achievement", ErrInvalidRequest)
			}
			if contains(s.state.Claimed, "achievement:"+achievementName(key)) {
				continue
			}
			if d.Target > 0 {
				if s.achievementProgress == nil {
					return nil, errors.New("missions: achievement progress unavailable")
				}
				group := d.CounterGroup
				if group == 0 {
					group = key.GroupID
				}
				value, err := s.achievementProgress.AchievementValue(ctx, group)
				if err != nil {
					return nil, err
				}
				if float64(value) < d.Target {
					return nil, fmt.Errorf("missions: achievement %v requires %g progress, got %d", key, d.Target, value)
				}
			}
		}
	}
	var allItems []assets.Item
	var addExp uint64
	var currencyRewards []gamedata.Reward
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
			items, err := s.grantRewards(ctx, identity, design.Rewards)
			if err != nil {
				return nil, err
			}
			if ^uint64(0)-addExp < design.AddExp {
				return nil, errors.New("missions: achievement exp overflow")
			}
			addExp += design.AddExp
			for _, reward := range design.Rewards {
				if reward.Type == 2 || reward.Type == 3 || reward.Type == 4 || reward.Type == 12 || reward.Type == 20 {
					currencyRewards = append(currencyRewards, reward)
				}
			}
			allItems = append(allItems, items...)
			next.Claimed = append(next.Claimed, identity)
		}
	}
	if addExp > uint64(^uint(0)>>1) {
		return nil, errors.New("missions: achievement exp overflow")
	}
	if err := s.commit(ctx, next); err != nil {
		return nil, err
	}
	response := wire.AppendVarint(nil, 1, addExp)
	bundle := rewardBundle(allItems)
	for _, reward := range currencyRewards {
		item := wire.AppendVarint(nil, 3, reward.Type)
		item = wire.AppendVarint(item, 4, reward.Count)
		bundle = wire.AppendBytes(bundle, 1, item)
	}
	return wire.AppendBytes(response, 2, bundle), nil
}

func rewardBundle(items []assets.Item) []byte {
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, view)
	}
	return bundle
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
