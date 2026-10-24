package social

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"bd2server/internal/server/protocol/wire"
)

func (f *Friends) searchRooms(tx Tx, self FriendProfile, path string, p packet, now int64) ([]byte, error) {
	typ := uint64(0)
	if path == "/MyRoomSearchRecommend" {
		var err error
		typ, err = p.integer(2)
		if err != nil || typ > 1 {
			return nil, fmt.Errorf("%w: room search type %d: %v", ErrInvalid, typ, err)
		}
	}
	selfAccount, err := readAccount(tx, self.AccountID)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		info         []byte
		owner, likes uint64
	}
	var candidates []candidate
	month := time.UnixMilli(now).UTC().Format("2006-01")
	for cursor := ""; ; {
		rows, err := tx.Scan("friend.directory", "", cursor, 256)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			cursor = row.Key
			var other FriendProfile
			if err := json.Unmarshal(row.Value, &other); err != nil {
				return nil, err
			}
			if other.OwnerIndex == self.OwnerIndex {
				continue
			}
			if path == "/MyRoomSearchFriend" {
				yes, err := supporterRelation(tx, self.OwnerIndex, other.OwnerIndex)
				if err != nil {
					return nil, err
				}
				if !yes {
					continue
				}
			}
			if path == "/MyRoomSearchGuild" {
				account, err := readAccount(tx, other.AccountID)
				if err != nil {
					return nil, err
				}
				if selfAccount.GuildID == 0 || account.GuildID != selfAccount.GuildID {
					continue
				}
			}
			var state personalRooms
			found, err := load(tx, "friend.rooms", numberKey(other.OwnerIndex), &state)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			info, err := f.roomUser(tx, self, other, state, true)
			if errors.Is(err, ErrPermission) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var count roomLikes
			if _, err := load(tx, "friend.room_like_counts", numberKey(other.OwnerIndex), &count); err != nil {
				return nil, err
			}
			monthly := uint64(0)
			if count.Month == month {
				monthly = count.Monthly
			}
			if path == "/MyRoomSearchRecommend" && typ == 0 {
				if count.Total == 0 {
					continue
				}
				populated := false
				for _, room := range state.Rooms {
					if !room.Hidden && len(room.Positions) > 0 {
						populated = true
						break
					}
				}
				if !populated {
					continue
				}
			}
			candidates = append(candidates, candidate{info: info, owner: other.OwnerIndex, likes: monthly})
		}
	}
	if path == "/MyRoomSearchRecommend" && typ == 1 {
		slices.SortFunc(candidates, func(a, b candidate) int {
			if a.likes > b.likes {
				return -1
			}
			if a.likes < b.likes {
				return 1
			}
			if a.owner < b.owner {
				return -1
			}
			if a.owner > b.owner {
				return 1
			}
			return 0
		})
	} else if path == "/MyRoomSearchRecommend" {
		rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	}
	if uint64(len(candidates)) > f.rooms.ListCount {
		candidates = candidates[:f.rooms.ListCount]
	}
	var out []byte
	for _, row := range candidates {
		out = wire.AppendBytes(out, 1, row.info)
	}
	return out, nil
}
