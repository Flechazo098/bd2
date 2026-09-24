package player

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"bd2server/internal/gamedata"
	"bd2server/internal/wire"
)

const talentSkillUpgradePacketCode = 44

func (s *CharacterStore) talentSkillUpgrade(request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("player: TalentSkillUpgrade missing sequence")
	}
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: TalentSkillUpgrade missing character")
	}
	materials, err := equipmentRequestItems(request, 3, "TalentSkillUpgrade")
	if err != nil {
		return 0, nil, true, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cacheKey := "talent-upgrade:" + s.sessionID + ":seq:" + strconv.FormatUint(seq, 10)
	if reply, ok := s.talentReplies[cacheKey]; ok {
		return reply.code, append([]byte(nil), reply.body...), true, nil
	}
	if s.talentGrowth == nil || s.wallet == nil || s.inventory == nil {
		return 0, nil, true, errors.New("player: talent upgrade unavailable")
	}

	position := -1
	var current Character
	fromCollection := false
	for i, character := range s.characters {
		if character.InvenIndex == index {
			position, current = i, character
			break
		}
	}
	if position < 0 && s.collection != nil {
		current, fromCollection = s.collection.FindCharacter(index)
	}
	if position < 0 && !fromCollection {
		return 0, nil, true, fmt.Errorf("player: unknown talent character inventory index %d", index)
	}
	rule, err := s.talentGrowth.UpgradeRule(current.ID, current.TalentLevel)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: resolve talent upgrade: %w", err)
	}
	if current.TalentExp < rule.RequiredTotalExp {
		return 0, nil, true, fmt.Errorf("player: character %d talent experience %d is below required %d", index, current.TalentExp, rule.RequiredTotalExp)
	}

	items, gold, err := validateTalentUpgradeMaterials(rule.Costs, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for talent upgrade")
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate talent upgrade items: %w", err)
		}
	}

	previousID := current.ID
	current.TalentLevel++
	if fromCollection {
		if err := s.collection.CanUpdateCharacter(previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate collection talent upgrade: %w", err)
		}
	}
	identity := cacheKey + ":character:" + strconv.FormatUint(index, 10) + ":level:" + strconv.FormatUint(rule.CurrentLevel, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume talent upgrade gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume talent upgrade items: %w", err)
		}
	}
	if fromCollection {
		if err := s.collection.UpdateCharacter(previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist collection talent upgrade: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(next); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist talent upgrade: %w", err)
		}
		s.characters = next
	}
	// TalentSkillUpgradeResponse.item_info is an optional grant list. Current
	// GameData defines no refund, so the correct protobuf response is empty.
	s.talentReplies[cacheKey] = talentUpgradeReply{code: talentSkillUpgradePacketCode}
	return talentSkillUpgradePacketCode, nil, true, nil
}

func validateTalentUpgradeMaterials(costs []gamedata.PromotionCost, materials []Item) ([]Item, uint64, error) {
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		if cost.Type == 0 || cost.Count == 0 || (cost.Type == 4 && cost.ID != 0) || (cost.Type != 4 && cost.ID == 0) {
			return nil, 0, errors.New("player: invalid GameData talent upgrade cost")
		}
		key := [2]uint64{cost.Type, cost.ID}
		if want[key] > math.MaxUint64-cost.Count {
			return nil, 0, errors.New("player: talent upgrade GameData cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	var items []Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		if got[key] > math.MaxUint64-material.Count {
			return nil, 0, errors.New("player: talent upgrade material overflow")
		}
		got[key] += material.Count
		if material.Type == 4 {
			if gold != 0 || material.InvenIndex != 0 || material.ID != 0 {
				return nil, 0, errors.New("player: invalid talent upgrade currency")
			}
			gold = material.Count
		} else {
			items = append(items, material)
		}
	}
	if len(got) != len(want) {
		return nil, 0, errors.New("player: talent upgrade material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return nil, 0, fmt.Errorf("player: talent upgrade material %d/%d=%d want=%d", key[0], key[1], got[key], count)
		}
	}
	return items, gold, nil
}
