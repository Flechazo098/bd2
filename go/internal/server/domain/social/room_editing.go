package social

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

func (f *Friends) PrepareRoomText(ctx context.Context, identity command.Identity, path string, raw []byte, check func(string) error) error {
	if path != "/MyRoomNameChange" && path != "/MyRoomSave" && path != "/MyRoomPresetSave" && path != "/MyRoomPresetNameChange" {
		return nil
	}
	key, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	receiptKey := sha256.Sum256(key)
	var prior friendReceipt
	found := false
	if err := f.store.View(ctx, func(tx Tx) error {
		var err error
		found, err = load(tx, "friend.receipts", hex.EncodeToString(receiptKey[:]), &prior)
		return err
	}); err != nil {
		return err
	}
	p, err := parsePacket(raw)
	if err != nil {
		return err
	}
	seq, err := p.integer(1)
	if err != nil {
		return err
	}
	_, digest := requestIdentity(identity, path, seq, raw)
	if found {
		if prior.Digest != digest {
			return fmt.Errorf("%w: %s sequence %d was replayed with a different body", ErrConflict, path, seq)
		}
		return nil
	}
	var texts []string
	switch path {
	case "/MyRoomNameChange":
		text, err := p.text(3)
		if err != nil {
			return err
		}
		texts = append(texts, text)
	case "/MyRoomPresetSave", "/MyRoomPresetNameChange":
		texts, err = f.presetText(p, path)
		if err != nil {
			return err
		}
	default:
		for _, row := range p[2] {
			if row.Type != 2 {
				return fmt.Errorf("%w: %s room row has wire type %d, want a message", ErrInvalid, path, row.Type)
			}
			p, err := parsePacket(row.Value)
			if err != nil {
				return err
			}
			text, err := p.text(2)
			if err != nil {
				return err
			}
			texts = append(texts, text)
		}
	}
	for _, text := range texts {
		if !validRoomName(text) {
			return friendError(friendCodes[path], 9003, "Invalid room name")
		}
		if text != "" {
			if err := check(text); err != nil {
				return err
			}
		}
	}
	return nil
}

