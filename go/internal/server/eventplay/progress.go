package eventplay

import "bd2server/internal/server/events"

func (s *Service) AttachProgress(fn func(condition, sub, count uint64) error) { s.onProgress = fn }
func (s *Service) AssociatedMissionGroup(c events.Schedule) (uint64, error) {
	if c.Type != 11 {
		return 0, nil
	}
	game, e := s.design.Row("PackEventMiniGameTable", 8, c.ID)
	if e != nil {
		return 0, e
	}
	switch num(game, 3) {
	case 5:
		row, e := s.design.Row("FieldMiniGameSurvivalTable", 9, num(game, 4))
		if e != nil {
			return 0, e
		}
		return num(row, 8), nil
	case 7:
		if len(s.design.Tables["MGDDefaultTable"]) > 0 {
			return num(s.design.Tables["MGDDefaultTable"][0], 5), nil
		}
	case 8:
		if len(s.design.Tables["ActionGameDefaultTable"]) > 0 {
			return num(s.design.Tables["ActionGameDefaultTable"][0], 8), nil
		}
	case 12:
		if len(s.design.Tables["HopscotchDefaultTable"]) > 0 {
			return num(s.design.Tables["HopscotchDefaultTable"][0], 20), nil
		}
	}
	return 0, nil
}
