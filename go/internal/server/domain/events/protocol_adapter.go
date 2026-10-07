package events

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

func (h SkinHandler) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {

	if code, response, handled, err := h.Economy.OwnedItemInfo(ctx, path, request); handled || err != nil {
		return code, response, handled, err
	}
	return h.Economy.Handle(ctx, path, request)
}

func (e *Economy) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	if path != "/PrestigeSkinSet" {
		return e.PrestigeSkinInfo(ctx, path, req)
	}

	if err := wire.Walk(req, func(f wire.Field) error {
		if (f.Number == 1 && f.Type != 0) || (f.Number == 2 && f.Type != 2) {
			return fmt.Errorf("events: invalid prestige request field")
		}
		return nil
	}); err != nil {
		return 426, nil, true, err
	}
	seq, _, err := wire.Varint(req, 1)
	info, found, err2 := wire.Bytes(req, 2)
	if err != nil || err2 != nil || !found || seq == 0 || seq > math.MaxInt32 || commandSession == "" {
		return 426, nil, true, fmt.Errorf("events: invalid prestige set request")
	}
	if err := wire.Walk(info, func(f wire.Field) error {
		if f.Number >= 1 && f.Number <= 4 && f.Type != 0 {
			return fmt.Errorf("events: invalid prestige info field")
		}
		return nil
	}); err != nil {
		return 426, nil, true, err
	}
	costume, _, err := wire.Varint(info, 1)
	design, _, err2 := wire.Varint(info, 2)
	setting, _, err3 := wire.Varint(info, 3)
	timeValue, _, err4 := wire.Varint(info, 4)
	if err != nil || err2 != nil || err3 != nil || err4 != nil || costume == 0 || costume > math.MaxInt32 || design == 0 || design > math.MaxInt32 || setting > 1 || timeValue > math.MaxInt64 || e.prestige[design] != costume {
		return 426, nil, true, fmt.Errorf("events: invalid prestige skin")
	}
	s, err := e.loadPrestigeSkins(ctx)
	if err != nil {
		return 426, nil, true, err
	}
	key := commandSession + ":" + strconv.FormatUint(seq, 10)
	if receipt, ok := s.Receipts[key]; ok {
		if !bytes.Equal(receipt.Request, req) {
			return 426, nil, true, fmt.Errorf("events: prestige sequence conflict")
		}
		return 426, append([]byte(nil), receipt.Response...), true, nil
	}
	owned := false
	for _, item := range e.items.All(ctx) {
		if item.Type == 45 && item.ID == design && item.Count > 0 {
			owned = true
			break
		}
	}
	if !owned {
		return 426, nil, true, fmt.Errorf("events: prestige skin not owned")
	}
	if setting == 1 {
		s.Selections[costume] = design
	} else if s.Selections[costume] == design {
		delete(s.Selections, costume)
	}
	portrait := costume
	if e.prestigePortrait != nil {
		portrait = e.prestigePortrait()
	}
	out := wire.AppendVarint(nil, 1, portrait)
	out = wire.AppendVarint(out, 2, s.Selections[portrait])
	s.Receipts[key] = prestigeSkinReceipt{Request: append([]byte(nil), req...), Response: append([]byte(nil), out...)}
	raw, err := json.Marshal(s)
	if err == nil {
		err = e.store.Save(ctx.State, "prestige_skin_sets", raw)
	}
	if err != nil {
		return 426, nil, true, err
	}
	return 426, out, true, nil
}

