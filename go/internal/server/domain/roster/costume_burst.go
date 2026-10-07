package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"errors"
	"fmt"
	"math"
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
	design     *gamedata.CostumeBurstDesign
	collection *CollectionStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet

	replies map[string]map[uint64]costumeBurstReply
}

func NewCostumeBurstService(design *gamedata.CostumeBurstDesign, collection *CollectionStore, inventory *assets.Inventory, wallet *assets.Wallet) (*CostumeBurstService, error) {
	if design == nil || collection == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete costume burst service")
	}
	return &CostumeBurstService{
		design: design, collection: collection, inventory: inventory, wallet: wallet,
		replies: make(map[string]map[uint64]costumeBurstReply),
	}, nil
}

// BeginLogin creates the replay cache for the newly authenticated login.
func (s *CostumeBurstService) BeginLogin(ctx command.Context) {
	id := ctx.SessionID

	if id == "" {
		return
	}
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

func validateCostumeBurstMaterials(costs []gamedata.PromotionCost, materials []assets.Item) ([]assets.Item, uint64, error) {
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
	items := make([]assets.Item, 0, len(materials))
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
