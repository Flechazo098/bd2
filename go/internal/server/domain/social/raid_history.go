package social

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const historyBucket = "social.raid_history"

type RaidHistory struct{ Day, Level, Score uint64 }

func raidDay(s Season, day uint64) (string, error) {
	if day == 0 || day > 366 {
		return "", fmt.Errorf("%w: raid day %d, want 1..366", ErrInvalid, day)
	}
	return time.UnixMilli(s.StartsMS).UTC().AddDate(0, 0, int(day)-1).Format("2006-01-02"), nil
}
func (o *Owner) MemberHistory(ctx context.Context, guild, season uint64, account string) ([]RaidHistory, error) {
	var out []RaidHistory
	e := o.store.View(ctx, func(tx Tx) error {
		rows, e := tx.Scan(historyBucket, memberBestKey(guild, season, account)+":", "", 256)
		if e != nil {
			return e
		}
		for _, row := range rows {
			var v RaidHistory
			if e = json.Unmarshal(row.Value, &v); e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return out, e
}