func validRoomName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) <= 20 && !stringsContainControl(name)
}
func stringsContainControl(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

func (f *Friends) editRooms(state *personalRooms, self FriendProfile, path string, p packet) error {
	if path == "/MyRoomShareOptionUpdate" {
		scope, err := p.integer(2)
		if err != nil || scope > 4 {
			return fmt.Errorf("%w: room share scope %d, want 0..4: %v", ErrInvalid, scope, err)
		}
		state.Scope = scope
		return nil
	}
	if path == "/MyRoomSave" {
		if len(p[2]) == 0 || len(p[2]) > len(state.Rooms) {
			return fmt.Errorf("%w: room save carries %d rooms, %d owned", ErrInvalid, len(p[2]), len(state.Rooms))
		}
		seen := map[uint64]bool{}
		for _, field := range p[2] {
			if field.Type != 2 {
				return fmt.Errorf("%w: saved room has wire type %d, want a message", ErrInvalid, field.Type)
			}
			row, err := parsePacket(field.Value)
			if err != nil {
				return err
			}
			id, err := row.integer(1)
			if err != nil || id == 0 || seen[id] {
				return fmt.Errorf("%w: saved room id %d is zero or repeated: %v", ErrInvalid, id, err)
			}
			seen[id] = true
			index := -1
			for i, v := range state.Rooms {
				if v.ID == id {
					index = i
					break
				}
			}
			if index < 0 {
				return fmt.Errorf("%w: room %d is not owned", ErrPermission, id)
			}
			name, err := row.text(2)
			if err != nil || !validRoomName(name) {
				return fmt.Errorf("%w: room name %q: %v", ErrInvalid, name, err)
			}
			hidden, err := row.integer(4)
			if err != nil || hidden > 1 {
				return fmt.Errorf("%w: room hidden flag %d: %v", ErrInvalid, hidden, err)
			}
			room := personalRoom{ID: id, Name: name, Hidden: hidden == 1}
			for _, position := range row[3] {
				if position.Type != 2 {
					return fmt.Errorf("%w: room %d position has wire type %d, want a message", ErrInvalid, id, position.Type)
				}
				room.Positions = append(room.Positions, append([]byte(nil), position.Value...))
			}
			if err := f.validatePositions(room, self, f.rooms.Limits[id]); err != nil {
				return err
			}
			state.Rooms[index] = room
		}
		return validateRoomInventory(*state, self)
	}
	id, err := p.integer(2)
	if err != nil || id == 0 {
		return fmt.Errorf("%w: %s room id %d: %v", ErrInvalid, path, id, err)
	}
	index := -1
	for i, room := range state.Rooms {
		if room.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: room %d is not owned", ErrPermission, id)
	}
	switch path {
	case "/MyRoomNameChange":
		name, err := p.text(3)
		if err != nil || !validRoomName(name) {
			return fmt.Errorf("%w: room name %q: %v", ErrInvalid, name, err)
		}
		state.Rooms[index].Name = name
	case "/MyRoomPrimaryChange":
		if state.Rooms[index].Hidden {
			return fmt.Errorf("%w: hidden room %d cannot be primary", ErrIneligible, id)
		}
		state.Primary = id
	case "/MyRoomMove":
		target, err := p.integer(3)
		if err != nil || target == 0 {
			return fmt.Errorf("%w: room move target %d: %v", ErrInvalid, target, err)
		}
		other := -1
		for i, v := range state.Rooms {
			if v.ID == target {
				other = i
				break
			}
		}
		if other < 0 {
			return fmt.Errorf("%w: room move target %d is not owned", ErrPermission, target)
		}
		state.Rooms[index], state.Rooms[other] = state.Rooms[other], state.Rooms[index]
		state.Rooms[index].ID = id
		state.Rooms[other].ID = target
		if err := f.validatePositions(state.Rooms[index], self, f.rooms.Limits[id]); err != nil {
			return err
		}
		if err := f.validatePositions(state.Rooms[other], self, f.rooms.Limits[target]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown room edit %s", ErrInvalid, path)
	}
	return nil
}

func validateRoomInventory(state personalRooms, self FriendProfile) error {
	used := map[uint64]uint64{}
	for _, room := range state.Rooms {
		for _, position := range room.Positions {
			index, _, err := wire.Varint(position, 1)
			if err != nil {
				return err
			}
			typ, _, err := wire.Varint(position, 2)
			if err != nil {
				return err
			}
			used[index]++
			if typ == 1 {
				if used[index] > 1 {
					return fmt.Errorf("%w: room costume %d is placed %d times", ErrPermission, index, used[index])
				}
				continue
			}
			var count uint64
			for _, item := range self.RoomItems {
				if item.InvenIndex == index {
					count = item.Count
					break
				}
			}
			if used[index] > count {
				return fmt.Errorf("%w: room item %d is placed %d times, %d owned", ErrPermission, index, used[index], count)
			}
		}
	}
	return nil
}

func (f *Friends) validatePositions(room personalRoom, self FriendProfile, limits gamedata.RoomLimits) error {
	if len(room.Positions) > 256 {
		return fmt.Errorf("%w: room %d has %d placements, at most 256", ErrCapacity, room.ID, len(room.Positions))
	}
	var objects, carpets, characters uint64
	used := map[string]uint64{}
	for _, raw := range room.Positions {
		p, err := parsePacket(raw)
		if err != nil {
			return err
		}
		index, err := p.integer(1)
		if err != nil || index == 0 {
			return fmt.Errorf("%w: room placement inventory index %d: %v", ErrInvalid, index, err)
		}
		typ, err := p.integer(2)
		if err != nil || typ > 2 {
			return fmt.Errorf("%w: room placement type %d, want 0..2: %v", ErrInvalid, typ, err)
		}
		position, err := p.integer(3)
		if err != nil || position > 3 {
			return fmt.Errorf("%w: room placement location %d, want 0..3: %v", ErrInvalid, position, err)
		}
		for _, field := range []int{4, 5, 6, 7, 8} {
			n, err := p.integer(field)
			if err != nil || n > math.MaxInt32 {
				return fmt.Errorf("%w: room placement field %d value %d: %v", ErrInvalid, field, n, err)
			}
		}
		wallHidden, err := p.integer(9)
		if err != nil || wallHidden > 1 {
			return fmt.Errorf("%w: room placement wall flag %d: %v", ErrInvalid, wallHidden, err)
		}
		key := fmt.Sprintf("%d:%d", typ, index)
		used[key]++
		if typ == 1 {
			found := false
			for _, costume := range self.RoomCostumes {
				if costume.InvenIndex == index {
					found = true
					break
				}
			}
			if !found || used[key] > 1 {
				return fmt.Errorf("%w: room costume %d is missing or placed %d times", ErrPermission, index, used[key])
			}
			characters++
			continue
		}
		var id, count uint64
		for _, item := range self.RoomItems {
			if item.InvenIndex == index && (typ == 0 && item.Type == 27 || typ == 2 && item.Type == 28) {
				id = item.ID
				count = item.Count
				break
			}
		}
		if id == 0 || used[key] > count {
			return fmt.Errorf("%w: room item %d (type %d) is missing or placed %d times, %d owned", ErrPermission, index, typ, used[key], count)
		}
		if typ == 2 {
			objects++
			continue
		}
		item, found := f.rooms.Items[id]
		if !found || item.ObjectType != 0 {
			return fmt.Errorf("%w: room item %d is not a placeable object (known=%t)", ErrInvalid, id, found)
		}
		if item.Location == 0 && position != 1 || item.Location == 1 && position != 2 && position != 3 {
			return fmt.Errorf("%w: room item %d at location %d cannot go to position %d", ErrInvalid, id, item.Location, position)
		}
		switch item.Kind {
		case 0:
			if used[key] > 2 {
				return fmt.Errorf("%w: room item %d is placed %d times", ErrInvalid, id, used[key])
			}
		case 1:
			objects++
		case 2:
			carpets++
		default:
			return fmt.Errorf("%w: room item %d has kind %d", ErrInvalid, id, item.Kind)
		}
	}
	if objects > limits.Objects || carpets > limits.Carpets || characters > limits.Characters {
		return fmt.Errorf("%w: room %d holds %d objects, %d carpets and %d characters, limits %+v", ErrCapacity, room.ID, objects, carpets, characters, limits)
	}
	return nil
}
