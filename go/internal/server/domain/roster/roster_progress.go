package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

type RosterProgressService struct {
	design         *gamedata.RosterProgressDesign
	potential      *gamedata.CostumePotentialDesign
	characters     *CharacterStore
	collection     *CollectionStore
	inventory      *assets.Inventory
	wallet         *assets.Wallet
	expiry         func(command.Context, uint64) ([]byte, error)
	purchase       func(command.Context, string, gamedata.CashProductKey) error
	now            func() time.Time
	AllRounderCode int
}

func NewRosterProgressService(d *gamedata.RosterProgressDesign, p *gamedata.CostumePotentialDesign, c *CharacterStore, col *CollectionStore, i *assets.Inventory, w *assets.Wallet) (*RosterProgressService, error) {
	if d == nil || p == nil || c == nil || col == nil || i == nil || w == nil {
		return nil, fmt.Errorf("player: incomplete roster progression runtime")
	}
	s := &RosterProgressService{design: d, potential: p, characters: c, collection: col, inventory: i, wallet: w, now: time.Now}
	for key, v := range col.data.CharPartners {
		if key != partnerKey(v.MainUniqueID, v.SubUniqueID) || v.Point > d.PartnerMaximum || v.Reward > 1 || !s.partnerExists(v.MainUniqueID, v.SubUniqueID) {
			return nil, fmt.Errorf("player: invalid partner ledger %s", key)
		}
	}
	for key, ids := range col.data.CostumePotentialLocks {
		index, e := strconv.ParseUint(key, 10, 64)
		costume, ok := col.CostumeByIndex(index)
		if e != nil || !ok || key != strconv.FormatUint(index, 10) {
			return nil, fmt.Errorf("player: invalid costume potential locks")
		}
		seen := map[uint64]bool{}
		for _, id := range ids {
			node, ok := p.Nodes[costume.ID][id]
			if !ok || (node.NodeType < 3 || node.NodeType > 7) || seen[id] || !slices.Contains(costume.PotentialIDs, id) {
				return nil, fmt.Errorf("player: invalid saved skill potential lock")
			}
			seen[id] = true
		}
	}
	return s, nil
}
func (s *RosterProgressService) AttachExpiry(f func(command.Context, uint64) ([]byte, error)) {
	s.expiry = f
}
func (s *RosterProgressService) AttachPurchase(f func(command.Context, string, gamedata.CashProductKey) error) {
	s.purchase = f
}
func partnerKey(main, sub uint64) string { return fmt.Sprintf("%d:%d", main, sub) }
func (s *RosterProgressService) partnerExists(main, sub uint64) bool {
	return slices.Contains(s.design.Partners[main].Partners, sub)
}
func PartnerWire(v CharPartnerProgress) []byte {
	b := wire.AppendVarint(nil, 1, v.MainUniqueID)
	b = wire.AppendVarint(b, 2, v.SubUniqueID)
	b = wire.AppendVarint(b, 3, v.Point)
	return wire.AppendVarint(b, 4, v.Reward)
}
func (s *RosterProgressService) partnerInfo() []byte {
	var out []byte
	keys := make([]string, 0, len(s.collection.data.CharPartners))
	for k := range s.collection.data.CharPartners {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = wire.AppendBytes(out, 1, PartnerWire(s.collection.data.CharPartners[k]))
	}
	return out
}
func (s *RosterProgressService) ApplyBattlePartners(ctx command.Context, identity string, indices []uint64, win bool) ([]CharPartnerProgress, error) {
	if identity == "" {
		return nil, fmt.Errorf("player: missing partner battle identity")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(indices, win))))
	raw, exists, e := s.collection.store.LoadEntry(ctx.State, "collection", "partner_battles", identity)
	if e != nil {
		return nil, e
	}
	var receipt struct {
		Digest   string
		Partners []CharPartnerProgress
	}
	if exists {
		if json.Unmarshal(raw, &receipt) != nil || receipt.Digest != digest {
			return nil, fmt.Errorf("player: changed partner battle replay")
		}
		return receipt.Partners, nil
	}
	unique := map[uint64]bool{}
	for _, idx := range indices {
		c, ok := s.characters.Find(ctx, idx)
		if !ok || IsCharmCharacter(c) || IsStoryCharacter(c) {
			continue
		}
		id := s.potential.CharacterUnique[c.ID]
		if id != 0 {
			unique[id] = true
		}
	}
	next := cloneCollection(s.collection.data)
	var changed []CharPartnerProgress
	for main, rule := range s.design.Partners {
		if !unique[main] {
			continue
		}
		for _, sub := range rule.Partners {
			if !unique[sub] {
				continue
			}
			key := partnerKey(main, sub)
			v := next.CharPartners[key]
			v.MainUniqueID = main
			v.SubUniqueID = sub
			delta := -int64(s.design.LosePoint)
			if win {
				delta = int64(math.Round(float64(s.design.WinPoint) * (1 + rule.Weight/100)))
			}

			point := max(int64(0), min(int64(s.design.PartnerMaximum), int64(v.Point)+delta))
			v.Point = uint64(point)
			next.CharPartners[key] = v
			changed = append(changed, v)
		}
	}
	slices.SortFunc(changed, func(a, b CharPartnerProgress) int {
		if a.MainUniqueID != b.MainUniqueID {
			return int(a.MainUniqueID) - int(b.MainUniqueID)
		}
		return int(a.SubUniqueID) - int(b.SubUniqueID)
	})
	if e = s.collection.commit(ctx, next); e != nil {
		return nil, e
	}
	receipt.Digest = digest
	receipt.Partners = changed
	raw, e = json.Marshal(receipt)
	if e != nil {
		return nil, e
	}
	e = s.collection.store.PutEntry(ctx.State, "collection", "partner_battles", identity, raw)
	return changed, e
}
func (s *RosterProgressService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/CharPartnerInfo": 65, "/CharPartnerReward": 66, "/CharPartnerStoryReward": 67, "/CharClassUp": 99, "/CharExpiry": 133, "/CostumeUpgrade": 42, "/CostumeAllRounderUpgrade": s.AllRounderCode, "/CostumeSkillPotentialLock": 626}
	code, handled := codes[path]
	if !handled {
		return 0, nil, false, nil
	}
	fail := func(e error) (int, []byte, bool, error) { return code, nil, true, e }
	seq, e := rosterScalar(request, 1, true)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return fail(fmt.Errorf("player: invalid roster sequence"))
	}
	if path == "/CharPartnerInfo" {
		return code, s.partnerInfo(), true, nil
	}
	if ctx.SessionID == "" {
		return fail(fmt.Errorf("player: roster mutation requires session"))
	}
	h := sha256.Sum256(request)
	digest := hex.EncodeToString(h[:])
	key := fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)
	raw, found, e := s.collection.store.LoadEntry(ctx.State, "collection", "roster_replies", key)
	if e != nil {
		return fail(e)
	}
	var prior talentUpgradeReply
	if found {
		if json.Unmarshal(raw, &prior) != nil || prior.Code != code || prior.Digest != digest {
			return fail(fmt.Errorf("player: changed roster replay"))
		}
		return code, prior.Body, true, nil
	}
	index, e := rosterScalar(request, 2, true)
	if e != nil || index == 0 || index > math.MaxInt64 {
		return fail(fmt.Errorf("player: invalid roster inventory index"))
	}
	var body []byte
	switch path {
	case "/CharClassUp":
		body, e = s.classUp(ctx, index, request, key)
	case "/CharExpiry":
		body, e = s.expire(ctx, index)
	case "/CharPartnerReward", "/CharPartnerStoryReward":
		body, e = s.claimPartner(ctx, path, index, request)
	case "/CostumeUpgrade", "/CostumeAllRounderUpgrade":
		e = s.upgradeCostume(ctx, path, index, request, key)
	case "/CostumeSkillPotentialLock":
		body, e = s.lockPotential(ctx, index, request)
	}
	if e != nil {
		return fail(e)
	}
	raw, e = json.Marshal(talentUpgradeReply{Digest: digest, Code: code, Body: body})
	if e != nil {
		return fail(e)
	}
	if e = s.collection.store.PutEntry(ctx.State, "collection", "roster_replies", key, raw); e != nil {
		return fail(e)
	}
	return code, body, true, nil
}
func rosterScalar(raw []byte, number int, required bool) (uint64, error) {
	var value uint64
	count := 0
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == number {
			if f.Type != 0 || count != 0 {
				return fmt.Errorf("player: invalid duplicate scalar %d", number)
			}
			count++
			v, _, e := wire.Varint(raw, number)
			value = v
			return e
		}
		return nil
	})
	if e != nil {
		return 0, e
	}
	if required && count != 1 {
		return 0, fmt.Errorf("player: missing scalar %d", number)
	}
	return value, nil
}
func rosterMaterials(raw []byte, number int) ([]assets.Item, error) {
	var result []assets.Item
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type != 2 || len(result) >= 256 {
			return fmt.Errorf("player: invalid roster materials")
		}
		var item assets.Item
		fields := map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count}
		for n, p := range fields {
			v, e := rosterScalar(f.Value, n, false)
			if e != nil {
				return e
			}
			*p = v
		}
		if item.Type == 0 || item.Type > math.MaxInt32 || item.Count == 0 || item.Count > math.MaxInt32 || item.ID > math.MaxInt32 || item.InvenIndex > math.MaxInt64 {
			return fmt.Errorf("player: invalid roster material")
		}
		result = append(result, item)
		return nil
	})
	return result, e
}
func (s *RosterProgressService) pay(ctx command.Context, identity string, submitted []assets.Item, costs []gamedata.Reward, charge bool) error {
	want := map[[2]uint64]uint64{}
	for _, c := range costs {
		if c.Count == 0 {
			continue
		}
		k := [2]uint64{c.Type, c.ID}
		if c.Count > math.MaxInt32-want[k] {
			return fmt.Errorf("player: roster cost overflow")
		}
		want[k] += c.Count
	}
	got := map[[2]uint64]uint64{}
	var items []assets.Item
	for _, i := range submitted {
		k := [2]uint64{i.Type, i.ID}
		if i.Count > math.MaxInt32-got[k] {
			return fmt.Errorf("player: material overflow")
		}
		got[k] += i.Count
		switch i.Type {
		case 2, 3, 4, 12:
			if i.ID != 0 || i.InvenIndex != 0 {
				return fmt.Errorf("player: forged roster currency")
			}
		default:
			items = append(items, i)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("player: roster material mismatch")
	}
	for k, n := range want {
		if got[k] != n {
			return fmt.Errorf("player: roster material mismatch")
		}
	}
	if !charge {
		return nil
	}
	balance := s.wallet.Snapshot(ctx)
	for k, n := range want {
		var have uint64
		switch k[0] {
		case 2:
			have = balance.Jewelry
		case 3:
			have = balance.FreeJewelry
		case 4:
			have = balance.Gold
		case 12:
			have = balance.Catalyst
		default:
			continue
		}
		if n > have {
			return fmt.Errorf("player: insufficient roster currency")
		}
	}
	if e := s.inventory.CanConsume(ctx, items); e != nil {
		return e
	}
	if len(items) > 0 {
		if e := s.inventory.Consume(ctx, items); e != nil {
			return e
		}
	}
	for k, n := range want {
		var e error
		id := fmt.Sprintf("%s:%d", identity, k[0])
		switch k[0] {
		case 2:
			_, e = s.wallet.SpendJewelryOnce(ctx, id, n)
		case 3:
			_, e = s.wallet.SpendFreeJewelryOnce(ctx, id, n)
		case 4:
			_, e = s.wallet.SpendGoldOnce(ctx, id, n)
		case 12:
			_, e = s.wallet.SpendCatalystOnce(ctx, id, n)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *RosterProgressService) saveCharacter(ctx command.Context, old uint64, c Character) error {
	for i, v := range s.characters.characters {
		if v.InvenIndex == c.InvenIndex {
			next := append([]Character(nil), s.characters.characters...)
			next[i] = c
			if e := s.characters.persist(ctx, next); e != nil {
				return e
			}
			s.characters.characters = next
			return nil
		}
	}
	return s.collection.UpdateCharacter(ctx, old, c)
}
func (s *RosterProgressService) classUp(ctx command.Context, index uint64, raw []byte, key string) ([]byte, error) {
	c, ok := s.characters.Find(ctx, index)
	if !ok || IsStoryCharacter(c) || IsCharmCharacter(c) {
		return nil, fmt.Errorf("player: class-up requires permanent character")
	}
	next, costs, e := gamedata.CharacterPromotion(s.characters.gameDataRoot, s.characters.gameDataVersion, int(c.ID), c.Level, c.Exp)
	if e != nil {
		return nil, e
	}
	materials, e := rosterMaterials(raw, 3)
	if e != nil {
		return nil, e
	}
	r := make([]gamedata.Reward, len(costs))
	for i, v := range costs {
		r[i] = gamedata.Reward(v)
	}
	old := c.ID
	c.ID = next
	if s.characters.maxHealth != nil {
		c.HP, e = s.characters.maxHealth(ctx, c)
		if e != nil {
			return nil, e
		}
	}
	if e = s.pay(ctx, key, materials, r, true); e != nil {
		return nil, e
	}
	if e = s.saveCharacter(ctx, old, c); e != nil {
		return nil, e
	}
	if e = s.characters.resetCurrentHealth(ctx, index); e != nil {
		return nil, e
	}
	return wire.AppendBytes(nil, 1, CharacterWire(c)), nil
}
func (s *RosterProgressService) expire(ctx command.Context, index uint64) ([]byte, error) {
	var c Character
	found := false
	for _, v := range s.characters.characters {
		if v.InvenIndex == index {
			c = v
			found = true
			break
		}
	}
	if !found || !IsCharmCharacter(c) || !CharacterExpired(c, s.now()) {
		return nil, fmt.Errorf("player: character is not an expired charm companion")
	}
	if s.expiry == nil {
		return nil, fmt.Errorf("player: missing charm expiry deck runtime")
	}
	body, e := s.expiry(ctx, index)
	if e != nil {
		return nil, e
	}
	var next []Character
	for _, v := range s.characters.characters {
		if v.InvenIndex != index {
			next = append(next, v)
		}
	}
	if e = s.characters.persistWithChanges(ctx, next, []stateio.EntryMutation{{Bucket: "current_hp", Key: strconv.FormatUint(index, 10), Delete: true}}); e != nil {
		return nil, e
	}
	s.characters.characters = next
	return body, nil
}
func (s *RosterProgressService) claimPartner(ctx command.Context, path string, index uint64, raw []byte) ([]byte, error) {
	c, ok := s.characters.Find(ctx, index)
	if !ok || IsCharmCharacter(c) || IsStoryCharacter(c) {
		return nil, fmt.Errorf("player: partner requires permanent character")
	}
	main := s.potential.CharacterUnique[c.ID]
	rule, exists := s.design.Partners[main]
	if !exists {
		return nil, fmt.Errorf("player: character has no partner design")
	}
	var rewards []gamedata.Reward
	identity := ""
	next := cloneCollection(s.collection.data)
	if path == "/CharPartnerReward" {
		step, e := rosterScalar(raw, 3, true)
		if e != nil {
			return nil, e
		}
		r, ok := s.design.Solidarity[step]
		if !ok || step != c.SolidarityReward+1 {
			return nil, fmt.Errorf("player: invalid solidarity reward step")
		}
		var total uint64
		for _, sub := range rule.Partners {
			total += next.CharPartners[partnerKey(main, sub)].Point
		}
		if total < r.Require {
			return nil, fmt.Errorf("player: solidarity reward locked")
		}
		rewards = r.Rewards
		identity = fmt.Sprintf("solidarity:%d:%d", main, step)
		c.SolidarityReward = step
	} else {
		subIndex, e := rosterScalar(raw, 3, true)
		if e != nil {
			return nil, e
		}
		sub, ok := s.characters.Find(ctx, subIndex)
		if !ok || IsCharmCharacter(sub) || IsStoryCharacter(sub) {
			return nil, fmt.Errorf("player: invalid partner story character")
		}
		subID := s.potential.CharacterUnique[sub.ID]
		pos := slices.Index(rule.Partners, subID)
		if pos < 0 || rule.Stories[pos] == 0 {
			return nil, fmt.Errorf("player: invalid partner story")
		}
		key := partnerKey(main, subID)
		v := next.CharPartners[key]
		if v.Point < s.design.PartnerMaximum || v.Reward != 0 {
			return nil, fmt.Errorf("player: partner story locked or claimed")
		}
		v.Reward = 1
		next.CharPartners[key] = v
		rewards = []gamedata.Reward{rule.Rewards[pos]}
		identity = "partner-story:" + key
	}
	var items []gamedata.BattleReward
	for _, r := range rewards {
		switch r.Type {
		case 2, 3, 4, 12, 20:
		default:
			items = append(items, gamedata.BattleReward(r))
		}
	}
	if _, e := s.wallet.GrantQuestOnce(ctx, identity, rewards); e != nil {
		return nil, e
	}
	granted, e := s.inventory.GrantOnce(ctx, identity, items)
	if e != nil {
		return nil, e
	}
	var body []byte
	for _, i := range granted {
		body = wire.AppendBytes(body, 1, assets.ItemWire(i))
	}
	for _, r := range rewards {
		switch r.Type {
		case 2, 3, 4, 12, 20:
			body = wire.AppendBytes(body, 1, assets.ItemWire(assets.Item{Type: r.Type, ID: r.ID, Count: r.Count}))
		}
	}
	if path == "/CharPartnerReward" {
		if e = s.saveCharacter(ctx, c.ID, c); e != nil {
			return nil, e
		}
	} else {
		if e = s.collection.commit(ctx, next); e != nil {
			return nil, e
		}
	}
	return body, nil
}
func (s *RosterProgressService) upgradeCostume(ctx command.Context, path string, index uint64, raw []byte, key string) error {
	c, ok := s.collection.CostumeByIndex(index)
	if !ok {
		return fmt.Errorf("player: costume not owned")
	}
	rule, known := s.design.Costumes[c.ID]
	if !known || c.Level >= rule.Maximum {
		return fmt.Errorf("player: costume already at maximum")
	}
	field := 3
	costs := s.design.Growth[[2]uint64{rule.Group, c.Level}]
	if path == "/CostumeAllRounderUpgrade" {
		field = 5
		g, e := rosterScalar(raw, 3, true)
		if e != nil {
			return e
		}
		id, e := rosterScalar(raw, 4, true)
		if e != nil {
			return e
		}
		p, ok := s.design.AllRounder[[2]uint64{g, id}]
		if !ok {
			return fmt.Errorf("player: invalid all-rounder product")
		}
		if s.purchase == nil {
			return fmt.Errorf("player: missing all-rounder purchase runtime")
		}
		costs = []gamedata.Reward{{Type: p.PriceType, ID: p.PriceID, Count: p.PriceCount}}
		materials, e := rosterMaterials(raw, field)
		if e != nil {
			return e
		}
		if e = s.pay(ctx, key, materials, costs, false); e != nil {
			return e
		}
		if e = s.purchase(ctx, key, p.Key); e != nil {
			return e
		}
	} else {
		if len(costs) == 0 {
			return fmt.Errorf("player: costume has no payable upgrade")
		}
		materials, e := rosterMaterials(raw, field)
		if e != nil {
			return e
		}
		if e = s.pay(ctx, key, materials, costs, true); e != nil {
			return e
		}
	}
	next := cloneCollection(s.collection.data)
	for i, v := range next.Costumes {
		if v.InvenIndex == index {
			next.Costumes[i].Level++
			return s.collection.commit(ctx, next)
		}
	}
	next.BaseCostumeLevels[strconv.FormatUint(index, 10)] = c.Level + 1
	return s.collection.commit(ctx, next)
}
func (s *RosterProgressService) lockPotential(ctx command.Context, index uint64, raw []byte) ([]byte, error) {
	c, ok := s.collection.CostumeByIndex(index)
	if !ok {
		return nil, fmt.Errorf("player: costume not owned")
	}
	id, e := rosterScalar(raw, 3, true)
	if e != nil {
		return nil, e
	}
	locked, e := rosterScalar(raw, 4, false)
	if e != nil || locked > 1 {
		return nil, fmt.Errorf("player: invalid potential lock flag")
	}
	node, exists := s.potential.Nodes[c.ID][id]
	if !exists || (node.NodeType < 3 || node.NodeType > 7) || !slices.Contains(c.PotentialIDs, id) {
		return nil, fmt.Errorf("player: skill potential not unlocked")
	}
	ids := append([]uint64(nil), c.PotentialLockIDs...)
	if locked == 1 && !slices.Contains(ids, id) {
		ids = append(ids, id)
	}
	if locked == 0 {
		ids = slices.DeleteFunc(ids, func(v uint64) bool { return v == id })
	}
	slices.Sort(ids)
	next := cloneCollection(s.collection.data)
	key := strconv.FormatUint(index, 10)
	if len(ids) == 0 {
		delete(next.CostumePotentialLocks, key)
	} else {
		next.CostumePotentialLocks[key] = ids
	}
	if e = s.collection.commit(ctx, next); e != nil {
		return nil, e
	}
	c.PotentialLockIDs = ids
	return wire.AppendBytes(nil, 1, CostumeWire(c)), nil
}

func (s *RosterProgressService) DiscardCostume(ctx command.Context, item assets.Item) error {
	c, owned := s.collection.CostumeByIndex(item.InvenIndex)
	rule, known := s.design.Costumes[item.ID]
	if item.Type != 11 || item.Count != 1 || !owned || !known || c.ID != item.ID || rule.NotTrash != 0 || c.UseChar != 0 {
		return fmt.Errorf("player: costume cannot be discarded")
	}
	next := cloneCollection(s.collection.data)
	found := false
	next.Costumes = slices.DeleteFunc(next.Costumes, func(v Costume) bool {
		if v.InvenIndex == c.InvenIndex {
			found = true
			return true
		}
		return false
	})
	if !found {
		return fmt.Errorf("player: immutable starter costume cannot be discarded")
	}
	key := strconv.FormatUint(c.InvenIndex, 10)
	delete(next.CostumePotential, key)
	delete(next.CostumePotentialLocks, key)
	delete(next.CostumeBurstLevels, key)
	for k, v := range next.CostumeBurstUpgrades {
		if v.CostumeID == c.ID {
			delete(next.CostumeBurstUpgrades, k)
		}
	}
	return s.collection.commit(ctx, next)
}

func (s *RosterProgressService) TeamBattlePower(ctx command.Context, characters []Character, equipment []assets.Equipment) (uint64, error) {
	for _, c := range characters {
		owned, ok := s.characters.Find(ctx, c.InvenIndex)
		if !ok || owned.ID != c.ID {
			return 0, fmt.Errorf("player: invalid power roster ownership")
		}
	}
	return s.SnapshotBattlePower(characters, s.collection.Costumes(), equipment, s.collection.CharAwakeStates())
}

func (s *RosterProgressService) SnapshotBattlePower(characters []Character, costumes []Costume, equipment []assets.Equipment, awake map[uint64]CharAwakeProgress) (uint64, error) {
	var total uint64
	seen := map[uint64]bool{}
	add := func(count, value uint64) error {
		if value != 0 && count > (math.MaxInt32-total)/value {
			return fmt.Errorf("player: battle power overflow")
		}
		total += count * value
		return nil
	}
	for _, c := range characters {
		if c.InvenIndex == 0 || seen[c.InvenIndex] || IsStoryCharacter(c) || IsCharmCharacter(c) {
			return 0, fmt.Errorf("player: invalid power roster")
		}
		seen[c.InvenIndex] = true
		unique := s.potential.CharacterUnique[c.ID]
		if unique == 0 {
			return 0, fmt.Errorf("player: power character design missing")
		}
		if e := add(c.Level, s.design.Power[6]); e != nil {
			return 0, e
		}
		for _, costume := range costumes {
			if s.potential.CostumeUnique[costume.ID] != unique {
				continue
			}
			if e := add(1, s.design.Power[3]); e != nil {
				return 0, e
			}
			if e := add(costume.Level, s.design.Power[2]); e != nil {
				return 0, e
			}
			for _, id := range costume.PotentialIDs {
				node, ok := s.potential.Nodes[costume.ID][id]
				if !ok {
					return 0, fmt.Errorf("player: battle power potential missing")
				}
				var factor uint64
				switch node.NodeType {
				case 1:
					if c.ConnectPotentialCostume == costume.ID {
						factor = s.design.Power[7]
					}
				case 2:
					factor = s.design.Power[8]
				case 3, 4, 5, 6, 7:
					factor = s.design.Power[9]
				}
				if e := add(1, factor); e != nil {
					return 0, e
				}
			}
		}
		for _, equip := range equipment {
			if equip.UseChar != c.InvenIndex {
				continue
			}
			groups, ok := s.design.EquipmentGroups[equip.ID]
			if !ok {
				return 0, fmt.Errorf("player: battle power equipment design missing")
			}
			point, ok := s.design.EquipmentGrowthPoints[[2]uint64{groups[0], equip.Level}]
			if !ok {
				return 0, fmt.Errorf("player: battle power equipment growth missing")
			}
			if e := add(1, point); e != nil {
				return 0, e
			}
			for i, rank := range equip.Rank {
				points := s.design.EquipmentRankPoints[[2]uint64{groups[1], uint64(i + 1)}]
				if rank == 0 || rank > uint64(len(points)) {
					break
				}
				if e := add(1, points[rank-1]); e != nil {
					return 0, e
				}
			}
		}
		progress := awake[unique]
		if progress.IsAwake {
			if e := add(1, s.design.Power[1]); e != nil {
				return 0, e
			}
		}
		for _, level := range progress.ImprintLevels {
			if e := add(level, s.design.Power[5]); e != nil {
				return 0, e
			}
		}
	}
	return total, nil
}
