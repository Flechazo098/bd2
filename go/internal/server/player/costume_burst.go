package player

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

const costumeBurstPacketCode = 578

type costumeBurstReply struct {
	Digest string
	Code   int
	Body   []byte
}

// CostumeBurstService applies one adjacent costume-burst level from the
// authoritative GameData row. The account request transaction makes the
// wallet, inventory, and collection writes one atomic SQLite operation.
type CostumeBurstService struct {
	mu         sync.Mutex
	design     *gamedata.CostumeBurstDesign
	collection *CollectionStore
	inventory  *Inventory
	wallet     *Wallet
	sessionID  string
	replies    map[string]map[uint64]costumeBurstReply
}

func NewCostumeBurstService(design *gamedata.CostumeBurstDesign, collection *CollectionStore, inventory *Inventory, wallet *Wallet) (*CostumeBurstService, error) {
	if design == nil || collection == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete costume burst service")
	}
	return &CostumeBurstService{
		design: design, collection: collection, inventory: inventory, wallet: wallet,
		replies: make(map[string]map[uint64]costumeBurstReply),
	}, nil
}

// BeginSession scopes protobuf sequence replay without forgetting older live
// sessions. Session dispatch calls it before each authenticated request.
func (s *CostumeBurstService) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || id == s.sessionID {
		return
	}
	s.sessionID = id
	if s.replies[id] != nil {
		return
	}
	if len(s.replies) >= 1024 {
		for oldID := range s.replies {
			if oldID != id {
				delete(s.replies, oldID)
				break
			}
		}
	}
	s.replies[id] = make(map[uint64]costumeBurstReply)
}

func (s *CostumeBurstService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/CostumeBurst" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: CostumeBurst missing sequence")
	}
	costumeID, found, err := wire.Varint(request, 2)
	if err != nil || !found || costumeID == 0 || costumeID > math.MaxInt32 {
		return 0, nil, true, errors.New("player: CostumeBurst missing costume")
	}
	materials, err := equipmentRequestItems(request, 3, "CostumeBurst")
	if err != nil {
		return 0, nil, true, err
	}
	for _, material := range materials {
		if material.InvenIndex > math.MaxInt64 || material.ID > math.MaxInt32 || material.Type > math.MaxInt32 || material.Count > math.MaxInt32 ||
			material.KeepFlag > math.MaxInt32 || material.TimeValue > math.MaxInt64 || material.ExpiryTime > math.MaxInt64 ||
			material.SortID > math.MaxInt32 || material.UseCount > math.MaxInt32 {
			return 0, nil, true, errors.New("player: CostumeBurst material exceeds protocol range")
		}
	}

	digestBytes := sha256.Sum256(request)
	digest := hex.EncodeToString(digestBytes[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = "__direct__"
	}
	if s.replies[sessionID] == nil {
		s.replies[sessionID] = make(map[uint64]costumeBurstReply)
	}
	if reply, ok := s.replies[sessionID][seq]; ok {
		if reply.Digest != digest {
			return 0, nil, true, errors.New("player: CostumeBurst sequence reused with different request")
		}
		return reply.Code, append([]byte(nil), reply.Body...), true, nil
	}

	costume, found := s.collection.CostumeByID(costumeID)
	if !found {
		return 0, nil, true, fmt.Errorf("player: CostumeBurst costume %d is not owned", costumeID)
	}
	if costume.BurstLevel != 0 {
		if applied, ok := s.collection.CostumeBurstReplay(costume.InvenIndex, costume.BurstLevel); ok && applied.Digest == digest {
			reply := costumeBurstReply{Digest: digest, Code: applied.Code, Body: append([]byte(nil), applied.Body...)}
			s.replies[sessionID][seq] = reply
			return reply.Code, append([]byte(nil), reply.Body...), true, nil
		}
	}
	rule, err := s.design.UpgradeRule(costume.ID, costume.BurstLevel)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: resolve CostumeBurst rule: %w", err)
	}
	items, gold, err := validateCostumeBurstMaterials(rule.Costs, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for CostumeBurst")
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate CostumeBurst items: %w", err)
		}
	}

	identity := "costume-burst:" + strconv.FormatUint(costume.InvenIndex, 10) + ":" + strconv.FormatUint(rule.NextLevel, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume CostumeBurst gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume CostumeBurst items: %w", err)
		}
	}
	body := wire.AppendVarint(nil, 1, rule.NextLevel)
	record := CostumeBurstUpgradeRecord{CostumeID: costume.ID, Level: rule.NextLevel, Digest: digest, Code: costumeBurstPacketCode, Body: append([]byte(nil), body...)}
	if err := s.collection.ApplyCostumeBurst(costume.InvenIndex, costume.BurstLevel, rule.NextLevel, record); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist CostumeBurst: %w", err)
	}
	reply := costumeBurstReply{Digest: digest, Code: costumeBurstPacketCode, Body: append([]byte(nil), body...)}
	s.replies[sessionID][seq] = reply
	return reply.Code, append([]byte(nil), reply.Body...), true, nil
}

func validateCostumeBurstMaterials(costs []gamedata.PromotionCost, materials []Item) ([]Item, uint64, error) {
	if len(costs) == 0 || len(materials) == 0 {
		return nil, 0, errors.New("player: CostumeBurst has no material")
	}
	want := make(map[[2]uint64]uint64, len(costs))
	for _, cost := range costs {
		if cost.Count == 0 || (cost.Type == 4 && cost.ID != 0) || (cost.Type == 8 && cost.ID == 0) || (cost.Type != 4 && cost.Type != 8) {
			return nil, 0, errors.New("player: invalid CostumeBurst GameData cost")
		}
		key := [2]uint64{cost.Type, cost.ID}
		if want[key] > math.MaxUint64-cost.Count {
			return nil, 0, errors.New("player: CostumeBurst GameData cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64, len(materials))
	items := make([]Item, 0, len(materials))
	var gold uint64
	for _, material := range materials {
		if material.Type != 4 && material.Type != 8 {
			return nil, 0, fmt.Errorf("player: unsupported CostumeBurst material type %d", material.Type)
		}
		key := [2]uint64{material.Type, material.ID}
		if got[key] > math.MaxUint64-material.Count {
			return nil, 0, errors.New("player: CostumeBurst submitted material overflow")
		}
		got[key] += material.Count
		if material.Type == 4 {
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return nil, 0, errors.New("player: invalid CostumeBurst currency")
			}
			gold = material.Count
		} else {
			items = append(items, material)
		}
	}
	if len(got) != len(want) {
		return nil, 0, errors.New("player: CostumeBurst material kinds mismatch")
	}
	for key, count := range want {
		if got[key] != count {
			return nil, 0, fmt.Errorf("player: CostumeBurst material %d/%d=%d want %d", key[0], key[1], got[key], count)
		}
	}
	return items, gold, nil
}
