package profile

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
)

type avatarPurchase struct {
	ID     uint64
	Cost   gamedata.Reward
	Motion bool
}

func avatarNumbers(data []byte, field int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(data, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("%w: avatar field %d has wire type %d", wire.ErrMalformed, f.Number, f.Type)
		}
		b := f.Value
		for len(b) > 0 {
			n, size := binary.Uvarint(b)
			if size <= 0 || n > math.MaxInt32 {
				return fmt.Errorf("%w: avatar field %d value is truncated or above int32", wire.ErrMalformed, f.Number)
			}
			out = append(out, n)
			b = b[size:]
			if f.Type == 0 && len(b) > 0 {
				return fmt.Errorf("%w: avatar field %d varint has trailing bytes", wire.ErrMalformed, f.Number)
			}
		}
		return nil
	})
	return out, err
}
func avatarNumber(data []byte, field int) (uint64, error) {
	var value uint64
	seen := false
	err := wire.Walk(data, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if seen || f.Type != 0 {
			return fmt.Errorf("%w: field %d has wire type %d or repeats", wire.ErrMalformed, f.Number, f.Type)
		}
		seen = true
		value, _ = binary.Uvarint(f.Value)
		if value > math.MaxInt32 {
			return fmt.Errorf("%w: avatar field %d value %d is above int32", wire.ErrMalformed, f.Number, value)
		}
		return nil
	})
	return value, err
}
func avatarMessage(data []byte, field int) ([]byte, error) {
	var out []byte
	seen := false
	err := wire.Walk(data, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if seen || f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d or repeats", wire.ErrMalformed, f.Number, f.Type)
		}
		seen = true
		out = slices.Clone(f.Value)
		return nil
	})
	return out, err
}
func (s *AvatarService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code := 0
	switch path {
	case "/AvatarInfo":
		code = 467
	case "/AvatarSave":
		code = 468
	case "/AvatarShopBuy":
		code = 469
	case "/AvatarShopWishListInfo":
		code = 470
	case "/AvatarShopWishListSave":
		code = 471
	case "/AvatarMotionShopBuy":
		code = 472
	default:
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	seq, err := avatarNumber(request, 1)
	if err != nil || seq == 0 {
		return fail(fmt.Errorf("avatar: invalid request sequence"))
	}
	st, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	var out []byte
	switch path {
	case "/AvatarInfo":
		out = wire.AppendBytes(nil, 1, AvatarUseWire(st.Use))
		for _, item := range s.source.AvatarItems(ctx) {
			if item.Type == 49 || item.Type == 50 || item.Type == 61 {
				out = wire.AppendBytes(out, 2, assets.ItemWire(item))
			}
		}
	case "/AvatarShopWishListInfo":
		for _, id := range st.Wishlist {
			out = wire.AppendVarint(out, 1, id)
		}
	case "/AvatarShopWishListSave":
		ids, err := avatarNumbers(request, 2)
		if err != nil {
			return fail(err)
		}
		seen := map[uint64]bool{}
		for _, id := range ids {
			if _, ok := s.design.Shop[id]; !ok || seen[id] {
				return fail(fmt.Errorf("avatar: invalid wishlist product"))
			}
			seen[id] = true
		}
		st.Wishlist = ids
		if err = s.save(ctx, st); err != nil {
			return fail(err)
		}
	case "/AvatarSave":
		b, err := avatarMessage(request, 2)
		if err != nil || b == nil {
			return fail(fmt.Errorf("avatar: missing use configuration"))
		}
		var use AvatarUse
		use.UseCharID, err = avatarNumber(b, 1)
		if err != nil {
			return fail(err)
		}
		for i := range use.Parts {
			use.Parts[i], err = avatarNumber(b, i+2)
			if err != nil {
				return fail(err)
			}
		}
		if err = s.applyUse(ctx, &st, use); err != nil {
			return fail(err)
		}
	case "/AvatarMotionShopBuy":
		id, err := avatarNumber(request, 2)
		if err != nil || id == 0 {
			return fail(fmt.Errorf("avatar: invalid motion product"))
		}
		out, err = s.buy(ctx, &st, []avatarPurchase{{ID: id, Motion: true}})
		if err != nil {
			return fail(err)
		}
	case "/AvatarShopBuy":
		var products []avatarPurchase
		err = wire.Walk(request, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 2 {
				return fmt.Errorf("%w: avatar purchase has wire type %d, want a message", wire.ErrMalformed, f.Type)
			}
			id, err := avatarNumber(f.Value, 1)
			if err != nil || id == 0 {
				return fmt.Errorf("avatar: missing shop product")
			}
			b, err := avatarMessage(f.Value, 2)
			if err != nil || b == nil {
				return fmt.Errorf("avatar: missing submitted price")
			}
			var cost gamedata.Reward
			cost.Type, err = avatarNumber(b, 3)
			if err != nil {
				return err
			}
			cost.ID, err = avatarNumber(b, 2)
			if err != nil {
				return err
			}
			cost.Count, err = avatarNumber(b, 4)
			if err != nil {
				return err
			}
			products = append(products, avatarPurchase{ID: id, Cost: cost})
			return nil
		})
		if err != nil {
			return fail(err)
		}
		if len(products) == 0 || len(products) > len(s.design.Shop) {
			return fail(fmt.Errorf("avatar: empty or excessive purchase"))
		}
		out, err = s.buy(ctx, &st, products)
		if err != nil {
			return fail(err)
		}
	}
	return code, out, true, nil
}
