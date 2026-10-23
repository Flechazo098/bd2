package world

import (
	"fmt"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

type packKind uint8

const (
	storyCard packKind = iota
	fieldCard
	eventCard
)

// A card is resolved once per operation. Calendar-authorized hidden cards
// take precedence over static field and story catalogs.
type packDefinition struct {
	id    int
	kind  packKind
	story gamedata.StoryPack
	field gamedata.FieldPack
	event gamedata.EventFieldPack
}

func (s *Service) resolvePack(ctx command.Context, id int) (packDefinition, error) {
	if event, found, err := s.resolveEventFieldPack(ctx, id); err != nil {
		return packDefinition{}, fmt.Errorf("world: resolve pack %d calendar: %w", id, err)
	} else if found {
		return packDefinition{id: id, kind: eventCard, event: event}, nil
	}
	if field, found := s.fieldPacks[id]; found {
		return packDefinition{id: id, kind: fieldCard, field: field}, nil
	}
	if s.storyCatalog != nil {
		if story, found := s.storyCatalog.Packs[id]; found {
			return packDefinition{id: id, kind: storyCard, story: story}, nil
		}
	}
	return packDefinition{}, fmt.Errorf("%w: unknown pack %d", ErrInvalidRequest, id)
}

func (s *Service) packPurchased(id int) bool {
	if s.collection == nil {
		return false
	}
	_, found := s.collection.Grant(packPurchaseIdentity(id))
	return found
}

func (s *Service) CanEnterFieldPackType(ctx command.Context, packType uint64) bool {
	for id, pack := range s.fieldPacks {
		if pack.Type == packType && s.packPurchased(id) && s.packAccessible(ctx, packDefinition{id: id, kind: fieldCard, field: pack}) {
			return true
		}
	}
	return false
}

func (s *Service) packRequirementsMet(ctx command.Context, pack packDefinition) bool {
	switch pack.kind {
	case eventCard:
		return true // The resolver already checked the calendar.
	case fieldCard:
		return s.fieldPackRequirementsMet(ctx, pack.field)
	default:
		return s.storyPackUnlocked(ctx, pack.id)
	}
}

func (s *Service) packAccessible(ctx command.Context, pack packDefinition) bool {
	if !s.packRequirementsMet(ctx, pack) {
		return false
	}
	switch pack.kind {
	case eventCard:
		return s.packPurchased(pack.id)
	case fieldCard:
		return pack.field.BuyPrice == 0 || s.packPurchased(pack.id)
	default:
		return true
	}
}

func (p packDefinition) purchaseTerms() (uint64, uint64, []gamedata.Reward) {
	switch p.kind {
	case eventCard:
		return p.event.BuyType, p.event.BuyPrice, p.event.BuyRewards
	case fieldCard:
		return p.field.BuyType, p.field.BuyPrice, p.field.BuyRewards
	default:
		return p.story.BuyType, p.story.BuyPrice, p.story.BuyRewards
	}
}
