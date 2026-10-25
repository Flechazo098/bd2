package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
)

func requestQuest(request []byte) (int, int, error) {
	quest, err := requestPack(request) // fields 1 and 2 have the same validation.
	if err != nil {
		return 0, 0, err
	}
	pack, found, err := wire.Varint(request, 3)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, 0, fmt.Errorf("%w: quest request pack %d (present=%t): %v", ErrInvalidRequest, pack, found, err)
	}
	return quest, int(pack), nil
}

func (s *Service) questClearRewards(packID, quest int, designRewards []gamedata.Reward, items []assets.Item, questEquipment *assets.Equipment) []byte {
	var rewards []byte
	if s.collection != nil {
		if grant, found := s.collection.Grant(questRewardIdentity(packID, quest, s.questDifficultyFor(packID, quest)) + ":costumes"); found {
			rewards = append(rewards, roster.CollectionRewardBundle(s.collection, grant)...)
		}
	}
	for _, reward := range designRewards {
		if reward.Type != 2 && reward.Type != 3 && reward.Type != 4 && reward.Type != 12 && reward.Type != 20 {
			continue
		}
		currency := wire.AppendVarint(nil, 3, reward.Type)
		currency = wire.AppendVarint(currency, 4, reward.Count)
		rewards = wire.AppendBytes(rewards, 1, currency)
	}
	for _, item := range items {
		entry := assets.ItemWire(item)
		if item.Type == 17 {
			pictorial := wire.AppendVarint(nil, 1, 5)
			pictorial = wire.AppendVarint(pictorial, 2, item.ID)
			entry = wire.AppendBytes(entry, 7, pictorial)
		}
		rewards = wire.AppendBytes(rewards, 1, entry)
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		rewards = wire.AppendBytes(rewards, 6, view)
	}
	if questEquipment != nil {
		// RewardDBInfoBundle field 4 is EquipDBInfo. Equipment is an instance,
		// not an ItemDBInfo with a fabricated stack count.
		rewards = wire.AppendBytes(rewards, 4, assets.EquipmentWire(*questEquipment))
	}
	if packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID && s.questDifficultyFor(packID, quest) == 0 {
		rewardCharacter := encodeCharacter(s.seed.RewardCharacter)
		rewards = wire.AppendBytes(rewards, 2, rewardCharacter)
		costume := encodeCostume(s.seed.RewardCostume)
		rewards = wire.AppendBytes(rewards, 3, costume)
		for _, character := range s.seed.StoryCharacters {
			view := wire.AppendVarint(nil, 2, character.ID)
			view = wire.AppendVarint(view, 3, 6)
			rewards = wire.AppendBytes(rewards, 6, view)
		}
		viewCostume := wire.AppendVarint(nil, 2, s.seed.RewardCostume.ID)
		viewCostume = wire.AppendVarint(viewCostume, 3, 11)
		rewards = wire.AppendBytes(rewards, 6, viewCostume)
		viewCharacter := wire.AppendVarint(nil, 2, s.seed.RewardCharacter.ID)
		viewCharacter = wire.AppendVarint(viewCharacter, 3, 6)
		viewCharacter = wire.AppendVarint(viewCharacter, 4, 1)
		rewards = wire.AppendBytes(rewards, 6, viewCharacter)
	}
	return rewards
}

