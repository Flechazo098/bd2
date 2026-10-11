package social

import (
	"context"
	"fmt"
	"math"
	"slices"
)

func (o *Owner) OpenSeason(ctx context.Context, season Season) error {
	if season.GuildID == 0 || season.ID == 0 || season.StartsMS <= 0 || season.EndsMS <= season.StartsMS || season.RulesVersion == "" || len(o.design.Raid.Bosses[season.BossGroupID]) == 0 {
		return ErrInvalid
	}
	return o.store.Update(ctx, func(tx Tx) error {
		g, err := readGuild(tx, season.GuildID)
		if err != nil {
			return err
		}
		if _, found, err := tx.Get(boardBucket, numberKey(season.ID)); err != nil {
			return err
		} else if found {
			return ErrSeasonLocked
		}
		var old Season
		found, err := load(tx, seasonBucket, seasonKey(season.GuildID, season.ID), &old)
		if err != nil {
			return err
		}
		if found {
			if old.StartsMS != season.StartsMS || old.EndsMS != season.EndsMS || old.BossGroupID != season.BossGroupID || old.RulesVersion != season.RulesVersion {
				return ErrConflict
			}
			return nil
		}
		season.State = "active"
		season.Pending = 0
		season.TotalScore = 0
		season.FrozenMS = 0
		season.FrozenMembers = nil
		season.GuildView = g
		if err = save(tx, seasonGuildBucket, numberKey(season.ID)+":"+numberKey(season.GuildID), season.GuildID); err != nil {
			return err
		}
		return save(tx, seasonBucket, seasonKey(season.GuildID, season.ID), season)
	})
}

// SettleRaid accepts validated battle results from a committed player outbox.
// The entry fixes the guild and epoch; current membership cannot redirect it.
func (o *Owner) SettleRaid(ctx context.Context, result RaidResult) (MemberBest, error) {
	if result.ResultID == "" || len(result.ResultID) > 256 || result.EntryID == "" || result.AccountID == "" {
		return MemberBest{}, ErrInvalid
	}
	var out MemberBest
	err := o.store.Update(ctx, func(tx Tx) error {
		var seenEntry string
		if found, err := load(tx, resultIdentityBucket, result.ResultID, &seenEntry); err != nil {
			return err
		} else if found && seenEntry != result.EntryID {
			return ErrConflict
		}
		var previous RaidResult
		found, err := load(tx, resultBucket, result.EntryID, &previous)
		if err != nil {
			return err
		}
		if found {
			if previous != result {
				return ErrConflict
			}
			_, err = load(tx, bestBucket, memberBestKey(result.GuildID, result.Season, result.AccountID), &out)
			return err
		}
		var entry RaidEntry
		found, err = load(tx, raidEntryBucket, result.EntryID, &entry)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if entry.Status != "confirmed" || entry.AccountID != result.AccountID || entry.GuildID != result.GuildID || entry.Season != result.Season || entry.MembershipEpoch != result.MembershipEpoch || entry.Digest != result.Digest {
			return ErrConflict
		}
		var season Season
		found, err = load(tx, seasonBucket, seasonKey(result.GuildID, result.Season), &season)
		if err != nil {
			return err
		}
		if !found || season.State != "active" && season.State != "freezing" || season.Pending == 0 || season.RulesVersion != result.RulesVersion {
			return ErrIneligible
		}
		if result.Level == 0 {
			if result.Score != 0 || result.Overkill != 0 {
				return ErrInvalid
			}
		} else if _, ok := o.design.Raid.Bosses[season.BossGroupID][result.Level]; !ok {
			return ErrInvalid
		}
		key := memberBestKey(result.GuildID, result.Season, result.AccountID)
		_, err = load(tx, bestBucket, key, &out)
		if err != nil {
			return err
		}
		if out.AccountID == "" {
			if entry.Participant.AccountID != result.AccountID || entry.Participant.OwnerIndex == 0 {
				return ErrIneligible
			}
			out = MemberBest{GuildID: result.GuildID, Season: season.ID, AccountID: result.AccountID, OwnerIndex: entry.Participant.OwnerIndex, Name: entry.Participant.Name, TitleID: entry.Participant.TitleID, PortraitID: entry.Participant.PortraitID, PortraitDesignID: entry.Participant.PortraitDesignID}
		}
		if result.Score > out.Score || result.Score == out.Score && result.Overkill > out.Overkill {
			season.AchievedMS = result.AtMS
			if season.AchievedMS == 0 {
				season.AchievedMS = entry.AtMS
			}
		}
		if result.Score > out.Score {
			delta := result.Score - out.Score
			if season.TotalScore > math.MaxUint64-delta {
				return ErrInvalid
			}
			season.TotalScore += delta
			out.Score = result.Score
			out.Overkill = result.Overkill
			out.ResultID = result.ResultID
		} else if result.Score == out.Score && result.Overkill > out.Overkill {
			out.Overkill = result.Overkill
			out.ResultID = result.ResultID
		}
		if result.Level > out.Level {
			out.Level = result.Level
		}
		season.Pending--
		if entry.UseSupporter && result.Level == 0 {
			if err = o.reserveSupporter(tx, Operation{Actor: entry.Participant, Day: entry.Day}, entry.GuildID, -1); err != nil {
				return err
			}
		}
		if result.Level == 0 && out.Score == 0 {
			a, e := readAccount(tx, result.AccountID)
			if e != nil {
				return e
			}
			delete(a.SeasonLocks, seasonKey(result.GuildID, result.Season))
			if e = save(tx, accountBucket, result.AccountID, a); e != nil {
				return e
			}
		}
		if result.Level > 0 {
			seen := map[string]bool{}
			for _, supporter := range entry.Supporters {
				if seen[supporter.AccountID] {
					continue
				}
				seen[supporter.AccountID] = true
				key := seasonKey(result.GuildID, result.Season) + ":" + accountPrefix(supporter.AccountID)
				var count uint64
				if _, e := load(tx, rentalBucket, key, &count); e != nil {
					return e
				}
				if count == math.MaxUint64 {
					return ErrInvalid
				}
				if e := save(tx, rentalBucket, key, count+1); e != nil {
					return e
				}
			}
		}
		entry.Status = "settled"
		day := uint64(entry.AtMS/86400000 - season.StartsMS/86400000 + 1)
		historyKey := memberBestKey(result.GuildID, result.Season, result.AccountID) + ":" + numberKey(day)
		var history RaidHistory
		if _, e := load(tx, historyBucket, historyKey, &history); e != nil {
			return e
		}
		history.Day = day
		if result.Level > history.Level {
			history.Level = result.Level
		}
		if result.Score > history.Score {
			history.Score = result.Score
		}
		if e := save(tx, historyBucket, historyKey, history); e != nil {
			return e
		}
		members, e := memberBests(tx, result.GuildID, result.Season)
		if e != nil {
			return e
		}
		if out.Score > 0 {
			members = append(members, out)
		}
		for _, reward := range o.design.Raid.ScoreRewards {
			if season.TotalScore < reward.Score {
				continue
			}
			for _, member := range members {
				ent := Entitlement{ID: fmt.Sprintf("score:%d:%d:%d", result.GuildID, result.Season, reward.ID), AccountID: member.AccountID, GuildID: result.GuildID, Season: result.Season, Rewards: reward.Rewards}
				key := accountPrefix(ent.AccountID) + ent.ID
				if _, found, e := tx.Get(entitlementBucket, key); e != nil {
					return e
				} else if !found {
					if e = save(tx, entitlementBucket, key, ent); e != nil {
						return e
					}
				}
			}
		}
		if err = save(tx, seasonBucket, seasonKey(result.GuildID, result.Season), season); err != nil {
			return err
		}
		if out.Score > 0 {
			if err = save(tx, bestBucket, key, out); err != nil {
				return err
			}
		}
		if g, e := readGuild(tx, result.GuildID); e == nil {
			season.GuildView = g
			if e = save(tx, seasonBucket, seasonKey(result.GuildID, result.Season), season); e != nil {
				return e
			}
		} else if e != ErrNotFound {
			return e
		}
		if err = save(tx, raidEntryBucket, result.EntryID, entry); err != nil {
			return err
		}
		if err = remove(tx, raidPendingBucket, result.EntryID); err != nil {
			return err
		}
		if err = save(tx, resultIdentityBucket, result.ResultID, result.EntryID); err != nil {
			return err
		}
		return save(tx, resultBucket, result.EntryID, result)
	})
	return out, err
}

