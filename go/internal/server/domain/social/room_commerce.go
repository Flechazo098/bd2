package social

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type RoomEconomy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	NativePurchaseCosts(command.Context, gamedata.Reward) ([]gamedata.Reward, error)
}

type RoomCommerce struct {
	Design      *gamedata.RoomCommerceDesign
	Rooms       *gamedata.RoomDesign
	Economy     RoomEconomy
	PackCleared func(uint64, uint64) bool
}

type roomCommerceState struct {
	Rooms  []uint64
	Bought map[uint64]uint64
}

type roomCommerceReceipt struct {
	Digest string
	Body   []byte
}

func roomCommerceLoad(ctx command.Context) (roomCommerceState, error) {
	s := roomCommerceState{Bought: map[uint64]uint64{}}
	raw, found, err := ctx.State.LoadEntry("social", "room_commerce", "core")
	if err != nil || !found {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if s.Bought == nil {
		return s, fmt.Errorf("%w: saved room purchases lack their counts", ErrInvalid)
	}
	return s, nil
}

func (s RoomCommerce) OwnedRooms(ctx command.Context) ([]uint64, error) {
	state, err := roomCommerceLoad(ctx)
	return slices.Clone(state.Rooms), err
}

func (s RoomCommerce) Handle(ctx command.Context, path string, raw []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/MyRoomShopInfo": 245, "/MyRoomShopBuy": 246, "/MyRoomShopMultiBuy": 646, "/MyRoomExpand": 418}
	code, found := codes[path]
	if !found {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) {
		return code, nil, true, friendError(code, 9003, err.Error())
	}
	if s.Design == nil || s.Rooms == nil || s.Economy == nil {
		return fail(fmt.Errorf("%w: room commerce is not configured", ErrInvalid))
	}
	p, err := parsePacket(raw)
	if err != nil {
		return fail(err)
	}
	seq, err := p.integer(1)
	if err != nil || seq == 0 {
		return fail(fmt.Errorf("%w: %s sequence %d: %v", ErrInvalid, path, seq, err))
	}
	state, err := roomCommerceLoad(ctx)
	if err != nil {
		return code, nil, true, err
	}
	if path == "/MyRoomShopInfo" {
		keys := make([]uint64, 0, len(state.Bought))
		for key := range state.Bought {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var out []byte
		for _, key := range keys {
			row := wire.AppendVarint(nil, 1, key)
			row = wire.AppendVarint(row, 2, state.Bought[key])
			out = wire.AppendBytes(out, 1, row)
		}
		return code, out, true, nil
	}
	key := ctx.SessionID + "/" + ctx.RequestID
	if ctx.RequestID == "" {
		key = fmt.Sprintf("%s/%s/%d", ctx.SessionID, path, seq)
	}
	digest := sha256.Sum256(append([]byte(path), raw...))
	if saved, found, err := ctx.State.LoadEntry("social", "room_commerce_replies", key); err != nil {
		return code, nil, true, err
	} else if found {
		var r roomCommerceReceipt
		if err := json.Unmarshal(saved, &r); err != nil {
			return code, nil, true, err
		}
		if r.Digest != hex.EncodeToString(digest[:]) {
			return fail(fmt.Errorf("%w: %s sequence %d was replayed with a different body", ErrConflict, path, seq))
		}
		return code, r.Body, true, nil
	}
	var costs, rewards []gamedata.Reward
	var room uint64
	if path == "/MyRoomExpand" {
		room, err = p.integer(2)
		if err != nil {
			return fail(err)
		}
		cost, known := s.Design.Expansion[room]
		owned := append(slices.Clone(s.Rooms.Defaults), state.Rooms...)
		slices.Sort(owned)
		if !known || slices.Contains(owned, room) || len(owned) == 0 || room != owned[len(owned)-1]+1 {
			return fail(fmt.Errorf("%w: room %d is not the next expansion after %v (priced=%t)", ErrIneligible, room, owned, known))
		}
		if err := roomPayment(p, 3, []gamedata.Reward{cost}, 1, &costs); err != nil {
			return fail(err)
		}
		state.Rooms = append(state.Rooms, room)
	} else {
		rows := []packet{p}
		idField, countField, paymentField := 2, 3, 4
		if path == "/MyRoomShopMultiBuy" {
			rows = nil
			idField, countField, paymentField = 1, 2, 3
			if len(p[2]) == 0 || len(p[2]) > 256 {
				return fail(fmt.Errorf("%w: room purchase carries %d products, want 1..256", ErrInvalid, len(p[2])))
			}
			for _, f := range p[2] {
				if f.Type != 2 {
					return fail(fmt.Errorf("%w: room product has wire type %d, want a message", ErrInvalid, f.Type))
				}
				row, err := parsePacket(f.Value)
				if err != nil {
					return fail(err)
				}
				rows = append(rows, row)
			}
		}
		seen := map[uint64]bool{}
		for _, row := range rows {
			id, err := row.integer(idField)
			if err != nil {
				return fail(err)
			}
			n, err := row.integer(countField)
			if err != nil {
				return fail(err)
			}
			product, known := s.Design.Products[id]
			if !known || n == 0 || seen[id] || n > product.Limit || state.Bought[id] > product.Limit-n {
				return fail(fmt.Errorf("%w: room product %d count %d exceeds its limit (known=%t, repeated=%t, bought %d)", ErrCapacity, id, n, known, seen[id], state.Bought[id]))
			}
			seen[id] = true
			item, known := s.Rooms.Items[product.Reward.ID]
			if !known || product.Reward.Type != 27 {
				return fail(fmt.Errorf("%w: room product %d has an unknown furnishing %d", ErrInvalid, id, product.Reward.ID))
			}
			if item.PackID != 0 && (s.PackCleared == nil || !s.PackCleared(item.PackID, item.QuestLevel)) {
				return fail(fmt.Errorf("%w: room product %d requires pack %d difficulty %d", ErrIneligible, id, item.PackID, item.QuestLevel))
			}
			if err := roomPayment(row, paymentField, product.Prices, n, &costs); err != nil {
				return fail(err)
			}
			if product.Reward.Count > math.MaxInt32/n {
				return fail(fmt.Errorf("%w: room product %d reward count overflows at %d", ErrCapacity, id, n))
			}
			reward := product.Reward
			reward.Count *= n
			rewards = append(rewards, reward)
			state.Bought[id] += n
		}
	}
	merged := map[[2]uint64]uint64{}
	for _, cost := range costs {
		merged[[2]uint64{cost.Type, cost.ID}] += cost.Count
	}
	costs = nil
	for key, n := range merged {
		resolved, err := s.Economy.NativePurchaseCosts(ctx, gamedata.Reward{Type: key[0], ID: key[1], Count: n})
		if err != nil {
			return fail(err)
		}
		costs = append(costs, resolved...)
	}
	sort.Slice(costs, func(i, j int) bool {
		if costs[i].Type != costs[j].Type {
			return costs[i].Type < costs[j].Type
		}
		return costs[i].ID < costs[j].ID
	})
	bundle, err := s.Economy.Apply(ctx, "room-commerce:"+key, costs, rewards)
	if err != nil {
		return fail(err)
	}
	out := wire.AppendBytes(nil, 1, bundle)
	if room != 0 {
		out = wire.AppendBytes(out, 2, wire.AppendVarint(nil, 1, room))
	}
	stateRaw, err := json.Marshal(state)
	if err != nil {
		return code, nil, true, err
	}
	if err := ctx.State.PutEntry("social", "room_commerce", "core", stateRaw); err != nil {
		return code, nil, true, err
	}
	receipt, err := json.Marshal(roomCommerceReceipt{Digest: hex.EncodeToString(digest[:]), Body: out})
	if err != nil {
		return code, nil, true, err
	}
	return code, out, true, ctx.State.PutEntry("social", "room_commerce_replies", key, receipt)
}

func roomPayment(p packet, field int, prices []gamedata.Reward, count uint64, out *[]gamedata.Reward) error {
	if len(p[field]) != 1 || p[field][0].Type != 2 {
		return fmt.Errorf("%w: room payment field %d needs one message", ErrInvalid, field)
	}
	item, err := parsePacket(p[field][0].Value)
	if err != nil {
		return err
	}
	typ, err := item.integer(3)
	if err != nil {
		return err
	}
	id, err := item.integer(2)
	if err != nil {
		return err
	}
	n, err := item.integer(4)
	if err != nil {
		return err
	}
	for _, price := range prices {
		if price.Count > math.MaxInt32/count {
			return fmt.Errorf("%w: room price overflows at count %d", ErrCapacity, count)
		}
		if price.Type == typ && price.ID == id && price.Count*count == n {
			price.Count = n
			*out = append(*out, price)
			return nil
		}
	}
	return fmt.Errorf("%w: room payment type %d id %d count %d matches no price", ErrInvalid, typ, id, n)
}
