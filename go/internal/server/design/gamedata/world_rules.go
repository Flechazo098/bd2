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
	rules.QuestCostumes, err = LoadQuestCostumes(root, version, rules.Packs, story)
	if err != nil {
		return nil, err
	}
	rules.FieldPacks, err = LoadFieldPacks(root, version)
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
		start := 0
		for _, qid := range pack.MainQuestIDs {
			if pack.Quests[qid].PriorQuestID == 0 {
				if start != 0 {
					return fmt.Errorf("world: pack %d has multiple main quest roots", id)
				}
				start = qid
			}
		}
		var ordered []int
		seen := map[int]bool{}
		for qid := start; qid != 0; {
			q, exists := pack.Quests[qid]
			if !exists || q.Type != 0 || seen[qid] {
				return fmt.Errorf("world: invalid main quest chain pack %d quest %d", id, qid)
			}
			seen[qid] = true
			ordered = append(ordered, qid)
			qid = q.NextQuestID
		}
		if len(ordered) != len(pack.MainQuestIDs) {
			return fmt.Errorf("world: disconnected main quest chain pack %d", id)
		}
		pack.MainQuestIDs = ordered
		story.Packs[id] = pack
	}
	return nil
}
