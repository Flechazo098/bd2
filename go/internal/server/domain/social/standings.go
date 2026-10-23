package social

import (
	"bd2server/internal/server/design/gamedata"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

func memberBests(tx Tx, guild, season uint64) ([]MemberBest, error) {
	var out []MemberBest
	for after := ""; ; {
		rows, e := tx.Scan(bestBucket, seasonKey(guild, season)+":", after, 256)
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			var member MemberBest
			if e = json.Unmarshal(row.Value, &member); e != nil {
				return nil, e
			}
			out = append(out, member)
		}
		if len(rows) < 256 {
			break
		}
		after = rows[len(rows)-1].Key
	}
	return out, nil
}
func sortMembers(members []MemberBest) {
	sort.Slice(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Overkill != b.Overkill {
			return a.Overkill > b.Overkill
		}
		return a.OwnerIndex < b.OwnerIndex
	})
}

// standingRows ranks guilds by score; LocalText 2001674/30061021 break ties by
// the overkill sum of the guild's overkillMembers highest-scoring members.
func standingRows(tx Tx, season, overkillMembers uint64) ([]GuildStanding, error) {
	var rows []GuildStanding
	for after := ""; ; {
		page, e := tx.Scan(seasonGuildBucket, numberKey(season)+":", after, 256)
		if e != nil {
			return nil, e
		}
		for _, row := range page {
			var guild uint64
			if e = json.Unmarshal(row.Value, &guild); e != nil {
				return nil, e
			}
			var s Season
			found, e := load(tx, seasonBucket, seasonKey(guild, season), &s)
			if e != nil {
				return nil, e
			}
			if !found {
				return nil, ErrInvalid
			}
			g, e := readGuild(tx, guild)
			if e == ErrNotFound {
				g = s.GuildView
				e = nil
			}
			if e != nil {
				return nil, e
			}
			members, e := memberBests(tx, guild, season)
			if e != nil {
				return nil, e
			}
			sortMembers(members)
			overkill := uint64(0)
			for i, m := range members {
				if uint64(i) >= overkillMembers {
					break
				}
				if overkill > math.MaxUint64-m.Overkill {
					return nil, ErrInvalid
				}
				overkill += m.Overkill
			}
			rows = append(rows, GuildStanding{Guild: g, Season: season, Score: s.TotalScore, Overkill: overkill, Members: members, AchievedMS: s.AchievedMS})
		}
		if len(page) < 256 {
			break
		}
		after = page[len(page)-1].Key
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Overkill != b.Overkill {
			return a.Overkill > b.Overkill
		}
		if a.AchievedMS != b.AchievedMS {
			return a.AchievedMS < b.AchievedMS
		}
		return a.Guild.ID < b.Guild.ID
	})
	qualified := 0
	for _, row := range rows {
		if row.Score > 0 {
			qualified++
		}
	}
	for i := range rows {
		if rows[i].Score == 0 {
			continue
		}
		rows[i].Rank = uint64(i + 1)
		rows[i].TopPercent = float64(i+1) * 100 / float64(qualified)
	}
	return rows, nil
}
func (o *Owner) Standings(ctx context.Context, season uint64) ([]GuildStanding, error) {
	var out []GuildStanding
	e := o.store.View(ctx, func(tx Tx) error {
		found, e := load(tx, boardBucket, numberKey(season), &out)
		if e != nil || found {
			return e
		}
		out, e = standingRows(tx, season, o.design.Raid.OverkillMemberCount)
		return e
	})
	return out, e
}

func (o *Owner) StatueRanking(ctx context.Context) (uint64, []GuildStanding, error) {
	var latest []byte
	err := o.store.View(ctx, func(tx Tx) error {
		for cursor := ""; ; {
			page, e := tx.Scan(boardBucket, "", cursor, 256)
			if e != nil {
				return e
			}
			if len(page) == 0 {
				return nil
			}
			latest = append(latest[:0], page[len(page)-1].Value...)
			cursor = page[len(page)-1].Key
			if len(page) < 256 {
				return nil
			}
		}
	})
	if err != nil || len(latest) == 0 {
		return 0, nil, err
	}
	var rows []GuildStanding
	if err = json.Unmarshal(latest, &rows); err != nil {
		return 0, nil, err
	}
	if len(rows) == 0 {
		return 0, nil, nil
	}
	return rows[0].Season, rows[:min(3, len(rows))], nil
}
func (o *Owner) MemberStandings(ctx context.Context, guild, season uint64) ([]MemberBest, error) {
	var out []MemberBest
	e := o.store.View(ctx, func(tx Tx) error { var e error; out, e = memberBests(tx, guild, season); return e })
	sortMembers(out)
	return out, e
}
func (o *Owner) Entitlements(ctx context.Context, account string) ([]Entitlement, error) {
	var out []Entitlement
	e := o.store.View(ctx, func(tx Tx) error {
		for after := ""; ; {
			rows, e := tx.Scan(entitlementBucket, accountPrefix(account), after, 256)
			if e != nil {
				return e
			}
			for _, row := range rows {
				var ent Entitlement
				if e = json.Unmarshal(row.Value, &ent); e != nil {
					return e
				}
				if !ent.Claimed {
					out = append(out, ent)
				}
			}
			if len(rows) < 256 {
				return nil
			}
			after = rows[len(rows)-1].Key
		}
	})
	return out, e
}

