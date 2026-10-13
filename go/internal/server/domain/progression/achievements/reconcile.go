package achievements

import "sort"

// Remaining possessions establish a minimum acquisition history. They never
// replace a larger counter, nor imply consumed items or discarded equipment.
func (p *OwnedGameplayAchievementProvider) ownedConditions(s GameplayAchievementSnapshot) []GameplayAchievementCondition {
	values := map[[2]uint64]uint64{}
	add := func(kind, sub, count uint64) { values[[2]uint64{kind, sub}] += count }
	for _, c := range s.Characters {
		add(20, p.CharacterGrades[c.ID], 1)
		if grade := p.CharacterGrowth[c.ID]; grade > 0 {
			add(21, 0, grade-1)
		}
		if c.TalentLevel > 0 {
			add(24, 0, c.TalentLevel-1)
			for level := uint64(2); level <= c.TalentLevel; level++ {
				add(25, level, 1)
			}
		}
	}
	for _, c := range s.Costumes {
		add(26, 0, 1)
		add(26, c.ID, 1)
		add(27, 0, c.Level)
		add(83, p.CostumeGrades[c.ID], c.Level)
	}
	for _, e := range s.Equipment {
		add(7, 0, 1)
		if grade := p.EquipmentGrades[e.ID]; grade > 0 {
			add(7, grade, 1)
		}
		add(9, p.EquipmentGrades[e.ID], e.Level)
		if e.UpgradeAttempts >= e.Level {
			add(10, 0, e.UpgradeAttempts-e.Level)
		}
		if p.EquipmentPrivate[e.ID] > 0 {
			add(86, 0, 1)
			add(87, 0, e.Level)
		}
	}
	for key, count := range s.Items {
		add(1, key[0], count)
		if key[0] == 8 {
			add(3, key[1], count)
		}
	}
	for _, count := range s.GachaGrants {
		add(54, 0, count)
	}
	conditions := make([]GameplayAchievementCondition, 0, len(values))
	for key, value := range values {
		conditions = append(conditions, GameplayAchievementCondition{Type: key[0], SubType: key[1], Value: value, LowerBound: true})
	}
	if p.Potential != nil && p.Design != nil {
		potential, maximum, private := map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
		for _, c := range s.Costumes {
			unique := p.Potential.CostumeUnique[c.ID]
			nodes := p.Potential.Nodes[c.ID]
			all := len(nodes) > 0 && len(c.PotentialIDs) == len(nodes)
			for _, id := range c.PotentialIDs {
				if _, found := nodes[id]; !found {
					all = false
				}
			}
			potential[unique] = potential[unique] || all
			maximum[unique] = maximum[unique] || p.CostumeMaximum[c.ID] > 0 && c.Level >= p.CostumeMaximum[c.ID]
		}
		for _, e := range s.Equipment {
			if p.EquipmentGrades[e.ID] == 4 {
				private[p.EquipmentPrivate[e.ID]] = true
			}
		}
		for group, children := range p.Design.Children {
			if p.Design.Conditions[group].Type != 111 {
				continue
			}
			var completed uint64
			for _, child := range children {
				var satisfied bool
				switch child.Type {
				case 112:
					satisfied = potential[child.SubType]
				case 113:
					satisfied = s.CharAwake[child.SubType].IsAwake
				case 114:
					satisfied = maximum[child.SubType]
				case 115:
					satisfied = private[child.SubType]
				}
				if satisfied {
					completed++
				}
			}
			conditions = append(conditions, GameplayAchievementCondition{Type: 111, GroupID: uint64(group), Value: completed, LowerBound: true})
		}
	}
	sort.Slice(conditions, func(i, j int) bool {
		if conditions[i].Type != conditions[j].Type {
			return conditions[i].Type < conditions[j].Type
		}
		if conditions[i].SubType != conditions[j].SubType {
			return conditions[i].SubType < conditions[j].SubType
		}
		return conditions[i].GroupID < conditions[j].GroupID
	})
	return conditions
}
