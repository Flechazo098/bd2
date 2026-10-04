package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"
)

// RecruitNPCResolver must authorize the current pack/map/quest NPC and return
// its Scout interaction value. A client NPC ID alone grants no authority.
type RecruitNPCResolver func(uint64) (uint64, error)

// RecruitService participates in the enclosing account transaction, which
// atomically commits inventory costs, collection copies and completion/replay.
type RecruitService struct {
	mu         sync.Mutex
	design     *gamedata.RecruitDesign
	catalog    CostumeDesignSource
	collection *CollectionStore
	inventory  *Inventory
	wallet     *Wallet
	resolver   RecruitNPCResolver
	session    string
	now        func() time.Time
}

func NewRecruitService(design *gamedata.RecruitDesign, catalog CostumeDesignSource, collection *CollectionStore, inventory *Inventory, wallet *Wallet, resolver RecruitNPCResolver) (*RecruitService, error) {
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
func (s *RecruitService) BeginSession(id string) { s.mu.Lock(); defer s.mu.Unlock(); s.session = id }
func recruitIdentity(id uint64) string           { return "recruit:" + strconv.FormatUint(id, 10) }
func (s *RecruitService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/MercenaryScout" && path != "/CharScoutInfo" && path != "/CharSpecialScoutBuy" && path != "/CharSpecialScoutReset" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: invalid recruitment sequence")
	}
	if path == "/CharScoutInfo" {
		state, e := s.specialState()
		if e != nil {
			return 0, nil, true, e
		}
		if e = s.persistSpecial(state); e != nil {
			return 0, nil, true, e
		}
		return 148, s.scoutInfo(state), true, nil
	}
	if s.session == "" {
		return 0, nil, true, errors.New("player: recruitment requires authenticated session")
	}
	digestBytes := sha256.Sum256(request)
	digest := hex.EncodeToString(digestBytes[:])
	keyBytes := sha256.Sum256([]byte(s.session + ":" + path + ":" + strconv.FormatUint(seq, 10)))
	replyKey := "recruit-reply:" + hex.EncodeToString(keyBytes[:])
	if b, ok, e := s.collection.GachaBatchResponse(replyKey, digest); ok || e != nil {
		return recruitCode(path), b, true, e
	}
	var state specialRecruitState
	if path != "/MercenaryScout" {
		state, err = s.specialState()
		if err != nil {
			return 0, nil, true, err
		}
	}
	if path == "/CharSpecialScoutReset" {
		if state.Count >= s.design.ResetLimit {
			return 0, nil, true, errors.New("player: scout reset limit reached")
		}
		if !s.wallet.CanSpendFreeJewelry(s.design.ResetCount) {
			return 0, nil, true, errors.New("player: insufficient scout reset jewelry")
		}
		state, err = s.rollSpecial(state.Count + 1)
		if err != nil {
			return 0, nil, true, err
		}
		if _, err = s.wallet.SpendFreeJewelryOnce(replyKey, s.design.ResetCount); err != nil {
			return 0, nil, true, err
		}
		if err = s.persistSpecial(state); err != nil {
			return 0, nil, true, err
		}
		var body []byte
		for _, id := range state.IDs {
			body = wire.AppendVarint(body, 1, id)
		}
		body = wire.AppendVarint(body, 2, state.Next)
		if err = s.collection.RecordGachaBatch(replyKey, digest, body); err != nil {
			return 0, nil, true, err
		}
		return 150, body, true, nil
	}
	npc, found, err := wire.Varint(request, 2)
	if err != nil || !found || npc == 0 || npc > math.MaxInt32 {
		return 0, nil, true, errors.New("player: invalid recruit NPC")
	}
	id := npc
	if path == "/MercenaryScout" {
		id, err = s.resolver(npc)
		if err != nil {
			return 0, nil, true, err
		}
	}
	r, ok := s.design.Rules[id]
	if !ok {
		return 0, nil, true, errors.New("player: missing recruit rule")
	}
	if path == "/MercenaryScout" && r.Type != 0 {
		return 0, nil, true, errors.New("player: NPC is not ordinary recruit")
	}
	if path == "/CharSpecialScoutBuy" {
		available := false
		for _, v := range state.IDs {
			if v == id {
				available = true
			}
		}
		if r.Type != 1 || !available {
			return 0, nil, true, errors.New("player: special recruit is not appearing")
		}
	}
	identity := recruitIdentity(id)
	if _, ok := s.collection.Grant(identity); ok {
		return 0, nil, true, errors.New("player: character already recruited")
	}
	materials, err := equipmentRequestItems(request, 3, "MercenaryScout")
	if err != nil {
		return 0, nil, true, err
	}
	items, gold, err := validateCostumeBurstMaterials(r.Costs, materials)
	if err != nil || gold != 0 {
		return 0, nil, true, fmt.Errorf("player: invalid recruit materials: %v", err)
	}
	if err = s.inventory.CanConsume(items); err != nil {
		return 0, nil, true, err
	}
	if err = s.inventory.Consume(items); err != nil {
		return 0, nil, true, err
	}
	grant, err := s.collection.GrantCostumes(identity, []uint64{r.CostumeID}, s.catalog)
	if err != nil {
		return 0, nil, true, err
	}
	var rewards []gamedata.Reward
	for _, x := range grant.Exchanges {
		rewards = append(rewards, gamedata.Reward{Type: x.ExchangeItemType, ID: x.ExchangeItemID, Count: x.ExchangeCount})
	}
	if len(rewards) > 0 {
		if _, err = s.wallet.GrantQuestOnce(identity+":exchange", rewards); err != nil {
			return 0, nil, true, err
		}
	}
	if path == "/CharSpecialScoutBuy" {
		if err = s.persistSpecial(state); err != nil {
			return 0, nil, true, err
		}
	}
	body := wire.AppendBytes(nil, 1, recruitRewardBundle(s.collection, grant))
	if err = s.collection.RecordGachaBatch(replyKey, digest, body); err != nil {
		return 0, nil, true, err
	}
	return recruitCode(path), body, true, nil
}

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

