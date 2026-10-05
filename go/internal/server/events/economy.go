package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type HuntingAP interface {
	CanExchangeAP(costs, rewards []gamedata.Reward) error
	ExchangeAPOnce(identity string, costs, rewards []gamedata.Reward) error
}

type rewardResolver interface {
	Resolve([]gamedata.BattleReward) ([]gamedata.BattleReward, error)
}
type equipmentRoller interface {
	RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error)
}

type economyReceipt struct{ Digest, Definition, Bundle []byte }
type economySnapshot struct {
	Balances map[uint64]uint64         `json:"balances"`
	Receipts map[string]economyReceipt `json:"receipts"`
}

// Economy dispatches verified static rewards to their owning domains. It is
// called inside the same account transaction as the gameplay operation.
type Economy struct {
	ownedDesign  map[uint64]map[uint64]bool
	mu           sync.Mutex
	store        stateio.Store
	items        *player.Inventory
	wallet       *player.Wallet
	collection   *player.CollectionStore
	equipment    *player.EquipmentInventory
	costumes     player.CostumeDesignSource
	options      equipmentRoller
	graph        rewardResolver
	hunting      HuntingAP
	prestige     map[uint64]uint64
	apCaps       map[uint64]uint64
	resetSeconds int64
	now          func() time.Time
	initial      map[uint64]uint64
}

func NewEconomy(store stateio.Store, items *player.Inventory, wallet *player.Wallet, collection *player.CollectionStore, equipment *player.EquipmentInventory, costumes player.CostumeDesignSource, options equipmentRoller, graph rewardResolver, initial map[uint64]uint64) (*Economy, error) {
	if store == nil || items == nil || wallet == nil || collection == nil || equipment == nil || costumes == nil || options == nil || graph == nil {
		return nil, fmt.Errorf("events: invalid economy configuration")
	}
	e := &Economy{store: store, items: items, wallet: wallet, collection: collection, equipment: equipment, costumes: costumes, options: options, graph: graph, initial: map[uint64]uint64{}}
	for t, n := range initial {
		if _, ok := AdditionalCurrencyFields[t]; !ok || n > math.MaxInt32 {
			return nil, fmt.Errorf("events: invalid initial currency %d", t)
		}
		e.initial[t] = n
	}
	_, err := e.load()
	return e, err
}

// AdditionalCurrencyFields maps EElementType to its UserDBInfo field. These
// balances use a gameplay entry, leaving the frozen wallet schema unchanged.
var AdditionalCurrencyFields = map[uint64]int{15: 16, 16: 17, 18: 18, 24: 22, 30: 34, 31: 35, 32: 36, 33: 37, 43: 48, 44: 54, 60: 60}

func (e *Economy) AttachHuntingAP(h HuntingAP) { e.hunting = h }
func (e *Economy) AdditionalCurrencies() (map[int]uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	if err = e.refreshAP(&s); err != nil {
		return nil, err
	}
	out := map[int]uint64{}
	for t, f := range AdditionalCurrencyFields {
		out[f] = s.Balances[t]
	}
	return out, nil
}
func (e *Economy) load() (economySnapshot, error) {
	s := economySnapshot{Balances: map[uint64]uint64{}, Receipts: map[string]economyReceipt{}}
	for t, n := range e.initial {
		s.Balances[t] = n
	}
	b, err := e.store.Load("event_economy")
	if err != nil || b == nil {
		return s, err
	}
	if err = stateio.RequireExactJSONObject(b, "balances", "receipts"); err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Balances == nil || s.Receipts == nil {
		return s, fmt.Errorf("events: invalid saved economy")
	}
	for t, n := range s.Balances {
		if _, ok := AdditionalCurrencyFields[t]; !ok || n > math.MaxInt32 {
			return s, fmt.Errorf("events: invalid saved currency %d", t)
		}
	}
	for id, r := range s.Receipts {
		if id == "" || len(r.Digest) != sha256.Size {
			return s, fmt.Errorf("events: invalid economy receipt")
		}
	}
	return s, nil
}

func walletCurrency(t uint64) bool {
	switch t {
	case 2, 3, 4, 12, 20, 22, 68:
		return true
	}
	return false
}
func extraCurrency(t uint64) bool { _, ok := AdditionalCurrencyFields[t]; return ok }
func apCurrency(t uint64) bool    { return t == 21 || t == 23 }
func inventoryType(t uint64) bool {
	switch t {
	case 5, 7, 8, 9, 13, 14, 17, 19, 25, 26, 27, 29, 34, 45, 46, 47, 49:
		return true
	}
	return false
}

