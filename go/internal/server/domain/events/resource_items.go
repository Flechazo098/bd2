package events

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
)

func (s *BoxService) useResource(ctx command.Context, req []byte) ([]byte, error) {
	if s.resources == nil || ctx.SessionID == "" {
		return nil, fmt.Errorf("events: resource item service unavailable")
	}
	var values [4]uint64
	seen := map[int]bool{}
	if err := wire.Walk(req, func(f wire.Field) error {
		if f.Number > 4 {
			return nil
		}
		if f.Type != 0 || seen[f.Number] {
			return fmt.Errorf("events: invalid resource request field")
		}
		seen[f.Number] = true
		values[f.Number-1], _ = binary.Uvarint(f.Value)
		return nil
	}); err != nil {
		return nil, err
	}
	seq, index, count, choice := values[0], values[1], values[2], values[3]
	if seq == 0 || seq > math.MaxInt32 || index == 0 || index > math.MaxInt64 || count == 0 || count > math.MaxInt32 || choice == 0 || choice > math.MaxInt32 {
		return nil, fmt.Errorf("events: invalid resource request")
	}
	key := "resource:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
	receipts := map[string]boxReceipt{}
	raw, err := s.store.Load(ctx.State, "eventboxes")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &receipts); err != nil {
			return nil, err
		}
		if receipts == nil {
			return nil, fmt.Errorf("events: invalid resource receipts")
		}
	}
	if r, ok := receipts[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return nil, fmt.Errorf("events: resource sequence conflict")
		}
		return bytes.Clone(r.Response), nil
	}
	var item assets.Item
	for _, candidate := range s.items.All(ctx) {
		if candidate.InvenIndex == index {
			item = candidate
			break
		}
	}
	if item.Type != 8 || item.Count < count {
		return nil, fmt.Errorf("events: resource item unavailable")
	}
	rewards, err := s.resources.Select(item.ID, choice, count)
	if err != nil {
		return nil, err
	}
	item.Count = count
	bundle, err := s.economy.ConsumeAndGrant(ctx, "use:"+key, []assets.Item{item}, rewards)
	if err != nil {
		return nil, err
	}
	out := wire.AppendBytes(nil, 1, bundle)
	receipts[key] = boxReceipt{bytes.Clone(req), bytes.Clone(out)}
	raw, err = json.Marshal(receipts)
	if err != nil {
		return nil, err
	}
	if err := s.store.Save(ctx.State, "eventboxes", raw); err != nil {
		return nil, err
	}
	return out, nil
}
