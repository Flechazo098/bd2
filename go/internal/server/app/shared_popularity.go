package app

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/outbox"
	"bd2server/internal/server/storage/shared"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const popularityWeekMS uint64 = 7 * 24 * 60 * 60 * 1000

// Friday 00:00 KST is Thursday 15:00 UTC.
func popularityWeek(at uint64) uint64 {
	const anchor uint64 = 15 * 60 * 60 * 1000
	if at < anchor {
		return 0
	}
	return (at - anchor) / popularityWeekMS
}
func popularityContext(content, side, order, rank, stage uint64) string {
	return fmt.Sprintf("%d:%d:%02d:%010d:%010d:", content, side, order, rank, stage)
}
func popularityCount(tx *shared.Tx, bucket, key string, delta int64) error {
	raw, found, e := tx.Get(bucket, key)
	if e != nil {
		return e
	}
	var n int64
	if found && json.Unmarshal(raw, &n) != nil {
		return fmt.Errorf("invalid popularity counter")
	}
	if delta < 0 && n < -delta || delta > 0 && n > math.MaxInt64-delta {
		return fmt.Errorf("popularity counter overflow")
	}
	n += delta
	if n == 0 {
		_, e = tx.Delete(bucket, key)
		return e
	}
	raw, e = json.Marshal(n)
	if e != nil {
		return e
	}
	return tx.Put(bucket, key, raw)
}
func popularityReplace(tx *shared.Tx, identity string, next map[string]int64) error {
	raw, found, e := tx.Get("popularity_sources", identity)
	if e != nil {
		return e
	}
	old := map[string]int64{}
	if found && json.Unmarshal(raw, &old) != nil {
		return fmt.Errorf("invalid popularity source")
	}
	for key, n := range old {
		if e = popularityCount(tx, "popularity_counts", key, -n); e != nil {
			return e
		}
	}
	for key, n := range next {
		if n <= 0 {
			return fmt.Errorf("invalid popularity contribution")
		}
		if e = popularityCount(tx, "popularity_counts", key, n); e != nil {
			return e
		}
	}
	raw, e = json.Marshal(next)
	if e != nil {
		return e
	}
	return tx.Put("popularity_sources", identity, raw)
}
func scanPopularity(tx *shared.Tx, bucket, prefix string, visit func(shared.Entry) error) error {
	for after := ""; ; {
		rows, e := tx.Scan(bucket, prefix, after, 256)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			after = row.Key
			if e = visit(row); e != nil {
				return e
			}
		}
	}
}
func rotatePopularity(tx *shared.Tx, at uint64) error {
	week := popularityWeek(at)
	raw, found, e := tx.Get("popularity_clock", "week")
	if e != nil {
		return e
	}
	previous := week
	if found && json.Unmarshal(raw, &previous) != nil {
		return fmt.Errorf("invalid popularity week")
	}
	if previous > week {
		return nil
	}
	if previous < week {
		for _, live := range []string{"pvp:", "total:"} {
			if e = scanPopularity(tx, "popularity_counts", live, func(row shared.Entry) error {
				if e := tx.Put("popularity_counts", fmt.Sprintf("week:%020d:", previous)+strings.TrimPrefix(row.Key, live), row.Value); e != nil {
					return e
				}
				return nil
			}); e != nil {
				return e
			}
		}
	}
	raw, e = json.Marshal(week)
	if e != nil {
		return e
	}
	return tx.Put("popularity_clock", "week", raw)
}

func (s *sharedRuntime) refreshPopularity(ctx context.Context, now time.Time) error {
	return s.store.Update(ctx, func(tx *shared.Tx) error { return rotatePopularity(tx, uint64(now.UnixMilli())) })
}

func (s *sharedRuntime) equipmentPopularity(tx *shared.Tx, source string, r *deck.RecommendRecord) error {
	next := map[string]int64{}
	if r != nil {
		chars := map[uint64]uint64{}
		for _, c := range r.Characters {
			if v, ok := s.statistics.Characters[c.ID]; ok {
				chars[c.InvenIndex] = v.UniqueID
			}
		}
		seen := map[string]bool{}
		for _, v := range r.Equipment {
			unique := chars[v.UseChar]
			design, ok := s.statistics.Equipment[v.ID]
			if !ok || unique == 0 || design.UniqueID == 0 {
				continue
			}
			key := fmt.Sprintf("equip:%010d:%d:%010d", unique, design.Slot, design.UniqueID)
			if !seen[key] {
				next[key]++
				seen[key] = true
			}
		}
	}
	return popularityReplace(tx, "equipment:"+source, next)
}

