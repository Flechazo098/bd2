package gamedata

// LoadQuestCostumes loads only costume rewards referenced by the installed
// quest catalog, using the same character and duplicate-copy metadata as gacha.
func LoadQuestCostumes(root, version string, packs map[int]map[int]QuestDesign) (*RegularGachaCatalog, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	catalog := &RegularGachaCatalog{characters: map[uint64]CharacterDesign{}}
	for _, quests := range packs {
		for _, quest := range quests {
			for _, slot := range quest.Rewards {
				for _, reward := range slot {
					if reward.Type != 11 {
						continue
					}
					if _, exists := catalog.characters[reward.ID]; exists {
						continue
					}
					design, err := loadGachaCharacterDesign(db, reward.ID)
					if err != nil {
						return nil, err
					}
					catalog.characters[reward.ID] = design
				}
			}
		}
	}
	return catalog, nil
}
