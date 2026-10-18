package progress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"bd2server/internal/server/domain/command"
)

const MapExplorationRows = 8

func (s *Store) MapExploration(ctx command.Context, pack int) (map[int][]byte, error) {
	if ctx.State == nil || pack <= 0 {
		return nil, fmt.Errorf("progress: map exploration requires account storage and pack")
	}
	raw, found, err := ctx.State.LoadEntry("progress", "map_exploration", strconv.Itoa(pack))
	if err != nil {
		return nil, err
	}
	maps := map[int][]byte{}
	if !found {
		return maps, nil
	}
	if err := json.Unmarshal(raw, &maps); err != nil || maps == nil {
		return nil, fmt.Errorf("progress: invalid saved map exploration")
	}
	for id, rows := range maps {
		if id <= 0 || len(rows) != MapExplorationRows {
			return nil, fmt.Errorf("progress: invalid saved map exploration rows")
		}
	}
	return maps, nil
}

func (s *Store) RevealMap(ctx command.Context, pack, id int, rows []byte) error {
	if id <= 0 || len(rows) != MapExplorationRows {
		return fmt.Errorf("progress: invalid map exploration rows")
	}
	maps, err := s.MapExploration(ctx, pack)
	if err != nil {
		return err
	}
	merged := make([]byte, MapExplorationRows)
	copy(merged, maps[id])
	for i, row := range rows {
		merged[i] |= row
	}
	if bytes.Equal(merged, maps[id]) || bytes.Equal(merged, make([]byte, MapExplorationRows)) {
		return nil
	}
	maps[id] = merged
	raw, err := json.Marshal(maps)
	if err != nil {
		return err
	}
	return ctx.State.PutEntry("progress", "map_exploration", strconv.Itoa(pack), raw)
}
