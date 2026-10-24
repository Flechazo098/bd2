package social

import (
	"context"
	"fmt"
	"math"
)

type GameplayCounts struct{ BorrowCount, RentalClaims, RoomLikes uint64 }

func addGameplayCount(tx Tx, owner uint64, borrow, claims uint64) error {
	var count GameplayCounts
	if _, err := load(tx, "friend.gameplay_counts", numberKey(owner), &count); err != nil {
		return err
	}
	if borrow > math.MaxInt64-count.BorrowCount || claims > math.MaxInt64-count.RentalClaims {
		return fmt.Errorf("%w: owner %d gameplay counts overflow", ErrCapacity, owner)
	}
	count.BorrowCount += borrow
	count.RentalClaims += claims
	return save(tx, "friend.gameplay_counts", numberKey(owner), count)
}

func (f *Friends) GameplayCounts(ctx context.Context, account string, owner uint64) (GameplayCounts, error) {
	var count GameplayCounts
	err := f.store.View(ctx, func(tx Tx) error {
		var p FriendProfile
		found, err := load(tx, "friend.directory", numberKey(owner), &p)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if p.AccountID != account {
			return fmt.Errorf("%w: owner %d is not account %q", ErrPermission, owner, account)
		}
		if _, err := load(tx, "friend.gameplay_counts", numberKey(owner), &count); err != nil {
			return err
		}
		var likes roomLikes
		if _, err := load(tx, "friend.room_like_counts", numberKey(owner), &likes); err != nil {
			return err
		}
		count.RoomLikes = likes.Total
		return nil
	})
	return count, err
}
