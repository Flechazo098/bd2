package social

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

var friendCodes = map[string]int{"/FriendInfoList": 204, "/FriendSend": 205, "/FriendAccept": 206, "/FriendRefuse": 207, "/FriendRemove": 208, "/FriendSendList": 209, "/FriendReceiveList": 210, "/FriendRecommend": 211, "/FriendSearch": 212, "/FriendSendRemove": 213, "/LikeUser": 203, "/MyLikeInfo": 218, "/SupporterBattleInfo": 439, "/SupporterBorrow": 437, "/SupporterStatus": 438, "/MyRoomUserInfo": 228, "/MyRoomSearchFriend": 230, "/MyRoomSearchRecommend": 231, "/MyRoomSearchGuild": 424, "/MyRoomLike": 420, "/MyRoomShareOptionUpdate": 645, "/MyRoomSave": 229, "/MyRoomNameChange": 234, "/MyRoomPrimaryChange": 235, "/MyRoomMove": 419, "/MyRoomItemInfo": 232, "/MyRoomPresetInfo": 641, "/MyRoomPresetSave": 642, "/MyRoomPresetNameChange": 643, "/MyRoomPresetDelete": 644}

func OwnsFriend(path string) bool { _, found := friendCodes[path]; return found }
func FriendMutates(path string) bool {
	return roomMutates(path) || path == "/SupporterBattleInfo" || path == "/LikeUser" || path == "/SupporterBorrow" || path == "/FriendSend" || path == "/FriendAccept" || path == "/FriendRefuse" || path == "/FriendRemove" || path == "/FriendSendRemove"
}

type FriendProfile struct {
	AccountID        string
	OwnerIndex       uint64
	Version          uint64
	LastLoginMS      int64
	Snapshot         profile.Snapshot
	RoomIDs          []uint64
	RoomItems        []inventory.Item
	RoomCostumes     []roster.Costume
	SupporterDetails map[uint64][]byte
}

type FriendRelation struct {
	A, B, From uint64
	CreatedMS  int64
	Accepted   bool
}
type friendReceipt struct {
	Digest   string
	Code     int
	Response []byte
}
type Friends struct {
	store      Store
	rules      gamedata.FriendRules
	now        func() time.Time
	rooms      *gamedata.RoomDesign
	supporters gamedata.SupporterRules
	online     func(string) bool
}

func (f *Friends) AttachPresence(online func(string) bool) { f.online = online }

func NewFriends(store Store, rules gamedata.FriendRules, supporters gamedata.SupporterRules, rooms *gamedata.RoomDesign, now func() time.Time) (*Friends, error) {
	if store == nil || rules.Friends == 0 || rules.Sent == 0 || rules.Received == 0 || rules.Recommended == 0 || rules.ExpireDays == 0 {
		return nil, fmt.Errorf("%w: friend rules %+v or store missing", ErrInvalid, rules)
	}
	if now == nil {
		now = time.Now
	}
	return &Friends{store: store, rules: rules, supporters: supporters, rooms: rooms, now: now}, nil
}

