package roster

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"fmt"
)

type potentialConnectReceipt struct {
	Digest string
	Body   []byte
}

func (s *CostumePotentialService) AttachConnectStore(store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("player: missing potential connect store")
	}
	s.connectStore = store
	return nil
}

func (s *CharacterStore) setPotentialConnection(ctx command.Context, index, costume uint64) error {

	for i, c := range s.characters {
		if c.InvenIndex == index {
			next := append([]Character(nil), s.characters...)
			next[i].ConnectPotentialCostume = costume
			if err := s.persist(ctx, next); err != nil {

				return err
			}
			s.characters = next

			return nil
		}
	}
	collection := s.collection

	if collection != nil {
		if c, ok := collection.FindCharacter(index); ok {
			c.ConnectPotentialCostume = costume
			return collection.UpdateCharacter(ctx, c.ID, c)
		}
	}
	return fmt.Errorf("player: unknown potential connection character")
}
