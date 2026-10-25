package social

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

func (f *Friends) RoomProfile(ctx context.Context, viewer, target uint64) ([]byte, uint64, error) {
	var out []byte
	var total uint64
	err := f.store.View(ctx, func(tx Tx) error {
		var self, other FriendProfile
		found, err := load(tx, "friend.directory", numberKey(viewer), &self)
		if err != nil || !found {
			return err
		}
		found, err = load(tx, "friend.directory", numberKey(target), &other)
		if err != nil || !found {
			return err
		}
		var state personalRooms
		found, err = load(tx, "friend.rooms", numberKey(target), &state)
		if err != nil || !found {
			return err
		}
		var likes roomLikes
		if _, err := load(tx, "friend.room_like_counts", numberKey(target), &likes); err != nil {
			return err
		}
		total = likes.Total
		out, err = f.roomUser(tx, self, other, state, false)
		if errors.Is(err, ErrPermission) {
			out = wire.AppendVarint(nil, 1, target)
			out = wire.AppendString(out, 2, other.Snapshot.UserID)
			out = wire.AppendVarint(out, 12, state.Scope)
			return nil
		}
		return err
	})
	return out, total, err
}

type personalRoom struct {
	ID        uint64
	Name      string
	Hidden    bool
	Positions [][]byte
}
type personalRooms struct {
	Primary, Scope uint64
	Rooms          []personalRoom
}
type roomLikes struct {
	Month          string
	Monthly, Total uint64
}

func roomOwns(path string) bool { return strings.HasPrefix(path, "/MyRoom") }
func roomMutates(path string) bool {
	switch path {
	case "/MyRoomPresetSave", "/MyRoomPresetNameChange", "/MyRoomPresetDelete", "/MyRoomLike", "/MyRoomShareOptionUpdate", "/MyRoomSave", "/MyRoomNameChange", "/MyRoomPrimaryChange", "/MyRoomMove":
		return true
	}
	return false
}

func (f *Friends) ensureRooms(tx Tx, p FriendProfile) error {
	var state personalRooms
	_, err := load(tx, "friend.rooms", numberKey(p.OwnerIndex), &state)
	if err != nil {
		return err
	}
	ids := append(append([]uint64(nil), f.rooms.Defaults...), p.RoomIDs...)
	for _, id := range ids {
		if _, found := f.rooms.Limits[id]; !found {
			return fmt.Errorf("%w: room %d has no limits", ErrInvalid, id)
		}
		found := false
		for _, room := range state.Rooms {
			if room.ID == id {
				found = true
				break
			}
		}
		if !found {
			state.Rooms = append(state.Rooms, personalRoom{ID: id})
		}
	}
	if state.Primary == 0 {
		slices.SortFunc(state.Rooms, func(a, b personalRoom) int { return int(a.ID) - int(b.ID) })
		state.Primary = state.Rooms[0].ID
	}
	return save(tx, "friend.rooms", numberKey(p.OwnerIndex), state)
}

func roomVisible(tx Tx, viewer, owner FriendProfile, state personalRooms) (bool, error) {
	if viewer.OwnerIndex == owner.OwnerIndex {
		return true, nil
	}
	friend, err := supporterRelation(tx, viewer.OwnerIndex, owner.OwnerIndex)
	if err != nil {
		return false, err
	}
	a, err := readAccount(tx, viewer.AccountID)
	if err != nil {
		return false, err
	}
	b, err := readAccount(tx, owner.AccountID)
	if err != nil {
		return false, err
	}
	guild := a.GuildID != 0 && a.GuildID == b.GuildID
	switch state.Scope {
	case 0:
		return true, nil
	case 1:
		return friend || guild, nil
	case 2:
		return friend, nil
	case 3:
		return guild, nil
	case 4:
		return false, nil
	}
	return false, fmt.Errorf("%w: room share scope %d", ErrInvalid, state.Scope)
}