func (s *Service) clearResponse(ctx command.Context, packID, quest int, rewards []byte, nextItems []assets.Item, nextChars [][]byte) ([]byte, error) {
	var out []byte
	out = wire.AppendBytes(out, 1, rewards)
	next := s.nextQuestFor(ctx, packID, quest)
	if next != 0 {
		out = wire.AppendBytes(out, 2, s.questInfoWire(packID, next))
	} else {
		// QuestClearResponse.QuestInfo is dereferenced by the client
		// even when this is the final quest of a pack. An explicitly present,
		// empty QuestDBInfo gives that generated protobuf property a non-null
		// object whose Id is the client-recognized zero sentinel. Omitting the
		// field parses as null and makes the completion coroutine throw before
		// it can mark the pack complete. A final-pack official capture has not
		// yet been obtained, so this exact wire choice remains marked for parity
		// verification even though its client behavior is deterministic.
		out = wire.AppendBytes(out, 2, nil)
	}
	out = wire.AppendVarint(out, 3, uint64(quest))
	for _, item := range nextItems {
		out = wire.AppendBytes(out, 6, assets.ItemWire(item))
	}
	if s.packQuestDesigns(packID)[quest].Type == 0 && next == 0 && s.packCompleteFor(packID) {
		// The final normal quest unlocks PackTable.NextPackId. Without these
		// PackDBInfo updates the client cannot find the next story pack and
		// falls back to presenting the hard-difficulty objective.
		rows, err := s.packDBInfoRows(ctx)
		if err != nil {
			return nil, err
		}
		for _, info := range rows {
			out = wire.AppendBytes(out, 11, info)
		}
	}
	// Receiving a character is not a request to change the saved formation.
	out = s.appendCurrentBattleDeck(out, 4)
	for _, char := range nextChars {
		out = wire.AppendBytes(out, 5, char)
	}
	out = wire.AppendBytes(out, 9, s.questLevelInfoWire(packID, s.questDifficultyFor(packID, quest)))
	out = wire.AppendBytes(out, 12, nil)
	return wire.AppendBytes(out, 13, nil), nil
}

