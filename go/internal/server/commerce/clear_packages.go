package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type ClearPackageInventory interface{ All() []player.Item }
type clearClaim struct{ Kind, GroupID, TicketID, TargetID, Level uint64 }
type clearClaimReceipt struct {
	Claim    clearClaim `json:"claim"`
	Response []byte     `json:"response"`
}
type ClearPackages struct {
	mu           sync.Mutex
	store        stateio.Store
	economy      Economy
	items        ClearPackageInventory
	design       map[clearClaim]gamedata.ClearPackageRewardDesign
	packCleared  func(uint64, uint64) bool
	towerCleared func(uint64, uint64) bool
	now          func() time.Time
}

func NewClearPackages(store stateio.Store, design *gamedata.ClearPackageCatalog, economy Economy, items ClearPackageInventory) (*ClearPackages, error) {
	if store == nil || design == nil || economy == nil || items == nil {
		return nil, fmt.Errorf("commerce: invalid clear package dependencies")
	}
	s := &ClearPackages{store: store, economy: economy, items: items, design: map[clearClaim]gamedata.ClearPackageRewardDesign{}, now: time.Now}
	for _, r := range design.Rewards {
		k := clearClaim{r.Kind, r.GroupID, r.TicketID, r.TargetID, r.Level}
		if _, ok := s.design[k]; ok {
			return nil, fmt.Errorf("commerce: duplicate clear reward")
		}
		s.design[k] = r
	}
	_, err := s.load()
	return s, err
}
func (s *ClearPackages) AttachProgress(pack, tower func(uint64, uint64) bool) {
	s.packCleared = pack
	s.towerCleared = tower
}
func (s *ClearPackages) load() (map[string]clearClaimReceipt, error) {
	v := map[string]clearClaimReceipt{}
	raw, err := s.store.Load("commerce_clear_claims")
	if err != nil || raw == nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v)
	if err == nil && v == nil {
		err = fmt.Errorf("commerce: invalid clear claim state")
	}
	if err == nil {
		for identity, receipt := range v {
			if identity != clearClaimID(receipt.Claim) || len(receipt.Response) == 0 {
				err = fmt.Errorf("commerce: invalid saved clear claim")
				break
			}
			if _, ok := s.design[receipt.Claim]; !ok {
				err = fmt.Errorf("commerce: unknown saved clear claim")
				break
			}
		}
	}
	return v, err
}
func clearClaimID(c clearClaim) string {
	return fmt.Sprintf("clear-package:%d:%d:%d:%d:%d", c.Kind, c.GroupID, c.TicketID, c.TargetID, c.Level)
}
func (s *ClearPackages) entitled(ticket uint64) bool {
	for _, item := range s.items.All() {
		if item.Type == 19 && item.ID == ticket && item.Count > 0 && (item.ExpiryTime == 0 || item.ExpiryTime > uint64(s.now().UnixMilli())) {
			return true
		}
	}
	return false
}
func (s *ClearPackages) Handle(path string, request []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, request, "")
}
func (s *ClearPackages) HandleSession(path string, request []byte, session string) (int, []byte, bool, error) {
	if path != "/ClearPackageReward" {
		return 0, nil, false, nil
	}
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 {
		return 286, nil, true, fmt.Errorf("commerce: invalid clear claim sequence")
	}
	kind, _, err := wire.Varint(request, 2)
	if err != nil || kind > 1 {
		return 286, nil, true, fmt.Errorf("commerce: invalid clear claim type")
	}
	var nested []byte
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number == 3 || f.Number == 4 {
			if int(f.Number) != int(kind)+3 || f.Type != 2 || nested != nil {
				return fmt.Errorf("commerce: invalid clear claim row")
			}
			nested = append([]byte(nil), f.Value...)
		}
		return nil
	})
	if err != nil || nested == nil {
		return 286, nil, true, fmt.Errorf("commerce: missing clear claim row")
	}
	var values [4]uint64
	for i := range values {
		values[i], _, err = wire.Varint(nested, i+1)
		if err != nil {
			return 286, nil, true, err
		}
	}
	claim := clearClaim{kind, values[0], values[1], values[2], values[3]}
	d, ok := s.design[claim]
	if !ok {
		return 286, nil, true, fmt.Errorf("commerce: unknown clear reward")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return 286, nil, true, err
	}
	identity := clearClaimID(claim)
	if receipt, ok := v[identity]; ok {
		return 286, append([]byte(nil), receipt.Response...), true, nil
	}
	if d.Type == 1 && !s.entitled(d.TicketID) {
		return 286, nil, true, fmt.Errorf("commerce: clear reward premium ticket required")
	}
	proof := s.packCleared
	if kind == 1 {
		proof = s.towerCleared
	}
	if proof == nil || !proof(d.TargetID, d.Level) {
		return 286, nil, true, fmt.Errorf("commerce: clear reward progression incomplete")
	}
	bundle, err := s.economy.Apply(identity, nil, []gamedata.Reward{{Type: 9, ID: d.RandomBoxID, Count: 1}})
	if err != nil {
		return 286, nil, true, err
	}
	response := wire.AppendBytes(nil, 1, bundle)
	v[identity] = clearClaimReceipt{Claim: claim, Response: response}
	raw, err := json.Marshal(v)
	if err == nil {
		err = s.store.Save("commerce_clear_claims", raw)
	}
	return 286, response, true, err
}

// RewardDBInfos is attached to PackInfoResponse fields 3 and 4 so reconnects
// restore claimed reward buttons from server state.
func (s *ClearPackages) RewardDBInfos() (pack, evil [][]byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := v[k].Claim
		b := wire.AppendVarint(nil, 1, c.GroupID)
		b = wire.AppendVarint(b, 2, c.TicketID)
		b = wire.AppendVarint(b, 3, c.TargetID)
		b = wire.AppendVarint(b, 4, c.Level)
		if c.Kind == 0 {
			pack = append(pack, b)
		} else {
			evil = append(evil, b)
		}
	}
	return pack, evil, nil
}