// BeginFreeze seals new entries while previously accepted settlements drain.
func (o *Owner) BeginFreeze(ctx context.Context, guild, season uint64, atMS int64) error {
	return o.store.Update(ctx, func(tx Tx) error {
		var s Season
		found, err := load(tx, seasonBucket, seasonKey(guild, season), &s)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if atMS < s.EndsMS {
			return ErrIneligible
		}
		if s.State == "frozen" || s.State == "freezing" {
			return nil
		}
		if s.State != "active" {
			return ErrInvalid
		}
		s.State = "freezing"
		return save(tx, seasonBucket, seasonKey(guild, season), s)
	})
}

func (o *Owner) FinishFreeze(ctx context.Context, guild, season uint64, atMS int64) (Season, error) {
	var out Season
	err := o.store.Update(ctx, func(tx Tx) error {
		found, err := load(tx, seasonBucket, seasonKey(guild, season), &out)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if out.State == "frozen" {
			return nil
		}
		if out.State != "freezing" || atMS < out.EndsMS {
			return ErrIneligible
		}
		if out.Pending != 0 {
			return ErrPending
		}
		members, err := memberBests(tx, guild, season)
		if err != nil {
			return err
		}
		var total uint64
		for _, best := range members {
			if best.GuildID != guild || best.Season != season || total > math.MaxUint64-best.Score {
				return ErrInvalid
			}
			total += best.Score
			out.FrozenMembers = append(out.FrozenMembers, best)
		}
		if total != out.TotalScore {
			return ErrInvalid
		}
		slices.SortFunc(out.FrozenMembers, func(a, b MemberBest) int {
			if a.Score > b.Score {
				return -1
			}
			if a.Score < b.Score {
				return 1
			}
			if a.Overkill > b.Overkill {
				return -1
			}
			if a.Overkill < b.Overkill {
				return 1
			}
			if a.OwnerIndex < b.OwnerIndex {
				return -1
			}
			if a.OwnerIndex > b.OwnerIndex {
				return 1
			}
			return 0
		})
		out.State = "frozen"
		out.FrozenMS = atMS
		return save(tx, seasonBucket, seasonKey(guild, season), out)
	})
	return out, err
}