func roomWire(v personalRoom) []byte {
	out := wire.AppendVarint(nil, 1, v.ID)
	out = wire.AppendString(out, 2, v.Name)
	for _, position := range v.Positions {
		out = wire.AppendBytes(out, 3, position)
	}
	if v.Hidden {
		out = wire.AppendVarint(out, 4, 1)
	}
	return out
}

func (f *Friends) roomUser(tx Tx, viewer, owner FriendProfile, state personalRooms, all bool) ([]byte, error) {
	visible, err := roomVisible(tx, viewer, owner, state)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, fmt.Errorf("%w: rooms of owner %d are not visible to owner %d", ErrPermission, owner.OwnerIndex, viewer.OwnerIndex)
	}
	usage, err := roomUsage(state)
	if err != nil {
		return nil, err
	}
	out := wire.AppendVarint(nil, 1, owner.OwnerIndex)
	out = wire.AppendString(out, 2, owner.Snapshot.UserID)
	out = wire.AppendVarint(out, 3, owner.Snapshot.PortraitCostumeID)
	out = wire.AppendVarint(out, 4, state.Primary)
	for _, item := range owner.RoomItems {
		switch item.Type {
		case 27:
			item.UseCount = usage[[2]uint64{0, item.InvenIndex}]
			out = wire.AppendBytes(out, 5, inventory.ItemWire(item))
		case 28:
			v := wire.AppendVarint(nil, 1, item.InvenIndex)
			v = wire.AppendVarint(v, 2, item.ID)
			v = wire.AppendVarint(v, 5, usage[[2]uint64{2, item.InvenIndex}])
			out = wire.AppendBytes(out, 7, v)
		}
	}
	for _, costume := range owner.RoomCostumes {
		v := wire.AppendVarint(roster.CostumeWire(costume), 7, usage[[2]uint64{1, costume.InvenIndex}])
		out = wire.AppendBytes(out, 6, v)
	}
	primaryVisible := false
	for _, room := range state.Rooms {
		if room.Hidden && viewer.OwnerIndex != owner.OwnerIndex {
			continue
		}
		if room.ID == state.Primary {
			primaryVisible = true
		}
		if all || viewer.OwnerIndex == owner.OwnerIndex || room.ID == state.Primary {
			out = wire.AppendBytes(out, 8, roomWire(room))
		}
	}
	if !primaryVisible && viewer.OwnerIndex != owner.OwnerIndex {
		return nil, fmt.Errorf("%w: primary room of owner %d is hidden", ErrPermission, owner.OwnerIndex)
	}
	var likes roomLikes
	if _, err := load(tx, "friend.room_like_counts", numberKey(owner.OwnerIndex), &likes); err != nil {
		return nil, err
	}
	out = wire.AppendVarint(out, 9, likes.Total)
	var stamp int64
	if _, err := load(tx, "friend.room_likes", friendKeyDirectional(viewer.OwnerIndex, owner.OwnerIndex), &stamp); err != nil {
		return nil, err
	}
	out = wire.AppendVarint(out, 10, uint64(stamp))
	out = wire.AppendVarint(out, 11, owner.Snapshot.PortraitDesignID)
	return wire.AppendVarint(out, 12, state.Scope), nil
}
func friendKeyDirectional(a, b uint64) string { return numberKey(a) + ":" + numberKey(b) }