func (s *Service) ApplyTalentFieldAbsorb(ctx command.Context, _ string, _ roster.Character, rule gamedata.TalentUseRule, ids []uint64) ([]byte, error) {
	pack, mapID, _, err := s.TalentFieldContext(ctx)
	if err != nil {
		return nil, err
	}
	if rule.Class != 4 || len(ids) == 0 {
		return nil, fmt.Errorf("%w: field absorb talent class %d with %d objects", ErrInvalidRequest, rule.Class, len(ids))
	}
	design, err := s.fieldObjectDesign(ctx, pack)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, id := range ids {
		obj, ok := design.Objects[int(id)]
		if !ok || obj.MapID != int(mapID) || (obj.Type != 1 && obj.Type != 3) {
			return nil, fmt.Errorf("world: absorption target is not an acquisition object")
		}
		bundle, e := s.openFieldObject(ctx, pack, obj.GroupID, int(id))
		if e != nil {
			return nil, e
		}
		e = wire.Walk(bundle, func(f wire.Field) error {
			to := map[int]int{1: 3, 2: 5, 3: 6, 4: 4}[f.Number]
			if to > 0 {
				out = wire.AppendBytes(out, to, f.Value)
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

// QuestClear.CharInfo is processed by AddCharDBInfoReward and the join UI,
// not as a complete roster snapshot. DeckInfo still carries the full formation;
// only joining characters or authored level/costume changes belong in CharInfo.
func storyPartyChanges(previous []roster.Character, next [][]byte) ([][]byte, error) {
	type appearance struct{ id, level, costume, useCostume uint64 }
	known := make(map[uint64]appearance, len(previous))
	for _, c := range previous {
		known[c.InvenIndex] = appearance{c.ID, c.Level, c.CostumeID, c.UseCostume}
	}
	var changed [][]byte
	for _, body := range next {
		var index uint64
		var value appearance
		for field, destination := range map[int]*uint64{1: &index, 2: &value.id, 4: &value.level, 5: &value.costume, 7: &value.useCostume} {
			var err error
			*destination, _, err = wire.Varint(body, field)
			if err != nil {
				return nil, err
			}
		}
		if old, exists := known[index]; exists && old == value {
			continue
		}
		changed = append(changed, body)
		known[index] = value
	}
	return changed, nil
}

func (s *Service) resolveActivePartyWires(ctx command.Context, packID, questID int) ([][]byte, [][]byte, error) {
	party, err := s.resolveStoryCharacters(ctx, packID, questID)
	if err != nil {
		return nil, nil, err
	}
	characters := make([][]byte, 0, len(party))
	for _, c := range party {
		characters = append(characters, encodeCharacter(c))
	}
	return characters, s.currentBattleDeckWires(), nil
}

func intsRequest(raw []byte, number int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type == 0 {
			v, n := binary.Uvarint(f.Value)
			if n <= 0 {
				return fmt.Errorf("%w: field %d varint", wire.ErrMalformed, number)
			}
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d, want varint or packed", ErrInvalidRequest, number, f.Type)
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return fmt.Errorf("%w: field %d packed varint", wire.ErrMalformed, number)
			}
			out = append(out, v)
			raw = raw[n:]
		}
		return nil
	})
	return out, err
}

func (s *Service) handleFieldResearch(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 || seq > 0x7fffffff {
		return 59, nil, true, fmt.Errorf("%w: field research sequence %d (present=%t): %v", ErrInvalidRequest, seq, present, err)
	}
	pack, err := requestPack(request)
	if err != nil {
		return 59, nil, true, err
	}
	id, _, err := wire.Varint(request, 3)
	if err != nil || id == 0 || id > 0x7fffffff || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return 59, nil, true, fmt.Errorf("%w: field research object %d in pack %d is unavailable: %v", ErrInvalidRequest, id, pack, err)
	}
	design, err := s.researchDesign(pack)
	if err != nil {
		return 59, nil, true, err
	}
	obj, ok := design.Objects[int(id)]
	if !ok || obj.CollectionID == 0 && obj.Reward.Type == 0 {
		return 59, nil, true, fmt.Errorf("%w: field object %d in pack %d has no research reward", ErrInvalidRequest, id, pack)
	}
	position, saved := s.state.Position()
	mapOK := false
	for _, mapID := range obj.Maps {
		if saved && position.PackID == pack && position.Position.MapID == mapID {
			mapOK = true
		}
	}
	if !mapOK {
		return 59, nil, true, fmt.Errorf("%w: research outside current map", ErrInvalidRequest)
	}
	// Quest interactions use QuestUpdate, while FieldObjectResearch grants the
	// object's collection/reward after its quest interaction is no longer active.
	for _, quest := range obj.InteractionQuests {
		if _, active := s.state.QuestInPack(quest, pack, s.questDifficulty(pack)); active && !s.state.QuestCleared(quest, pack, s.questDifficulty(pack)) {
			return 59, nil, true, fmt.Errorf("%w: research belongs to active quest", ErrInvalidRequest)
		}
	}
	if obj.Type == 1 {
		eligible := false
		// This server validates a learned research talent. Client animation and
		// temporary highlight flags are presentation state rather than authority.
		if s.characters != nil {
			for _, c := range s.characters.All(ctx) {
				if c.TalentLevel > 0 && s.researchCharacters[c.ID] {
					eligible = true
					break
				}
			}
		}
		if !eligible {
			return 59, nil, true, fmt.Errorf("%w: research talent unavailable", ErrInvalidRequest)
		}
	}
	prior, err := s.state.ResearchObjects(ctx, pack)
	if err != nil {
		return 59, nil, true, err
	}
	for _, v := range prior {
		if v == int(id) {
			_, found, e := s.state.ResearchObjectReply(ctx, pack, int(id))
			if e != nil {
				return 59, nil, true, e
			}
			if !found {
				return 59, nil, true, fmt.Errorf("world: researched object reward receipt missing")
			}
			// RewardItem is a delta. A fresh request for an already researched
			// object must not credit that delta again on the client.
			return 59, wire.AppendVarint(nil, 1, seq), true, nil
		}
	}
	if s.researchEconomy == nil {
		return 59, nil, true, fmt.Errorf("world: research economy unavailable")
	}
	var rewards []gamedata.Reward
	if obj.Reward.Type != 0 && obj.Reward.Count > 0 {
		rewards = append(rewards, obj.Reward)
	}
	if obj.CollectionID > 0 {
		rewards = append(rewards, gamedata.Reward{Type: 17, ID: uint64(obj.CollectionID), Count: 1})
	}
	bundle, err := s.researchEconomy.Apply(ctx, fmt.Sprintf("research:%d:%d", pack, id), nil, rewards)
	if err != nil {
		return 59, nil, true, err
	}
	var items []byte
	err = wire.Walk(bundle, func(f wire.Field) error {
		if f.Number == 1 && f.Type == 2 {
			items = wire.AppendBytes(items, 2, f.Value)
		}
		return nil
	})
	if err != nil {
		return 59, nil, true, err
	}
	if err = s.state.MarkResearchObject(ctx, pack, int(id), items); err != nil {
		return 59, nil, true, err
	}
	return 59, append(wire.AppendVarint(nil, 1, seq), items...), true, nil
}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if code, body, handled, err := s.handlePacks(ctx, path, request); handled {
		return code, body, handled, err
	}
	if code, out, handled, err := s.handleSkyWay(ctx, path, request); handled {
		return code, out, handled, err
	}
	if s.todayQuests != nil {
		if code, body, handled, err := s.todayQuests.Handle(ctx, path, request); handled {
			if err == nil {
				body = s.commissionResponseDeck(code, body)
			}
			return code, body, handled, err
		}
	}
	switch path {
	case "/PackSubQuestClearInfo":
		return s.packSubQuestClearInfo(request)
	case "/ReputationInfo":
		return s.reputationInfo(ctx, request)
	case "/QuestImmortalReset":
		if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
			return 70, nil, true, fmt.Errorf("%w: QuestImmortalReset sequence %d (present=%t): %v", ErrInvalidRequest, seq, found, err)
		}
		return 70, nil, true, fmt.Errorf("world: immortal quest difficulty is unavailable in the current design")
	case "/QuestMaxClearInfo":
		return s.questMaxClearInfo(request)
	case "/ActiveMap":
		return s.handleActiveMap(ctx, request)
	case "/StatueObjectReward":
		return s.handleStatueReward(ctx, request)
	case "/TimePause":
		return s.handleTimePause(ctx, request)
	case "/MonsterInfo":
		return s.handleMonsterInfo(ctx, request)
	case "/FieldMonsterRegen":
		return s.handleFieldMonsterRegen(ctx, request)
	case "/FieldMonsterEvent", "/FieldMonsterDamage":
		return s.handleFieldMonsterEvent(ctx, path, request)
	case "/FieldMonsterReward":
		return s.handleFieldMonsterReward(request)
	case "/Overwhelm":
		return s.handleOverwhelm(ctx, request)
	case "/QuestUpdate":
		return s.handleQuestUpdate(ctx, request)
	case "/FieldObjectInfo":
		return s.handleFieldObjectInfo(ctx, request)
	case "/FieldObjectReward":
		return s.handleFieldObjectReward(ctx, request)
	case "/FieldObjectRewardList":
		return s.handleFieldObjectRewardList(ctx, request)
	case "/FieldObjectPreview":
		return s.handleFieldObjectPreview(ctx, request)
	case "/FieldObjectRespawn":
		return s.handleFieldObjectRespawn(ctx, request)
	case "/FieldObjecPositionUpdate", "/FieldObjectPositionUpdate":
		return s.handleFieldObjectPosition(ctx, request)
	case "/FieldObjectResearch":
		return s.handleFieldResearch(ctx, request)
	case "/TrapDamage", "/FieldTrapInfo", "/InteractionTrigger":
		return s.handleFieldTraps(ctx, path, request)
	case "/QuestInfo", "/QuestAccept", "/QuestGiveUp":
		return s.handleQuestSelection(ctx, path, request)
	case "/QuestRetryBattleSkipSave":
		return s.saveQuestReplayBattleSkip(ctx, request)
	case "/CharInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 0, nil, true, errors.New("world: CharInfo missing sequence")
		}
		var response []byte
		characters := s.visibleOwnedCharacters(s.characters.All(ctx))
		if s.tutorialRosterRestricted() {
			// The tutorial roster still contains only starter identities, but
			// their field HP must come from persisted state rather than falling
			// through to Starter.Handle's immutable new-account HP.
			characters = make([]roster.Character, 0, len(s.starter.Characters))
			for _, seeded := range s.starter.Characters {
				character, exists := s.characters.Find(ctx, seeded.InvenIndex)
				if !exists {
					return 0, nil, true, fmt.Errorf("world: missing starter character %d", seeded.InvenIndex)
				}
				characters = append(characters, character)
			}
		}
		if s.decks != nil {
			seen := map[uint64]bool{}
			for _, char := range characters {
				seen[char.InvenIndex] = true
			}
			deckCharacters := s.decks.CurrentDeck()
			for _, entry := range s.decks.CurrentFieldDeck(ctx) {
				deckCharacters = append(deckCharacters, deck.DeckEntry{CharacterInvenIndex: entry.CharacterInvenIndex})
			}
			for _, entry := range deckCharacters {
				if seen[entry.CharacterInvenIndex] {
					continue
				}
				if char, ok := s.characters.Find(ctx, entry.CharacterInvenIndex); ok && roster.IsStoryCharacter(char) {
					characters = append(characters, char)
					seen[char.InvenIndex] = true
				}
			}
		}
		for _, character := range characters {
			response = wire.AppendBytes(response, 1, encodeCharacter(character))
		}
		control := s.starter.FieldCharControlDeckType
		if s.decks != nil {
			control = s.decks.FieldControlType()
		}
		response = wire.AppendVarint(response, 2, control)
		return 9, response, true, nil
	case "/CostumeInfo":
		if s.tutorialRosterRestricted() {
			return 0, nil, false, nil
		}
		var response []byte
		costumes := s.starter.Costumes
		if s.collection != nil {
			costumes = s.collection.Costumes()
		}
		var selections map[uint64]uint64
		if s.prestigeSelections != nil {
			var err error
			selections, err = s.prestigeSelections(ctx)
			if err != nil {
				return 0, nil, true, err
			}
		}
		for _, costume := range costumes {
			if design := selections[costume.ID]; design != 0 {
				costume.DesignID = design
			}
			response = wire.AppendBytes(response, 1, encodeCostume(costume))
		}
		if s.collection == nil {
			costume := s.seed.RewardCostume
			if design := selections[costume.ID]; design != 0 {
				costume.DesignID = design
			}
			response = wire.AppendBytes(response, 1, encodeCostume(costume))
		}
		return 40, response, true, nil
	case "/QuestClear":
		quest, pack, err := requestQuest(request)
		if err != nil {
			return 0, nil, true, err
		}
		quests, unlocked := s.questsFor(ctx, pack)
		design, exists := quests[quest]
		if !unlocked || !s.packUnlocked(ctx, pack) || !exists {
			return 0, nil, true, fmt.Errorf("%w: quest %d pack %d", ErrInvalidRequest, quest, pack)
		}
		if !s.canClear(ctx, pack, quest) {
			return 0, nil, true, fmt.Errorf("%w: quest %d is not active", ErrInvalidRequest, quest)
		}
		quickPlay, _, err := wire.Varint(request, 5)
		if err != nil || quickPlay > 1 {
			return 0, nil, true, fmt.Errorf("%w: invalid quick-play flag", ErrInvalidRequest)
		}
		if quickPlay != 0 {
			difficulty := s.questDifficultyFor(pack, quest)
			if design.Type != 0 || difficulty < 1 || difficulty > 2 || !s.questDifficulties[pack][difficulty] {
				return 0, nil, true, fmt.Errorf("%w: quest %d pack %d difficulty %d unavailable for quick play", ErrInvalidRequest, quest, pack, difficulty)
			}
			for _, id := range s.storyCatalog.Packs[pack].MainQuestIDs {
				if !s.state.QuestCleared(id, pack, difficulty-1) {
					return 0, nil, true, fmt.Errorf("%w: quick play requires previous difficulty completion", ErrInvalidRequest)
				}
			}
			if design.ConditionType == 1 || design.ConditionType == 3 {
				if design.ConditionType != 3 || design.ConditionCount != 1 || s.questBattleWon == nil {
					return 0, nil, true, fmt.Errorf("%w: quick battle completion rule unavailable for quest %d", ErrInvalidRequest, quest)
				}
				won, err := s.questBattleWon(ctx, pack, quest, difficulty)
				if err != nil {
					return 0, nil, true, err
				}
				if !won {
					return 0, nil, true, fmt.Errorf("%w: quick quest requires committed battle victory pack=%d quest=%d difficulty=%d", ErrInvalidRequest, pack, quest, difficulty)
				}
			}
		}
		if design.Type == 0 {
			replay, replaying, err := s.state.QuestReplay(ctx, pack)
			if err != nil {
				return 0, nil, true, err
			}
			if replaying {
				return s.clearQuestReplay(ctx, pack, quest, replay)
			}
		}
		wasCleared := s.state.QuestCleared(quest, pack, s.questDifficultyFor(pack, quest))
		var previousParty []roster.Character
		if design.Type == 0 && s.storyRoster != nil {
			previousParty, err = s.resolveStoryCharacters(ctx, pack, quest)
			if err != nil {
				return 0, nil, true, err
			}
		}
		items, questEquipment, err := s.grantQuestRewards(ctx, pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)])
		if err != nil {
			return 0, nil, true, err
		}
		lifeRewards, err := s.grantLifeQuestRewards(ctx, pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)])
		if err != nil {
			return 0, nil, true, err
		}
		currencyRewards, err := s.grantAdditionalQuestCurrencies(ctx, pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)])
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.state.ClearQuest(ctx, quest, pack, s.questDifficultyFor(pack, quest)); err != nil {
			return 0, nil, true, fmt.Errorf("world: clear quest: %w", err)
		}
		if !wasCleared && quickPlay == 0 {
			if err := s.beginQuestCompletionPresentation(ctx, pack, quest, s.questDifficultyFor(pack, quest)); err != nil {
				return 0, nil, true, err
			}
		}
		if s.collection != nil && quest == s.seed.BattleUnlockQuestID && pack == s.seed.PackID && s.questDifficulty(pack) == 0 && !wasCleared {
			if err := s.collection.AttachRewardCostume(ctx, s.seed.RewardCostume); err != nil {
				return 0, nil, true, fmt.Errorf("world: attach cleared quest costume: %w", err)
			}
		}
		if selection, selected := s.state.Selection(pack); selected && design.Type == 0 && !wasCleared {
			selection.QuestID = s.nextQuestFor(ctx, pack, quest)
			if err := s.state.SelectQuest(ctx, pack, selection); err != nil {
				return 0, nil, true, err
			}
		}
		var nextItems []assets.Item
		var nextChars [][]byte
		if next := s.nextQuestFor(ctx, pack, quest); next != 0 {
			if design.Type == 0 {
				nextChars, _, err = s.resolveActivePartyWires(ctx, pack, next)
				if err != nil {
					return 0, nil, true, err
				}
				nextChars, err = storyPartyChanges(previousParty, nextChars)
				if err != nil {
					return 0, nil, true, err
				}
			} else if !wasCleared {
				if err := s.state.AcceptQuest(ctx, next, pack, 0); err != nil {
					return 0, nil, true, err
				}
			}
			nextItems, err = s.ensureQuestItems(ctx, pack, next)
			if err != nil {
				return 0, nil, true, err
			}
		}
		saved, positionFound := s.state.Position()
		slog.Info("quest transition prepared", "account", ctx.AccountID, "pack", pack, "quest", quest,
			"difficulty", s.questDifficultyFor(pack, quest), "already_cleared", wasCleared,
			"quick_play", quickPlay != 0,
			"next_quest", s.nextQuestFor(ctx, pack, quest), "timeline", design.CompleteCinemaName,
			"timeline_map", design.TimelineStartMapID, "position_found", positionFound,
			"saved_position", saved.Position, "party_changes", len(nextChars), "items", len(items))
		rewards := append(s.questClearRewards(pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)], items, questEquipment), lifeRewards...)
		rewards = append(rewards, currencyRewards...)
		out, err := s.clearResponse(ctx, pack, quest, rewards, nextItems, nextChars)
		if err != nil {
			return 0, nil, true, err
		}
		return 18, out, true, nil
	default:
		return 0, nil, false, nil
	}
}
