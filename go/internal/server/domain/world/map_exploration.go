package world

import (
	"fmt"
	"slices"
	"sort"
	"strconv"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) handleActiveMap(ctx command.Context, request []byte) (int, []byte, bool, error) {
	fail := func(reason string) (int, []byte, bool, error) {
		return 0, nil, true, fmt.Errorf("%w: map exploration %s", ErrInvalidRequest, reason)
	}
	pack, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(ctx, pack) {
		return fail("pack unavailable")
	}
	mapID, found, err := wire.Varint(request, 3)
	if err != nil || !found || mapID == 0 || mapID > 1<<31-1 {
		return fail("invalid map")
	}
	validMap := false
	if event, found, err := s.resolveEventFieldPack(ctx, pack); err != nil {
		return 0, nil, true, err
	} else if found {
		validMap = slices.Contains(event.MapIDs, int(mapID))
	} else if field, found := s.fieldPacks[pack]; found {
		validMap = field.MapIDs[int(mapID)]
	} else {
		design, err := s.packDetailDesign(pack)
		if err != nil {
			return 0, nil, true, err
		}
		validMap = slices.Contains(design.MapIDs, int(mapID))
	}
	if !validMap {
		return fail("map outside pack")
	}
	var rows []byte
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 4 {
			return nil
		}
		if field.Type != 2 || len(rows) >= progress.MapExplorationRows || len(field.Value) > 2 {
			return fmt.Errorf("%w: map exploration row has wire type %d, %d bytes, %d rows already", ErrInvalidRequest, field.Type, len(field.Value), len(rows))
		}
		value := uint64(0)
		if len(field.Value) != 0 {
			var err error
			value, err = strconv.ParseUint(string(field.Value), 16, 8)
			if err != nil {
				return fmt.Errorf("%w: map exploration value %q is not hex: %v", ErrInvalidRequest, field.Value, err)
			}
		}
		rows = append(rows, byte(value))
		return nil
	}); err != nil || len(rows) != progress.MapExplorationRows {
		return fail("invalid row masks")
	}
	err = s.state.RevealMap(ctx, pack, int(mapID), rows)
	return 0, nil, true, err
}

func (s *Service) mapExplorationWire(ctx command.Context, pack int) ([]byte, error) {
	maps, err := s.state.MapExploration(ctx, pack)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(maps))
	for id := range maps {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var out []byte
	for _, id := range ids {
		row := wire.AppendVarint(nil, 1, uint64(id))
		for _, mask := range maps[id] {
			text := ""
			if mask != 0 {
				text = strconv.FormatUint(uint64(mask), 16)
			}
			row = wire.AppendString(row, 2, text)
		}
		out = wire.AppendBytes(out, 10, row)
	}
	return out, nil
}