// FinalizeSeason seals each guild before publishing one immutable ranking and reward set.
func (o *Owner) FinalizeSeason(ctx context.Context, season uint64, at int64) error {
	rows, e := o.Standings(ctx, season)
	if e != nil {
		return e
	}
	for _, row := range rows {
		if e = o.BeginFreeze(ctx, row.Guild.ID, season, at); e != nil {
			return e
		}
		if _, e = o.FinishFreeze(ctx, row.Guild.ID, season, at); e != nil {
			return e
		}
	}
	return o.store.Update(ctx, func(tx Tx) error {
		var prior []GuildStanding
		if found, e := load(tx, boardBucket, numberKey(season), &prior); e != nil || found {
			return e
		}
		rows, e := standingRows(tx, season, o.design.Raid.OverkillMemberCount)
		if e != nil {
			return e
		}
		for _, row := range rows {
			var s Season
			if _, e = load(tx, seasonBucket, seasonKey(row.Guild.ID, season), &s); e != nil {
				return e
			}
			if s.State != "frozen" {
				return ErrPending
			}
			if row.Score == 0 {
				continue
			}
			for _, reward := range o.design.Raid.RankRewards {
				if row.TopPercent > reward.TopPercent {
					continue
				}
				for _, member := range row.Members {
					ent := Entitlement{ID: fmt.Sprintf("rank:%d:%d", season, row.Guild.ID), AccountID: member.AccountID, GuildID: row.Guild.ID, Season: season, Rewards: reward.Rewards}
					key := accountPrefix(member.AccountID) + ent.ID
					if e = save(tx, entitlementBucket, key, ent); e != nil {
						return e
					}
				}
				break
			}
			donors := map[string]uint64{}
			for _, v := range row.Guild.Supporters {
				donors[v.AccountID] = 0
			}
			prefix := seasonKey(row.Guild.ID, season) + ":"
			for after := ""; ; {
				page, e := tx.Scan(rentalBucket, prefix, after, 256)
				if e != nil {
					return e
				}
				for _, entry := range page {
					raw, e := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(entry.Key, prefix), ":"))
					if e != nil {
						return e
					}
					var count uint64
					if e = json.Unmarshal(entry.Value, &count); e != nil {
						return e
					}
					donors[string(raw)] = count
				}
				if len(page) < 256 {
					break
				}
				after = page[len(page)-1].Key
			}
			for account, count := range donors {
				rule := o.design.Raid
				if rule.RentalSupportReward != 0 && count > (math.MaxUint64-rule.BasicSupportReward)/rule.RentalSupportReward {
					return ErrInvalid
				}
				gold := rule.BasicSupportReward + count*rule.RentalSupportReward
				if gold == 0 {
					continue
				}
				ent := Entitlement{ID: fmt.Sprintf("support:%d:%d", season, row.Guild.ID), AccountID: account, GuildID: row.Guild.ID, Season: season, Rewards: []gamedata.Reward{{Type: 4, Count: gold}}}
				if e = save(tx, entitlementBucket, accountPrefix(account)+ent.ID, ent); e != nil {
					return e
				}
			}
		}
		return save(tx, boardBucket, numberKey(season), rows)
	})
}

func (o *Owner) RentalCount(ctx context.Context, guild, season uint64, account string) (uint64, error) {
	var count uint64
	e := o.store.View(ctx, func(tx Tx) error {
		_, e := load(tx, rentalBucket, seasonKey(guild, season)+":"+accountPrefix(account), &count)
		return e
	})
	return count, e
}
