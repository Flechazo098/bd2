package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/progression/conditions"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"slices"
)

func (s *Service) AttachActiveBattlePack(pack func(command.Context) int) {
	s.activeBattlePack = pack
}

func (s *Service) handlePackEntry(ctx command.Context, request []byte) (int, []byte, bool, error) {
	id, err := requestPack(request)
	if err != nil {
		return 5, nil, true, err
	}
	pack, err := s.resolvePack(ctx, id)
	if err != nil {
		return 5, nil, true, err
	}
	if !s.packAccessible(ctx, pack) {
		return 5, nil, true, fmt.Errorf("%w: unavailable entry pack %d", ErrInvalidRequest, id)
	}
	activeBattle := s.battleActive != nil && s.battleActive(ctx)
	if activeBattle {
		current, err := s.CurrentPackID(ctx)
		if err != nil {
			return 5, nil, true, err
		}
		encounterPack := 0
		if s.activeBattlePack != nil {
			encounterPack = s.activeBattlePack(ctx)
		}
		allowedPack := current
		if encounterPack > 0 {
			allowedPack = encounterPack
		}
		if id != allowedPack {
			return 5, nil, true, fmt.Errorf("%w: cannot change pack during active battle: requested=%d current=%d battle_pack=%d", ErrInvalidRequest, id, current, encounterPack)
		}
	}
	if err := s.ResumeFieldTime(ctx); err != nil {
		return 5, nil, true, err
	}
	var body []byte
	if pack.kind == eventCard {
		body, err = s.enterEventFieldPack(ctx, pack.event)
	} else {
		body, err = s.enterPersistentPack(ctx, pack, activeBattle)
	}
	if err != nil {
		return 5, nil, true, err
	}
	if s.talentPackInfo != nil {
		extra, err := s.talentPackInfo(ctx, id)
		if err != nil {
			return 5, nil, true, err
		}
		body = append(body, extra...)
	}
	if activeBattle {
		body = wire.AppendVarint(body, 17, 1)
	}
	// Hidden events keep the outside login destination and saved position.
	if pack.kind != eventCard {
		if err := s.state.SetActivePackID(ctx, id); err != nil {
			return 5, nil, true, err
		}
		if pack.kind == storyCard && pack.story.Type == 0 && pack.story.BuyPrice == 0 && s.collection != nil {
			if err := s.collection.RecordGrantMarker(ctx, packEntryIdentity(id)); err != nil {
				return 5, nil, true, err
			}
		}
	}
	s.setCurrentPack(id)
	if err := s.recordMission(ctx, conditions.Event{Type: conditions.EnterPackInField, Sub: uint64(id), Count: 1}); err != nil {
		return 5, nil, true, err
	}
	return 5, body, true, nil
}

func (s *Service) enterPersistentPack(ctx command.Context, pack packDefinition, activeBattle bool) ([]byte, error) {
	if pack.kind == fieldCard && pack.field.Type == 5 {
		if s.skyway == nil || !s.packPurchased(pack.id) {
			return nil, fmt.Errorf("world: SkyWay pack is not purchased")
		}
		v, err := s.skyway.load(ctx)
		if err != nil {
			return nil, err
		}
		if !activeBattle {
			v.Run = skyWayRun{Generation: v.Run.Generation}
			if err := s.skyway.save(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	if active := s.firstUnclearedQuestFor(pack.id); active != 0 {
		if _, err := s.ensureQuestItems(ctx, pack.id, active); err != nil {
			return nil, err
		}
	}
	if err := s.ensureLifeQuest(ctx, pack.id); err != nil {
		return nil, err
	}
	return s.packInfoFor(ctx, pack.id)
}

func (s *Service) enterEventFieldPack(ctx command.Context, pack gamedata.EventFieldPack) ([]byte, error) {
	if !s.packPurchased(pack.ID) {
		return nil, fmt.Errorf("%w: event pack %d is not purchased", ErrInvalidRequest, pack.ID)
	}
	position := pack.InitialPosition
	if position == "" {
		return nil, fmt.Errorf("world: missing event pack initial position")
	}
	if saved, found := s.state.Position(); found && saved.PackID == pack.ID {
		if !slices.Contains(pack.MapIDs, saved.Position.MapID) {
			return nil, fmt.Errorf("%w: map outside event pack", ErrInvalidRequest)
		}
		position = saved.RawJSON
	}
	response := wire.AppendString(nil, 4, position)
	exploration, err := s.mapExplorationWire(ctx, pack.ID)
	if err != nil {
		return nil, err
	}
	response = append(response, exploration...)
	buffs, err := s.fieldBuffInfo(ctx)
	if err != nil {
		return nil, err
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
			return nil, err
		}
	}
	response = wire.AppendBytes(response, 12, hunting)
	// Hidden-pack entry must retain the outside field position: the client
	// intentionally suppresses SaveUserPosition while playing these packs. The
	// persistent active pack also stays outside, so relogin cannot be stranded
	// in a hidden scene after its calendar closes.
	return response, nil
}