func (s *CollectionStore) saveRecruitState(body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	next.Grants["special-recruit-state"] = CollectionGrant{GachaResponse: append([]byte(nil), body...)}
	return s.commit(next)
}
func (s *RecruitService) specialState() (specialRecruitState, error) {
	g, ok := s.collection.Grant("special-recruit-state")
	if !ok {
		return s.rollSpecial(0)
	}
	ids, e := recruitIDs(g.GachaResponse, 1)
	if e != nil {
		return specialRecruitState{}, e
	}
	count, _, e := wire.Varint(g.GachaResponse, 2)
	if e != nil {
		return specialRecruitState{}, e
	}
	next, _, e := wire.Varint(g.GachaResponse, 3)
	if e != nil {
		return specialRecruitState{}, e
	}
	dayBytes, dayOK, e := wire.Bytes(g.GachaResponse, 4)
	if e != nil || !dayOK {
		return specialRecruitState{}, errors.New("player: missing scout rotation day")
	}
	day := string(dayBytes)
	if _, e = time.Parse("2006-01-02", day); e != nil {
		return specialRecruitState{}, errors.New("player: invalid scout rotation day")
	}
	state := specialRecruitState{IDs: ids, Count: count, Next: next, Day: day}
	if e != nil {
		return state, e
	}
	seen := map[uint64]bool{}
	if next == 0 || next > math.MaxInt64 || count > s.design.ResetLimit || len(ids) > int(s.design.AppearCount) {
		return state, errors.New("player: invalid saved scout rotation")
	}
	for _, id := range ids {
		r, ok := s.design.Rules[id]
		if !ok || r.Type != 1 || r.AppearProb == 0 || seen[id] {
			return state, errors.New("player: invalid saved scout appearance")
		}
		seen[id] = true
	}
	if state.Day != s.now().UTC().Format("2006-01-02") {
		state.Count = 0
		state.Day = s.now().UTC().Format("2006-01-02")
	}
	if next <= uint64(s.now().UnixMilli()) {
		return s.rollSpecial(state.Count)
	}
	return state, nil
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
	sort.Slice(pool, func(i, j int) bool { return pool[i] < pool[j] })
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
func (s *RecruitService) persistSpecial(state specialRecruitState) error {
	var b []byte
	for _, id := range state.IDs {
		b = wire.AppendVarint(b, 1, id)
	}
	b = wire.AppendVarint(b, 2, state.Count)
	b = wire.AppendVarint(b, 3, state.Next)
	b = wire.AppendString(b, 4, state.Day)
	return s.collection.saveRecruitState(b)
}

func (s *RecruitService) scoutInfo(state specialRecruitState) []byte {
	var b []byte
	for _, id := range state.IDs {
		if _, done := s.collection.Grant(recruitIdentity(id)); !done {
			b = wire.AppendVarint(b, 1, id)
		}
	}
	b = wire.AppendVarint(b, 2, state.Count)
	b = wire.AppendVarint(b, 3, state.Next)
	var ids []uint64
	for id := range s.design.Rules {
		if _, ok := s.collection.Grant(recruitIdentity(id)); ok {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		b = wire.AppendVarint(b, 4, id)
	}
	return b
}

func recruitRewardBundle(c *CollectionStore, g CollectionGrant) []byte {
	var b []byte
	for _, idx := range g.CharacterIndices {
		if v, ok := c.CharacterByIndex(idx); ok {
			b = wire.AppendBytes(b, 2, CharacterWire(v))
			b = wire.AppendBytes(b, 6, ItemWire(Item{ID: v.ID, Type: 6, Count: 1}))
		}
	}
	for _, idx := range g.CostumeIndices {
		if v, ok := c.CostumeByIndex(idx); ok {
			b = wire.AppendBytes(b, 3, CostumeWire(v))
		}
	}
	for _, id := range g.ViewCostumeIDs {
		b = wire.AppendBytes(b, 6, ItemWire(Item{ID: id, Type: 11, Count: 1}))
	}
	for _, u := range g.Upgrades {
		v := wire.AppendVarint(nil, 1, u.InvenIndex)
		v = wire.AppendVarint(v, 2, 11)
		v = wire.AppendVarint(v, 3, u.CostumeID)
		v = wire.AppendVarint(v, 4, u.Before)
		v = wire.AppendVarint(v, 5, u.After)
		b = wire.AppendBytes(b, 9, v)
	}
	for _, x := range g.Exchanges {
		v := wire.AppendVarint(nil, 1, x.OriginalItemType)
		v = wire.AppendVarint(v, 2, x.OriginalItemID)
		v = wire.AppendVarint(v, 3, x.OriginalCount)
		v = wire.AppendVarint(v, 4, x.ExchangeItemType)
		v = wire.AppendVarint(v, 5, x.ExchangeItemID)
		v = wire.AppendVarint(v, 6, x.ExchangeCount)
		b = wire.AppendBytes(b, 8, v)
		r := wire.AppendVarint(nil, 2, x.ExchangeItemType)
		r = wire.AppendVarint(r, 3, x.ExchangeCount)
		b = wire.AppendBytes(b, 10, r)
	}
	return b
}

func recruitIDs(b []byte, n int) ([]uint64, error) {
	var ids []uint64
	err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == n {
			if f.Type != 0 {
				return errors.New("player: invalid scout state IDs")
			}
			id, _ := binary.Uvarint(f.Value)
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}
