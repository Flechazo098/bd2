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
			return ErrConflict
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
				return ErrInvalid
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
			return ErrInvalid
		}
		state.Scope = scope
		return nil
	}
	if path == "/MyRoomSave" {
		if len(p[2]) == 0 || len(p[2]) > len(state.Rooms) {
			return ErrInvalid
		}
		seen := map[uint64]bool{}
		for _, field := range p[2] {
			if field.Type != 2 {
				return ErrInvalid
			}
			row, err := parsePacket(field.Value)
			if err != nil {
				return err
			}
			id, err := row.integer(1)
			if err != nil || id == 0 || seen[id] {
				return ErrInvalid
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
				return ErrPermission
			}
			name, err := row.text(2)
			if err != nil || !validRoomName(name) {
				return ErrInvalid
			}
			hidden, err := row.integer(4)
			if err != nil || hidden > 1 {
				return ErrInvalid
			}
			room := personalRoom{ID: id, Name: name, Hidden: hidden == 1}
			for _, position := range row[3] {
				if position.Type != 2 {
					return ErrInvalid
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
		return ErrInvalid
	}
	index := -1
	for i, room := range state.Rooms {
		if room.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrPermission
	}
	switch path {
	case "/MyRoomNameChange":
		name, err := p.text(3)
		if err != nil || !validRoomName(name) {
			return ErrInvalid
		}
		state.Rooms[index].Name = name
	case "/MyRoomPrimaryChange":
		if state.Rooms[index].Hidden {
			return ErrIneligible
		}
		state.Primary = id
	case "/MyRoomMove":
		target, err := p.integer(3)
		if err != nil || target == 0 {
			return ErrInvalid
		}
		other := -1
		for i, v := range state.Rooms {
			if v.ID == target {
				other = i
				break
			}
		}
		if other < 0 {
			return ErrPermission
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
		return ErrInvalid
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
					return ErrPermission
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
				return ErrPermission
			}
		}
	}
	return nil
}

func (f *Friends) validatePositions(room personalRoom, self FriendProfile, limits gamedata.RoomLimits) error {
	if len(room.Positions) > 256 {
		return ErrCapacity
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
			return ErrInvalid
		}
		typ, err := p.integer(2)
		if err != nil || typ > 2 {
			return ErrInvalid
		}
		position, err := p.integer(3)
		if err != nil || position > 3 {
			return ErrInvalid
		}
		for _, field := range []int{4, 5, 6, 7, 8} {
			n, err := p.integer(field)
			if err != nil || n > math.MaxInt32 {
				return ErrInvalid
			}
		}
		wallHidden, err := p.integer(9)
		if err != nil || wallHidden > 1 {
			return ErrInvalid
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
				return ErrPermission
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
			return ErrPermission
		}
		if typ == 2 {
			objects++
			continue
		}
		item, found := f.rooms.Items[id]
		if !found || item.ObjectType != 0 {
			return ErrInvalid
		}
		if item.Location == 0 && position != 1 || item.Location == 1 && position != 2 && position != 3 {
			return ErrInvalid
		}
		switch item.Kind {
		case 0:
			if used[key] > 2 {
				return ErrInvalid
			}
		case 1:
			objects++
		case 2:
			carpets++
		default:
			return ErrInvalid
		}
	}
	if objects > limits.Objects || carpets > limits.Carpets || characters > limits.Characters {
		return ErrCapacity
	}
	return nil
}
