package gamedata

import "fmt"

type WorldRules struct {
	Story          *StoryCatalog
	Packs          map[int]map[int]QuestDesign
	Transitions    map[int]PackTransition
	QuestCostumes  *RegularGachaCatalog
	FieldPacks     map[int]FieldPack
	SummaryTargets map[int]bool
	Jam            *PackJamDesign
	Difficulties   map[int]map[int]bool
	Characters     *StoryCharacterCatalog
}

func LoadWorldRules(root, version string, placeholder uint64) (*WorldRules, error) {
	story, err := LoadStoryCatalog(root, version)
	if err != nil {
		return nil, err
	}
	if err := orderMainQuests(story); err != nil {
		return nil, err
	}
	rules := &WorldRules{Story: story, Packs: map[int]map[int]QuestDesign{}, Transitions: map[int]PackTransition{}}
	var packs []int
	for id, pack := range story.Packs {
		rules.Packs[id] = pack.Quests
		rules.Transitions[id] = PackTransition{PackID: id, NextPackID: pack.NextPackID}
		packs = append(packs, id)
	}
	rules.FieldPacks, err = LoadFieldPacks(root, version)
	if err != nil {
		return nil, err
	}
	for id, pack := range rules.FieldPacks {
		if _, exists := rules.Packs[id]; exists || len(pack.MainQuestIDs) == 0 {
			continue
		}
		rules.Packs[id] = pack.Quests
		packs = append(packs, id)
	}
	rules.QuestCostumes, err = LoadQuestCostumes(root, version, rules.Packs, story)
	if err != nil {
		return nil, err
	}
	rules.SummaryTargets, err = LoadPackSummaryTargets(root, version)
	if err != nil {
		return nil, err
	}
	rules.Jam, err = LoadPackJamDesign(root, version)
	if err != nil {
		return nil, err
	}
	rules.Difficulties, err = LoadQuestDifficulties(root, version)
	if err != nil {
		return nil, err
	}
	rules.Characters, err = LoadStoryCharacterCatalog(root, version, packs, placeholder)
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// orderMainQuests follows the actual QuestTable links. Subquests never become
// a predecessor merely because their numeric ID lies between two main quests.
func orderMainQuests(story *StoryCatalog) error {
	for id, pack := range story.Packs {
		ids, err := orderedQuestIDs(id, pack.Quests, 0)
		if err != nil {
			return err
		}
		pack.MainQuestIDs = ids
		story.Packs[id] = pack
	}
	return nil
}

func orderedQuestIDs(packID int, quests map[int]QuestDesign, questType int) ([]int, error) {
	start, count := 0, 0
	for id, quest := range quests {
		if quest.Type != questType {
			continue
		}
		count++
		if quest.PriorQuestID == 0 {
			if start != 0 {
				return nil, fmt.Errorf("world: pack %d has multiple quest roots", packID)
			}
			start = id
		}
	}
	var ordered []int
	seen := map[int]bool{}
	previous := 0
	for id := start; id != 0; {
		quest, exists := quests[id]
		if !exists || quest.Type != questType || seen[id] || quest.PriorQuestID != previous {
			return nil, fmt.Errorf("world: invalid quest chain pack %d quest %d", packID, id)
		}
		seen[id] = true
		ordered = append(ordered, id)
		previous, id = id, quest.NextQuestID
	}
	if len(ordered) != count {
		return nil, fmt.Errorf("world: disconnected quest chain pack %d", packID)
	}
	return ordered, nil
}
