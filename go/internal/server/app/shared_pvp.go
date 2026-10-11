package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/storage/shared"
	"context"
)

type rankingStore struct{ store *shared.Store }
type rankingTx struct{ *shared.Tx }

func (s rankingStore) View(ctx context.Context, read func(ranking.Tx) error) error {
	return s.store.View(ctx, func(tx *shared.Tx) error { return read(rankingTx{tx}) })
}
func (s rankingStore) Update(ctx context.Context, write func(ranking.Tx) error) error {
	return s.store.Update(ctx, func(tx *shared.Tx) error { return write(rankingTx{tx}) })
}
func (t rankingTx) Scan(bucket, prefix, after string, limit int) ([]ranking.Entry, error) {
	entries, err := t.Tx.Scan(bucket, prefix, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ranking.Entry, len(entries))
	for i, row := range entries {
		out[i] = ranking.Entry{Key: row.Key, Value: row.Value}
	}
	return out, nil
}
func newSharedPVP(ctx context.Context, store *shared.Store, design *gamedata.PvpDesign, seasons []pvp.Season) (*ranking.PVP, error) {
	return ranking.NewPVP(ctx, rankingStore{store}, design, seasons)
}
