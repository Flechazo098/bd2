package gacha

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
)

func (s *Service) AttachStatistics(design *gamedata.CollectionStatisticsDesign) error {
	if design == nil || len(design.CostumeGrade) == 0 || len(design.Equipment) == 0 {
		return fmt.Errorf("gacha: incomplete statistics design")
	}
	s.statistics = design
	return nil
}

func (s *Service) recordHistory(ctx command.Context, identity string, group, id, buyType, count, gachaType, pickup uint64, response []byte, grant roster.CollectionGrant, tickets []byte) error {
	if group == 0 && s.statistics != nil {
		group = s.statistics.GachaGroups[id]
	}
	if group == 0 {
		return fmt.Errorf("gacha: draw %d has no history group", id)
	}
	var totals [4][13]uint64
	counts := map[uint64]uint64{}
	bundle, _, e := wire.Bytes(response, 1)
	if e != nil {
		return e
	}
	bundle, e = historyRewardBundle(bundle, grant)
	if e != nil {
		return e
	}
	e = wire.Walk(bundle, func(f wire.Field) error {
		var itemType, itemID, n uint64
		switch f.Number {
		case 3:
			itemType = 11
			itemID, _, e = wire.Varint(f.Value, 2)
			n = 1
		case 4:
			itemType = 10
			var base []byte
			base, _, e = wire.Bytes(f.Value, 5)
			if e == nil {
				itemID, _, e = wire.Varint(base, 1)
			}
			n = 1
		case 9:
			itemType, _, e = wire.Varint(f.Value, 2)
			if e == nil {
				itemID, _, e = wire.Varint(f.Value, 3)
			}
			n = 1
		case 8:
			itemType, _, e = wire.Varint(f.Value, 1)
			if e == nil {
				itemID, _, e = wire.Varint(f.Value, 2)
			}
			if e == nil {
				n, _, e = wire.Varint(f.Value, 3)
			}
		default:
			return nil
		}
		if e != nil {
			return e
		}
		if itemType == 11 {
			counts[itemID] += n
			return nil
		}
		if itemType != 10 {
			return nil
		}
		if s.statistics == nil {
			return fmt.Errorf("gacha: equipment history needs statistics design")
		}
		v, ok := s.statistics.Equipment[itemID]
		if !ok {
			return fmt.Errorf("gacha: unknown history equipment %d", itemID)
		}
		var grade uint64
		for _, c := range s.statistics.Characters {
			if c.UniqueID == v.CharacterUniqueID {
				grade = c.Grade
				break
			}
		}
		kind := 3
		if pickup != 0 {
			kind = 2
		}
		field := -1
		switch {
		case grade == 5 && v.Grade == 4:
			field = 5
			if itemID == pickup {
				field = 4
			}
		case grade == 5 && v.Grade == 3:
			field = 6
		case grade == 4 && v.Grade == 4:
			field = 7
		case grade == 4 && v.Grade == 3:
			field = 8
		case grade == 4 && v.Grade == 2:
			field = 9
		case grade == 3 && v.Grade == 4:
			field = 10
		case grade == 3 && v.Grade == 3:
			field = 11
		case grade == 3 && v.Grade == 2:
			field = 12
		}
		if field >= 0 {
			totals[kind][field] += n
		}
		return nil
	})
	if e != nil {
		return e
	}
	for costume, n := range counts {
		var grade uint64
		if s.statistics != nil {
			grade = s.statistics.CostumeGrade[costume]
		} else {
			for _, g := range s.regular.Gachas {
				if g.Grades[costume] != 0 {
					grade = g.Grades[costume]
					break
				}
			}
		}
		kind := 1
		if pickup != 0 {
			kind = 0
		}
		switch grade {
		case 5:
			field := 1
			if costume == pickup {
				field = 0
			}
			totals[kind][field] += n
		case 4:
			totals[kind][2] += n
		case 3:
			totals[kind][3] += n
		}
	}
	body := wire.AppendVarint(nil, 1, group)
	body = wire.AppendVarint(body, 2, id)
	body = wire.AppendVarint(body, 3, buyType)
	body = wire.AppendVarint(body, 4, count)
	body = wire.AppendVarint(body, 5, grant.GachaPoint)
	body = wire.AppendVarint(body, 6, gachaType)
	body = wire.AppendVarint(body, 7, pickup)
	for _, fixed := range grant.GachaFixed {
		body = wire.AppendBytes(body, 8, gachaFixedWire(fixed))
	}
	body = wire.AppendBytes(body, 9, bundle)
	for _, index := range grant.CostumeIndices {
		if c, ok := s.collection.CostumeByIndex(index); ok {
			body = wire.AppendVarint(body, 10, c.SortID)
		}
	}
	for _, sortID := range grant.SelectionApplySortIDs {
		body = wire.AppendVarint(body, 11, sortID)
	}
	body = append(body, tickets...)
	now := s.now().UnixMilli()
	if now <= 0 {
		return fmt.Errorf("gacha: invalid history time")
	}
	return s.collection.RecordGachaHistory(ctx, identity, uint64(now), body, totals)
}

func historyRewardBundle(raw []byte, grant roster.CollectionGrant) ([]byte, error) {
	targets := map[[3]uint64]bool{}
	for _, exchange := range grant.Exchanges {
		targets[[3]uint64{exchange.ExchangeItemType, exchange.ExchangeItemID, exchange.SortID}] = true
	}
	var body []byte
	err := wire.Walk(raw, func(f wire.Field) error {
		// History renders original draw results directly, including auto-exchange
		// origins. Runtime ownership and currency updates would duplicate them.
		if f.Number == 2 {
			return nil
		}
		if f.Number == 1 {
			kind, _, e := wire.Varint(f.Value, 3)
			if e != nil {
				return e
			}
			id, _, e := wire.Varint(f.Value, 2)
			if e != nil {
				return e
			}
			sort, _, e := wire.Varint(f.Value, 9)
			if e != nil {
				return e
			}
			if targets[[3]uint64{kind, id, sort}] {
				return nil
			}
		}
		if f.Type != 2 {
			return fmt.Errorf("gacha: invalid history reward field")
		}
		body = wire.AppendBytes(body, f.Number, f.Value)
		return nil
	})
	return body, err
}

func historyConsumedTickets(tickets []assets.Item) []byte {
	var body []byte
	for _, item := range tickets {
		v := wire.AppendVarint(nil, 1, item.InvenIndex)
		v = wire.AppendVarint(v, 2, item.ID)
		v = wire.AppendVarint(v, 3, item.Type)
		v = wire.AppendVarint(v, 4, item.Count)
		body = wire.AppendBytes(body, 12, v)
	}
	return body
}

func (s *Service) gachaHistory(ctx command.Context, raw []byte) (int, []byte, bool, error) {
	var after uint64
	seen := false
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 0 || seen {
			return fmt.Errorf("gacha: invalid history cursor")
		}
		seen = true
		var e error
		after, _, e = wire.Varint(raw, 2)
		if e != nil || after > math.MaxInt64 {
			return fmt.Errorf("gacha: invalid history cursor")
		}
		return nil
	})
	if e != nil {
		return 201, nil, true, e
	}
	body, e := s.collection.GachaHistory(ctx, after)
	return 201, body, true, e
}