func (e *Economy) PrestigeSkinInfo(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if path != "/PrestigeSkinInfo" {
		return 0, nil, false, nil
	}
	seq, _, err := wire.Varint(req, 1)
	if err != nil || seq == 0 || seq > math.MaxInt32 {
		return 425, nil, true, fmt.Errorf("events: missing sequence")
	}

	s, err := e.loadPrestigeSkins(ctx)
	if err != nil {
		return 425, nil, true, err
	}
	var out []byte
	seen := map[uint64]bool{}
	for _, item := range e.items.All(ctx) {
		if item.Type != 45 || item.Count == 0 || seen[item.ID] {
			continue
		}
		costume, ok := e.prestige[item.ID]
		if !ok {
			return 425, nil, true, fmt.Errorf("events: owned skin missing design")
		}
		seen[item.ID] = true
		b := wire.AppendVarint(nil, 1, costume)
		b = wire.AppendVarint(b, 2, item.ID)
		if s.Selections[costume] == item.ID {
			b = wire.AppendVarint(b, 3, 1)
		}
		b = wire.AppendVarint(b, 4, item.TimeValue)
		out = wire.AppendBytes(out, 1, b)
	}
	return 425, out, true, nil
}

func (r *Registry) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if path != "/EventScheduleInfo" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return 163, nil, true, fmt.Errorf("events: invalid sequence")
	}
	var out []byte
	for _, s := range r.List() {
		b := wire.AppendVarint(nil, 1, s.UID)
		b = wire.AppendVarint(b, 2, s.Type)
		b = wire.AppendVarint(b, 3, s.ID)
		if s.SubID != 0 {
			b = wire.AppendVarint(b, 4, s.SubID)
		}
		b = wire.AppendVarint(b, 5, uint64(s.Start))
		b = wire.AppendVarint(b, 6, uint64(s.End))
		if now := r.now().UnixMilli(); now >= s.Start && now < s.End {
			b = wire.AppendVarint(b, 7, 1)
		}
		out = wire.AppendBytes(out, 1, b)
	}
	return 163, out, true, nil
}

// ID card items are loaded through ItemInfo; there is no IdCardInfo ownership
// endpoint. AvatarInfo separately includes its owned ItemDBInfo list.
func (e *Economy) OwnedItemInfo(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if path != "/AvatarInfo" {
		return 0, nil, false, nil
	}
	seq, _, err := wire.Varint(req, 1)
	if err != nil || seq == 0 {
		return 467, nil, true, fmt.Errorf("events: missing sequence")
	}
	var out []byte
	for _, item := range e.items.All(ctx) {
		if item.Type == 49 || item.Type == 50 || item.Type == 61 {
			out = wire.AppendBytes(out, 2, assets.ItemWire(item))
		}
	}
	return 467, out, true, nil
}

func (e *Economy) Apply(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	definition, _ := json.Marshal(struct{ Costs, Rewards []gamedata.Reward }{costs, rewards})
	d := sha256.Sum256(definition)

	s, err := e.load(ctx)
	if err != nil {

		return nil, err
	}
	if r, ok := s.Receipts[identity]; ok {

		if !bytes.Equal(r.Definition, d[:]) {
			return nil, fmt.Errorf("events: reward identity reused with different definition")
		}
		return append([]byte(nil), r.Bundle...), nil
	}

	var selected []assets.Item
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
			selected = append(selected, assets.Item{Type: k[0], Count: n})
			continue
		}
		items, err := e.items.SelectMutable(ctx, k[0], k[1], n)
		if err != nil {
			return nil, err
		}
		selected = append(selected, items...)
	}
	var rs []gamedata.BattleReward
	for _, r := range rewards {
		rs = append(rs, gamedata.BattleReward(r))
	}
	return e.consumeAndGrant(ctx, identity, selected, rs, d[:], false)
}

func (e *Economy) ConsumeAndGrant(ctx command.Context, identity string, consumed []assets.Item, rewards []gamedata.BattleReward) ([]byte, error) {
	return e.consumeAndGrant(ctx, identity, consumed, rewards, nil, false)
}

