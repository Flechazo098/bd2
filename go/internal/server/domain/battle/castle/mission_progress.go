package castle

import (
	"encoding/json"
	"fmt"

	"bd2server/internal/server/domain/command"
)

// MissionProgress is the saved castle progress mission conditions derive
// from. Pride floors include every archived season.
type MissionProgress struct {
	// Towers maps a battle mode to its cleared tower floors.
	Towers                    map[uint64]map[uint64]bool
	PrideFought, PrideCleared map[uint64]bool
	TotalWarBest              uint64
}

// MissionProgress reads the castle save without rolling its season over.
func (s *Service) MissionProgress(ctx command.Context) (MissionProgress, error) {
	out := MissionProgress{Towers: map[uint64]map[uint64]bool{}, PrideFought: map[uint64]bool{}, PrideCleared: map[uint64]bool{}}
	v := state{}
	raw, ok, err := ctx.State.LoadEntry("missions", "castle_state", "progress")
	if err != nil {
		return out, err
	}
	if ok && json.Unmarshal(raw, &v) != nil {
		return out, fmt.Errorf("castle: invalid saved progress")
	}
	for _, t := range v.Towers {
		if out.Towers[t.Mode] == nil {
			out.Towers[t.Mode] = map[uint64]bool{}
		}
		out.Towers[t.Mode][t.ID] = true
	}
	seasons := []map[uint64]floor{v.Floors}
	archived, err := ctx.State.ListEntries("missions", "castle_seasons")
	if err != nil {
		return out, err
	}
	for key, raw := range archived {
		floors := map[uint64]floor{}
		if err := json.Unmarshal(raw, &floors); err != nil {
			return out, fmt.Errorf("castle: invalid archived season %s", key)
		}
		seasons = append(seasons, floors)
	}
	for _, floors := range seasons {
		for id, f := range floors {
			out.PrideFought[id] = true
			if f.Highest > 0 {
				out.PrideCleared[id] = true
			}
		}
	}
	out.TotalWarBest = sum(v.TotalWarScores)
	return out, nil
}
