package social

import (
	"bd2server/internal/server/design/gamedata"
)

func (o *Owner) prepareSupporterClaim(tx Tx, op Operation, reservation *Reservation) error {
	if op.Day == "" || len(op.UsageIDs) == 0 || uint64(len(op.UsageIDs)) > o.design.Supporters.DailyRewardClaims {
		return ErrInvalid
	}
	var published FriendProfile
	found, err := load(tx, "friend.directory", numberKey(op.Actor.OwnerIndex), &published)
	if err != nil {
		return err
	}
	if !found || published.AccountID != op.Actor.AccountID {
		return ErrPermission
	}
	var claimed uint64
	key := numberKey(op.Actor.OwnerIndex) + ":" + op.Day
	if _, err := load(tx, supporterClaimsBucket, key, &claimed); err != nil {
		return err
	}
	if claimed+uint64(len(op.UsageIDs)) > o.design.Supporters.DailyRewardClaims {
		return ErrCapacity
	}
	seen := map[uint64]bool{}
	for _, id := range op.UsageIDs {
		if id == 0 || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
		key := supporterHistoryKey(op.Actor.OwnerIndex, id)
		var usage supporterUsage
		found, err := load(tx, supporterHistoryBucket, key, &usage)
		if err != nil {
			return err
		}
		if !found || usage.Owner != op.Actor.OwnerIndex || usage.Received || !usage.RewardEligible {
			return ErrIneligible
		}
		reservation.UsageIDs = append(reservation.UsageIDs, id)
	}
	reward := o.design.Supporters.Reward
	reservation.Grants = []gamedata.Reward{{Type: reward.Type, ID: reward.ID, Count: reward.Count * uint64(len(op.UsageIDs))}}
	return nil
}

func (o *Owner) confirmSupporterClaim(tx Tx, op Operation) error {
	for _, id := range op.UsageIDs {
		key := supporterHistoryKey(op.Actor.OwnerIndex, id)
		var usage supporterUsage
		found, err := load(tx, supporterHistoryBucket, key, &usage)
		if err != nil {
			return err
		}
		if !found || usage.Received || !usage.RewardEligible {
			return ErrConflict
		}
		usage.Received = true
		if err := save(tx, supporterHistoryBucket, key, usage); err != nil {
			return err
		}
	}
	key := numberKey(op.Actor.OwnerIndex) + ":" + op.Day
	var claimed uint64
	if _, err := load(tx, supporterClaimsBucket, key, &claimed); err != nil {
		return err
	}
	if err := addGameplayCount(tx, op.Actor.OwnerIndex, 0, uint64(len(op.UsageIDs))); err != nil {
		return err
	}
	return save(tx, supporterClaimsBucket, key, claimed+uint64(len(op.UsageIDs)))
}