func (f *Friends) Publish(ctx context.Context, p FriendProfile) error {
	if p.OwnerIndex == 0 || p.OwnerIndex > math.MaxInt64 || p.AccountID == "" || p.Snapshot.OwnerIndex != p.OwnerIndex || p.Version == 0 {
		return fmt.Errorf("%w: friend profile owner %d account %q snapshot owner %d version %d", ErrInvalid, p.OwnerIndex, p.AccountID, p.Snapshot.OwnerIndex, p.Version)
	}
	return f.store.Update(ctx, func(tx Tx) error {
		var old FriendProfile
		found, err := load(tx, "friend.directory", numberKey(p.OwnerIndex), &old)
		if err != nil {
			return err
		}
		if found && old.AccountID != p.AccountID {
			return fmt.Errorf("%w: owner %d belongs to account %q, not %q", ErrConflict, p.OwnerIndex, old.AccountID, p.AccountID)
		}
		if found && old.Version >= p.Version {
			return nil
		}
		if f.rooms != nil {
			if err := f.ensureRooms(tx, p); err != nil {
				return err
			}
		}
		return save(tx, "friend.directory", numberKey(p.OwnerIndex), p)
	})
}
func (f *Friends) Profile(ctx context.Context, owner uint64) (FriendProfile, error) {
	var out FriendProfile
	err := f.store.View(ctx, func(tx Tx) error {
		found, err := load(tx, "friend.directory", numberKey(owner), &out)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: friend profile for owner %d", ErrNotFound, owner)
		}
		if _, err = load(tx, "friend.like_counts", numberKey(owner), &out.Snapshot.LikeCount); err != nil {
			return err
		}
		row, err := f.friendWire(tx, out, 0)
		if err != nil {
			return err
		}
		out.Snapshot.GuildBase, _, err = wire.Bytes(row, 7)
		return err
	})
	return out, err
}
func friendKey(a, b uint64) string {
	if a > b {
		a, b = b, a
	}
	return numberKey(a) + ":" + numberKey(b)
}
func friendEdges(tx Tx, owner uint64, now int64, days uint64) ([]FriendRelation, error) {
	var out []FriendRelation
	for cursor := ""; ; {
		rows, err := tx.Scan("friend.edges", numberKey(owner)+":", cursor, 256)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursor = row.Key
			var rel FriendRelation
			found, err := load(tx, "friend.relations", string(row.Value), &rel)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("%w: friend relation %q is indexed but missing", ErrConflict, row.Value)
			}
			if !rel.Accepted && now-rel.CreatedMS >= int64(days)*86400000 {
				continue
			}
			out = append(out, rel)
		}
	}
	return out, nil
}
func otherFriend(rel FriendRelation, owner uint64) uint64 {
	if rel.A == owner {
		return rel.B
	}
	return rel.A
}
func friendCounts(rows []FriendRelation, owner uint64) (accepted, sent, received uint64) {
	for _, rel := range rows {
		if rel.Accepted {
			accepted++
		} else if rel.From == owner {
			sent++
		} else {
			received++
		}
	}
	return
}
func putFriend(tx Tx, rel FriendRelation) error {
	key := friendKey(rel.A, rel.B)
	if err := save(tx, "friend.relations", key, rel); err != nil {
		return err
	}
	for _, id := range []uint64{rel.A, rel.B} {
		if err := tx.Put("friend.edges", numberKey(id)+":"+key, []byte(key)); err != nil {
			return err
		}
	}
	return nil
}
func deleteFriend(tx Tx, a, b uint64) error {
	key := friendKey(a, b)
	for _, id := range []uint64{a, b} {
		if err := remove(tx, "friend.edges", numberKey(id)+":"+key); err != nil {
			return err
		}
	}
	return remove(tx, "friend.relations", key)
}
func (f *Friends) friendWire(tx Tx, p FriendProfile, date int64) ([]byte, error) {
	out := wire.AppendVarint(nil, 1, p.OwnerIndex)
	out = wire.AppendVarint(out, 2, p.Snapshot.PortraitCostumeID)
	out = wire.AppendVarint(out, 3, p.Snapshot.PortraitDesignID)
	out = wire.AppendString(out, 4, p.Snapshot.UserID)
	out = wire.AppendVarint(out, 5, p.Snapshot.TitleID)
	out = wire.AppendVarint(out, 6, uint64(date))
	account, err := readAccount(tx, p.AccountID)
	if err != nil {
		return nil, err
	}
	if account.GuildID != 0 {
		guild, err := readGuild(tx, account.GuildID)
		if err != nil {
			return nil, err
		}
		base := wire.AppendVarint(nil, 1, guild.ID)
		base = wire.AppendString(base, 2, guild.Name)
		base = wire.AppendVarint(base, 3, guild.Icon)
		base = wire.AppendString(base, 4, guild.IconColor)
		out = wire.AppendBytes(out, 7, base)
	}
	lastLogin := p.LastLoginMS
	if f.online != nil && f.online(p.AccountID) {
		lastLogin = f.now().Add(90 * time.Second).UnixMilli()
	}
	out = wire.AppendVarint(out, 8, uint64(lastLogin))
	for _, costume := range p.Snapshot.Supporters {
		b := wire.AppendVarint(nil, 1, costume.ID)
		b = wire.AppendVarint(b, 2, costume.Level)
		b = wire.AppendVarint(b, 3, costume.DesignID)
		out = wire.AppendBytes(out, 9, b)
	}
	return out, nil
}
func friendError(code, kind int, message string) error {
	return &command.GameError{PacketCode: code, ErrorType: kind, Message: message}
}

