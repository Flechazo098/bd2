package social

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
)

func clearApplications(tx Tx, account string) error {
	for after := ""; ; {
		rows, err := tx.Scan(applicationBucket, accountPrefix(account), after, 256)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var app Application
			if err = json.Unmarshal(row.Value, &app); err != nil {
				return err
			}
			if err = removeApplication(tx, app.GuildID, account); err != nil {
				return err
			}
		}
		if len(rows) < 256 {
			return nil
		}
		after = rows[len(rows)-1].Key
	}
}

func (o *Owner) Applications(ctx context.Context, guild uint64, actor, after string, limit int) ([]Application, string, error) {
	if limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []Application
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		g, err := readGuild(tx, guild)
		if err != nil {
			return err
		}
		member, ok := g.Members[actor]
		if !ok || member.Role != Admin && member.Role != SubAdmin {
			return ErrPermission
		}
		rows, err := tx.Scan(applicationGuildBucket, numberKey(guild)+":", after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var app Application
			if err = json.Unmarshal(row.Value, &app); err != nil {
				return err
			}
			out = append(out, app)
			next = row.Key
		}
		return nil
	})
	return out, next, err
}

func (o *Owner) ApplicationsSent(ctx context.Context, account, after string, limit int) ([]Application, string, error) {
	if account == "" || limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var out []Application
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, err := tx.Scan(applicationBucket, accountPrefix(account), after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var app Application
			if err = json.Unmarshal(row.Value, &app); err != nil {
				return err
			}
			out = append(out, app)
			next = row.Key
		}
		return nil
	})
	return out, next, err
}

// FinalizeDeletion is a trusted due-work operation, never a client timer result.

func deletionKey(at int64, guild uint64) string { return fmt.Sprintf("%020d:%020d", at, guild) }

func (o *Owner) DueDeletions(ctx context.Context, atMS int64, after string, limit int) ([]uint64, string, error) {
	if atMS <= 0 || limit < 1 || limit > 256 {
		return nil, "", ErrInvalid
	}
	var ids []uint64
	var next string
	err := o.store.View(ctx, func(tx Tx) error {
		rows, err := tx.Scan(deletionBucket, "", after, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Key > deletionKey(atMS, math.MaxUint64) {
				break
			}
			var id uint64
			if err := json.Unmarshal(row.Value, &id); err != nil {
				return err
			}
			ids = append(ids, id)
			next = row.Key
		}
		return nil
	})
	return ids, next, err
}

func (o *Owner) FinalizeDeletion(ctx context.Context, guild uint64, atMS int64) error {
	return o.store.Update(ctx, func(tx Tx) error {
		g, err := readGuild(tx, guild)
		if err == ErrNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if g.DeleteAtMS == 0 || atMS < g.DeleteAtMS || len(g.Members) != 1 || len(g.Reserved) != 0 || g.PendingManagement != "" {
			return ErrIneligible
		}
		for after := ""; ; {
			rows, e := tx.Scan(seasonBucket, numberKey(guild)+":", after, 256)
			if e != nil {
				return e
			}
			for _, row := range rows {
				var season Season
				if e = json.Unmarshal(row.Value, &season); e != nil {
					return e
				}
				if season.Pending > 0 {
					return ErrPending
				}
			}
			if len(rows) < 256 {
				break
			}
			after = rows[len(rows)-1].Key
		}
		for account := range g.Members {
			a, err := readAccount(tx, account)
			if err != nil {
				return err
			}
			if a.PendingOperation != "" {
				return ErrPending
			}
			if memberLocked(a, atMS) {
				return ErrSeasonLocked
			}
			if a.GuildID != guild || a.Epoch == math.MaxUint64 {
				return ErrInvalid
			}
			a.GuildID = 0
			a.Epoch++
			if a.LeaveUntilMS, err = hoursAfter(atMS, o.design.Guild.LeaveDelayHours); err != nil {
				return err
			}
			if err = save(tx, accountBucket, account, a); err != nil {
				return err
			}
		}
		if err = remove(tx, nameBucket, g.NameKey); err != nil {
			return err
		}
		for after := ""; ; {
			rows, e := tx.Scan(applicationGuildBucket, numberKey(guild)+":", after, 256)
			if e != nil {
				return e
			}
			for _, row := range rows {
				var app Application
				if e = json.Unmarshal(row.Value, &app); e != nil {
					return e
				}
				if e = removeApplication(tx, guild, app.Applicant.AccountID); e != nil {
					return e
				}
			}
			if len(rows) < 256 {
				break
			}
			after = rows[len(rows)-1].Key
		}
		if err := remove(tx, deletionBucket, deletionKey(g.DeleteAtMS, g.ID)); err != nil {
			return err
		}
		return remove(tx, guildBucket, numberKey(guild))
	})
}
