package commerce

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
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

// Both clear-info fields are ordinary proto3 messages, not a oneof. The native
// client creates an empty message for the inactive kind (CommonPacket), and
// omits scalar zero values such as ClearPackagePack and the normal pack level.
func parseClearClaim(request []byte) (clearClaim, error) {
	var scalar [2]uint64
	var rows [2][]byte
	var seen [4]bool
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number < 1 || f.Number > 4 {
			return nil
		}
		if seen[f.Number-1] {
			return fmt.Errorf("commerce: duplicate clear claim field %d", f.Number)
		}
		seen[f.Number-1] = true
		if f.Number <= 2 {
			if f.Type != 0 {
				return fmt.Errorf("commerce: invalid clear claim scalar %d", f.Number)
			}
			scalar[f.Number-1], _ = binary.Uvarint(f.Value)
			if scalar[f.Number-1] > math.MaxInt32 {
				return fmt.Errorf("commerce: clear claim scalar %d exceeds int32", f.Number)
			}
		} else {
			if f.Type != 2 {
				return fmt.Errorf("commerce: invalid clear claim row %d", f.Number)
			}
			rows[f.Number-3] = f.Value
		}
		return nil
	})
	if err != nil {
		return clearClaim{}, err
	}
	if scalar[0] == 0 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim sequence")
	}
	kind := scalar[1]
	if kind > 1 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim type")
	}
	if !seen[kind+2] {
		return clearClaim{}, fmt.Errorf("commerce: missing clear claim row")
	}
	var active [4]uint64
	for i, row := range rows {
		var values [4]uint64
		var fields [4]bool
		if err := wire.Walk(row, func(f wire.Field) error {
			if f.Number < 1 || f.Number > 4 {
				// Unknown active fields retain normal protobuf compatibility. An
				// inactive row must contain only the schema's default scalars.
				if uint64(i) != kind {
					return fmt.Errorf("commerce: nonempty inactive clear claim row")
				}
				return nil
			}
			if fields[f.Number-1] || f.Type != 0 {
				return fmt.Errorf("commerce: invalid clear claim row scalar %d", f.Number)
			}
			fields[f.Number-1] = true
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt32 {
				return fmt.Errorf("commerce: clear claim row scalar %d exceeds int32", f.Number)
			}
			if uint64(i) != kind && v != 0 {
				return fmt.Errorf("commerce: conflicting inactive clear claim row")
			}
			values[f.Number-1] = v
			return nil
		}); err != nil {
			return clearClaim{}, err
		}
		if uint64(i) == kind {
			active = values
		}
	}
	if active[0] == 0 || active[1] == 0 {
		return clearClaim{}, fmt.Errorf("commerce: invalid clear claim identity")
	}
	return clearClaim{kind, active[0], active[1], active[2], active[3]}, nil
}

func (s *ClearPackages) HandleSession(path string, request []byte, session string) (int, []byte, bool, error) {
	if path != "/ClearPackageReward" {
		return 0, nil, false, nil
	}
	claim, err := parseClearClaim(request)
	if err != nil {
		return 286, nil, true, err
	}
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
	if claim.Kind == 1 {
		proof = s.towerCleared
	}
	if proof == nil || !proof(d.TargetID, d.Level) {
		return 286, nil, true, fmt.Errorf("commerce: clear reward progression incomplete")
	}
	rewards := []gamedata.Reward{{Type: 9, ID: d.RandomBoxID, Count: 1}}
	var bundle []byte
	if delivery, ok := s.economy.(interface {
		ApplyPurchase(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	}); ok {
		// Clear reward groups carry MailId just like cash products. The
		// delivery resolver selects the versioned mail template and keeps
		// mailed contents out of the direct inventory response.
		bundle, err = delivery.ApplyPurchase(identity, nil, rewards)
	} else {
		bundle, err = s.economy.Apply(identity, nil, rewards)
	}
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
