package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sort"
)

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
	research, err := s.state.ResearchObjects(ctx, packID)
	if err != nil {
		return nil, err
	}
	for _, id := range research {
		out = wire.AppendVarint(out, 11, uint64(id))
	}
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
	out, err := s.mapExplorationWire(ctx, packID)
	if err != nil {
		return nil, err
	}
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
	statues, err := s.statueInfo(ctx, packID)
	if err != nil {
		return nil, err
	}
	out = append(out, statues...)
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

func (s *Service) packDBInfoRows(ctx command.Context) ([][]byte, error) {
	if s.storyCatalog == nil {
		return nil, nil
	}
	return s.storyPackDBInfoRows(ctx)
}

func (s *Service) accountPackInfo(ctx command.Context) ([]byte, error) {
	rows, err := s.packDBInfoRows(ctx)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, info := range rows {
		out = wire.AppendBytes(out, 1, info)
	}
	for _, info := range rows {
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
	replays, err := s.state.QuestReplays(ctx)
	if err != nil {
		return nil, err
	}
	packs := make([]int, 0, len(replays))
	for pack := range replays {
		packs = append(packs, pack)
	}
	sort.Ints(packs)
	for _, pack := range packs {
		out = wire.AppendBytes(out, 7, replayWire(pack, replays[pack]))
	}
	return out, nil
}

func (s *Service) storyPackDBInfoRows(ctx command.Context) ([][]byte, error) {
	replays, err := s.state.QuestReplays(ctx)
	if err != nil {
		return nil, err
	}
	played := make(map[int]bool)
	for _, quest := range s.state.AllClearedQuests() {
		played[quest.PackID] = true
	}
	ids := make([]int, 0, len(s.storyCatalog.Packs))
	for id := range s.storyCatalog.Packs {
		if s.storyCatalog.Packs[id].Type == 5 {
			if s.collection == nil {
				continue
			}
			if _, owned := s.collection.Grant(packPurchaseIdentity(id)); !owned {
				continue
			}
		}
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
			if pack := s.storyCatalog.Packs[id]; pack.Type == 0 && pack.BuyPrice == 0 {
				_, entered := s.collection.Grant(packEntryIdentity(id))
				purchased = purchased || entered || played[id]
			}
		}
		if purchased {
			row = wire.AppendVarint(row, 8, 1)
		}
		if _, replaying := replays[id]; replaying {
			row = wire.AppendVarint(row, 9, 1)
		}
		rows = append(rows, row)
	}
	listed := make(map[int]bool, len(ids))
	for _, id := range ids {
		listed[id] = true
	}
	var fieldIDs []int
	for id, pack := range s.fieldPacks {
		// Skyway ownership rides on its story-catalog row above.
		if pack.Type == 5 || s.collection == nil {
			continue
		}
		if _, purchased := s.collection.Grant(packPurchaseIdentity(id)); purchased {
			fieldIDs = append(fieldIDs, id)
			listed[id] = true
		}
	}
	sort.Ints(fieldIDs)
	for _, id := range fieldIDs {
		rows = append(rows, s.fieldPackDBInfo(s.fieldPacks[id]))
	}
	if saved, found := s.state.Position(); found && !listed[saved.PackID] {
		if pack, arena := s.fieldPacks[saved.PackID]; arena && pack.Type != 5 && pack.Type != 9 && pack.MapIDs[saved.Position.MapID] {
			rows = append(rows, wire.AppendVarint(wire.AppendVarint(nil, 1, uint64(saved.PackID)), 8, 1))
		}
	}
	return rows, nil
}

func (s *Service) eventPackDBInfo(pack gamedata.EventFieldPack) []byte {
	row := wire.AppendVarint(nil, 1, uint64(pack.ID))
	if s.packPurchased(pack.ID) {
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
		if s.packPurchased(pack.ID) {
			rows = append(rows, s.eventPackDBInfo(pack))
		}
	}
	return rows, nil
}
