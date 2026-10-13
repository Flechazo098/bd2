package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"

	"bd2server/internal/server/protocol/wire"
)

const supporterHistoryBucket = "friend.supporter_history"
const supporterClaimsBucket = "friend.supporter_claims"

func (f *Friends) SupporterRewardAvailable(ctx context.Context, owner uint64) (bool, error) {
	available := false
	err := f.store.View(ctx, func(tx Tx) error {
		var claims uint64
		if _, err := load(tx, supporterClaimsBucket, numberKey(owner)+":"+socialDay(f.now().UnixMilli()), &claims); err != nil {
			return err
		}
		if claims >= f.supporters.DailyRewardClaims {
			return nil
		}
		for cursor := ""; ; {
			rows, err := tx.Scan(supporterHistoryBucket, numberKey(owner)+":", cursor, 256)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				cursor = row.Key
				var v supporterUsage
				if err := json.Unmarshal(row.Value, &v); err != nil {
					return err
				}
				if !v.Received && v.RewardEligible {
					available = true
					return nil
				}
			}
		}
	})
	return available, err
}

type supporterSelection struct {
	Content uint64
	Allowed bool
}

type supporterUsage struct {
	ID, Owner, Slot, Costume, Design, BorrowType uint64
	Borrower                                     FriendProfile
	AtMS                                         int64
	Received, RewardEligible                     bool
}

func supporterHistoryKey(owner, id uint64) string { return numberKey(owner) + ":" + numberKey(id) }
func supporterUseKey(borrower, lender uint64, day string, content uint64) string {
	return numberKey(borrower) + ":" + day + ":" + numberKey(lender) + ":" + numberKey(content)
}

func supporterRelation(tx Tx, a, b uint64) (bool, error) {
	var rel FriendRelation
	_, err := load(tx, "friend.relations", friendKey(a, b), &rel)
	return rel.Accepted, err
}

func supporterDeck(tx Tx, p FriendProfile, borrower uint64, day string, content uint64) ([]byte, error) {
	friends, err := supporterRelation(tx, borrower, p.OwnerIndex)
	if err != nil {
		return nil, err
	}
	count := uint64(0)
	if _, err = load(tx, "friend.supporter_uses", supporterUseKey(borrower, p.OwnerIndex, day, content), &count); err != nil {
		return nil, err
	}
	out := wire.AppendVarint(nil, 1, p.OwnerIndex)
	out = wire.AppendString(out, 2, p.Snapshot.UserID)
	out = wire.AppendVarint(out, 3, p.Snapshot.TitleID)
	out = wire.AppendVarint(out, 4, p.Snapshot.PortraitCostumeID)
	out = wire.AppendString(out, 5, p.Snapshot.Greeting)
	if friends {
		out = wire.AppendVarint(out, 6, 1)
	}
	base, err := friendWire(tx, p, 0)
	if err != nil {
		return nil, err
	}
	guild, _, err := wire.Bytes(base, 7)
	if err != nil {
		return nil, err
	}
	out = wire.AppendBytes(out, 7, guild)
	out = wire.AppendVarint(out, 8, p.Snapshot.PortraitDesignID)
	for slot := range uint64(2) {
		if data := p.SupporterDetails[slot]; len(data) > 0 {
			row, found, err := wire.Bytes(data, 1)
			if err != nil || !found {
				return nil, ErrInvalid
			}
			out = wire.AppendBytes(out, 9, row)
		}
	}
	return wire.AppendVarint(out, 10, count), nil
}

func supporterHistoryWire(v supporterUsage) []byte {
	p := v.Borrower
	out := wire.AppendVarint(nil, 1, v.ID)
	out = wire.AppendVarint(out, 2, p.OwnerIndex)
	out = wire.AppendString(out, 3, p.Snapshot.UserID)
	out = wire.AppendVarint(out, 4, p.Snapshot.PortraitCostumeID)
	out = wire.AppendVarint(out, 5, p.Snapshot.PortraitDesignID)
	out = wire.AppendVarint(out, 6, p.Snapshot.TitleID)
	out = wire.AppendVarint(out, 7, v.Owner)
	out = wire.AppendVarint(out, 8, v.Slot)
	out = wire.AppendVarint(out, 9, v.Costume)
	out = wire.AppendVarint(out, 10, v.Design)
	out = wire.AppendVarint(out, 11, v.BorrowType)
	if v.Received || !v.RewardEligible {
		out = wire.AppendVarint(out, 12, 1)
	}
	return wire.AppendVarint(out, 13, uint64(v.AtMS))
}

