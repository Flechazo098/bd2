package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"math"

	"time"
)

type HuntingAP interface {
	CanExchangeAP(ctx command.Context, costs, rewards []gamedata.Reward) error
	ExchangeAPOnce(ctx command.Context, identity string, costs, rewards []gamedata.Reward) error
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
	ownedDesign   map[uint64]map[uint64]bool
	avatarRewards *gamedata.AvatarRewardDesign
	buffRewards   *BuffRewards

	store            stateio.Store
	items            *assets.Inventory
	wallet           *assets.Wallet
	collection       *roster.CollectionStore
	equipment        *assets.EquipmentInventory
	costumes         roster.CostumeDesignSource
	options          equipmentRoller
	graph            rewardResolver
	hunting          HuntingAP
	prestige         map[uint64]uint64
	prestigePortrait func() uint64
	apCaps           map[uint64]uint64
	resetSeconds     int64
	now              func() time.Time
	initial          map[uint64]uint64
}

func NewEconomy(ctx command.Context, store stateio.Store, items *assets.Inventory, wallet *assets.Wallet, collection *roster.CollectionStore, equipment *assets.EquipmentInventory, costumes roster.CostumeDesignSource, options equipmentRoller, graph rewardResolver, initial map[uint64]uint64) (*Economy, error) {
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
	_, err := e.load(ctx)
	return e, err
}

// AdditionalCurrencyFields maps EElementType to its UserDBInfo field. These
// balances use a gameplay entry, leaving the frozen wallet schema unchanged.
var AdditionalCurrencyFields = map[uint64]int{15: 16, 16: 17, 18: 18, 24: 22, 30: 34, 31: 35, 32: 36, 33: 37, 38: 42, 39: 44, 40: 45, 43: 48, 44: 54, 54: 61, 60: 60, 70: 71}

func (e *Economy) AttachAvatarRewards(d *gamedata.AvatarRewardDesign) { e.avatarRewards = d }

func (e *Economy) AttachHuntingAP(h HuntingAP) { e.hunting = h }
func (e *Economy) AdditionalCurrencies(ctx command.Context) (map[int]uint64, error) {

	s, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	if err = e.refreshAP(ctx, &s); err != nil {
		return nil, err
	}
	out := map[int]uint64{}
	for t, f := range AdditionalCurrencyFields {
		out[f] = s.Balances[t]
	}
	return out, nil
}
func (e *Economy) load(ctx command.Context) (economySnapshot, error) {
	s := economySnapshot{Balances: map[uint64]uint64{}, Receipts: map[string]economyReceipt{}}
	maps.Copy(s.Balances, e.initial)
	b, err := e.store.Load(ctx.State, "event_economy")
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
	case 5, 7, 8, 9, 13, 14, 17, 19, 25, 26, 27, 29, 34, 45, 46, 47, 49, 50, 61, 69:
		return true
	}
	return false
}

func (e *Economy) resolveGranted(rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	if g, ok := e.graph.(interface {
		ResolveGranted([]gamedata.BattleReward) ([]gamedata.BattleReward, error)
	}); ok {
		return g.ResolveGranted(rewards)
	}
	return e.graph.Resolve(rewards)
}
