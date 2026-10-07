package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/achievements"
	"fmt"
	"sort"
	"strings"
)

// achievements.GameplayAchievementProvider exposes only authoritative owned instances and
// persisted quest/object state. It does not manufacture past acquisition counts.
func (s *Service) GameplayAchievementProvider(ctx command.Context, design *gamedata.AchievementCounterDesign, grades gamedata.GameplayAchievementGrades) *achievements.OwnedGameplayAchievementProvider {
	p := &achievements.OwnedGameplayAchievementProvider{Design: design, CharacterGrades: grades.Characters, EquipmentGrades: grades.Equipment}
	observedPeriods := map[[2]int]gamedata.FieldRewardObject{}
	p.TransientVersion = func() string {
		var signature strings.Builder
		fmt.Fprintf(&signature, "%d/%d", len(s.packs), len(s.fieldPacks))
		keys := make([][2]int, 0, len(observedPeriods))
		for key := range observedPeriods {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i][0] != keys[j][0] {
				return keys[i][0] < keys[j][0]
			}
			return keys[i][1] < keys[j][1]
		})
		for _, key := range keys {
			period, err := s.fieldObjectPeriodFor(key[0], observedPeriods[key])
			fmt.Fprintf(&signature, "/%d:%d:%s:%v", key[0], key[1], period, err)
		}
		return signature.String()
	}
	if s.characters != nil {
		p.Characters = s.characters
	}
	if s.collection != nil {
		p.Costumes = s.collection
		p.Gacha = s.collection
	}
	if s.equipment != nil {
		p.Equipment = s.equipment
	}
	if s.inventory != nil {
		p.Items = s.inventory
	}
	p.Conditions = func(ctx command.Context) ([]achievements.GameplayAchievementCondition, error) {
		var conditions []achievements.GameplayAchievementCondition
		seen := map[[2]uint64]bool{}
		if s.storyCatalog == nil {
			return nil, nil
		}
		for _, c := range design.Conditions {
			if c.Type < 14 || c.Type > 16 {
				continue
			}
			key := [2]uint64{c.Type, c.SubType}
			if seen[key] {
				continue
			}
			seen[key] = true
			pack, known := s.storyCatalog.Packs[int(c.SubType)]
			if !known || len(pack.MainQuestIDs) == 0 {
				continue
			}
			complete := true
			for _, qid := range pack.MainQuestIDs {
				if !s.state.QuestCleared(qid, pack.ID, int(c.Type-14)) {
					complete = false
					break
				}
			}
			if complete {
				conditions = append(conditions, achievements.GameplayAchievementCondition{Type: c.Type, SubType: c.SubType, Value: 1})
			}
		}
		return conditions, nil
	}
	p.FieldObjects = func(ctx command.Context) (map[string]gamedata.FieldRewardObject, error) {
		observedPeriods = map[[2]int]gamedata.FieldRewardObject{}
		objects := map[string]gamedata.FieldRewardObject{}
		openedPeriods, err := s.state.OpenedFieldRewardPeriods(ctx)
		if err != nil {
			return nil, err
		}
		// Opened IDs are persisted independently of quest difficulty. Only loaded
		// packs with real opened entries require their reward design to be resolved.
		packs := map[int]bool{}
		for id := range s.packs {
			packs[id] = true
		}
		for id := range s.fieldPacks {
			packs[id] = true
		}
		for pack := range packs {
			periods := openedPeriods[pack]
			if len(periods) == 0 {
				continue
			}
			d, err := s.fieldObjectDesign(ctx, pack)
			if err != nil {
				return nil, err
			}
			for id, openedPeriod := range periods {
				obj, known := d.Objects[id]
				if !known {
					return nil, fmt.Errorf("achievement: opened field design absent")
				}
				observedPeriods[[2]int{pack, obj.ResetType}] = obj
				period, err := s.fieldObjectPeriodFor(pack, obj)
				if err != nil {
					continue
				}
				if openedPeriod == period {
					objects[fmt.Sprintf("field:%d:%d:%s", pack, id, period)] = obj
				}
			}
		}
		return objects, nil
	}
	return p
}
