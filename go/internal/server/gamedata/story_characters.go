package gamedata

import "fmt"

type StoryCharacterDesign struct {
	CharacterID, UniqueCharacterID, Level, CostumeID, HP, Order uint64
	TemporaryPack                                               uint64
	InitialTalentLevel                                          uint64
}

// StoryCharacterCatalog contains available temporary pack characters, not a
// mandatory battle formation for ordinary story or master-story quests.
type StoryCharacterCatalog struct {
	formations map[int]map[int]QuestFormation
	characters map[[2]int][]StoryCharacterDesign
}

func LoadStoryCharacterCatalog(root, version string, packs []int, placeholderCostumes ...uint64) (*StoryCharacterCatalog, error) {
	if len(placeholderCostumes) > 1 {
		return nil, fmt.Errorf("gamedata: multiple story placeholders")
	}
	db, cleanup, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stats, err := loadCharacterStatDesign(db)
	if err != nil {
		return nil, err
	}
	catalog := &StoryCharacterCatalog{formations: map[int]map[int]QuestFormation{}, characters: map[[2]int][]StoryCharacterDesign{}}
	instances := map[[3]uint64]uint64{}
	initialTalents := map[uint64]uint64{}
	for _, pack := range packs {
		formations, err := loadQuestFormationsDB(db, pack)
		if err != nil {
			return nil, err
		}
		catalog.formations[pack] = formations
		for quest, formation := range formations {
			for _, row := range formation.Characters {
				if row.Banned {
					continue
				}
				var data []byte
				if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", row.CharacterID).Scan(&data); err != nil {
					return nil, fmt.Errorf("gamedata: story character %d: %w", row.CharacterID, err)
				}
				unique, err := requiredScalar(data, 20)
				if err != nil {
					return nil, err
				}
				temporaryPack, err := optionalScalar(data, 21)
				if err != nil {
					return nil, err
				}
				characterType, err := optionalScalar(data, 19)
				if err != nil {
					return nil, err
				}
				if characterType != 1 || temporaryPack == 0 {
					continue
				}
				talentID, err := optionalScalar(data, 18)
				if err != nil {
					return nil, err
				}
				var talentLevel uint64
				if talentID != 0 {
					// CharDBInfo.talent_level is the TalentSkillTable row ID,
					// independent of the authored battle character level.
					var known bool
					talentLevel, known = initialTalents[talentID]
					if !known {
						if err := db.QueryRow("SELECT MIN(skill.id) FROM TalentTable talent JOIN TalentSkillTable skill ON skill.groupId=talent.talentSkillGroupId WHERE talent.id=? AND skill.id>0", talentID).Scan(&talentLevel); err != nil {
							return nil, fmt.Errorf("gamedata: story character %d initial talent %d: %w", row.CharacterID, talentID, err)
						}
						initialTalents[talentID] = talentLevel
					}
				}
				costume, err := requiredScalar(data, 5)
				if err != nil {
					return nil, err
				}
				// CharGroup supplies available temporary instances. StoryCharGroup
				// supplies cosmetic field actors and cannot override their costume.
				base, err := stats.BaseStats(row.CharacterID, row.Level)
				if err != nil {
					return nil, err
				}
				if base.Health <= 0 {
					return nil, fmt.Errorf("gamedata: story character %d has invalid health", row.CharacterID)
				}
				instance := [3]uint64{uint64(pack), row.CharacterID, row.Level}
				if previous, exists := instances[instance]; exists && previous != costume {
					return nil, fmt.Errorf("gamedata: story instance pack%d character%d level%d has conflicting costumes %d/%d", pack, row.CharacterID, row.Level, previous, costume)
				}
				instances[instance] = costume
				key := [2]int{pack, quest}
				catalog.characters[key] = append(catalog.characters[key], StoryCharacterDesign{
					CharacterID: row.CharacterID, UniqueCharacterID: unique, Level: row.Level,
					CostumeID: costume, HP: uint64(base.Health), Order: row.Order,
					TemporaryPack: temporaryPack, InitialTalentLevel: talentLevel,
				})
			}
		}
	}
	return catalog, nil
}

func (c *StoryCharacterCatalog) Formation(packID, questID int) (QuestFormation, bool) {
	if c == nil {
		return QuestFormation{}, false
	}
	f, ok := c.formations[packID][questID]
	return f, ok
}

func (c *StoryCharacterCatalog) Characters(packID, questID int) ([]StoryCharacterDesign, error) {
	if _, ok := c.Formation(packID, questID); !ok {
		return nil, fmt.Errorf("gamedata: missing story formation pack%d quest%d", packID, questID)
	}
	return append([]StoryCharacterDesign(nil), c.characters[[2]int{packID, questID}]...), nil
}
