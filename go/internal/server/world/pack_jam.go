package world

import (
	"fmt"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

func packJamIdentity(packID int) string { return fmt.Sprintf("pack-jam:%d", packID) }

func (s *Service) handlePackDocking(path string, request []byte) (int, []byte, bool, error) {
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if _, known := s.questsFor(packID); !known || !s.packUnlocked(packID) {
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
	s.packJamMu.Lock()
	defer s.packJamMu.Unlock()
	if s.wallet.WasGranted(identity) {
		return 72, nil, true, nil
	}
	reward := s.packJamDesign.Reward
	if reward.Type != 3 || reward.ID != 0 || reward.Count == 0 || reward.Count > uint64(^uint32(0)>>1) {
		return 0, nil, true, fmt.Errorf("world: unsupported pack jam reward")
	}
	if _, err := s.wallet.GrantQuestOnce(identity, []gamedata.Reward{reward}); err != nil {
		return 0, nil, true, fmt.Errorf("world: grant pack jam reward: %w", err)
	}
	item := wire.AppendVarint(nil, 3, reward.Type)
	item = wire.AppendVarint(item, 4, reward.Count)
	return 72, wire.AppendBytes(nil, 1, item), true, nil
}