func (e *Economy) consumeAndGrant(ctx command.Context, identity string, consumed []assets.Item, rewards []gamedata.BattleReward, definition []byte, openBox bool) ([]byte, error) {

	if identity == "" {
		return nil, fmt.Errorf("events: empty reward identity")
	}
	s, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Costs   []assets.Item
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
	if err = e.refreshAP(ctx, &s); err != nil {
		return nil, err
	}
	var itemCosts []assets.Item
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
		if err = e.items.CanConsume(ctx, itemCosts); err != nil {
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
	expanded, err = e.avatarRewards.Expand(expanded)
	if err != nil {
		return nil, err
	}
	var walletRewards, apRewards, buffItems []gamedata.Reward
	var itemRewards []gamedata.BattleReward
	var costumes []uint64
	var equips []assets.Equipment
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
			bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(assets.Item{Type: r.Type, Count: r.Count}))
		case apCurrency(r.Type):
			if r.ID != 0 {
				return nil, fmt.Errorf("events: invalid AP reward")
			}
			apRewards = append(apRewards, gamedata.Reward(r))
			bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(assets.Item{Type: r.Type, Count: r.Count}))
		case extraCurrency(r.Type):
			if r.ID != 0 || s.Balances[r.Type] > math.MaxInt32-r.Count {
				return nil, fmt.Errorf("events: currency overflow")
			}
			s.Balances[r.Type] += r.Count
			bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(assets.Item{Type: r.Type, Count: r.Count}))
		case r.Type == 63:
			if e.buffRewards == nil {
				return nil, fmt.Errorf("events: buff reward runtime unavailable")
			}
			buffItems = append(buffItems, gamedata.Reward(r))
			bundle = wire.AppendBytes(bundle, 6, assets.ItemWire(assets.Item{Type: r.Type, ID: r.ID, Count: r.Count}))
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
				entry := assets.Equipment{ID: r.ID, Rank: []uint64{0, 0, 0}}
				for _, o := range main {
					entry.MainOption = append(entry.MainOption, assets.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
				}
				for _, o := range sub {
					entry.SubOption = append(entry.SubOption, assets.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
				}
				if private != nil {
					entry.PrivateOption = &assets.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
				}
				equips = append(equips, entry)
			}
		case inventoryType(r.Type):
			if r.Type == 47 || r.Type == 69 {
				if !e.ownedDesign[r.Type][r.ID] {
					return nil, fmt.Errorf("events: unknown owned item design %d:%d", r.Type, r.ID)
				}
			}
			if r.Type == 49 || r.Type == 50 || r.Type == 61 {
				valid := e.avatarRewards != nil && e.avatarRewards.Items[r.Type][r.ID]
				if !valid && !e.ownedDesign[r.Type][r.ID] {
					return nil, fmt.Errorf("events: unknown avatar member %d:%d", r.Type, r.ID)
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
	if len(buffItems) > 0 {
		if err = e.buffRewards.Validate(buffItems); err != nil {
			return nil, err
		}
	}
	if err = e.wallet.CanExchange(ctx, walletCosts, walletRewards); err != nil {
		return nil, err
	}
	if len(apCosts)+len(apRewards) > 0 {
		if e.hunting == nil {
			return nil, fmt.Errorf("events: hunting AP runtime unavailable")
		}
		if err = e.hunting.CanExchangeAP(ctx, apCosts, apRewards); err != nil {
			return nil, err
		}
	}
	// Validation and random generation finish before the first account write.
	if len(itemCosts) > 0 {
		if err = e.items.Consume(ctx, itemCosts); err != nil {
			return nil, err
		}
	}
	if err = e.wallet.ExchangeOnce(ctx, identity+":wallet", walletCosts, walletRewards); err != nil {
		return nil, err
	}
	if len(apCosts)+len(apRewards) > 0 {
		if err = e.hunting.ExchangeAPOnce(ctx, identity+":ap", apCosts, apRewards); err != nil {
			return nil, err
		}
	}
	if len(buffItems) > 0 {
		if err = e.buffRewards.GrantOnce(ctx, identity+":buff-items", buffItems); err != nil {
			return nil, err
		}
	}
	items, err := e.items.GrantOnce(ctx, identity+":items", itemRewards)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
		bundle = wire.AppendBytes(bundle, 6, assets.ItemWire(assets.Item{Type: item.Type, ID: item.ID, Count: item.Count}))
	}
	if len(costumes) > 0 {
		grant, err := e.collection.GrantCostumes(ctx, identity+":costumes", costumes, e.costumes)
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
			if _, err = e.wallet.GrantMileageOnce(ctx, identity+":overflow", overflow); err != nil {
				return nil, err
			}
		}
		bundle = append(bundle, roster.CollectionRewardBundle(e.collection, grant)...)
	}
	for i, entry := range equips {
		saved, err := e.equipment.GrantGeneratedOnce(ctx, identity+":equipment:"+strconv.Itoa(i), entry)
		if err != nil {
			return nil, err
		}
		bundle = wire.AppendBytes(bundle, 4, assets.EquipmentWire(saved))
		bundle = wire.AppendBytes(bundle, 6, assets.ItemWire(assets.Item{Type: 10, ID: saved.ID, Count: 1}))
	}
	s.Receipts[identity] = economyReceipt{Digest: digest[:], Definition: definition, Bundle: bundle}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if err = e.store.Save(ctx.State, "event_economy", data); err != nil {
		return nil, err
	}
	return bundle, nil
}

