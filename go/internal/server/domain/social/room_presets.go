package social

import (
	"bytes"
	"fmt"

	"bd2server/internal/server/protocol/wire"
)

type roomPreset struct {
	Slot         uint64
	Name         string
	Items, Rooms [][]byte
}

func roomPresetWire(p roomPreset) []byte {
	out := wire.AppendVarint(nil, 1, p.Slot)
	out = wire.AppendString(out, 2, p.Name)
	for _, item := range p.Items {
		out = wire.AppendBytes(out, 3, item)
	}
	for _, room := range p.Rooms {
		out = wire.AppendBytes(out, 4, room)
	}
	return out
}
func roomPresetKey(owner, kind, slot uint64) string {
	return numberKey(owner) + ":" + numberKey(kind) + ":" + numberKey(slot)
}

func (f *Friends) roomPreset(tx Tx, self FriendProfile, path string, p packet) ([]byte, error) {
	if path == "/MyRoomPresetInfo" {
		var out []byte
		for kind := uint64(1); kind <= 2; kind++ {
			limit := f.rooms.MyPresets
			if kind == 2 {
				limit = f.rooms.UserPresets
			}
			for slot := uint64(1); slot <= limit; slot++ {
				var preset roomPreset
				found, err := load(tx, "friend.room_presets", roomPresetKey(self.OwnerIndex, kind, slot), &preset)
				if err != nil {
					return nil, err
				}
				if found {
					out = wire.AppendBytes(out, int(kind), roomPresetWire(preset))
				}
			}
		}
		return out, nil
	}
	kind, err := p.integer(2)
	if err != nil {
		return nil, err
	}
	var slot uint64
	if path != "/MyRoomPresetSave" {
		slot, err = p.integer(3)
		if err != nil {
			return nil, err
		}
	}
	var preset roomPreset
	if path == "/MyRoomPresetSave" {
		sourceOwner := kind
		kind = 1
		if sourceOwner != self.OwnerIndex {
			kind = 2
		}
		if len(p[3]) != 1 || p[3][0].Type != 2 {
			return nil, fmt.Errorf("%w: %s needs one preset message", ErrInvalid, path)
		}
		row, err := parsePacket(p[3][0].Value)
		if err != nil {
			return nil, err
		}
		slot, err = row.integer(1)
		if err != nil {
			return nil, err
		}
		preset.Slot = slot
		preset.Name, err = row.text(2)
		if err != nil || !validRoomName(preset.Name) {
			return nil, fmt.Errorf("%w: room preset name %q: %v", ErrInvalid, preset.Name, err)
		}
		var source FriendProfile
		found, err := load(tx, "friend.directory", numberKey(sourceOwner), &source)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%w: friend profile for owner %d", ErrNotFound, sourceOwner)
		}
		var state personalRooms
		found, err = load(tx, "friend.rooms", numberKey(sourceOwner), &state)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%w: rooms of owner %d", ErrNotFound, sourceOwner)
		}
		visible, err := roomVisible(tx, self, source, state)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, fmt.Errorf("%w: rooms of owner %d are not visible to owner %d", ErrPermission, sourceOwner, self.OwnerIndex)
		}
		if err := f.validateRoomPreset(source, self.OwnerIndex == sourceOwner, state, row, &preset); err != nil {
			return nil, err
		}
	}
	if err != nil || kind < 1 || kind > 2 {
		return nil, fmt.Errorf("%w: room preset kind %d, want 1 or 2: %v", ErrInvalid, kind, err)
	}
	limit := f.rooms.MyPresets
	if kind == 2 {
		limit = f.rooms.UserPresets
	}
	if slot == 0 || slot > limit {
		return nil, fmt.Errorf("%w: room preset slot %d, want 1..%d", ErrInvalid, slot, limit)
	}
	key := roomPresetKey(self.OwnerIndex, kind, slot)
	if path == "/MyRoomPresetDelete" {
		return nil, remove(tx, "friend.room_presets", key)
	}
	if path == "/MyRoomPresetNameChange" {
		found, err := load(tx, "friend.room_presets", key, &preset)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%w: room preset kind %d slot %d", ErrNotFound, kind, slot)
		}
		preset.Name, err = p.text(4)
		if err != nil || !validRoomName(preset.Name) {
			return nil, fmt.Errorf("%w: room preset name %q: %v", ErrInvalid, preset.Name, err)
		}
	}
	return nil, save(tx, "friend.room_presets", key, preset)
}

