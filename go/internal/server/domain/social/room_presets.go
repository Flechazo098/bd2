package social

import (
	"bytes"

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
			return nil, ErrInvalid
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
			return nil, ErrInvalid
		}
		var source FriendProfile
		found, err := load(tx, "friend.directory", numberKey(sourceOwner), &source)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrNotFound
		}
		var state personalRooms
		found, err = load(tx, "friend.rooms", numberKey(sourceOwner), &state)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrNotFound
		}
		visible, err := roomVisible(tx, self, source, state)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, ErrPermission
		}
		if err := f.validateRoomPreset(source, self.OwnerIndex == sourceOwner, state, row, &preset); err != nil {
			return nil, err
		}
	}
	if err != nil || kind < 1 || kind > 2 {
		return nil, ErrInvalid
	}
	limit := f.rooms.MyPresets
	if kind == 2 {
		limit = f.rooms.UserPresets
	}
	if slot == 0 || slot > limit {
		return nil, ErrInvalid
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
			return nil, ErrNotFound
		}
		preset.Name, err = p.text(4)
		if err != nil || !validRoomName(preset.Name) {
			return nil, ErrInvalid
		}
	}
	return nil, save(tx, "friend.room_presets", key, preset)
}

func (f *Friends) validateRoomPreset(source FriendProfile, own bool, state personalRooms, row packet, result *roomPreset) error {
	if len(row[4]) == 0 || len(row[4]) > len(f.rooms.Limits) || len(row[3]) > 256 {
		return ErrInvalid
	}
	items := map[uint64]uint64{}
	seen := map[uint64]bool{}
	for _, field := range row[3] {
		if field.Type != 2 {
			return ErrInvalid
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
			return ErrInvalid
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
			return ErrPermission
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
			return ErrInvalid
		}
		room, err := parsePacket(field.Value)
		if err != nil {
			return err
		}
		id, err := room.integer(1)
		if err != nil || roomIDs[id] {
			return ErrInvalid
		}
		roomIDs[id] = true
		limits, found := f.rooms.Limits[id]
		if !found {
			return ErrInvalid
		}
		hidden, err := room.integer(4)
		if err != nil || hidden > 1 {
			return ErrInvalid
		}
		name, err := room.text(2)
		if err != nil || !validRoomName(name) {
			return ErrInvalid
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
			return ErrPermission
		}
		if hidden == 1 && len(room[3]) > 0 {
			return ErrInvalid
		}
		for _, position := range room[3] {
			if position.Type != 2 {
				return ErrInvalid
			}
			p, err := parsePacket(position.Value)
			if err != nil {
				return err
			}
			typ, err := p.integer(2)
			if err != nil || typ != 0 {
				return ErrInvalid
			}
			index, err := p.integer(1)
			if err != nil {
				return err
			}
			used[index]++
			if used[index] > items[index] {
				return ErrPermission
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
					return ErrPermission
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
		return nil, ErrInvalid
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
			return nil, ErrInvalid
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