func (e *Economy) OpenBox(ctx command.Context, identity string, box assets.Item) ([]byte, error) {
	return e.consumeAndGrant(ctx, identity, []assets.Item{box}, []gamedata.BattleReward{{Type: 9, ID: box.ID, Count: box.Count}}, nil, true)
}

func (s *BoxService) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	commandSession := ctx.SessionID
	if path != "/UseRandomBox" {
		return 0, nil, false, nil
	}

	seq, _, e := wire.Varint(req, 1)
	index, _, e2 := wire.Varint(req, 2)
	count, _, e3 := wire.Varint(req, 3)
	if e != nil || e2 != nil || e3 != nil || seq == 0 || index == 0 || count == 0 || count > 1000 || seq > math.MaxInt32 || index > math.MaxInt64 {
		return 143, nil, true, fmt.Errorf("events: invalid box request")
	}
	key := commandSession + ":" + strconv.FormatUint(seq, 10)
	receipts := map[string]boxReceipt{}
	raw, e := s.store.Load(ctx.State, "eventboxes")
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
	var box assets.Item
	for _, item := range s.items.All(ctx) {
		if item.InvenIndex == index {
			box = item
			break
		}
	}
	if box.Type != 9 || box.Count < count {
		return 143, nil, true, fmt.Errorf("events: box unavailable")
	}
	box.Count = count
	bundle, e := s.economy.OpenBox(ctx, "usebox:"+key, box)
	if e != nil {
		return 143, nil, true, e
	}
	out := wire.AppendBytes(nil, 1, bundle)
	receipts[key] = boxReceipt{append([]byte(nil), req...), out}
	raw, e = json.Marshal(receipts)
	if e != nil {
		return 143, nil, true, e
	}
	if e = s.store.Save(ctx.State, "eventboxes", raw); e != nil {
		return 143, nil, true, e
	}
	return 143, out, true, nil
}

func (e *Economy) ChargeInfo(ctx command.Context) ([]byte, error) {

	s, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	if err = e.refreshAP(ctx, &s); err != nil {
		return nil, err
	}
	raw, err := e.store.Load(ctx.State, "eventapclock")
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var clock economyAPClock
	if err = json.Unmarshal(raw, &clock); err != nil {
		return nil, err
	}
	var out []byte
	for _, typ := range []uint64{30, 32} {
		if _, ok := e.apCaps[typ]; !ok {
			continue
		}
		info := wire.AppendVarint(nil, 1, uint64(clock.Next-86400000))
		item := wire.AppendVarint(nil, 3, typ)
		item = wire.AppendVarint(item, 4, s.Balances[typ])
		info = wire.AppendBytes(info, 2, item)
		out = wire.AppendBytes(out, 1, info)
	}
	return out, nil
}
