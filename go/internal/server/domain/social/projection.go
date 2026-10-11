package social

import (
	"context"
	"encoding/json"
)

func (o *Owner) Guild(ctx context.Context, id uint64) (Guild, error) {
	var out Guild
	err := o.store.View(ctx, func(tx Tx) error {
		var err error
		out, err = readGuild(tx, id)
		if err == nil && out.PendingOperation != "" {
			return ErrNotFound
		}
		if err == nil {
			err = projectMemberProfiles(tx, &out)
		}
		return err
	})
	return out, err
}
func (o *Owner) Account(ctx context.Context, account string) (Account, error) {
	var out Account
	err := o.store.View(ctx, func(tx Tx) error { var err error; out, err = readAccount(tx, account); return err })
	return out, err
}
func (o *Owner) Reservation(ctx context.Context, id string) (Reservation, error) {
	var out Reservation
	err := o.store.View(ctx, func(tx Tx) error {
		r, found, err := readReservation(tx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out = r.Reservation
		return nil
	})
	return out, err
}
func (o *Owner) Season(ctx context.Context, guild, season uint64) (Season, error) {
	var out Season
	err := o.store.View(ctx, func(tx Tx) error {
		found, err := load(tx, seasonBucket, seasonKey(guild, season), &out)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}
func (o *Owner) MemberBest(ctx context.Context, guild, season uint64, account string) (MemberBest, error) {
	var out MemberBest
	err := o.store.View(ctx, func(tx Tx) error {
		found, err := load(tx, bestBucket, memberBestKey(guild, season, account), &out)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}

func (o *Owner) Operations(ctx context.Context, after string, limit int) ([]Reservation, string, error) {
	if limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []Reservation
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, err := tx.Scan(operationBucket, "", after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var record reservationRecord
			if err = json.Unmarshal(row.Value, &record); err != nil {
				return err
			}
			out = append(out, record.Reservation)
			next = row.Key
		}
		return nil
	})
	return out, next, err
}

func (o *Owner) PendingOperations(ctx context.Context, after string, limit int) ([]Reservation, string, error) {
	if limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []Reservation
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, err := tx.Scan(pendingBucket, "", after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			record, found, err := readReservation(tx, row.Key)
			if err != nil {
				return err
			}
			if !found || record.Reservation.Status != Prepared {
				return ErrInvalid
			}
			out = append(out, record.Reservation)
			next = row.Key
		}
		return nil
	})
	return out, next, err
}

func (o *Owner) PendingRaidEntries(ctx context.Context, after string, limit int) ([]RaidEntry, string, error) {
	if limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []RaidEntry
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, e := tx.Scan(raidPendingBucket, "", after, limit)
		if e != nil {
			return e
		}
		for _, row := range rows {
			var entry RaidEntry
			found, e := load(tx, raidEntryBucket, row.Key, &entry)
			if e != nil {
				return e
			}
			if !found || entry.Status != "confirmed" {
				return ErrInvalid
			}
			out = append(out, entry)
			next = row.Key
		}
		return nil
	})
	return out, next, err
}

func (o *Owner) Guilds(ctx context.Context, after string, limit int) ([]Guild, string, error) {
	if limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []Guild
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, err := tx.Scan(guildBucket, "", after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var guild Guild
			if err = json.Unmarshal(row.Value, &guild); err != nil {
				return err
			}
			next = row.Key
			if guild.PendingOperation == "" {
				out = append(out, guild)
			}
		}
		return nil
	})
	return out, next, err
}
