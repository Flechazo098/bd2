package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/json"
	"fmt"
	"slices"
)

func (s *Service) CompleteNPCReputation(ctx command.Context, identity string, pack int, group uint64) ([]byte, error) {
	if s.npcReputation == nil || identity == "" || group == 0 || !s.packUnlocked(ctx, pack) {
		return nil, ErrInvalidRequest
	}
	d, err := s.npcReputation.load(pack)
	if err != nil {
		return nil, err
	}
	rule, ok := d.Groups[group]
	if !ok {
		return nil, fmt.Errorf("world: unknown reputation group")
	}
	v, err := s.npcReputation.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	claim := fmt.Sprintf("%s:%d:%d", identity, pack, group)
	if !v.Claims[claim] {
		v.Claims[claim] = true
		v.Starts[reputationKey(pack, group)] = s.npcReputation.now().Unix()
		b, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		if e = s.npcReputation.store.Save(ctx.State, "npcreputation", b); e != nil {
			return nil, e
		}
	}
	state, elapsed, err := s.reputationState(ctx, pack, rule)
	if err != nil {
		return nil, err
	}
	return reputationWire(group, state, elapsed), nil
}

func reputationWire(group, state, elapsed uint64) []byte {
	b := wire.AppendVarint(nil, 1, group)
	b = wire.AppendVarint(b, 2, state)
	if elapsed > 0 {
		b = wire.AppendVarint(b, 3, elapsed)
	}
	return b
}

func (s *Service) npcReputationRows(ctx command.Context, pack int) ([][]byte, error) {
	if s.npcReputation == nil {
		return nil, nil
	}
	d, err := s.npcReputation.load(pack)
	if err != nil {
		return nil, err
	}
	var groups []uint64
	for id := range d.Groups {
		groups = append(groups, id)
	}
	slices.Sort(groups)
	var rows [][]byte
	for _, id := range groups {
		state, elapsed, e := s.reputationState(ctx, pack, d.Groups[id])
		if e != nil {
			return nil, e
		}
		rows = append(rows, reputationWire(id, state, elapsed))
	}
	return rows, nil
}

// handleQuestUpdate accepts the existing task update packet used when an NPC
// conversation finishes. Ordinary Talk is local and sends AchievementUpdate,
// not a separate NPC dialog packet. QuestUpdate has no NPC identity to validate.
func (s *Service) handleQuestUpdate(ctx command.Context, request []byte) (int, []byte, bool, error) {
	quest, pack, err := requestQuest(request)
	if err != nil {
		return 0, nil, true, err
	}
	current, err := s.CurrentPackID(ctx)
	if err != nil || current != pack || !s.canClear(ctx, pack, quest) {
		return 0, nil, true, ErrInvalidRequest
	}
	if s.state.QuestCleared(quest, pack, s.questDifficultyFor(pack, quest)) {
		// A delayed replay must not reinsert a cleared quest into active progress.
		return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(quest)), 2, nil), true, nil
	}
	if _, err := s.state.UpdateQuest(ctx, request); err != nil {
		return 0, nil, true, err
	}
	// field 2 is RewardDBInfoBundle, not QuestDBInfo. Task completion/claims
	// remain in QuestClear; an update alone must not invent or duplicate rewards.
	return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(quest)), 2, nil), true, nil
}
