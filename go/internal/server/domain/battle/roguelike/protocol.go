package roguelike

import "bd2server/internal/server/protocol/wire"

func stateWire(r *run) []byte {
	if r == nil {
		return wire.AppendVarint(nil, 1, 0)
	}
	b := wire.AppendVarint(nil, 1, r.Floor)
	b = wire.AppendVarint(b, 2, r.Room)
	return wire.AppendVarint(b, 3, r.State)
}
func roomWire(r room) []byte {
	b := wire.AppendVarint(nil, 1, r.Number)
	b = wire.AppendVarint(b, 2, r.Group)
	b = wire.AppendVarint(b, 3, r.ID)
	if r.Clear {
		b = wire.AppendVarint(b, 4, 1)
	}
	return b
}
func floorWire(f floor) []byte {
	b := wire.AppendVarint(nil, 1, f.Number)
	for _, r := range f.Rooms {
		b = wire.AppendBytes(b, 2, roomWire(r))
	}
	return b
}
func choiceWire(r *run) []byte {
	if r == nil || r.ChoiceType == 0 {
		return nil
	}
	b := wire.AppendVarint(nil, 1, r.ChoiceType)
	for _, id := range r.Choice {
		b = wire.AppendVarint(b, 2, id)
	}
	return b
}
func charWire(c character, level uint64) []byte {
	b := wire.AppendVarint(nil, 1, c.Index)
	b = wire.AppendVarint(b, 2, c.ID)
	b = wire.AppendVarint(b, 3, c.HP)
	b = wire.AppendVarint(b, 4, level)
	return wire.AppendVarint(b, 7, c.Costume)
}
func costumeWire(c costume) []byte {
	b := wire.AppendVarint(nil, 1, c.Index)
	b = wire.AppendVarint(b, 2, c.ID)
	b = wire.AppendVarint(b, 3, c.Level)
	b = wire.AppendVarint(b, 4, c.Character)
	return wire.AppendVarint(b, 10, c.Burst)
}
func relicWire(r relic) []byte { return wire.AppendVarint(wire.AppendVarint(nil, 1, r.Index), 2, r.ID) }
func shopWire(r *run, price uint64) []byte {
	b := wire.AppendVarint(nil, 2, price)
	for _, o := range r.Shop {
		x := wire.AppendVarint(nil, 1, o.Type)
		x = wire.AppendVarint(x, 2, o.ID)
		x = wire.AppendVarint(x, 3, o.Price)
		if o.Sold {
			x = wire.AppendVarint(x, 4, 1)
		}
		b = wire.AppendBytes(b, 1, x)
	}
	return b
}
func eventWire(r *run) []byte {
	return wire.AppendVarint(wire.AppendVarint(nil, 1, r.EventGroup), 2, r.EventID)
}
func deckWire(b []byte, f int, r *run) []byte {
	for _, d := range r.Deck {
		b = wire.AppendBytes(b, f, d)
	}
	return b
}
func clearedResponse(r *run, f uint64, room *room, sf, ff, rf int) []byte {
	b := wire.AppendBytes(nil, sf, stateWire(r))
	if f > 0 && room != nil {
		b = wire.AppendVarint(b, ff, f)
		b = wire.AppendBytes(b, rf, roomWire(*room))
	}
	return b
}