func (f *Friends) validateRoomPreset(source FriendProfile, own bool, state personalRooms, row packet, result *roomPreset) error {
	if len(row[4]) == 0 || len(row[4]) > len(f.rooms.Limits) || len(row[3]) > 256 {
		return fmt.Errorf("%w: room preset carries %d rooms and %d items", ErrInvalid, len(row[4]), len(row[3]))
	}
	items := map[uint64]uint64{}
	seen := map[uint64]bool{}
	for _, field := range row[3] {
		if field.Type != 2 {
			return fmt.Errorf("%w: room preset item has wire type %d, want a message", ErrInvalid, field.Type)
		}
		item, err := parsePacket(field.Value)
		if err != nil {
			return err
		}
		index, err := item.integer(1)
		if err != nil {
			return err
		}
		id, err := item.integer(2)
		if err != nil {
			return err
		}
		count, err := item.integer(4)
		if err != nil || count == 0 || seen[index] {
			return fmt.Errorf("%w: room preset item %d count %d is zero or repeated: %v", ErrInvalid, index, count, err)
		}
		seen[index] = true
		owned := false
		for _, v := range source.RoomItems {
			if v.Type == 27 && v.InvenIndex == index && v.ID == id && v.Count >= count {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("%w: owner %d lacks %d of room item %d at index %d", ErrPermission, source.OwnerIndex, count, id, index)
		}
		items[index] = count
		out := wire.AppendVarint(nil, 1, index)
		out = wire.AppendVarint(out, 2, id)
		out = wire.AppendVarint(out, 4, count)
		result.Items = append(result.Items, out)
	}
	roomIDs := map[uint64]bool{}
	used := map[uint64]uint64{}
	for _, field := range row[4] {
		if field.Type != 2 {
			return fmt.Errorf("%w: room preset room has wire type %d, want a message", ErrInvalid, field.Type)
		}
		room, err := parsePacket(field.Value)
		if err != nil {
			return err
		}
		id, err := room.integer(1)
		if err != nil || roomIDs[id] {
			return fmt.Errorf("%w: room preset room %d is repeated: %v", ErrInvalid, id, err)
		}
		roomIDs[id] = true
		limits, found := f.rooms.Limits[id]
		if !found {
			return fmt.Errorf("%w: room %d has no limits", ErrInvalid, id)
		}
		hidden, err := room.integer(4)
		if err != nil || hidden > 1 {
			return fmt.Errorf("%w: room preset hidden flag %d: %v", ErrInvalid, hidden, err)
		}
		name, err := room.text(2)
		if err != nil || !validRoomName(name) {
			return fmt.Errorf("%w: room preset name %q: %v", ErrInvalid, name, err)
		}
		v := personalRoom{ID: id, Name: name, Hidden: hidden == 1}
		var original *personalRoom
		for _, r := range state.Rooms {
			if r.ID == id {
				copy := r
				original = &copy
				break
			}
		}
		if hidden == 0 && (original == nil || original.Hidden && !own) {
			return fmt.Errorf("%w: room %d is not visible in the source rooms", ErrPermission, id)
		}
		if hidden == 1 && len(room[3]) > 0 {
			return fmt.Errorf("%w: hidden room %d carries %d placements", ErrInvalid, id, len(room[3]))
		}
		for _, position := range room[3] {
			if position.Type != 2 {
				return fmt.Errorf("%w: room %d placement has wire type %d, want a message", ErrInvalid, id, position.Type)
			}
			p, err := parsePacket(position.Value)
			if err != nil {
				return err
			}
			typ, err := p.integer(2)
			if err != nil || typ != 0 {
				return fmt.Errorf("%w: room preset placement type %d, want 0: %v", ErrInvalid, typ, err)
			}
			index, err := p.integer(1)
			if err != nil {
				return err
			}
			used[index]++
			if used[index] > items[index] {
				return fmt.Errorf("%w: room preset places item %d %d times, %d saved", ErrPermission, index, used[index], items[index])
			}
			if !own {
				matched := false
				for _, raw := range original.Positions {
					if bytes.Equal(raw, position.Value) {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("%w: room %d placement is not in the source room", ErrPermission, id)
				}
			}
			v.Positions = append(v.Positions, append([]byte(nil), position.Value...))
		}
		if err := f.validatePositions(v, source, limits); err != nil {
			return err
		}
		result.Rooms = append(result.Rooms, roomWire(v))
	}
	return nil
}

func (f *Friends) presetText(p packet, path string) ([]string, error) {
	if path == "/MyRoomPresetNameChange" {
		name, err := p.text(4)
		return []string{name}, err
	}
	if len(p[3]) != 1 || p[3][0].Type != 2 {
		return nil, fmt.Errorf("%w: %s needs one preset message", ErrInvalid, path)
	}
	row, err := parsePacket(p[3][0].Value)
	if err != nil {
		return nil, err
	}
	name, err := row.text(2)
	if err != nil {
		return nil, err
	}
	texts := []string{name}
	for _, room := range row[4] {
		if room.Type != 2 {
			return nil, fmt.Errorf("%w: room preset room has wire type %d, want a message", ErrInvalid, room.Type)
		}
		p, err := parsePacket(room.Value)
		if err != nil {
			return nil, err
		}
		name, err := p.text(2)
		if err != nil {
			return nil, err
		}
		texts = append(texts, name)
	}
	return texts, nil
}