func decodePopularityEquipment(raw []byte, r *deck.RecommendRecord) error {
	return wire.Walk(raw, func(f wire.Field) error {
		switch f.Number {
		case 2:
			var c roster.Character
			var e error
			c.InvenIndex, _, e = wire.Varint(f.Value, 1)
			if e != nil {
				return e
			}
			c.ID, _, e = wire.Varint(f.Value, 2)
			r.Characters = append(r.Characters, c)
			return e
		case 4:
			var v assets.Equipment
			var e error
			v.UseChar, _, e = wire.Varint(f.Value, 2)
			if e != nil {
				return e
			}
			base, _, e := wire.Bytes(f.Value, 5)
			if e != nil {
				return e
			}
			v.ID, _, e = wire.Varint(base, 1)
			r.Equipment = append(r.Equipment, v)
			return e
		}
		return nil
	})
}

func (s *sharedRuntime) totalWarPopularity(tx *shared.Tx, source string, p deck.PopularityPublication) error {
	if p.OwnerIndex == 0 || p.Score > math.MaxInt64 || len(p.TotalWarCostumes) > 100 {
		return fmt.Errorf("invalid total-war popularity")
	}
	for _, id := range p.TotalWarCostumes {
		if _, ok := s.statistics.CostumeGrade[id]; !ok {
			return fmt.Errorf("unknown total-war popularity costume")
		}
	}
	ownerKey := fmt.Sprintf("%020d", p.OwnerIndex)
	owner := recommendationSource(source)
	raw, found, e := tx.Get("popularity_total_owners", ownerKey)
	if e != nil {
		return e
	}
	if found && string(raw) != owner {
		return fmt.Errorf("total-war popularity owner conflict")
	}
	if e = tx.Put("popularity_total_owners", ownerKey, []byte(owner)); e != nil {
		return e
	}
	raw, found, e = tx.Get("popularity_total", owner)
	if e != nil {
		return e
	}
	if found {
		var old deck.PopularityPublication
		if json.Unmarshal(raw, &old) != nil {
			return fmt.Errorf("invalid total-war popularity score")
		}
		if old.Score >= p.Score {
			return nil
		}
	}
	raw, e = json.Marshal(p)
	if e != nil {
		return e
	}
	if e = tx.Put("popularity_total", owner, raw); e != nil {
		return e
	}
	var records []deck.PopularityPublication
	if e = scanPopularity(tx, "popularity_total", "", func(row shared.Entry) error {
		var p deck.PopularityPublication
		if json.Unmarshal(row.Value, &p) != nil {
			return fmt.Errorf("invalid total-war popularity snapshot")
		}
		records = append(records, p)
		return nil
	}); e != nil {
		return e
	}
	slices.SortFunc(records, func(a, b deck.PopularityPublication) int {
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return 0
	})
	next := map[string]int64{}
	rank := 0
	for i, r := range records {
		if i == 0 || records[i-1].Score != r.Score {
			rank = i + 1
		}
		if uint64(rank)*100 > uint64(len(records))*10 {
			continue
		}
		for _, id := range r.TotalWarCostumes {
			next["total:"+popularityContext(3, 0, 0, 0, 0)+fmt.Sprintf("%010d", id)]++
		}
	}
	return popularityReplace(tx, "total-war-ranking", next)
}
func (s *sharedRuntime) consumePopularity(ctx context.Context, event outbox.Pending) error {
	var p deck.PopularityPublication
	if json.Unmarshal(event.Envelope.Event.Payload, &p) != nil || p.CompletedAt == 0 {
		return fmt.Errorf("invalid popularity battle")
	}
	if len(p.TotalWarCostumes) == 0 {
		if e := p.Record.Validate(); e != nil {
			return e
		}
	}
	if s.statistics == nil {
		return fmt.Errorf("missing popularity design")
	}
	return s.store.Consume(ctx, event, func(tx *shared.Tx, envelope outbox.Envelope) error {
		if e := rotatePopularity(tx, p.CompletedAt); e != nil {
			return e
		}
		if len(p.TotalWarCostumes) != 0 {
			return s.totalWarPopularity(tx, envelope.Source, p)
		}
		mode := p.Record.Context.BattleMode
		content := uint64(0)
		switch mode {
		case 7:
			content = 1
		case 16:
			content = 2
		case 19:
			content = 4
		case 20:
			content = 5
		default:
			return nil
		}
		stage := s.statistics.CastleStages[mode][p.Record.Context.BattleIndex]
		if stage == 0 {
			return fmt.Errorf("unknown castle popularity stage")
		}
		day := p.CompletedAt / (24 * 60 * 60 * 1000)
		receipt := fmt.Sprintf("%s:%d:%d:%d", recommendationSource(envelope.Source), mode, p.Record.Context.BattleIndex, day)
		if _, found, e := tx.Get("popularity_castle_days", receipt); e != nil || found {
			return e
		}
		active := map[uint64]bool{}
		for _, c := range p.Record.Characters {
			active[c.UseCostume] = true
		}
		for _, c := range p.Record.Costumes {
			if !active[c.InvenIndex] {
				continue
			}
			for _, filter := range []uint64{0, stage} {
				key := fmt.Sprintf("week:%020d:", popularityWeek(p.CompletedAt)) + popularityContext(content, 0, 0, 0, filter) + fmt.Sprintf("%010d", c.ID)
				if e := popularityCount(tx, "popularity_counts", key, 1); e != nil {
					return e
				}
				if stage == 0 {
					break
				}
			}
		}
		return tx.Put("popularity_castle_days", receipt, []byte("1"))
	})
}

