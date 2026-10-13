package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/achievements"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// achievements.GameplayAchievementProvider exposes only authoritative owned instances and
// persisted quest/object state. Retained history establishes lower bounds.
func (s *Service) GameplayAchievementProvider(ctx command.Context, design *gamedata.AchievementCounterDesign, grades gamedata.GameplayAchievementGrades) *achievements.OwnedGameplayAchievementProvider {
	p := &achievements.OwnedGameplayAchievementProvider{Design: design, CharacterGrades: grades.Characters, EquipmentGrades: grades.Equipment, CostumeGrades: grades.Costumes, CharacterGrowth: grades.CharacterGrowth, EquipmentPrivate: grades.EquipmentPrivate, CostumeMaximum: grades.CostumeMaximum}
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
			difficulty := int(c.Type) - 14
			if c.Type == 72 {
				difficulty = 3
			}
			if c.Type == 73 {
				difficulty = 4
			}
			if (c.Type < 14 || c.Type > 16) && c.Type != 72 && c.Type != 73 {
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
				if !s.state.QuestCleared(qid, pack.ID, difficulty) {
					complete = false
					break
				}
			}
			if complete {
				conditions = append(conditions, achievements.GameplayAchievementCondition{Type: c.Type, SubType: c.SubType, Value: 1, LowerBound: true})
			}
		}
		return conditions, nil
	}
	var worldConditions []achievements.GameplayAchievementCondition
	var researchObjects map[string]gamedata.FieldResearchObject
	var previousSignature string
	var previousObjects map[string]gamedata.FieldRewardObject
	p.FieldObjects = func(ctx command.Context) (map[string]gamedata.FieldRewardObject, error) {
		openedPeriods, err := s.state.OpenedFieldRewardPeriods(ctx)
		if err != nil {
			return nil, err
		}
		research, err := s.state.AllResearchObjects(ctx)
		if err != nil {
			return nil, err
		}
		var waypointCount uint64
		if s.decks != nil {
			waypointCount = s.decks.ActivatedWaypointCount()
		}
		cleared := s.state.AllClearedQuests()
		raw, err := json.Marshal([]any{openedPeriods, research, cleared, waypointCount, p.TransientVersion()})
		if err != nil {
			return nil, err
		}
		if signature := string(raw); signature == previousSignature && previousObjects != nil {
			return previousObjects, nil
		}
		observedPeriods = map[[2]int]gamedata.FieldRewardObject{}
		objects := map[string]gamedata.FieldRewardObject{}
		values := map[[2]uint64]uint64{}
		add := func(kind, sub, count uint64) { values[[2]uint64{kind, sub}] += count }
		add(32, 0, waypointCount)
		for _, q := range cleared {
			if s.storyCatalog != nil && q.Difficulty == 0 && s.storyCatalog.Packs[q.PackID].Quests[q.QuestID].Type == 2 {
				add(18, 0, 1)
			}
		}
		packs := map[int]bool{}
		for id := range openedPeriods {
			packs[id] = true
		}
		for id := range research {
			packs[id] = true
		}
		projectedResearch := map[string]gamedata.FieldResearchObject{}
		collections := map[int]bool{}
		for pack := range packs {
			periods := openedPeriods[pack]
			d, err := s.achievementFieldDesign(ctx, pack)
			if err != nil {
				return nil, err
			}
			for id, openedPeriod := range periods {
				obj, known := d.Objects[id]
				if !known {
					return nil, fmt.Errorf("achievement: opened field design absent")
				}
				observedPeriods[[2]int{pack, obj.ResetType}] = obj
				switch obj.Type {
				case 1:
					add(28, 0, 1)
				case 2:
					add(31, 0, 1)
				case 5:
					add(30, 0, 1)
				}
				period, err := s.fieldObjectPeriodFor(pack, obj)
				if err != nil {
					continue
				}
				if openedPeriod == period {
					objects[fmt.Sprintf("field:%d:%d:%s", pack, id, period)] = obj
				}
			}
			rd, err := s.researchDesign(pack)
			if err != nil {
				return nil, err
			}
			for id := range research[pack] {
				obj, found := rd.Objects[id]
				if !found {
					return nil, fmt.Errorf("achievement: researched field design absent")
				}
				projectedResearch[fmt.Sprintf("research:%d:%d", pack, id)] = obj
				add(29, 0, 1)
				if obj.CollectionID > 0 {
					collections[obj.CollectionID] = true
				}
			}
			mapComplete := map[int]bool{}
			mapHasRewards := map[int]bool{}
			check := func(mapID int, completed bool) {
				if !mapHasRewards[mapID] {
					mapComplete[mapID] = true
				}
				mapHasRewards[mapID] = true
				mapComplete[mapID] = mapComplete[mapID] && completed
			}
			for id, obj := range d.Objects {
				if matchesFieldCount(obj, 2) {
					check(obj.MapID, periods[id] == "once")
				}
			}
			for id, obj := range rd.Objects {
				if obj.Reward.Type == 0 && obj.CollectionID == 0 {
					continue
				}
				for _, mapID := range obj.Maps {
					check(mapID, research[pack][id])
				}
			}
			packComplete := len(mapHasRewards) > 0
			for mapID := range mapHasRewards {
				if mapComplete[mapID] {
					add(33, uint64(mapID), 1)
				} else {
					packComplete = false
				}
			}
			if packComplete {
				add(34, uint64(pack), 1)
			}
		}
		add(5, 0, uint64(len(collections)))
		projectedConditions := make([]achievements.GameplayAchievementCondition, 0, len(values))
		for key, value := range values {
			projectedConditions = append(projectedConditions, achievements.GameplayAchievementCondition{Type: key[0], SubType: key[1], Value: value, LowerBound: true})
		}
		worldConditions, researchObjects, previousObjects = projectedConditions, projectedResearch, objects
		// Period discovery changes the transient signature on the first projection.
		raw, _ = json.Marshal([]any{openedPeriods, research, cleared, waypointCount, p.TransientVersion()})
		previousSignature = string(raw)
		return objects, nil
	}
	p.Project = func(_ command.Context, snapshot *achievements.GameplayAchievementSnapshot) error {
		snapshot.Conditions = append(snapshot.Conditions, worldConditions...)
		snapshot.ResearchObjects = researchObjects
		return nil
	}
	return p
}

func (s *Service) achievementFieldDesign(ctx command.Context, pack int) (gamedata.FieldObjectDesign, error) {
	if design, found := s.fieldObjects[pack]; found {
		return design, nil
	}
	if s.storyCatalog != nil {
		if _, known := s.storyCatalog.Packs[pack]; known && s.fieldObjectLoader != nil {
			design, err := s.fieldObjectLoader(pack)
			if err == nil {
				s.fieldObjects[pack] = design
			}
			return design, err
		}
	}
	return s.fieldObjectDesign(ctx, pack)
}
