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
	"slices"
	"sort"
)

func requestPack(request []byte) (int, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, ErrInvalidRequest
	}
	pack, found, err := wire.Varint(request, 2)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, ErrInvalidRequest
	}
	return int(pack), nil
}

func requestQuest(request []byte) (int, int, error) {
	quest, err := requestPack(request) // fields 1 and 2 have the same validation.
	if err != nil {
		return 0, 0, err
	}
	pack, found, err := wire.Varint(request, 3)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, 0, ErrInvalidRequest
	}
	return quest, int(pack), nil
}

// packInfo is the canonical protobuf encoding of the semantic new-account
// starter-pack state. Its response is generated from the authoritative local progress state.

func (s *Service) packInfoFor(ctx command.Context, packID int) ([]byte, error) {
	out, err := s.basePackInfoFor(ctx, packID)
	if err != nil {
		return out, err
	}
	rows, err := s.monsterRows(ctx, packID, nil)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out = wire.AppendBytes(out, 6, row)
	}
	buffs, err := s.fieldBuffInfo(ctx)
	if err != nil {
		return nil, err
	}
	out = append(out, buffs...)
	if s.huntingGround == nil {
		return out, nil
	}
	ground, err := s.huntingGround.EnsureForPack(ctx, packID)
	if err != nil {
		return nil, err
	}
	if len(ground) == 0 {
		return out, nil
	}
	out, _, err = wire.ReplaceBytes(out, 12, ground)
	return out, err
}

func (s *Service) basePackInfoFor(ctx command.Context, packID int) ([]byte, error) {
	var out []byte
	if active := s.firstUnclearedQuestFor(packID); active != 0 {
		quest := s.questInfoWire(packID, active)
		chars, _, err := s.resolveActivePartyWires(ctx, packID, active)
		if err != nil {
			return nil, err
		}
		for _, char := range chars {
			out = wire.AppendBytes(out, 1, char)
		}
		out = wire.AppendBytes(out, 2, quest)
	}
	for _, quest := range s.activeSideQuestWires(packID) {
		out = wire.AppendBytes(out, 2, quest)
	}
	cleared := s.completedQuestIDs(packID)
	// CommonPacket requests TodayQuestInfo after parsing this response, before
	// the waypoint callback calls PackManager.Enter. That separate response owns
	// commission restoration; including commissions here lets Enter append them
	// a second time when TodayQuestInfo arrives first, crashing the quest HUD.
	if len(cleared) != 0 {
		var packed []byte
		for _, id := range cleared {
			packed = binary.AppendUvarint(packed, uint64(id))
		}
		out = wire.AppendBytes(out, 3, packed)
	}
	position := "{}"
	mapID := 0
	restored := false
	if saved, found := s.state.Position(); found && saved.PackID == packID && saved.Difficulty == s.questDifficulty(packID) && saved.RawJSON != "" {
		if pack, arena := s.fieldPacks[packID]; arena && !pack.MapIDs[saved.Position.MapID] {
			return nil, fmt.Errorf("world: saved map %d does not belong to arena pack %d", saved.Position.MapID, packID)
		}
		position = saved.RawJSON
		mapID = saved.Position.MapID
		restored = true
	}
	slog.Info("world: deliver field position", "pack", packID, "map", mapID, "restored", restored)
	out = wire.AppendString(out, 4, position)
	if s.npcReputation != nil {
		rows, err := s.npcReputationRows(ctx, packID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = wire.AppendBytes(out, 9, row)
		}
	}
	// The remaining starter-only records were observed in the official
	// starter-pack response. They represent reputation, hunting-ground, statue,
	// and reward state, not generic defaults, so a newly entered later pack must
	// not inherit them.
	if packID != s.seed.PackID {
		visit := wire.AppendVarint(nil, 5, uint64(packID))
		return wire.AppendBytes(out, 12, visit), nil
	}
	for _, state := range s.seed.InitialReputations {
		if s.npcReputation != nil {
			break
		}
		row := wire.AppendVarint(nil, 1, state.GroupID)
		row = wire.AppendVarint(row, 2, state.State)
		if state.ElapsedSeconds != 0 {
			row = wire.AppendVarint(row, 3, state.ElapsedSeconds)
		}
		out = wire.AppendBytes(out, 9, row)
	}
	visit := wire.AppendVarint(nil, 5, uint64(packID))
	out = wire.AppendBytes(out, 12, visit)
	for _, state := range s.seed.InitialRankStatues {
		row := wire.AppendVarint(nil, 1, state.ID)
		row = wire.AppendVarint(row, 2, state.Season)
		if state.Error {
			row = wire.AppendVarint(row, 3, 1)
		}
		out = wire.AppendBytes(out, 14, row)
	}
	if s.packJamDesign == nil {
		return out, nil
	}
	if err := s.packJamDesign.ValidateReward(); err != nil {
		return nil, err
	}
	reward := wire.AppendVarint(nil, 3, s.packJamDesign.Reward.Type)
	reward = wire.AppendVarint(reward, 4, s.packJamDesign.Reward.Count)
	group := wire.AppendBytes(nil, 1, reward)
	group = wire.AppendBytes(group, 6, reward)
	return wire.AppendBytes(out, 16, group), nil
}