func popularityRequest(raw []byte, max int) (map[int]uint64, error) {
	values := map[int]uint64{}
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number < 1 || f.Number > max {
			return nil
		}
		if f.Type != 0 {
			return fmt.Errorf("invalid popularity request field")
		}
		if _, found := values[f.Number]; found {
			return fmt.Errorf("duplicate popularity request field")
		}
		v, _, e := wire.Varint(raw, f.Number)
		if e != nil || v > math.MaxInt32 {
			return fmt.Errorf("invalid popularity request value")
		}
		values[f.Number] = v
		return nil
	})
	if e != nil {
		return nil, e
	}
	if values[1] == 0 {
		return nil, fmt.Errorf("missing popularity sequence")
	}
	return values, nil
}
func (s *sharedRuntime) popularInfo(ctx command.Context, path string, raw []byte, now time.Time) (int, []byte, error) {
	code := 269
	max := 6
	if path == "/PopularEquipInfo" {
		code = 0
		max = 2
	}
	v, e := popularityRequest(raw, max)
	if e != nil {
		return code, nil, e
	}
	if s.statistics == nil {
		return code, nil, fmt.Errorf("missing popularity design")
	}
	if path == "/PopularCostumeInfo" && (v[2] > 5 || v[3] > 1 || v[4] > 4) {
		return code, nil, fmt.Errorf("invalid popularity filter")
	}
	if path == "/PopularEquipInfo" {
		known := false
		for _, c := range s.statistics.Characters {
			known = known || c.UniqueID == v[2]
		}
		if !known {
			return code, nil, fmt.Errorf("unknown popularity character")
		}
	}
	cancel := ctx.Cancellation
	if cancel == nil {
		cancel = context.Background()
	}
	var body []byte
	e = s.store.View(cancel, func(tx *shared.Tx) error {
		if path == "/PopularEquipInfo" {
			prefix := fmt.Sprintf("equip:%010d:", v[2])
			type winner struct{ id, count uint64 }
			winners := map[uint64]winner{}
			if e := scanPopularity(tx, "popularity_counts", prefix, func(row shared.Entry) error {
				var count uint64
				if json.Unmarshal(row.Value, &count) != nil {
					return fmt.Errorf("invalid equipment popularity")
				}
				parts := strings.Split(strings.TrimPrefix(row.Key, prefix), ":")
				if len(parts) != 2 {
					return fmt.Errorf("invalid equipment popularity key")
				}
				slot, e := strconv.ParseUint(parts[0], 10, 64)
				if e != nil {
					return e
				}
				id, e := strconv.ParseUint(parts[1], 10, 64)
				if e != nil {
					return e
				}
				prior := winners[slot]
				if count > prior.count || count == prior.count && (prior.id == 0 || id < prior.id) {
					winners[slot] = winner{id, count}
				}
				return nil
			}); e != nil {
				return e
			}
			for slot := range uint64(5) {
				winner, found := winners[slot]
				if !found {
					continue
				}
				b := wire.AppendVarint(nil, 1, v[2])
				b = wire.AppendVarint(b, 2, slot)
				b = wire.AppendVarint(b, 3, winner.id)
				b = wire.AppendVarint(b, 4, winner.count)
				body = wire.AppendBytes(body, 1, b)
			}
			return nil
		}
		counts := map[uint64][3]uint64{}
		week := popularityWeek(uint64(now.UnixMilli()))
		for i := uint64(0); i < 3 && i <= week; i++ {
			period := week - i
			if v[2] == 0 || v[2] == 3 {
				if period == 0 {
					continue
				}
				period--
			}
			prefix := fmt.Sprintf("week:%020d:", period)
			prefix += popularityContext(v[2], v[3], v[4], v[5], v[6])
			if e := scanPopularity(tx, "popularity_counts", prefix, func(row shared.Entry) error {
				id, e := strconv.ParseUint(strings.TrimPrefix(row.Key, prefix), 10, 64)
				if e != nil {
					return e
				}
				var n uint64
				if json.Unmarshal(row.Value, &n) != nil {
					return fmt.Errorf("invalid costume popularity")
				}
				a := counts[id]
				a[i] = n
				counts[id] = a
				return nil
			}); e != nil {
				return e
			}
		}
		ids := make([]uint64, 0, len(counts))
		for id := range counts {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			b := wire.AppendVarint(nil, 1, id)
			for i, n := range counts[id] {
				if n != 0 {
					b = wire.AppendVarint(b, i+2, n)
				}
			}
			body = wire.AppendBytes(body, 1, b)
		}
		return nil
	})
	return code, body, e
}
