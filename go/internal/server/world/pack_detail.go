package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"fmt"
)

func (s *Service) handlePackDetail(request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo invalid sequence", ErrInvalidRequest)
	}
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(packID) || !s.packSummaryTargets[packID] {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo unavailable pack %d", ErrInvalidRequest, packID)
	}
	if s.packDetailDesign == nil {
		return 0, nil, true, fmt.Errorf("world: missing pack detail design loader")
	}
	design, err := s.packDetailDesign(packID)
	if err != nil {
		return 0, nil, true, err
	}
	if len(design.RegenMonsterIDs) > 0 {
		// Empty monster state means defeated to the client. Until regeneration
		// has a persisted domain, do not invent active flags or claim history.
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo pack %d requires regenerating monster state", ErrInvalidRequest, packID)
	}
	ids, err := s.openedFieldObjects(packID)
	if err != nil {
		return 0, nil, true, err
	}
	response := []byte{}
	for _, id := range ids {
		response = wire.AppendBytes(response, 2, wire.AppendVarint(nil, 1, uint64(id)))
	}
	return 627, response, true, nil
}

func (s *Service) attachPackDetailDesign(root, version string) {
	s.packDetailDesign = func(packID int) (gamedata.PackDetailDesign, error) {
		return gamedata.LoadPackDetailDesign(root, version, packID)
	}
}
