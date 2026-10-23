package world

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

func (s *Service) handlePackSummary(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackSummaryInfoList missing or invalid sequence", ErrInvalidRequest)
	}
	rows, err := s.packDBInfoRows(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	for _, row := range rows {
		id, found, err := wire.Varint(row, 1)
		if err != nil || !found {
			return 0, nil, true, fmt.Errorf("world: invalid account pack row")
		}
		if !s.packSummaryTargets[int(id)] {
			continue
		}
		once, regen, research, err := s.packRewardCounts(ctx, int(id))
		if err != nil {
			return 0, nil, true, err
		}
		row := wire.AppendVarint(nil, 1, id)
		if once > 0 {
			row = wire.AppendVarint(row, 2, once)
		}
		if regen > 0 {
			row = wire.AppendVarint(row, 3, regen)
		}
		defeated, err := s.packMonsterDefeatedCount(ctx, int(id))
		if err != nil {
			return 0, nil, true, err
		}
		if defeated > 0 {
			row = wire.AppendVarint(row, 4, defeated)
		}
		if research > 0 {
			row = wire.AppendVarint(row, 5, research)
		}
		response = wire.AppendBytes(response, 1, row)
	}
	return 625, response, true, nil
}

func (s *Service) handlePackRewardCounts(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	packs, err := intsRequest(request, 2)
	if err != nil || len(packs) == 0 {
		return 226, nil, true, ErrInvalidRequest
	}
	types, err := intsRequest(request, 3)
	if err != nil {
		return 226, nil, true, err
	}
	if len(types) == 0 {
		types = []uint64{1, 2, 3, 4}
	}
	var out []byte
	seen := map[[2]uint64]bool{}
	for _, p := range packs {
		pack := int(p)
		if pack <= 0 || !s.packUnlocked(ctx, pack) {
			return 226, nil, true, ErrInvalidRequest
		}
		for _, t := range types {
			if t < 1 || t > 4 {
				return 226, nil, true, ErrInvalidRequest
			}
			key := [2]uint64{p, t}
			if seen[key] {
				continue
			}
			seen[key] = true
			var count, max uint64
			if t == 1 {
				d, e := s.researchDesign(pack)
				if e != nil {
					return 226, nil, true, e
				}
				ids, e := s.state.ResearchObjects(ctx, pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if o.CollectionID > 0 || o.Reward.Type > 0 {
						max++
					}
				}
				for _, id := range ids {
					if o, ok := d.Objects[id]; ok && (o.CollectionID > 0 || o.Reward.Type > 0) {
						count++
					}
				}
			} else {
				d, e := s.fieldObjectDesign(ctx, pack)
				if e != nil {
					return 226, nil, true, e
				}
				for _, o := range d.Objects {
					if !matchesFieldCount(o, t) {
						continue
					}
					period, e := s.fieldObjectPeriodFor(pack, o)
					if t != 4 || e == nil {
						max++
					}
					if e != nil {
						continue
					}
					opened, e := s.state.FieldRewardOpened(ctx, pack, o.ID, period)
					if e != nil {
						return 226, nil, true, e
					}
					if opened {
						count++
					}
				}
			}
			info := wire.AppendVarint(nil, 1, t)
			info = wire.AppendVarint(info, 2, p)
			info = wire.AppendVarint(info, 3, count)
			info = wire.AppendVarint(info, 4, max)
			out = wire.AppendBytes(out, 1, info)
		}
	}
	return 226, out, true, nil
}

// EnsureInitialPackPurchase is called only by new-account initialization. It
// awards the bootstrap pack's real purchase rewards, not a recovery inference
// from a saved position or from the former seed-only unlock chain.

func (s *Service) handlePackDetail(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo invalid sequence", ErrInvalidRequest)
	}
	packID, err := requestPack(request)
	if err != nil {
		return 0, nil, true, err
	}
	if !s.packUnlocked(ctx, packID) || !s.packSummaryTargets[packID] {
		return 0, nil, true, fmt.Errorf("%w: PackDetailInfo unavailable pack %d", ErrInvalidRequest, packID)
	}
	ids, err := s.openedFieldObjects(ctx, packID)
	if err != nil {
		return 0, nil, true, err
	}
	response := []byte{}
	rows, err := s.packMonsterRows(ctx, packID)
	if err != nil {
		return 0, nil, true, err
	}
	for _, row := range rows {
		response = wire.AppendBytes(response, 1, row)
	}
	for _, id := range ids {
		response = wire.AppendBytes(response, 2, wire.AppendVarint(nil, 1, uint64(id)))
	}
	research, e := s.state.ResearchObjects(ctx, packID)
	if e != nil {
		return 0, nil, true, e
	}
	for _, id := range research {
		response = wire.AppendVarint(response, 3, uint64(id))
	}
	return 627, response, true, nil
}
