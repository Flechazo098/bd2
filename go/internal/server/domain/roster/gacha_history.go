package roster

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

type GachaHistoryEntry struct {
	Time uint64
	Body []byte
}

// RecordGachaHistory shares the account transaction with the draw receipt.
func (s *CollectionStore) RecordGachaHistory(ctx command.Context, identity string, at uint64, body []byte, counts [4][13]uint64) error {
	if identity == "" || at == 0 || at >= math.MaxInt64 || len(body) == 0 {
		return fmt.Errorf("gacha: invalid history receipt")
	}
	if _, found, e := s.store.LoadEntry(ctx.State, collectionDomain, "gacha_history_receipts", identity); e != nil || found {
		return e
	}
	raw, found, e := s.store.LoadEntry(ctx.State, collectionDomain, "gacha_history_clock", "latest")
	if e != nil {
		return e
	}
	var latest uint64
	if found && json.Unmarshal(raw, &latest) != nil {
		return fmt.Errorf("gacha: invalid history clock")
	}
	if at <= latest {
		at = latest + 1
	}
	if at >= math.MaxInt64 {
		return fmt.Errorf("gacha: history clock overflow")
	}
	totals, e := s.gachaHistoryTotals(ctx)
	if e != nil {
		return e
	}
	for kind := range totals {
		for field := range totals[kind] {
			n := counts[kind][field]
			if n > math.MaxInt32-totals[kind][field] {
				return fmt.Errorf("gacha: total count overflow")
			}
			totals[kind][field] += n
		}
	}
	entry := GachaHistoryEntry{Time: at, Body: wire.AppendVarint(append([]byte(nil), body...), 13, at)}
	payload, e := json.Marshal(entry)
	if e != nil {
		return e
	}
	clock, e := json.Marshal(at)
	if e != nil {
		return e
	}
	summary, e := json.Marshal(totals)
	if e != nil {
		return e
	}
	key := fmt.Sprintf("%020d", at)
	return s.store.SaveWithEntries(ctx.State, collectionDomain, nil, []stateio.EntryMutation{{Bucket: "gacha_history", Key: key, Payload: payload}, {Bucket: "gacha_history_receipts", Key: identity, Payload: clock}, {Bucket: "gacha_history_clock", Key: "latest", Payload: clock}, {Bucket: "gacha_history_totals", Key: "all", Payload: summary}})
}

func (s *CollectionStore) gachaHistoryTotals(ctx command.Context) ([4][13]uint64, error) {
	var totals [4][13]uint64
	raw, found, e := s.store.LoadEntry(ctx.State, collectionDomain, "gacha_history_totals", "all")
	if e != nil {
		return totals, e
	}
	if found && json.Unmarshal(raw, &totals) != nil {
		return totals, fmt.Errorf("gacha: invalid history total")
	}
	return totals, nil
}

func (s *CollectionStore) GachaHistory(ctx command.Context, after uint64) ([]byte, error) {
	var entries []GachaHistoryEntry
	read := func(key string, raw []byte) error {
		var v GachaHistoryEntry
		if json.Unmarshal(raw, &v) != nil || v.Time == 0 || len(v.Body) == 0 || key != fmt.Sprintf("%020d", v.Time) {
			return fmt.Errorf("gacha: invalid saved history")
		}
		if v.Time > after {
			entries = append(entries, v)
		}
		return nil
	}
	if pager, ok := ctx.State.(interface {
		EntryPrefixPage(string, string, string, string, int) ([]stateio.Entry, error)
	}); ok {
		for cursor := fmt.Sprintf("%020d", after); ; {
			rows, e := pager.EntryPrefixPage(collectionDomain, "gacha_history", "", cursor, 256)
			if e != nil {
				return nil, e
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				cursor = row.Key
				if e = read(row.Key, row.Payload); e != nil {
					return nil, e
				}
			}
		}
	} else {
		rows, e := s.store.ListEntries(ctx.State, collectionDomain, "gacha_history")
		if e != nil {
			return nil, e
		}
		for key, raw := range rows {
			if e = read(key, raw); e != nil {
				return nil, e
			}
		}
	}
	slices.SortFunc(entries, func(a, b GachaHistoryEntry) int {
		if a.Time < b.Time {
			return 1
		}
		if a.Time > b.Time {
			return -1
		}
		return 0
	})
	var body []byte
	for _, entry := range entries {
		body = wire.AppendBytes(body, 1, entry.Body)
	}
	totals, e := s.gachaHistoryTotals(ctx)
	if e != nil {
		return nil, e
	}
	for kind, counts := range totals {
		v := wire.AppendVarint(nil, 1, uint64(kind))
		for i, n := range counts {
			if n != 0 {
				v = wire.AppendVarint(v, i+2, n)
			}
		}
		body = wire.AppendBytes(body, 2, v)
	}
	return body, nil
}
