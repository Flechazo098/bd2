package events

import (
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
)

type BoxService struct {
	mu      sync.Mutex
	items   *player.Inventory
	economy *Economy
	store   stateio.Store
}
type boxReceipt struct{ Request, Response []byte }

func OpenBoxes(store stateio.Store, items *player.Inventory, economy *Economy) (*BoxService, error) {
	if store == nil || items == nil || economy == nil {
		return nil, fmt.Errorf("events: invalid box runtime")
	}
	return &BoxService{store: store, items: items, economy: economy}, nil
}
func (s *BoxService) Handle(path string, req []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, req, "local")
}
func (s *BoxService) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	if path != "/UseRandomBox" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, _, e := wire.Varint(req, 1)
	index, _, e2 := wire.Varint(req, 2)
	count, _, e3 := wire.Varint(req, 3)
	if e != nil || e2 != nil || e3 != nil || seq == 0 || index == 0 || count == 0 || count > 1000 || seq > math.MaxInt32 || index > math.MaxInt64 {
		return 143, nil, true, fmt.Errorf("events: invalid box request")
	}
	key := session + ":" + strconv.FormatUint(seq, 10)
	receipts := map[string]boxReceipt{}
	raw, e := s.store.Load("eventboxes")
	if e != nil {
		return 143, nil, true, e
	}
	if raw != nil {
		if e = json.Unmarshal(raw, &receipts); e != nil {
			return 143, nil, true, e
		}
	}
	if r, ok := receipts[key]; ok {
		if !bytes.Equal(req, r.Request) {
			return 143, nil, true, fmt.Errorf("events: box sequence conflict")
		}
		return 143, r.Response, true, nil
	}
	var box player.Item
	for _, item := range s.items.All() {
		if item.InvenIndex == index {
			box = item
			break
		}
	}
	if box.Type != 9 || box.Count < count {
		return 143, nil, true, fmt.Errorf("events: box unavailable")
	}
	box.Count = count
	bundle, e := s.economy.OpenBox("usebox:"+key, box)
	if e != nil {
		return 143, nil, true, e
	}
	out := wire.AppendBytes(nil, 1, bundle)
	receipts[key] = boxReceipt{append([]byte(nil), req...), out}
	raw, e = json.Marshal(receipts)
	if e != nil {
		return 143, nil, true, e
	}
	if e = s.store.Save("eventboxes", raw); e != nil {
		return 143, nil, true, e
	}
	return 143, out, true, nil
}