func (s *Service) clearResponse(ctx command.Context, packID, quest int, designRewards []gamedata.Reward, items []assets.Item, questEquipment *assets.Equipment, nextItems []assets.Item, nextChars [][]byte) []byte {
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
	if s.storyCatalog.Packs[packID].Quests[quest].Type == 0 && next == 0 && s.packCompleteFor(packID) {
		// The final normal quest unlocks PackTable.NextPackId. Without these
		// PackDBInfo updates the client cannot find the next story pack and
		// falls back to presenting the hard-difficulty objective.
		for _, info := range s.packDBInfoRows(ctx) {
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
	return wire.AppendBytes(out, 13, nil)
}

func (s *Service) packDBInfoRows(ctx command.Context) [][]byte {
	if s.storyCatalog == nil {
		return nil
	}
	return s.storyPackDBInfoRows(ctx)
}

func (s *Service) accountPackInfo(ctx command.Context) []byte {
	var out []byte
	for _, info := range s.packDBInfoRows(ctx) {
		out = wire.AppendBytes(out, 1, info)
	}
	for _, info := range s.packDBInfoRows(ctx) {
		id, _, _ := wire.Varint(info, 1)
		for level := 0; level <= 4; level++ {
			if len(s.state.ClearedQuests(int(id), level)) > 0 || s.questDifficulty(int(id)) == level {
				out = wire.AppendBytes(out, 2, s.questLevelInfoWire(int(id), level))
			}
		}
	}
	if s.seed.SquareSceneID != 0 {
		out = wire.AppendVarint(out, 5, s.seed.SquareSceneID)
	}
	return out
}

// Absorb consumes the exact acquisition objects requested by the client using
// the same period receipts and reward graph as an ordinary field interaction.
func (s *Service) ApplyTalentFieldAbsorb(ctx command.Context, _ string, _ roster.Character, rule gamedata.TalentUseRule, ids []uint64) ([]byte, error) {
	pack, mapID, _, err := s.TalentFieldContext(ctx)
	if err != nil {
		return nil, err
	}
	if rule.Class != 4 || len(ids) == 0 {
		return nil, ErrInvalidRequest
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

func (s *Service) storyPackDBInfoRows(ctx command.Context) [][]byte {
	ids := make([]int, 0, len(s.storyCatalog.Packs))
	for id := range s.storyCatalog.Packs {
		if s.storyPackUnlocked(ctx, id) {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	var rows [][]byte
	for _, id := range ids {
		row := wire.AppendVarint(nil, 1, uint64(id))
		selection, _ := s.state.Selection(id)
		if selection.Difficulty != 0 {
			row = wire.AppendVarint(row, 4, uint64(selection.Difficulty))
		}
		if selection.Option != 0 {
			row = wire.AppendVarint(row, 5, uint64(selection.Option))
		}
		mainCleared := 0
		for _, qid := range s.storyCatalog.Packs[id].MainQuestIDs {
			if s.state.QuestCleared(qid, id, s.questDifficulty(id)) {
				mainCleared++
			}
		}
		if mainCleared > 0 {
			row = wire.AppendVarint(row, 2, uint64(mainCleared))
		}
		if s.packCompleteFor(id) {
			row = wire.AppendVarint(row, 3, 1)
		}
		purchased := false
		if s.collection != nil {
			_, owned := s.collection.Grant(packPurchaseIdentity(id))
			purchased = purchased || owned
		}
		if purchased {
			row = wire.AppendVarint(row, 8, 1)
		}
		rows = append(rows, row)
	}
	if saved, found := s.state.Position(); found {
		if pack, arena := s.fieldPacks[saved.PackID]; arena && pack.MapIDs[saved.Position.MapID] {
			rows = append(rows, wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(saved.PackID)), 8, 1))
		}
	}
	return rows
}

func (s *Service) handlePackSummary(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackSummaryInfoList missing or invalid sequence", ErrInvalidRequest)
	}
	var response []byte
	for _, row := range s.packDBInfoRows(ctx) {
		id, found, err := wire.Varint(row, 1)
		if err != nil || !found {
			return 0, nil, true, fmt.Errorf("world: invalid account pack row")
		}
		if !s.packSummaryTargets[int(id)] {
			continue
		}
		once, regen, research, err := s.packRewardCounts(ctx, int(id))
		if err != nil {
			return 0, nil, true, err
		}
		row := wire.AppendVarint(nil, 1, id)
		if once > 0 {
			row = wire.AppendVarint(row, 2, once)
		}
		if regen > 0 {
			row = wire.AppendVarint(row, 3, regen)
		}
		defeated, err := s.packMonsterDefeatedCount(ctx, int(id))
		if err != nil {
			return 0, nil, true, err
		}
		if defeated > 0 {
			row = wire.AppendVarint(row, 4, defeated)
		}
		if research > 0 {
			row = wire.AppendVarint(row, 5, research)
		}
		response = wire.AppendBytes(response, 1, row)
	}
	return 625, response, true, nil
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
				return wire.ErrMalformed
			}
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return ErrInvalidRequest
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return wire.ErrMalformed
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
		return 59, nil, true, ErrInvalidRequest
	}
	pack, err := requestPack(request)
	if err != nil {
		return 59, nil, true, err
	}
	id, _, err := wire.Varint(request, 3)
	if err != nil || id == 0 || id > 0x7fffffff || !s.packUnlocked(ctx, pack) || !s.fieldObjectCurrentPack(pack) {
		return 59, nil, true, ErrInvalidRequest
	}
	design, err := s.researchDesign(pack)
	if err != nil {
		return 59, nil, true, err
	}
	obj, ok := design.Objects[int(id)]
	if !ok || obj.CollectionID == 0 && obj.Reward.Type == 0 {
		return 59, nil, true, ErrInvalidRequest
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

func (s *Service) handlePackRewardCounts(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	packs, err := intsRequest(request, 2)
	if err != nil || len(packs) == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	types, err := intsRequest(request, 3)
	if err != nil {
		return 226, nil, true, err
	}
	if len(types) == 0 {
		types = []uint64{1, 2, 3, 4}
	}
	var out []byte
	seen := map[[2]uint64]bool{}
	for _, p := range packs {
		pack := int(p)
		if pack <= 0 || !s.packUnlocked(ctx, pack) {
			return 226, nil, true, ErrInvalidRequest
		}
		for _, t := range types {
			if t < 1 || t > 4 {
				return 226, nil, true, ErrInvalidRequest
			}
			key := [2]uint64{p, t}
			if seen[key] {
				continue
			}
			seen[key] = true
			var count, max uint64
			if t == 1 {
				d, e := s.researchDesign(pack)
				if e != nil {
					return 226, nil, true, e
				}
				ids, e := s.state.ResearchObjects(ctx, pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if o.CollectionID > 0 || o.Reward.Type > 0 {
						max++
					}
				}
				for _, id := range ids {
					if o, ok := d.Objects[id]; ok && (o.CollectionID > 0 || o.Reward.Type > 0) {
						count++
					}
				}
			} else {
				d, e := s.fieldObjectDesign(ctx, pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if !matchesFieldCount(o, t) {
						continue
					}
					period, e := s.fieldObjectPeriodFor(pack, o)
					if t != 4 || e == nil {
						max++
					}
					if e != nil {
						continue
					}
					opened, e := s.state.FieldRewardOpened(ctx, pack, o.ID, period)
					if e != nil {
						return 226, nil, true, e
					}
					if opened {
						count++
					}
				}
			}
			info := wire.AppendVarint(nil, 1, t)
			info = wire.AppendVarint(info, 2, p)
			info = wire.AppendVarint(info, 3, count)
			info = wire.AppendVarint(info, 4, max)
			out = wire.AppendBytes(out, 1, info)
		}
	}
	return 226, out, true, nil
}

// EnsureInitialPackPurchase is called only by new-account initialization. It
// awards the bootstrap pack's real purchase rewards, not a recovery inference
// from a saved position or from the former seed-only unlock chain.
func (s *Service) EnsureInitialPackPurchase(ctx command.Context) error {
	_, err := s.purchaseStoryPack(ctx, s.startingPack(), true)
	return err
}

func (s *Service) handlePackBuy(ctx command.Context, request []byte) (int, []byte, bool, error) {
	id, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if pack, found, err := s.resolveEventFieldPack(ctx, id); err != nil {
		return 0, nil, true, err
	} else if found {
		bundle, err := s.purchasePack(ctx, id, pack.BuyType, pack.BuyPrice, pack.BuyRewards, false)
		if err != nil {
			return 0, nil, true, err
		}
		response := wire.AppendBytes(nil, 1, s.eventPackDBInfo(pack))
		return 6, wire.AppendBytes(response, 2, bundle), true, nil
	}
	if !s.packUnlocked(ctx, id) {
		return 0, nil, true, fmt.Errorf("%w: unavailable purchase pack %d", ErrInvalidRequest, id)
	}
	bundle, err := s.purchaseStoryPack(ctx, id, false)
	if err != nil {
		return 0, nil, true, err
	}
	var info []byte
	for _, row := range s.packDBInfoRows(ctx) {
		value, _, _ := wire.Varint(row, 1)
		if value == uint64(id) {
			info = row
			break
		}
	}
	if info == nil {
		return 0, nil, true, fmt.Errorf("world: purchased pack absent from account")
	}
	response := wire.AppendBytes(nil, 1, info)
	return 6, wire.AppendBytes(response, 2, bundle), true, nil
}

func (s *Service) purchaseStoryPack(ctx command.Context, id int, initial bool) ([]byte, error) {
	if s.storyCatalog == nil {
		return nil, fmt.Errorf("world: purchase services unavailable")
	}
	pack, exists := s.storyCatalog.Packs[id]
	if !exists {
		return nil, fmt.Errorf("%w: unknown purchase pack %d", ErrInvalidRequest, id)
	}
	return s.purchasePack(ctx, id, pack.BuyType, pack.BuyPrice, pack.BuyRewards, initial)
}

// purchasePack shares the same durable receipt and enclosing account transaction
// across story and calendar-authorized hidden event packs.
func (s *Service) purchasePack(ctx command.Context, id int, buyType, buyPrice uint64, buyRewards []gamedata.Reward, initial bool) ([]byte, error) {
	if s.collection == nil || s.wallet == nil || s.inventory == nil {
		return nil, fmt.Errorf("world: purchase services unavailable")
	}
	identity := packPurchaseIdentity(id)
	if _, owned := s.collection.Grant(identity); owned {
		return []byte{}, nil
	}
	for _, reward := range buyRewards {
		if !purchaseCurrency(reward.Type) && reward.Type != 19 && reward.Type != 11 {
			return nil, fmt.Errorf("world: unsupported pack purchase reward type %d", reward.Type)
		}
	}
	if !initial && buyPrice != 0 {
		var err error
		switch buyType {
		case 2:
			_, err = s.wallet.SpendJewelryOnce(ctx, identity+":price", buyPrice)
		case 3:
			_, err = s.wallet.SpendFreeJewelryOnce(ctx, identity+":price", buyPrice)
		case 4:
			_, err = s.wallet.SpendGoldOnce(ctx, identity+":price", buyPrice)
		case 12:
			_, err = s.wallet.SpendCatalystOnce(ctx, identity+":price", buyPrice)
		default:
			return nil, fmt.Errorf("%w: unsupported pack purchase currency%d", ErrInvalidRequest, buyType)
		}
		if err != nil {
			return nil, err
		}
	}
	items, err := s.grantPurchaseRewards(ctx, identity, buyRewards)
	if err != nil {
		return nil, err
	}
	if err := s.collection.RecordGrantMarker(ctx, identity); err != nil {
		return nil, err
	}
	var bundle []byte
	if grant, found := s.collection.Grant(identity + ":costumes"); found {
		bundle = append(bundle, roster.CollectionRewardBundle(s.collection, grant)...)
	}
	for _, reward := range buyRewards {
		if purchaseCurrency(reward.Type) {
			currency := wire.AppendVarint(wire.AppendVarint(nil, 3, reward.Type), 4, reward.Count)
			bundle = wire.AppendBytes(bundle, 1, currency)
		}
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	return bundle, nil
}

func (s *Service) handlePackDocking(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if _, known := s.questsFor(ctx, packID); !known || !s.packUnlocked(ctx, packID) {
		return 0, nil, true, fmt.Errorf("%w: unavailable docking pack %d", ErrInvalidRequest, packID)
	}
	if s.wallet == nil || s.packJamDesign == nil {
		return 0, nil, true, fmt.Errorf("world: pack jam design or wallet unavailable")
	}
	identity := packJamIdentity(packID)
	if path == "/PackPreviewInfo" {
		var response []byte
		if active := s.firstUnclearedQuestFor(packID); active != 0 {
			quest := wire.AppendVarint(nil, 1, uint64(active))
			quest = wire.AppendVarint(quest, 6, uint64(packID))
			response = wire.AppendBytes(response, 1, quest)
		}
		if s.wallet.WasGranted(identity) {
			response = wire.AppendVarint(response, 3, 1)
		}
		return 104, response, true, nil
	}
	// Serialize the claim check and grant so concurrent domain callers cannot
	// return the animation reward twice. The wallet commits balance and ledger together.

	if s.wallet.WasGranted(identity) {
		return 72, nil, true, nil
	}
	reward := s.packJamDesign.Reward
	if s.packJamDesign.ValidateReward() != nil {
		return 0, nil, true, fmt.Errorf("world: unsupported pack jam reward")
	}
	if _, err := s.wallet.GrantQuestOnce(ctx, identity, []gamedata.Reward{reward}); err != nil {
		return 0, nil, true, fmt.Errorf("world: grant pack jam reward: %w", err)
	}
	item := wire.AppendVarint(nil, 3, reward.Type)
	item = wire.AppendVarint(item, 4, reward.Count)
	return 72, wire.AppendBytes(nil, 1, item), true, nil
}

func (s *Service) handlePackDetail(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo invalid sequence", ErrInvalidRequest)
	}
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(ctx, packID) || !s.packSummaryTargets[packID] {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo unavailable pack %d", ErrInvalidRequest, packID)
	}
	ids, err := s.openedFieldObjects(ctx, packID)
	if err != nil {
		return 0, nil, true, err
	}
	response := []byte{}
	rows, err := s.packMonsterRows(ctx, packID)
	if err != nil {
		return 0, nil, true, err
	}
	for _, row := range rows {
		response = wire.AppendBytes(response, 1, row)
	}
	for _, id := range ids {
		response = wire.AppendBytes(response, 2, wire.AppendVarint(nil, 1, uint64(id)))
	}
	research, e := s.state.ResearchObjects(ctx, packID)
	if e != nil {
		return 0, nil, true, e
	}
	for _, id := range research {
		response = wire.AppendVarint(response, 3, uint64(id))
	}
	return 627, response, true, nil
}

func (s *Service) eventPackDBInfo(pack gamedata.EventFieldPack) []byte {
	row := wire.AppendVarint(nil, 1, uint64(pack.ID))
	if s.eventPackPurchased(pack.ID) {
		row = wire.AppendVarint(row, 8, 1)
	}
	return row
}

func (s *Service) eventPackInfoRows(ctx command.Context) ([][]byte, error) {
	if s.eventFieldPacks == nil {
		return nil, nil
	}
	packs, err := s.eventFieldPacks.ListEventFieldPacks(ctx)
	if err != nil {
		return nil, err
	}
	var rows [][]byte
	for _, pack := range packs {
		// PackManager buys a hidden pack only when PackInfo has no row for it.
		if s.eventPackPurchased(pack.ID) {
			rows = append(rows, s.eventPackDBInfo(pack))
		}
	}
	return rows, nil
}

func (s *Service) enterEventFieldPack(ctx command.Context, pack gamedata.EventFieldPack) (int, []byte, bool, error) {
	if !s.eventPackPurchased(pack.ID) {
		return 0, nil, true, fmt.Errorf("%w: event pack %d is not purchased", ErrInvalidRequest, pack.ID)
	}
	position := pack.InitialPosition
	if position == "" {
		return 0, nil, true, fmt.Errorf("world: missing event pack initial position")
	}
	if saved, found := s.state.Position(); found && saved.PackID == pack.ID {
		if !slices.Contains(pack.MapIDs, saved.Position.MapID) {
			return 0, nil, true, fmt.Errorf("%w: map outside event pack", ErrInvalidRequest)
		}
		position = saved.RawJSON
	}
	response := wire.AppendString(nil, 4, position)
	buffs, err := s.fieldBuffInfo(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	response = append(response, buffs...)
	// The common callback dereferences HuntingGroundInfo even in hidden packs.
	// Use the domain-generated empty/current snapshot; never borrow the outside
	// map's monsters or story progress.
	var hunting []byte
	if s.huntingGround != nil {
		var err error
		hunting, err = s.huntingGround.EnsureForPack(ctx, pack.ID)
		if err != nil {
			return 0, nil, true, err
		}
	}
	response = wire.AppendBytes(response, 12, hunting)
	// Hidden-pack entry must retain the outside field position: the client
	// intentionally suppresses SaveUserPosition while playing these packs. The
	// persistent active pack also stays outside, so relogin cannot be stranded
	// in a hidden scene after its calendar closes.
	s.setCurrentPack(pack.ID)
	return 5, response, true, nil
}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if s.todayQuests != nil {
		if code, body, handled, err := s.todayQuests.Handle(ctx, path, request); handled {
			if err == nil {
				body = s.commissionResponseDeck(code, body)
			}
			return code, body, handled, err
		}
	}
	switch path {
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
	case "/PackRewardObjectCount":
		return s.handlePackRewardCounts(ctx, request)
	case "/QuestInfo", "/QuestAccept", "/QuestGiveUp":
		return s.handleQuestSelection(ctx, path, request)
	case "/PackBuy":
		return s.handlePackBuy(ctx, request)
	case "/PackDetailInfo":
		return s.handlePackDetail(ctx, request)
	case "/PackSummaryInfoList":
		return s.handlePackSummary(ctx, request)
	case "/PackPreviewInfo", "/PackJamEvent":
		return s.handlePackDocking(ctx, path, request)
	case "/PackInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 0, nil, true, errors.New("world: PackInfo missing sequence")
		}
		response := s.accountPackInfo(ctx)
		rows, err := s.eventPackInfoRows(ctx)
		if err != nil {
			return 0, nil, true, err
		}
		for _, row := range rows {
			response = wire.AppendBytes(response, 1, row)
		}
		return 4, response, true, nil
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
	case "/PackInGameInfo":
		if s.battleActive != nil && s.battleActive(ctx) {
			return 0, nil, true, fmt.Errorf("%w: active battle", ErrInvalidRequest)
		}
		pack, err := requestPack(request)
		if err != nil {
			return 0, nil, true, err
		}
		if eventPack, found, err := s.resolveEventFieldPack(ctx, pack); err != nil {
			return 0, nil, true, err
		} else if found {
			code, body, handled, e := s.enterEventFieldPack(ctx, eventPack)
			if e == nil && s.talentPackInfo != nil {
				extra, x := s.talentPackInfo(ctx, pack)
				if x != nil {
					return 0, nil, true, x
				}
				body = append(body, extra...)
			}
			return code, body, handled, e
		}
		if !s.packUnlocked(ctx, pack) {
			return 0, nil, true, fmt.Errorf("%w: unsupported pack %d", ErrInvalidRequest, pack)
		}
		if active := s.firstUnclearedQuestFor(pack); active != 0 {
			if _, err := s.ensureQuestItems(ctx, pack, active); err != nil {
				return 0, nil, true, err
			}
		}
		response, err := s.packInfoFor(ctx, pack)
		if err != nil {
			return 0, nil, true, err
		}
		if s.talentPackInfo != nil {
			extra, e := s.talentPackInfo(ctx, pack)
			if e != nil {
				return 0, nil, true, e
			}
			response = append(response, extra...)
		}
		if err := s.state.SetActivePackID(ctx, pack); err != nil {
			return 0, nil, true, err
		}
		s.setCurrentPack(pack)
		return 5, response, true, nil
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
		if err := s.state.ClearQuest(ctx, quest, pack, s.questDifficultyFor(pack, quest)); err != nil {
			return 0, nil, true, fmt.Errorf("world: clear quest: %w", err)
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
		return 18, s.clearResponse(ctx, pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)], items, questEquipment, nextItems, nextChars), true, nil
	default:
		return 0, nil, false, nil
	}
}