func (f *Friends) room(tx Tx, self FriendProfile, path string, p packet, now int64) ([]byte, error) {
	if f.rooms == nil {
		return nil, fmt.Errorf("%w: rooms are not configured", ErrInvalid)
	}
	var state personalRooms
	found, err := load(tx, "friend.rooms", numberKey(self.OwnerIndex), &state)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: rooms of owner %d", ErrNotFound, self.OwnerIndex)
	}
	fail := func(message string) ([]byte, error) { return nil, friendError(friendCodes[path], 9003, message) }
	if strings.HasPrefix(path, "/MyRoomPreset") {
		out, err := f.roomPreset(tx, self, path, p)
		if err != nil {
			return fail(err.Error())
		}
		return out, nil
	}
	if path == "/MyRoomUserInfo" || path == "/MyRoomLike" {
		target, err := p.integer(2)
		if err != nil || target == 0 {
			return fail("Invalid room owner")
		}
		var other FriendProfile
		found, err := load(tx, "friend.directory", numberKey(target), &other)
		if err != nil {
			return nil, err
		}
		if !found {
			return fail("Player not found")
		}
		var rooms personalRooms
		found, err = load(tx, "friend.rooms", numberKey(target), &rooms)
		if err != nil {
			return nil, err
		}
		if !found {
			return fail("Player has no rooms")
		}
		visible, err := roomVisible(tx, self, other, rooms)
		if err != nil {
			return nil, err
		}
		if !visible {
			return fail("Room is private")
		}
		if path == "/MyRoomUserInfo" {
			all, err := p.integer(3)
			if err != nil || all > 1 {
				return nil, fmt.Errorf("%w: MyRoomUserInfo all flag %d: %v", ErrInvalid, all, err)
			}
			out, err := f.roomUser(tx, self, other, rooms, true)
			if err != nil {
				return fail("Room is unavailable")
			}
			response := wire.AppendBytes(nil, 1, out)
			if target == self.OwnerIndex {
				response = wire.AppendVarint(response, 2, 1)
			}
			return response, nil
		}
		if target == self.OwnerIndex {
			return fail("Cannot like your own room")
		}
		if _, err := f.roomUser(tx, self, other, rooms, false); err != nil {
			return fail("Room is unavailable")
		}
		month := time.UnixMilli(now).UTC().Format("2006-01")
		var stamp int64
		key := friendKeyDirectional(self.OwnerIndex, target)
		if _, err := load(tx, "friend.room_likes", key, &stamp); err != nil {
			return nil, err
		}
		if stamp != 0 && time.UnixMilli(stamp).UTC().Format("2006-01") == month {
			return fail("Room already liked this month")
		}
		var count roomLikes
		if _, err := load(tx, "friend.room_like_counts", numberKey(target), &count); err != nil {
			return nil, err
		}
		if count.Total >= math.MaxInt32 {
			return nil, fmt.Errorf("%w: owner %d has %d room likes", ErrCapacity, target, count.Total)
		}
		if count.Month != month {
			count.Month = month
			count.Monthly = 0
		}
		count.Total++
		count.Monthly++
		if err := save(tx, "friend.room_like_counts", numberKey(target), count); err != nil {
			return nil, err
		}
		if err := save(tx, "friend.room_likes", key, now); err != nil {
			return nil, err
		}
		out := wire.AppendVarint(nil, 1, count.Monthly)
		out = wire.AppendVarint(out, 2, count.Total)
		return wire.AppendVarint(out, 3, uint64(now)), nil
	}
	if path == "/MyRoomSearchFriend" || path == "/MyRoomSearchGuild" || path == "/MyRoomSearchRecommend" {
		return f.searchRooms(tx, self, path, p, now)
	}
	if path == "/MyRoomItemInfo" {
		usage, err := roomUsage(state)
		if err != nil {
			return nil, err
		}
		var out []byte
		for _, item := range self.RoomItems {
			if item.Type == 28 {
				v := wire.AppendVarint(nil, 1, item.InvenIndex)
				v = wire.AppendVarint(v, 2, item.ID)
				v = wire.AppendVarint(v, 5, usage[[2]uint64{2, item.InvenIndex}])
				out = wire.AppendBytes(out, 1, v)
			}
		}
		return out, nil
	}
	if err := f.editRooms(&state, self, path, p); err != nil {
		return fail(err.Error())
	}
	return nil, save(tx, "friend.rooms", numberKey(self.OwnerIndex), state)
}

func roomUsage(state personalRooms) (map[[2]uint64]uint64, error) {
	used := map[[2]uint64]uint64{}
	for _, room := range state.Rooms {
		for _, position := range room.Positions {
			index, _, err := wire.Varint(position, 1)
			if err != nil {
				return nil, err
			}
			typ, _, err := wire.Varint(position, 2)
			if err != nil {
				return nil, err
			}
			used[[2]uint64{typ, index}]++
		}
	}
	return used, nil
}
