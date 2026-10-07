package eventplay

import (
	"fmt"
	"slices"
)

func (s *Service) validateStage(f string, id, mode uint64) error {
	switch f {
	case "Rhythm":
		r, e := s.design.Row("RhythmGameMusicTable", 5, id)
		if e != nil {
			return e
		}
		if mode > 2 {
			return fmt.Errorf("eventplay: unknown rhythm mode")
		}
		_ = r
	case "Hopscotch":
		_, e := s.design.Row("HopscotchStageTable", 6, id)
		return e
	case "Sichuan":
		_, e := s.design.Row("SichuanStageTable", 2, id)
		return e
	case "Survival":
		if len(s.design.Tables["FieldMiniGameSurvivalTable"]) == 0 {
			return fmt.Errorf("eventplay: survival design missing")
		}
	case "Action":
		if len(s.design.Tables["ActionGameDefaultTable"]) == 0 {
			return fmt.Errorf("eventplay: action design missing")
		}
	}
	return nil
}

func has(a []uint64, id uint64) bool {
	return slices.Contains(a, id)
}
func seqSeed(n uint64) uint64 { return n & 2147483647 }
