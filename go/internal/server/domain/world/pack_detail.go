package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"slices"
)

func (s *Service) attachPackDetailDesign(source *gamedata.Source) {
	s.packDetailDesign = func(packID int) (gamedata.PackDetailDesign, error) {
		return source.PackDetail(packID)
	}
}

func (s *Service) packRewardCounts(ctx command.Context, packID int) (once, regen, research uint64, err error) {
	pack, err := s.packDetailDesign(packID)
	if err != nil {
		return 0, 0, 0, err
	}
	ids, err := s.openedFieldObjects(ctx, packID)
	if err != nil {
		return 0, 0, 0, err
	}
	mapIDs := pack.MapIDs
	for _, id := range ids {
		object := s.fieldObjects[packID].Objects[id]
		if !slices.Contains(mapIDs, object.MapID) {
			continue
		}
		if matchesFieldCount(object, 2) {
			once++
		}
		if matchesFieldCount(object, 3) {
			regen++
		}
	}
	ids, err = s.state.ResearchObjects(ctx, packID)
	if err != nil || len(ids) == 0 {
		return once, regen, 0, err
	}
	design, err := s.researchDesign(packID)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, id := range ids {
		object, found := design.Objects[id]
		if !found || object.Reward.Type == 0 && object.CollectionID == 0 {
			continue
		}
		seen := map[int]bool{}
		for _, mapID := range object.Maps {
			if !seen[mapID] && slices.Contains(mapIDs, mapID) {
				research++
				seen[mapID] = true
			}
		}
	}
	return once, regen, research, nil
}
