package eventtasks

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"encoding/json"
	"strconv"
	"strings"
)

// Define_MissionConditionType values. Both conditions complete when the
// player has obtained a reward for the required reward level; they are not
// client-reported actions.
const (
	conditionRewardToAchievementLevel uint64 = 221
	conditionRewardToPassLevel        uint64 = 223
)

// AttachAchievementLevelReward supplies the highest achievement level whose
// reward has been claimed. Reward claims are sequential, so that durable value
// also proves every lower level reward was claimed.
func (s *Service) AttachAchievementLevelReward(source func() (uint64, error)) {
	s.achievementLevel = source
}

// reconcileRewardLevelProgress derives reward-level mission progress from
// durable claim state. The claim may predate the pass or happen while the pass
// is inactive, so action-only events are not enough.
func (s *Service) reconcileRewardLevelProgress(ctx command.Context) error {
	type pending struct {
		schedule events.Schedule
		task     gamedata.EventTask
	}
	var tasks []pending
	needAchievement, needPass := false, false
	for _, v := range s.taskSchedules() {
		if v.Type != 4 || !s.active(v) {
			continue
		}
		period := s.missionPeriod(v)
		for _, t := range s.design.Missions {
			if t.Type != conditionRewardToAchievementLevel && t.Type != conditionRewardToPassLevel || !s.availableTask(ctx, v, t) {
				continue
			}
			target := missionTarget(t)
			if m := s.state.Missions[scheduleKey(v)+"/"+key(t.ID)]; m != nil && m.Period == period && (m.Claimed || m.Value >= target) {
				continue
			}
			tasks = append(tasks, pending{schedule: v, task: t})
			if t.Type == conditionRewardToAchievementLevel {
				needAchievement = true
			} else {
				needPass = true
			}
		}
	}
	if len(tasks) == 0 {
		return nil
	}
	var achievementLevel uint64
	if needAchievement && s.achievementLevel != nil {
		var err error
		achievementLevel, err = s.achievementLevel()
		if err != nil {
			return err
		}
	}
	passLevels := map[uint64]bool{}
	if needPass {
		passLevels = s.claimedPassLevels()
	}
	before, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	changed := false
	for _, p := range tasks {
		reached := false
		switch p.task.Type {
		case conditionRewardToAchievementLevel:
			if s.achievementLevel != nil {
				reached = achievementLevelRewardReached(achievementLevel, p.task)
			}
		case conditionRewardToPassLevel:
			reached = passLevelRewardReached(passLevels, p.task)
		}
		if !reached {
			continue
		}
		m := s.mission(p.schedule, p.task.ID)
		if target := missionTarget(p.task); m.Value < target {
			m.Value = target
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := s.save(ctx); err != nil {
		_ = json.Unmarshal(before, &s.state)
		return err
	}
	return nil
}

func missionTarget(t gamedata.EventTask) uint64 {
	if t.Target == 0 {
		return 1
	}
	return t.Target
}

// achievementLevelRewardReached follows MissionTable.IsConditionSubTypeMoreCheck,
// mirroring MissionPassPacket.UpdateMissionUnit on the client.
func achievementLevelRewardReached(claimed uint64, t gamedata.EventTask) bool {
	if t.SubType == 0 {
		return claimed > 0
	}
	if t.SubTypeMoreCheck != 0 {
		return claimed >= t.SubType
	}
	return claimed == t.SubType
}

// claimedPassLevels collects levels with a basic or premium reward claim from
// every pass the account has used. Pass rewards are not claimed sequentially,
// so the exact claimed level matters.
func (s *Service) claimedPassLevels() map[uint64]bool {
	levels := map[uint64]bool{}
	for _, p := range s.state.Passes {
		if p == nil {
			continue
		}
		for claimed := range p.Claimed {
			level, _, ok := strings.Cut(claimed, "/")
			if !ok {
				continue
			}
			value, err := strconv.ParseUint(level, 10, 64)
			if err == nil && value > 0 {
				levels[value] = true
			}
		}
	}
	return levels
}

// passLevelRewardReached uses the same ConditionSubType comparison as the
// client. Condition 223's Params is a zero placeholder and not a filter.
func passLevelRewardReached(claimed map[uint64]bool, t gamedata.EventTask) bool {
	if t.SubType == 0 {
		return len(claimed) > 0
	}
	if t.SubTypeMoreCheck != 0 {
		for level := range claimed {
			if level >= t.SubType {
				return true
			}
		}
		return false
	}
	return claimed[t.SubType]
}
