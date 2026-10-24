package social

import (
	"context"
	"fmt"
	"math"
	"time"

	"bd2server/internal/server/protocol/wire"
)

type dailyLike struct {
	Day    string
	Target uint64
}

func socialDay(stamp int64) string { return time.UnixMilli(stamp).UTC().Format("2006-01-02") }

func (f *Friends) like(tx Tx, owner, target uint64, path string, now int64) ([]byte, error) {
	var current dailyLike
	if _, err := load(tx, "friend.daily_likes", numberKey(owner), &current); err != nil {
		return nil, err
	}
	day := socialDay(now)
	if path == "/MyLikeInfo" {
		if current.Day == day {
			return wire.AppendVarint(nil, 1, current.Target), nil
		}
		return nil, nil
	}
	if target == 0 || target == owner || target > math.MaxInt64 {
		return nil, friendError(203, 9003, "Invalid like target")
	}
	var other FriendProfile
	found, err := load(tx, "friend.directory", numberKey(target), &other)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, friendError(203, 9003, "Player not found")
	}
	if current.Day == day {
		return nil, friendError(203, 9003, "Daily like already used")
	}
	var count uint64
	if _, err := load(tx, "friend.like_counts", numberKey(target), &count); err != nil {
		return nil, err
	}
	if count >= math.MaxInt32 {
		return nil, fmt.Errorf("%w: owner %d has %d likes", ErrCapacity, target, count)
	}
	if err := save(tx, "friend.like_counts", numberKey(target), count+1); err != nil {
		return nil, err
	}
	return nil, save(tx, "friend.daily_likes", numberKey(owner), dailyLike{Day: day, Target: target})
}

func (f *Friends) LikeCount(ctx context.Context, owner uint64) (uint64, error) {
	var count uint64
	err := f.store.View(ctx, func(tx Tx) error { _, err := load(tx, "friend.like_counts", numberKey(owner), &count); return err })
	return count, err
}

func (f *Friends) IsFriend(ctx context.Context, owner, target uint64) (bool, error) {
	var relation FriendRelation
	err := f.store.View(ctx, func(tx Tx) error {
		_, err := load(tx, "friend.relations", friendKey(owner, target), &relation)
		return err
	})
	return relation.Accepted, err
}
