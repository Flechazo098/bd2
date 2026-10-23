package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

func (s *Service) handlePackDocking(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	pack, err := s.resolvePack(ctx, packID)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packAccessible(ctx, pack) {
		return 0, nil, true, fmt.Errorf("%w: unavailable docking pack %d", ErrInvalidRequest, packID)
	}
	identity := packJamIdentity(packID)
	if path == "/PackPreviewInfo" {
		var response []byte
		titles, err := s.questTitleInfo(packID)
		if err != nil {
			return 0, nil, true, err
		}
		for _, title := range titles {
			response = wire.AppendBytes(response, 2, title)
		}
		if active := s.firstUnclearedQuestFor(packID); active != 0 {
			quest := wire.AppendVarint(nil, 1, uint64(active))
			quest = wire.AppendVarint(quest, 6, uint64(packID))
			response = wire.AppendBytes(response, 1, quest)
		}
		if s.wallet != nil && s.wallet.WasGranted(identity) {
			response = wire.AppendVarint(response, 3, 1)
		}
		return 104, response, true, nil
	}
	if s.wallet == nil || s.packJamDesign == nil {
		return 0, nil, true, fmt.Errorf("world: pack jam design or wallet unavailable")
	}
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
