package profile

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

func parseCard(raw []byte) (Card, error) {
	var card Card
	seen := map[int]bool{}
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 6 {
			return nil
		}
		if f.Number == 6 {
			if f.Type != 0 || seen[6] {
				return fmt.Errorf("%w: field %d has wire type %d or repeats", wire.ErrMalformed, f.Number, f.Type)
			}
			seen[6] = true
			card.Rotate, _ = binary.Uvarint(f.Value)
			return nil
		}
		if f.Type != 2 || f.Number != 4 && seen[f.Number] {
			return fmt.Errorf("%w: field %d has wire type %d or repeats", wire.ErrMalformed, f.Number, f.Type)
		}
		seen[f.Number] = true
		item, err := parseCardItem(f.Value)
		if err != nil {
			return err
		}
		switch f.Number {
		case 1:
			card.Background = &item
		case 2:
			card.SubBackground = &item
		case 3:
			card.BackgroundEffect = &item
		case 4:
			card.Stickers = append(card.Stickers, item)
		case 5:
			card.MyInfo = &item
		}
		return nil
	})
	return card, err
}

func parseCardItem(raw []byte) (CardItem, error) {
	var item CardItem
	seen := map[int]bool{}
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 8 {
			return nil
		}
		if seen[f.Number] {
			return fmt.Errorf("%w: card item field %d repeats", wire.ErrMalformed, f.Number)
		}
		seen[f.Number] = true
		switch f.Number {
		case 5, 6:
			if f.Type != 5 {
				return fmt.Errorf("%w: card item field %d has wire type %d, want fixed32", wire.ErrMalformed, f.Number, f.Type)
			}
			n := math.Float32frombits(binary.LittleEndian.Uint32(f.Value))
			if f.Number == 5 {
				item.Rotate = n
			} else {
				item.Scale = n
			}
		case 8:
			if f.Type != 2 {
				return fmt.Errorf("%w: card item color has wire type %d", wire.ErrMalformed, f.Type)
			}
			item.Color = string(f.Value)
		default:
			if f.Type != 0 {
				return fmt.Errorf("%w: card item field %d has wire type %d, want varint", wire.ErrMalformed, f.Number, f.Type)
			}
			n, _ := binary.Uvarint(f.Value)
			switch f.Number {
			case 1:
				if n > math.MaxInt64 {
					return fmt.Errorf("%w: card item inventory index %d", wire.ErrMalformed, n)
				}
				item.InvenIndex = n
			case 2:
				if n > math.MaxInt32 {
					return fmt.Errorf("%w: card item id %d", wire.ErrMalformed, n)
				}
				item.ID = n
			case 3:
				if n != uint64(int64(int32(n))) {
					return fmt.Errorf("%w: card item x %d", wire.ErrMalformed, n)
				}
				item.X = int32(n)
			case 4:
				if n != uint64(int64(int32(n))) {
					return fmt.Errorf("%w: card item y %d", wire.ErrMalformed, n)
				}
				item.Y = int32(n)
			case 7:
				if n > math.MaxInt32 {
					return fmt.Errorf("%w: card item field 7 value %d", wire.ErrMalformed, n)
				}
				item.Layer = n
			}
		}
		return nil
	})
	return item, err
}

func (s *Service) validateCard(ctx command.Context, card *Card, bind bool) error {
	if card.Rotate != 0 && card.Rotate != 90 {
		return fmt.Errorf("profile: invalid card orientation")
	}
	counts := map[uint64]uint64{}
	placed := map[uint64]uint64{}
	owned := map[uint64]uint64{}
	instances := map[uint64]map[uint64]uint64{}
	instancePlaced := map[uint64]uint64{}
	for _, item := range s.source.OwnedItems(ctx) {
		if item.Type == 47 && item.Count > 0 {
			owned[item.ID] += item.Count
			if instances[item.ID] == nil {
				instances[item.ID] = map[uint64]uint64{}
			}
			instances[item.ID][item.InvenIndex] = item.Count
		}
	}
	validate := func(item *CardItem, kind uint64) error {
		if item == nil {
			return nil
		}
		if item.Layer >= s.design.PlacementLimit || math.IsNaN(float64(item.Scale)) || math.IsInf(float64(item.Scale), 0) || item.Scale <= 0 || math.IsNaN(float64(item.Rotate)) || math.IsInf(float64(item.Rotate), 0) {
			return fmt.Errorf("profile: invalid card transform")
		}
		if item.Color != "" {
			color := strings.ToUpper(item.Color)
			if len(color) != 6 && len(color) != 8 {
				return fmt.Errorf("profile: invalid card color")
			}
			if _, e := strconv.ParseUint(color, 16, 32); e != nil || !s.design.Colors[color[:6]] {
				return fmt.Errorf("profile: unavailable card color")
			}
		}
		if kind == 6 {
			if item.ID != 0 || item.InvenIndex != 0 {
				return fmt.Errorf("profile: invalid card identity label")
			}
			counts[6]++
			return nil
		}
		d, ok := s.design.CardItems[item.ID]
		if !ok || d.Kind != kind {
			return fmt.Errorf("profile: card component kind mismatch")
		}
		placed[item.ID]++
		counts[d.Kind]++
		if placed[item.ID] > owned[item.ID] || placed[item.ID] > d.MaxCount {
			return fmt.Errorf("profile: card item quantity not owned")
		}
		if item.InvenIndex != 0 {
			if instancePlaced[item.InvenIndex] >= instances[item.ID][item.InvenIndex] {
				return fmt.Errorf("profile: card inventory instance mismatch")
			}
		} else if bind {
			for index, count := range instances[item.ID] {
				if count > instancePlaced[index] && (item.InvenIndex == 0 || index < item.InvenIndex) {
					item.InvenIndex = index
				}
			}
		}
		if item.InvenIndex != 0 {
			instancePlaced[item.InvenIndex]++
		}
		return nil
	}
	if err := validate(card.Background, 1); err != nil {
		return err
	}
	if err := validate(card.SubBackground, 2); err != nil {
		return err
	}
	if err := validate(card.BackgroundEffect, 3); err != nil {
		return err
	}
	if err := validate(card.MyInfo, 6); err != nil {
		return err
	}
	for i := range card.Stickers {
		d, ok := s.design.CardItems[card.Stickers[i].ID]
		if !ok || d.Kind != 4 && d.Kind != 5 {
			return fmt.Errorf("profile: invalid sticker or illustration")
		}
		if err := validate(&card.Stickers[i], d.Kind); err != nil {
			return err
		}
	}
	var total uint64
	for kind, n := range counts {
		total += n
		if n > s.design.KindLimits[kind] {
			return fmt.Errorf("profile: card kind exceeds placement limit")
		}
	}
	if total > s.design.PlacementLimit {
		return fmt.Errorf("profile: card exceeds total placement limit")
	}
	return nil
}