func (e *Economy) Apply(identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	d := sha256.Sum256(definition)
	e.mu.Lock()
	s, err := e.load()
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if r, ok := s.Receipts[identity]; ok {
		e.mu.Unlock()
		if !bytes.Equal(r.Definition, d[:]) {
			return nil, fmt.Errorf("events: reward identity reused with different definition")
		}
		return append([]byte(nil), r.Bundle...), nil
	}
	e.mu.Unlock()
	var selected []player.Item
	// Merge repeated definitions before selecting stacks so the same quantity
	// cannot be selected twice from a single inventory entry.
	totals := map[[2]uint64]uint64{}
	for _, r := range costs {
		k := [2]uint64{r.Type, r.ID}
		if r.Count == 0 || r.Count > math.MaxInt64 || totals[k] > math.MaxInt64-r.Count {
			return nil, fmt.Errorf("events: invalid cost")
		}
		totals[k] += r.Count
	}
	keys := make([][2]uint64, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		n := totals[k]
		if walletCurrency(k[0]) || extraCurrency(k[0]) || apCurrency(k[0]) {
			if k[1] != 0 {
				return nil, fmt.Errorf("events: currency has item id")
			}
			selected = append(selected, player.Item{Type: k[0], Count: n})
			continue
		}
		items, err := e.items.SelectMutable(k[0], k[1], n)
		if err != nil {
			return nil, err
		}
		selected = append(selected, items...)
	}
	var rs []gamedata.BattleReward
	for _, r := range rewards {
		rs = append(rs, gamedata.BattleReward(r))
	}
	return e.consumeAndGrant(identity, selected, rs, d[:], false)
}

func (e *Economy) ConsumeAndGrant(identity string, consumed []player.Item, rewards []gamedata.BattleReward) ([]byte, error) {
	return e.consumeAndGrant(identity, consumed, rewards, nil, false)
}

