package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"

	"time"
)

// RecruitNPCResolver must authorize the current pack/map/quest NPC and return
// its Scout interaction value. A client NPC ID alone grants no authority.
type RecruitNPCResolver func(command.Context, uint64) (uint64, error)

// RecruitService participates in the enclosing account transaction, which
// atomically commits inventory costs, collection copies and completion/replay.
type RecruitService struct {
	design     *gamedata.RecruitDesign
	catalog    CostumeDesignSource
	collection *CollectionStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet
	resolver   RecruitNPCResolver

	now func() time.Time
}

func NewRecruitService(design *gamedata.RecruitDesign, catalog CostumeDesignSource, collection *CollectionStore, inventory *assets.Inventory, wallet *assets.Wallet, resolver RecruitNPCResolver) (*RecruitService, error) {
	if design == nil || catalog == nil || collection == nil || inventory == nil || wallet == nil || resolver == nil {
		return nil, errors.New("player: incomplete recruitment service")
	}
	for _, r := range design.Rules {
		if _, ok := catalog.Character(r.CostumeID); !ok {
			return nil, fmt.Errorf("player: recruit costume %d has no design", r.CostumeID)
		}
	}
	return &RecruitService{design: design, catalog: catalog, collection: collection, inventory: inventory, wallet: wallet, resolver: resolver, now: time.Now}, nil
}

func recruitIdentity(id uint64) string { return "recruit:" + strconv.FormatUint(id, 10) }

func recruitCode(path string) int {
	if path == "/CharSpecialScoutBuy" {
		return 149
	}
	if path == "/CharSpecialScoutReset" {
		return 150
	}
	return 13
}

type specialRecruitState struct {
	IDs         []uint64
	Count, Next uint64
	Day         string
}

func (s *CollectionStore) saveRecruitState(ctx command.Context, body []byte) error {

	next := cloneCollection(s.data)
	next.Grants["special-recruit-state"] = CollectionGrant{GachaResponse: append([]byte(nil), body...)}
	return s.commit(ctx, next)
}

// The local rotation policy uses GameData weights without replacement. Its
// distribution is a server policy; official initial/random state is unknown.
func (s *RecruitService) rollSpecial(count uint64) (specialRecruitState, error) {
	state := specialRecruitState{Day: s.now().UTC().Format("2006-01-02"), Count: count, Next: uint64(s.now().Add(time.Duration(s.design.AutoResetMinute) * time.Minute).UnixMilli())}
	var pool []uint64
	for id, r := range s.design.Rules {
		if r.Type == 1 && r.AppearProb > 0 {
			if _, done := s.collection.Grant(recruitIdentity(id)); !done {
				pool = append(pool, id)
			}
		}
	}
	slices.Sort(pool)
	for len(state.IDs) < int(s.design.AppearCount) && len(pool) > 0 {
		var total uint64
		for _, id := range pool {
			if total > math.MaxUint64-s.design.Rules[id].AppearProb {
				return state, errors.New("player: scout appearance weight overflow")
			}
			total += s.design.Rules[id].AppearProb
		}
		draw, e := rand.Int(rand.Reader, new(big.Int).SetUint64(total))
		if e != nil {
			return state, e
		}
		n := draw.Uint64()
		for i, id := range pool {
			w := s.design.Rules[id].AppearProb
			if n < w {
				state.IDs = append(state.IDs, id)
				pool = append(pool[:i], pool[i+1:]...)
				break
			}
			n -= w
		}
	}
	return state, nil
}