func (s *Service) buyCardItems(ctx command.Context, identity string, request []byte, settings *Settings) ([]byte, error) {
	var costs, rewards []gamedata.Reward
	seen := map[uint64]bool{}
	bought := map[uint64]uint64{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: card item purchase has wire type %d, want a message", wire.ErrMalformed, f.Type)
		}
		id, e := profileScalar(f.Value, 1, math.MaxInt32)
		if e != nil {
			return e
		}
		quantity, e := profileScalar(f.Value, 2, math.MaxInt32)
		if e != nil {
			return e
		}
		shop, ok := s.design.Shop[id]
		if !ok || seen[id] || quantity == 0 || settings.ShopBought[strconv.FormatUint(id, 10)] > shop.MaxCount || quantity > shop.MaxCount-settings.ShopBought[strconv.FormatUint(id, 10)] {
			return fmt.Errorf("profile: invalid card shop purchase")
		}
		seen[id] = true
		item := s.design.CardItems[id]
		allowed, e := s.source.CardUnlocked(ctx, item)
		if e != nil {
			return e
		}
		if !allowed {
			return fmt.Errorf("profile: card purchase is locked")
		}
		var count uint64
		for _, own := range s.source.OwnedItems(ctx) {
			if own.Type == 47 && own.ID == id {
				count += own.Count
			}
		}
		if count > item.MaxCount || quantity > item.MaxCount-count {
			return fmt.Errorf("profile: card ownership stack is full")
		}
		if len(shop.Costs) != 1 || shop.Costs[0].Count == 0 {
			return fmt.Errorf("profile: unsupported submitted card shop cost shape")
		}
		cost := shop.Costs[0]
		if quantity > math.MaxInt32/cost.Count {
			return fmt.Errorf("profile: card price overflow")
		}
		cost.Count *= quantity
		submitted, found, e := wire.Bytes(f.Value, 3)
		if e != nil || !found {
			return fmt.Errorf("profile: missing card shop payment")
		}
		typ, e := profileScalar(submitted, 3, math.MaxInt32)
		if e != nil {
			return e
		}
		priceID, e := profileScalar(submitted, 2, math.MaxInt32)
		if e != nil {
			return e
		}
		qty, e := profileScalar(submitted, 4, math.MaxInt32)
		if e != nil {
			return e
		}
		if typ != cost.Type || priceID != cost.ID || qty != cost.Count {
			return fmt.Errorf("profile: card shop payment differs from GameData")
		}
		index, e := profileScalar(submitted, 1, math.MaxInt64)
		if e != nil {
			return e
		}
		if priceID == 0 && index != 0 {
			return fmt.Errorf("profile: invalid currency payment index")
		}
		if priceID != 0 {
			matched := false
			for _, own := range s.source.OwnedItems(ctx) {
				matched = matched || own.Type == typ && own.ID == priceID && own.InvenIndex == index && own.Count >= qty
			}
			if !matched {
				return fmt.Errorf("profile: card payment inventory mismatch")
			}
		}
		costs = append(costs, cost)
		rewards = append(rewards, gamedata.Reward{Type: 47, ID: id, Count: quantity})
		bought[id] = quantity
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(rewards) == 0 {
		return nil, fmt.Errorf("profile: empty card purchase")
	}
	bundle, err := s.economy.Apply(ctx, "profile:cardshop:"+identity, costs, rewards)
	if err != nil {
		return nil, err
	}
	for id, n := range bought {
		settings.ShopBought[strconv.FormatUint(id, 10)] += n
	}
	return bundle, nil
}

func sortedMapKeys[V any](m map[string]V) []uint64 {
	ids := make([]uint64, 0, len(m))
	for k := range m {
		n, _ := strconv.ParseUint(k, 10, 64)
		ids = append(ids, n)
	}
	slices.Sort(ids)
	return ids
}