func (f *Friends) Execute(ctx context.Context, identity command.Identity, owner uint64, path string, raw []byte, contentType uint64) (int, []byte, error) {
	code, known := friendCodes[path]
	if !known {
		return 0, nil, fmt.Errorf("%w: unknown friend path %s", ErrInvalid, path)
	}
	p, err := parsePacket(raw)
	if err != nil {
		return code, nil, err
	}
	seq, err := p.integer(1)
	if err != nil || seq == 0 || seq > math.MaxInt32 {
		return code, nil, fmt.Errorf("%w: %s sequence %d: %v", ErrInvalid, path, seq, err)
	}
	var target uint64
	if !roomOwns(path) {
		target, err = p.integer(2)
		if err != nil {
			return code, nil, err
		}
	}
	if path == "/FriendInfoList" && target > 1 {
		return code, nil, fmt.Errorf("%w: FriendInfoList type %d, want 0 or 1", ErrInvalid, target)
	}
	key, err := json.Marshal(identity)
	if err != nil {
		return code, nil, err
	}
	receiptKey := sha256.Sum256(key)
	id := hex.EncodeToString(receiptKey[:])
	_, digest := requestIdentity(identity, path, seq, raw)
	if identity.AccountID == "" || identity.SessionID == "" || identity.RequestID == "" || owner == 0 {
		return code, nil, fmt.Errorf("%w: %s identity %+v for owner %d is incomplete", ErrInvalid, path, identity, owner)
	}
	var response []byte
	apply := func(tx Tx) error {
		var self FriendProfile
		found, err := load(tx, "friend.directory", numberKey(owner), &self)
		if err != nil {
			return err
		}
		if !found || self.AccountID != identity.AccountID {
			return fmt.Errorf("%w: owner %d is not account %q (present=%t)", ErrPermission, owner, identity.AccountID, found)
		}
		if FriendMutates(path) {
			var receipt friendReceipt
			found, err := load(tx, "friend.receipts", id, &receipt)
			if err != nil {
				return err
			}
			if found {
				if receipt.Digest != digest {
					return fmt.Errorf("%w: %s sequence %d was replayed with a different body", ErrConflict, path, seq)
				}
				response = receipt.Response
				return nil
			}
		}
		now := f.now().UnixMilli()
		if roomOwns(path) {
			response, err = f.room(tx, self, path, p, now)
			if err != nil {
				return err
			}
			if FriendMutates(path) {
				return save(tx, "friend.receipts", id, friendReceipt{Digest: digest, Code: code, Response: response})
			}
			return nil
		}

		if path == "/LikeUser" || path == "/MyLikeInfo" {
			response, err = f.like(tx, owner, target, path, now)
			if err != nil {
				return err
			}
			if FriendMutates(path) {
				return save(tx, "friend.receipts", id, friendReceipt{Digest: digest, Code: code, Response: response})
			}
			return nil
		}
		if path == "/SupporterBattleInfo" || path == "/SupporterBorrow" || path == "/SupporterStatus" {
			response, err = f.supporter(tx, self, path, p, now, identity.SessionID, contentType)
			if err != nil {
				return err
			}
			if FriendMutates(path) {
				return save(tx, "friend.receipts", id, friendReceipt{Digest: digest, Code: code, Response: response})
			}
			return nil
		}
		edges, err := friendEdges(tx, owner, now, f.rules.ExpireDays)
		if err != nil {
			return err
		}
		if path == "/FriendInfoList" || path == "/FriendSendList" || path == "/FriendReceiveList" {
			for _, rel := range edges {
				field := 1
				if !rel.Accepted {
					if rel.From == owner {
						field = 3
					} else {
						field = 2
					}
				}
				if path == "/FriendInfoList" && target == 0 && field != 1 {
					continue
				}
				if path == "/FriendSendList" && field != 3 || path == "/FriendReceiveList" && field != 2 {
					continue
				}
				if path != "/FriendInfoList" {
					field = 1
				}
				var profile FriendProfile
				found, err := load(tx, "friend.directory", numberKey(otherFriend(rel, owner)), &profile)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("%w: friend profile for owner %d", ErrNotFound, otherFriend(rel, owner))
				}
				row, err := f.friendWire(tx, profile, rel.CreatedMS)
				if err != nil {
					return err
				}
				response = wire.AppendBytes(response, field, row)
			}
			return nil
		}
		if path == "/FriendRecommend" {
			excluded := map[uint64]bool{owner: true}
			for _, rel := range edges {
				excluded[otherFriend(rel, owner)] = true
			}
			count := uint64(0)
			for cursor := ""; count < f.rules.Recommended; {
				rows, err := tx.Scan("friend.directory", "", cursor, 256)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					break
				}
				for _, row := range rows {
					cursor = row.Key
					var profile FriendProfile
					if err := json.Unmarshal(row.Value, &profile); err != nil {
						return err
					}
					if excluded[profile.OwnerIndex] {
						continue
					}
					friend, err := f.friendWire(tx, profile, 0)
					if err != nil {
						return err
					}
					response = wire.AppendBytes(response, 1, friend)
					count++
					if count == f.rules.Recommended {
						break
					}
				}
			}
			return nil
		}
		if target == 0 || target == owner {
			return friendError(code, 1, "Invalid friend target")
		}
		var other FriendProfile
		found, err = load(tx, "friend.directory", numberKey(target), &other)
		if err != nil {
			return err
		}
		if !found {
			return friendError(code, 1, "Player not found")
		}
		if path == "/FriendSearch" {
			row, err := f.friendWire(tx, other, 0)
			if err != nil {
				return err
			}
			response = wire.AppendBytes(nil, 1, row)
			return nil
		}
		var relation FriendRelation
		found, err = load(tx, "friend.relations", friendKey(owner, target), &relation)
		if err != nil {
			return err
		}
		if found && !relation.Accepted && now-relation.CreatedMS >= int64(f.rules.ExpireDays)*86400000 {
			found = false
		}
		switch path {
		case "/FriendSend":
			if found {
				if relation.Accepted {
					return friendError(code, 5, "Already friends")
				}
				if relation.From != owner {
					return friendError(code, 6, "Incoming friend request already exists")
				}
				break
			}
			friends, sent, _ := friendCounts(edges, owner)
			otherEdges, err := friendEdges(tx, target, now, f.rules.ExpireDays)
			if err != nil {
				return err
			}
			otherFriends, _, received := friendCounts(otherEdges, target)
			if friends >= f.rules.Friends {
				return friendError(code, 1, "Your friend list is full")
			}
			if otherFriends >= f.rules.Friends {
				return friendError(code, 2, "Other player's friend list is full")
			}
			if sent >= f.rules.Sent {
				return friendError(code, 3, "Your outgoing friend requests are full")
			}
			if received >= f.rules.Received {
				return friendError(code, 4, "Other player's incoming friend requests are full")
			}
			a, b := owner, target
			if a > b {
				a, b = b, a
			}
			if err := putFriend(tx, FriendRelation{A: a, B: b, From: owner, CreatedMS: now}); err != nil {
				return err
			}
		case "/FriendAccept":
			if found && relation.Accepted {
				return friendError(code, 1, "Already friends")
			}
			if !found || !relation.Accepted && relation.From != target {
				return friendError(code, 2, "Friend request no longer exists")
			}
			if !relation.Accepted {
				friends, _, _ := friendCounts(edges, owner)
				otherEdges, err := friendEdges(tx, target, now, f.rules.ExpireDays)
				if err != nil {
					return err
				}
				otherFriends, _, _ := friendCounts(otherEdges, target)
				if friends >= f.rules.Friends {
					return friendError(code, 3, "Your friend list is full")
				}
				if otherFriends >= f.rules.Friends {
					return friendError(code, 4, "Other player's friend list is full")
				}
				relation.Accepted = true
				relation.CreatedMS = now
				if err := putFriend(tx, relation); err != nil {
					return err
				}
			}
			row, err := f.friendWire(tx, other, relation.CreatedMS)
			if err != nil {
				return err
			}
			response = wire.AppendBytes(nil, 1, row)
		case "/FriendRefuse":
			if found && (!relation.Accepted && relation.From == target) {
				if err := deleteFriend(tx, owner, target); err != nil {
					return err
				}
			} else if found {
				return friendError(code, 1, "No incoming friend request")
			}
		case "/FriendSendRemove":
			if found && (!relation.Accepted && relation.From == owner) {
				if err := deleteFriend(tx, owner, target); err != nil {
					return err
				}
			} else if found {
				return friendError(code, 1, "No outgoing friend request")
			}
		case "/FriendRemove":
			if found && relation.Accepted {
				if err := deleteFriend(tx, owner, target); err != nil {
					return err
				}
			} else if found {
				return friendError(code, 1, "Player is not a friend")
			}
		default:
			return fmt.Errorf("%w: unknown friend path %s", ErrInvalid, path)
		}
		return save(tx, "friend.receipts", id, friendReceipt{Digest: digest, Code: code, Response: response})
	}
	if FriendMutates(path) {
		err = f.store.Update(ctx, apply)
	} else {
		err = f.store.View(ctx, apply)
	}
	return code, response, err
}