func (e *Economy) consumeAndGrant(identity string, consumed []player.Item, rewards []gamedata.BattleReward, definition []byte, openBox bool) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if identity == "" {
		return nil, fmt.Errorf("events: empty reward identity")
	}
	s, err := e.load()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Costs   []player.Item
		Rewards []gamedata.BattleReward
	}{consumed, rewards})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if receipt, ok := s.Receipts[identity]; ok {
		if !bytes.Equal(receipt.Digest, digest[:]) {
			return nil, fmt.Errorf("events: reward identity reused with different contents")
		}
		return append([]byte(nil), receipt.Bundle...), nil
	}
	if err = e.refreshAP(&s); err != nil {
		return nil, err
	}
	var itemCosts []player.Item
	var walletCosts, apCosts []gamedata.Reward
	for _, c := range consumed {
		if c.Count == 0 || c.Count > math.MaxInt32 {
			return nil, fmt.Errorf("events: invalid consumption count")
		}
		r := gamedata.Reward{Type: c.Type, ID: c.ID, Count: c.Count}
		switch {
		case walletCurrency(c.Type):
			if c.ID != 0 || c.InvenIndex != 0 {
				return nil, fmt.Errorf("events: invalid currency cost")
			}
			walletCosts = append(walletCosts, r)
		case apCurrency(c.Type):
			if c.ID != 0 || c.InvenIndex != 0 {
				return nil, fmt.Errorf("events: invalid hunting AP cost")
			}
			apCosts = append(apCosts, r)
		case extraCurrency(c.Type):
			if c.ID != 0 || c.InvenIndex != 0 || s.Balances[c.Type] < c.Count {
				return nil, fmt.Errorf("events: insufficient currency %d", c.Type)
			}
			s.Balances[c.Type] -= c.Count
		default:
			if !inventoryType(c.Type) {
				return nil, fmt.Errorf("events: unsupported consumed item type %d", c.Type)
			}
			itemCosts = append(itemCosts, c)
		}
	}
	if len(itemCosts) > 0 {
		if err = e.items.CanConsume(itemCosts); err != nil {
			return nil, err
		}
	}
	var expanded []gamedata.BattleReward
	if openBox {
		expanded, err = e.graph.Resolve(rewards)
	} else {
		expanded, err = e.resolveGranted(rewards)
	}
	if err != nil {
		return nil, err
	}
	var walletRewards, apRewards []gamedata.Reward
	var itemRewards []gamedata.BattleReward
	var costumes []uint64
	var equips []player.Equipment
	var bundle []byte
	for _, r := range expanded {
		if r.Count == 0 || r.Count > math.MaxInt32 {
			return nil, fmt.Errorf("events: invalid reward count")
		}
		switch {
		case walletCurrency(r.Type):
			if r.ID != 0 {
				return nil, fmt.Errorf("events: currency reward has item id")
			}
			walletRewards = append(walletRewards, gamedata.Reward(r))
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{Type: r.Type, Count: r.Count}))
		case apCurrency(r.Type):
			if r.ID != 0 {
				return nil, fmt.Errorf("events: invalid AP reward")
			}
			apRewards = append(apRewards, gamedata.Reward(r))
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{Type: r.Type, Count: r.Count}))
		case extraCurrency(r.Type):
			if r.ID != 0 || s.Balances[r.Type] > math.MaxInt32-r.Count {
				return nil, fmt.Errorf("events: currency overflow")
			}
			s.Balances[r.Type] += r.Count
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(player.Item{Type: r.Type, Count: r.Count}))
		case r.Type == 11:
			if _, ok := e.costumes.Character(r.ID); !ok || r.Count > 1000 {
				return nil, fmt.Errorf("events: unknown or excessive costume reward %d", r.ID)
			}
			for n := uint64(0); n < r.Count; n++ {
				costumes = append(costumes, r.ID)
			}
		case r.Type == 10:
			if r.Count > 1000 {
				return nil, fmt.Errorf("events: excessive equipment reward")
			}
			for n := uint64(0); n < r.Count; n++ {
				main, sub, private, rollErr := e.options.RollOptions(r.ID)
				if rollErr != nil {
					return nil, rollErr
				}
				entry := player.Equipment{ID: r.ID, Rank: []uint64{0, 0, 0}}
				for _, o := range main {
					entry.MainOption = append(entry.MainOption, player.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
				}
				for _, o := range sub {
					entry.SubOption = append(entry.SubOption, player.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
				}
				if private != nil {
					entry.PrivateOption = &player.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
				}
				equips = append(equips, entry)
			}
		case inventoryType(r.Type):
			if r.Type == 47 || r.Type == 49 {
				if !e.ownedDesign[r.Type][r.ID] {
					return nil, fmt.Errorf("events: unknown owned item design %d:%d", r.Type, r.ID)
				}
			}
			if r.Type == 45 {
				if _, ok := e.prestige[r.ID]; !ok {
					return nil, fmt.Errorf("events: unknown prestige skin %d", r.ID)
				}
			}
			if r.ID == 0 {
				return nil, fmt.Errorf("events: zero inventory reward id")
			}
			itemRewards = append(itemRewards, r)
		default:
			return nil, fmt.Errorf("events: unsupported reward type %d", r.Type)
		}
	}
	if err = e.wallet.CanExchange(walletCosts, walletRewards); err != nil {
		return nil, err
	}
	if len(apCosts)+len(apRewards) > 0 {
		if e.hunting == nil {
			return nil, fmt.Errorf("events: hunting AP runtime unavailable")
		}
		if err = e.hunting.CanExchangeAP(apCosts, apRewards); err != nil {
			return nil, err
		}
	}
	// Validation and random generation finish before the first account write.
	if len(itemCosts) > 0 {
		if err = e.items.Consume(itemCosts); err != nil {
			return nil, err
		}
	}
	if err = e.wallet.ExchangeOnce(identity+":wallet", walletCosts, walletRewards); err != nil {
		return nil, err
	}
	if len(apCosts)+len(apRewards) > 0 {
		if err = e.hunting.ExchangeAPOnce(identity+":ap", apCosts, apRewards); err != nil {
			return nil, err
		}
	}
	items, err := e.items.GrantOnce(identity+":items", itemRewards)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		bundle = wire.AppendBytes(bundle, 6, player.ItemWire(player.Item{Type: item.Type, ID: item.ID, Count: item.Count}))
	}
	if len(costumes) > 0 {
		grant, err := e.collection.GrantCostumes(identity+":costumes", costumes, e.costumes)
		if err != nil {
			return nil, err
		}
		var overflow uint64
		for _, x := range grant.Exchanges {
			if x.ExchangeItemType != 20 || x.ExchangeCount > math.MaxInt64-overflow {
				return nil, fmt.Errorf("events: invalid costume exchange")
			}
			overflow += x.ExchangeCount
		}
		if overflow > 0 {
			if _, err = e.wallet.GrantMileageOnce(identity+":overflow", overflow); err != nil {
				return nil, err
			}
		}
		bundle = append(bundle, player.CollectionRewardBundle(e.collection, grant)...)
	}
	for i, entry := range equips {
		saved, err := e.equipment.GrantGeneratedOnce(identity+":equipment:"+strconv.Itoa(i), entry)
		if err != nil {
			return nil, err
		}
		bundle = wire.AppendBytes(bundle, 4, player.EquipmentWire(saved))
		bundle = wire.AppendBytes(bundle, 6, player.ItemWire(player.Item{Type: 10, ID: saved.ID, Count: 1}))
	}
	s.Receipts[identity] = economyReceipt{Digest: digest[:], Definition: definition, Bundle: bundle}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if err = e.store.Save("event_economy", data); err != nil {
		return nil, err
	}
	return bundle, nil
}

func (e *Economy) resolveGranted(rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	if g, ok := e.graph.(interface {
		ResolveGranted([]gamedata.BattleReward) ([]gamedata.BattleReward, error)
	}); ok {
		return g.ResolveGranted(rewards)
	}
	return e.graph.Resolve(rewards)
}

func (e *Economy) OpenBox(identity string, box player.Item) ([]byte, error) {
	return e.consumeAndGrant(identity, []player.Item{box}, []gamedata.BattleReward{{Type: 9, ID: box.ID, Count: box.Count}}, nil, true)
}
