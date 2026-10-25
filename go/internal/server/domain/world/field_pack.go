package world

import (
	"errors"
	"slices"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
)

type FieldPackEconomy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}

func (s *Service) AttachFieldPackEconomy(economy FieldPackEconomy) {
	s.fieldPackEconomy = economy
}

func (s *Service) fieldPackDBInfo(pack gamedata.FieldPack) []byte {
	row := wire.AppendVarint(nil, 1, pack.ID)
	if s.collection != nil {
		if _, purchased := s.collection.Grant(packPurchaseIdentity(int(pack.ID))); purchased {
			row = wire.AppendVarint(row, 8, 1)
		}
	}
	cleared := 0
	for _, id := range pack.MainQuestIDs {
		if s.state.QuestCleared(id, int(pack.ID), s.questDifficulty(int(pack.ID))) {
			cleared++
		}
	}
	if cleared > 0 {
		row = wire.AppendVarint(row, 2, uint64(cleared))
	}
	if len(pack.MainQuestIDs) > 0 && cleared == len(pack.MainQuestIDs) {
		row = wire.AppendVarint(row, 3, 1)
	}
	return row
}

// LastPlayedPackID returns the pack LoginUser should restore. ActivePackID
// records the last selected content, but only a saved position proves that the
// scene can be restored; an arena entered without save can therefore fall back
// to the field that owns the position. Login falls back to the entry chapter
// instead of failing when no selected pack can restore the saved scene.
func (s *Service) LastPlayedPackID(ctx command.Context) (uint64, error) {
	id := uint64(s.state.ActivePackID())
	if id == 0 {
		if saved, found := s.state.Position(); found && saved.PackID > 0 {
			id = uint64(saved.PackID)
		}
	}
	if id == 0 {
		id = uint64(s.startingPack())
	}
	if s.packResumable(ctx, int(id)) {
		return id, nil
	}
	if saved, found := s.state.Position(); found && saved.PackID > 0 && uint64(saved.PackID) != id && s.packResumable(ctx, saved.PackID) {
		return uint64(saved.PackID), nil
	}
	if start := s.startingPack(); start > 0 && uint64(start) != id && s.packResumable(ctx, start) {
		return uint64(start), nil
	}
	return 0, errors.New("world: saved login pack is unavailable")
}

// packResumable reports whether login can restore this pack. A field pack
// other than a hunting ground needs a saved position of its own because the
// scene cannot be reconstructed otherwise.
func (s *Service) packResumable(ctx command.Context, id int) bool {
	if id <= 0 || !s.packUnlocked(ctx, id) {
		return false
	}
	if s.storyCatalog != nil {
		if _, story := s.storyCatalog.Packs[id]; story {
			return true
		}
	}
	if pack, field := s.fieldPacks[id]; field {
		if pack.Type == 5 {
			return true
		}
		saved, found := s.state.Position()
		return found && fieldPackAcceptsMap(pack, saved)
	}
	return true
}

// fieldPackAcceptsMap reports whether a saved position belongs to the pack.
func fieldPackAcceptsMap(pack gamedata.FieldPack, saved progress.SavedPosition) bool {
	return saved.PackID == int(pack.ID) && pack.MapIDs[saved.Position.MapID]
}

// ClaimOpenFieldPacks completes the free purchase of field content packs once
// their ContentOpen rule is satisfied. The client cannot buy a pack it does not
// list yet, so without this the pack card stays locked after the main quests
// that grant its ticket. The cafeteria card opens through its introduction.
func (s *Service) ClaimOpenFieldPacks(ctx command.Context) error {
	if s.collection == nil {
		return nil
	}
	ids := make([]int, 0, len(s.fieldPacks))
	for id := range s.fieldPacks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		pack := s.fieldPacks[id]
		if pack.BuyPrice != 0 || pack.Type == 9 {
			continue
		}
		if s.packPurchased(id) {
			continue
		}
		definition, err := s.resolvePack(ctx, id)
		if err != nil {
			return err
		}
		if definition.kind != fieldCard || !s.packRequirementsMet(ctx, definition) {
			continue
		}
		if pack.Type == 5 && s.skyway == nil || pack.Type != 5 && s.fieldPackEconomy == nil {
			continue
		}
		if _, err := s.purchaseResolvedPack(ctx, definition, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) AttachSquadLevel(ctx command.Context, provider func() (uint64, error)) error {
	if provider == nil {
		return errors.New("world: nil squad level provider")
	}
	s.squadLevel = provider
	return nil
}

func (s *Service) fieldPackRequirementsMet(ctx command.Context, pack gamedata.FieldPack) bool {
	// A static scheduled card needs authorization from the active calendar.
	// A saved position must never reopen a card after that calendar expires.
	if pack.UseSchedule != 0 {
		return false
	}
	// A valid committed field position is the existing account's entry marker.
	// Restoration does not repeat a purchase or an unlock check.
	if saved, found := s.state.Position(); found && saved.PackID == int(pack.ID) && pack.MapIDs[saved.Position.MapID] {
		return true
	}
	if pack.SquadLevel != 0 {
		if s.squadLevel == nil {
			return false
		}
		level, err := s.squadLevel()
		if err != nil || level < pack.SquadLevel {
			return false
		}
	}
	if pack.HasOpenRule {
		if s.inventory == nil {
			return false
		}
		found := false
		for _, item := range s.inventory.All(ctx) {
			if item.Type == 19 && item.ID == pack.TicketID && item.Count != 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