func (f *Friends) supporter(tx Tx, self FriendProfile, path string, p packet, now int64, session string, content uint64) ([]byte, error) {
	day := socialDay(now)
	if path == "/SupporterStatus" {
		limit, err := p.integer(2)
		if err != nil || limit == 0 || limit > 256 {
			return nil, ErrInvalid
		}
		last, err := p.integer(3)
		if err != nil {
			return nil, err
		}
		var out []byte
		var histories []supporterUsage
		for cursor := ""; ; {
			rows, err := tx.Scan(supporterHistoryBucket, numberKey(self.OwnerIndex)+":", cursor, 256)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				cursor = row.Key
				var v supporterUsage
				if err := json.Unmarshal(row.Value, &v); err != nil {
					return nil, err
				}
				if last == 0 || v.ID < last {
					histories = append(histories, v)
				}
			}
		}
		slices.SortFunc(histories, func(a, b supporterUsage) int {
			if a.ID > b.ID {
				return -1
			}
			if a.ID < b.ID {
				return 1
			}
			return 0
		})
		if len(histories) > int(limit) {
			histories = histories[:limit]
		}
		for _, row := range histories {
			out = wire.AppendBytes(out, 1, supporterHistoryWire(row))
		}
		for _, base := range self.Snapshot.Supporters {
			b := wire.AppendVarint(nil, 1, base.ID)
			b = wire.AppendVarint(b, 2, base.Level)
			b = wire.AppendVarint(b, 3, base.DesignID)
			out = wire.AppendBytes(out, 2, b)
		}
		account, err := readAccount(tx, self.AccountID)
		if err != nil {
			return nil, err
		}
		if account.GuildID != 0 {
			guild, err := readGuild(tx, account.GuildID)
			if err != nil {
				return nil, err
			}
			for slot := range uint64(2) {
				v := guild.Supporters[accountPrefix(self.AccountID)+numberKey(slot)]
				b := wire.AppendVarint(nil, 1, v.CostumeID)
				b = wire.AppendVarint(b, 2, v.CostumeLevel)
				b = wire.AppendVarint(b, 3, v.CostumeDesignID)
				out = wire.AppendBytes(out, 3, b)
			}
		}
		var claims uint64
		if _, err := load(tx, supporterClaimsBucket, numberKey(self.OwnerIndex)+":"+day, &claims); err != nil {
			return nil, err
		}
		return wire.AppendVarint(out, 4, claims), nil
	}
	if path == "/SupporterBattleInfo" {
		mode, err := p.integer(2)
		if err != nil {
			return nil, err
		}
		allowed := (content == 0 || content == 1 || content == 5 || content == 6 || content == 1000) && (mode >= 1 && mode <= 5 || mode >= 9 && mode <= 15 || mode == 17)
		selection := supporterSelection{Content: content, Allowed: allowed}
		if err := save(tx, "friend.supporter_selection", accountPrefix(self.AccountID)+session, selection); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, nil
		}
		var out []byte
		count := uint64(0)
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
				var v FriendProfile
				if err := json.Unmarshal(row.Value, &v); err != nil {
					return nil, err
				}
				if v.OwnerIndex == self.OwnerIndex || len(v.SupporterDetails) == 0 {
					continue
				}
				friends, err := supporterRelation(tx, self.OwnerIndex, v.OwnerIndex)
				if err != nil {
					return nil, err
				}
				field := 1
				if !friends {
					if count >= f.rules.Recommended {
						continue
					}
					field = 2
					count++
				}
				deck, err := supporterDeck(tx, v, self.OwnerIndex, day, content)
				if err != nil {
					return nil, err
				}
				out = wire.AppendBytes(out, field, deck)
			}
		}
		return out, nil
	}
	var selection supporterSelection
	found, err := load(tx, "friend.supporter_selection", accountPrefix(self.AccountID)+session, &selection)
	if err != nil {
		return nil, err
	}
	if !found || !selection.Allowed || selection.Content != content {
		return nil, friendError(437, 9003, "Supporter selection is unavailable for this content")
	}
	lender, err := p.integer(2)
	if err != nil || lender == 0 || lender == self.OwnerIndex {
		return nil, ErrInvalid
	}
	slot, err := p.integer(3)
	if err != nil || slot > 1 {
		return nil, ErrInvalid
	}
	var other FriendProfile
	found, err = load(tx, "friend.directory", numberKey(lender), &other)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, friendError(437, 9003, "Player not found")
	}
	row, found, err := wire.Bytes(other.SupporterDetails[slot], 1)
	if err != nil || !found {
		return nil, friendError(437, 9003, "Supporter slot is unavailable")
	}
	bundle, found, err := wire.Bytes(row, 6)
	if err != nil || !found {
		return nil, ErrInvalid
	}
	char, err := base64.StdEncoding.DecodeString(string(bundle))
	if err != nil {
		return nil, err
	}
	friends, err := supporterRelation(tx, self.OwnerIndex, lender)
	if err != nil {
		return nil, err
	}
	limit := f.supporters.DailyRecommendedUses
	kind := uint64(0)
	if friends {
		limit = f.supporters.DailyUses
		kind = 1
	}
	useKey := supporterUseKey(self.OwnerIndex, lender, day, content)
	var count uint64
	if _, err := load(tx, "friend.supporter_uses", useKey, &count); err != nil {
		return nil, err
	}
	if count >= limit {
		return nil, friendError(437, 9003, "Daily supporter uses exhausted")
	}
	if err := save(tx, "friend.supporter_uses", useKey, count+1); err != nil {
		return nil, err
	}
	var id uint64
	if _, err := load(tx, "friend.supporter_sequence", "id", &id); err != nil {
		return nil, err
	}
	if id >= math.MaxInt64 {
		return nil, ErrCapacity
	}
	id++
	if err := save(tx, "friend.supporter_sequence", "id", id); err != nil {
		return nil, err
	}
	costume, _, err := wire.Varint(row, 3)
	if err != nil {
		return nil, err
	}
	design := uint64(0)
	if int(slot) < len(other.Snapshot.Supporters) {
		design = other.Snapshot.Supporters[slot].DesignID
	}
	usage := supporterUsage{ID: id, Owner: lender, Slot: slot, Costume: costume, Design: design, BorrowType: kind, Borrower: self, AtMS: now}
	accrualKey := numberKey(lender) + ":" + day
	var accrued uint64
	if _, err := load(tx, "friend.supporter_accruals", accrualKey, &accrued); err != nil {
		return nil, err
	}
	if accrued < f.supporters.DailyRewardClaims {
		usage.RewardEligible = true
		if err := save(tx, "friend.supporter_accruals", accrualKey, accrued+1); err != nil {
			return nil, err
		}
	}

	// Rental history retains the public identity at use time, without a copy of the player's roster.
	usage.Borrower.SupporterDetails = nil
	usage.Borrower.Snapshot.Supporters = nil
	if err := save(tx, supporterHistoryBucket, supporterHistoryKey(lender, id), usage); err != nil {
		return nil, err
	}
	if err := addGameplayCount(tx, self.OwnerIndex, 1, 0); err != nil {
		return nil, err
	}
	deck, err := supporterDeck(tx, other, self.OwnerIndex, day, content)
	if err != nil {
		return nil, err
	}
	out := wire.AppendBytes(nil, 1, deck)
	var relation FriendRelation
	if _, err := load(tx, "friend.relations", friendKey(self.OwnerIndex, lender), &relation); err != nil {
		return nil, err
	}
	if !friends && relation.From == 0 {
		out = wire.AppendVarint(out, 2, 1)
	}
	return wire.AppendBytes(out, 3, char), nil
}
