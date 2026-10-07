package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/ownership"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

func CostumeWire(v Costume) []byte {
	out := ownership.Costume{InvenIndex: v.InvenIndex, ID: v.ID, Level: v.Level, UseChar: v.UseChar, SortID: v.SortID, PotentialIDs: v.PotentialIDs, DesignID: v.DesignID, BurstLevel: v.BurstLevel, TimeValue: v.TimeValue}
	for _, p := range v.Pictorialbook {
		out.Pictorialbook = append(out.Pictorialbook, ownership.Pictorial{ID: p.ID, GroupID: p.GroupID})
	}
	return ownership.EncodeCostume(out)
}

func (s *RecruitService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/MercenaryScout" && path != "/CharScoutInfo" && path != "/CharSpecialScoutBuy" && path != "/CharSpecialScoutReset" {
		return 0, nil, false, nil
	}

	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: invalid recruitment sequence")
	}
	if path == "/CharScoutInfo" {
		state, e := s.specialState()
		if e != nil {
			return 0, nil, true, e
		}
		if e = s.persistSpecial(ctx, state); e != nil {
			return 0, nil, true, e
		}
		return 148, s.scoutInfo(state), true, nil
	}
	if ctx.SessionID == "" {
		return 0, nil, true, errors.New("player: recruitment requires authenticated session")
	}
	digestBytes := sha256.Sum256(request)
	digest := hex.EncodeToString(digestBytes[:])
	keyBytes := sha256.Sum256([]byte(ctx.SessionID + ":" + path + ":" + strconv.FormatUint(seq, 10)))
	replyKey := "recruit-reply:" + hex.EncodeToString(keyBytes[:])
	if b, ok, e := s.collection.GachaBatchResponse(ctx, replyKey, digest); ok || e != nil {
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
		if _, err = s.wallet.SpendFreeJewelryOnce(ctx, replyKey, s.design.ResetCount); err != nil {
			return 0, nil, true, err
		}
		if err = s.persistSpecial(ctx, state); err != nil {
			return 0, nil, true, err
		}
		var body []byte
		for _, id := range state.IDs {
			body = wire.AppendVarint(body, 1, id)
		}
		body = wire.AppendVarint(body, 2, state.Next)
		if err = s.collection.RecordGachaBatch(ctx, replyKey, digest, body); err != nil {
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
		id, err = s.resolver(ctx, npc)
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
	materials, err := assets.DecodeItemRequest(request, 3, "MercenaryScout")
	if err != nil {
		return 0, nil, true, err
	}
	items, gold, err := validateCostumeBurstMaterials(r.Costs, materials)
	if err != nil || gold != 0 {
		return 0, nil, true, fmt.Errorf("player: invalid recruit materials: %v", err)
	}
	if err = s.inventory.CanConsume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	if err = s.inventory.Consume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	grant, err := s.collection.GrantCostumes(ctx, identity, []uint64{r.CostumeID}, s.catalog)
	if err != nil {
		return 0, nil, true, err
	}
	var rewards []gamedata.Reward
	for _, x := range grant.Exchanges {
		rewards = append(rewards, gamedata.Reward{Type: x.ExchangeItemType, ID: x.ExchangeItemID, Count: x.ExchangeCount})
	}
	if len(rewards) > 0 {
		if _, err = s.wallet.GrantQuestOnce(ctx, identity+":exchange", rewards); err != nil {
			return 0, nil, true, err
		}
	}
	if path == "/CharSpecialScoutBuy" {
		if err = s.persistSpecial(ctx, state); err != nil {
			return 0, nil, true, err
		}
	}
	body := wire.AppendBytes(nil, 1, recruitRewardBundle(s.collection, grant))
	if err = s.collection.RecordGachaBatch(ctx, replyKey, digest, body); err != nil {
		return 0, nil, true, err
	}
	return recruitCode(path), body, true, nil
}

func (s *FriendshipService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/FriendshipInfo" && path != "/FriendshipGift" && path != "/FriendshipCounseling" {
		return 0, nil, false, nil
	}

	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: friendship invalid sequence")
	}
	entries := s.collection.FriendshipEntries()
	if path == "/FriendshipInfo" {
		return 612, s.info(entries), true, nil
	}
	session := ctx.SessionID
	if session == "" {
		return 0, nil, true, errors.New("player: friendship mutation requires an authenticated session")
	}
	keyHash := sha256.Sum256([]byte(session + ":" + strconv.FormatUint(seq, 10)))
	key := "reply:" + hex.EncodeToString(keyHash[:])
	digestHash := sha256.Sum256(request)
	digest := hex.EncodeToString(digestHash[:])
	code := 614
	if path == "/FriendshipCounseling" {
		code = 613
	}
	if entry := entries[key]; entry.Reply != nil {
		if entry.Reply.Digest != digest || entry.Reply.Code != code {
			return 0, nil, true, errors.New("player: friendship sequence reused with different request")
		}
		return code, append([]byte(nil), entry.Reply.Body...), true, nil
	}
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 || id > math.MaxInt32 {
		return 0, nil, true, errors.New("player: friendship invalid costume")
	}
	costume, owned := s.owned(id)
	if !owned {
		return 0, nil, true, fmt.Errorf("player: friendship costume %d is not owned", id)
	}
	state := FriendshipState{CostumeID: id, Level: 1}
	if entry := entries[friendshipStateKey(id)]; entry.State != nil {
		state = *entry.State
		state.Sessions = append([]uint64(nil), state.Sessions...)
	}
	max := s.maximum(costume)
	var body []byte
	if code == 614 {
		body, err = s.gift(ctx, request, state, max, key, digest)
	} else {
		body, err = s.counsel(ctx, request, state, max, entries, key, digest)
	}
	return code, body, true, err
}

func (s *CostumePotentialService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/CostumePotentialConnect" {
		return s.connect(ctx, request)
	}
	if path != "/CostumeNodeActivation" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing sequence")
	}
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing character")
	}
	costumeIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || costumeIndex == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing costume")
	}
	var nodes []uint64
	var materials []assets.Item
	err = wire.Walk(request, func(field wire.Field) error {
		switch field.Number {
		case 4:
			if field.Type != 0 && field.Type != 2 {
				return errors.New("player: invalid potential node field")
			}
			for data := field.Value; len(data) != 0; {
				id, count := binary.Uvarint(data)
				if count <= 0 || id == 0 {
					return errors.New("player: invalid potential node ID")
				}
				nodes = append(nodes, id)
				data = data[count:]
			}
		case 5:
			if field.Type != 2 {
				return errors.New("player: invalid potential material field")
			}
			var item assets.Item
			if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
				return err
			}
			if item.Type == 0 || item.Count == 0 || (item.Type != 4 && (item.InvenIndex == 0 || item.ID == 0)) {
				return errors.New("player: incomplete potential material")
			}
			materials = append(materials, item)
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(nodes) == 0 || len(materials) == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation has no nodes or materials")
	}
	character, found := s.characters.Find(ctx, characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: unknown potential character %d", characterIndex)
	}
	costume, found := s.collection.CostumeByIndex(costumeIndex)
	if !found || costume.UseChar != characterIndex {
		return 0, nil, true, fmt.Errorf("player: potential costume %d is not owned by character %d", costumeIndex, characterIndex)
	}
	if err := s.collection.ValidateCostumePotentialActivation(ctx, costumeIndex, nodes); err != nil {
		return 0, nil, true, err
	}
	costs, err := s.design.Validate(costume.ID, character.ID, 0, costume.PotentialIDs, nodes)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate costume potential GameData: %w", err)
	}
	want := make(map[[2]uint64]uint64)
	for _, cost := range costs {
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Count > ^uint64(0)-want[key] {
			return 0, nil, true, errors.New("player: costume potential cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64)
	var items []assets.Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		if material.Type == 4 {
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return 0, nil, true, errors.New("player: invalid costume potential currency")
			}
			gold = material.Count
		} else {
			if material.Type != 8 {
				return 0, nil, true, fmt.Errorf("player: unsupported costume potential material type %d", material.Type)
			}
			items = append(items, material)
		}
		if material.Count > ^uint64(0)-got[key] {
			return 0, nil, true, errors.New("player: submitted costume potential material overflow")
		}
		got[key] += material.Count
	}
	if len(got) != len(want) {
		return 0, nil, true, fmt.Errorf("player: costume potential material kinds mismatch: request=%v GameData=%v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			return 0, nil, true, fmt.Errorf("player: costume potential material %d/%d=%d want %d", key[0], key[1], got[key], count)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate costume potential items: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for costume potential")
	}
	sortedNodes := append([]uint64(nil), nodes...)
	slices.Sort(sortedNodes)
	parts := make([]string, len(sortedNodes))
	for i, id := range sortedNodes {
		parts[i] = strconv.FormatUint(id, 10)
	}
	identity := "costume-potential:" + strconv.FormatUint(costumeIndex, 10) + ":" + strings.Join(parts, ",")
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend costume potential gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume costume potential items: %w", err)
		}
	}
	if err := s.collection.ActivateCostumePotential(ctx, costumeIndex, nodes); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist costume potential: %w", err)
	}
	return 261, nil, true, nil
}

func (s *CostumeBurstService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
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
	materials, err := assets.DecodeItemRequest(request, 3, "CostumeBurst")
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

	sessionID := ctx.SessionID
	if sessionID == "" {
		return 0, nil, true, errors.New("player: CostumeBurst requires an authenticated session")
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
		if err := s.inventory.CanConsume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate CostumeBurst items: %w", err)
		}
	}

	identity := "costume-burst:" + strconv.FormatUint(costume.InvenIndex, 10) + ":" + strconv.FormatUint(rule.NextLevel, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume CostumeBurst gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume CostumeBurst items: %w", err)
		}
	}
	body := wire.AppendVarint(nil, 1, rule.NextLevel)
	record := CostumeBurstUpgradeRecord{CostumeID: costume.ID, Level: rule.NextLevel, Digest: digest, Code: costumeBurstPacketCode, Body: append([]byte(nil), body...)}
	if err := s.collection.ApplyCostumeBurst(ctx, costume.InvenIndex, costume.BurstLevel, rule.NextLevel, record); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist CostumeBurst: %w", err)
	}
	reply := costumeBurstReply{Digest: digest, Code: costumeBurstPacketCode, Body: append([]byte(nil), body...)}
	s.replies[sessionID][seq] = reply
	return reply.Code, append([]byte(nil), reply.Body...), true, nil
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

func (s *RecruitService) persistSpecial(ctx command.Context, state specialRecruitState) error {
	var b []byte
	for _, id := range state.IDs {
		b = wire.AppendVarint(b, 1, id)
	}
	b = wire.AppendVarint(b, 2, state.Count)
	b = wire.AppendVarint(b, 3, state.Next)
	b = wire.AppendString(b, 4, state.Day)
	return s.collection.saveRecruitState(ctx, b)
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
	slices.Sort(ids)
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
			b = wire.AppendBytes(b, 6, assets.ItemWire(assets.Item{ID: v.ID, Type: 6, Count: 1}))
		}
	}
	for _, idx := range g.CostumeIndices {
		if v, ok := c.CostumeByIndex(idx); ok {
			b = wire.AppendBytes(b, 3, CostumeWire(v))
		}
	}
	for _, id := range g.ViewCostumeIDs {
		b = wire.AppendBytes(b, 6, assets.ItemWire(assets.Item{ID: id, Type: 11, Count: 1}))
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

func validateFriendshipEntries(entries map[string]FriendshipEntry) error {
	for key, entry := range entries {
		kinds := 0
		if entry.State != nil {
			kinds++
		}
		if entry.Daily != nil {
			kinds++
		}
		if entry.Reply != nil {
			kinds++
		}
		if kinds != 1 {
			return fmt.Errorf("player: invalid friendship entry %q", key)
		}
		if state := entry.State; state != nil {
			if key != friendshipStateKey(state.CostumeID) || state.CostumeID == 0 || state.CostumeID > math.MaxInt32 || state.Level == 0 || state.Level > math.MaxInt32 || state.EXP > math.MaxInt32 || state.LastCounselingDate > math.MaxInt64 {
				return fmt.Errorf("player: invalid friendship state %q", key)
			}
			if state.LastCounselingDate == 0 {
				if state.CounselingDay != "" || state.CounselingCount != 0 || len(state.Sessions) != 0 {
					return errors.New("player: friendship counseling state has no date")
				}
			} else if state.CounselingCount == 0 || state.CounselingDay != time.UnixMilli(int64(state.LastCounselingDate)).UTC().Format("2006-01-02") {
				return errors.New("player: inconsistent friendship counseling date")
			}
			seen := map[uint64]bool{}
			for _, id := range state.Sessions {
				if id == 0 || id > math.MaxInt32 || seen[id] {
					return errors.New("player: invalid friendship counseling sessions")
				}
				seen[id] = true
			}
		}
		if entry.Daily != nil && (key != "daily" || entry.Daily.Day == "") {
			return errors.New("player: invalid friendship daily entry")
		}
		if entry.Daily != nil {
			if parsed, err := time.Parse("2006-01-02", entry.Daily.Day); err != nil || parsed.Format("2006-01-02") != entry.Daily.Day {
				return errors.New("player: invalid friendship daily date")
			}
		}
		if reply := entry.Reply; reply != nil {
			if !strings.HasPrefix(key, "reply:") || len(key) != len("reply:")+64 || (reply.Code != 613 && reply.Code != 614) || len(reply.Digest) != 64 || len(reply.Body) == 0 {
				return errors.New("player: invalid friendship replay")
			}
			if _, err := hex.DecodeString(reply.Digest); err != nil {
				return errors.New("player: invalid friendship request digest")
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(key, "reply:")); err != nil {
				return errors.New("player: invalid friendship replay key")
			}
			if err := validateFriendshipReply(*reply); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFriendshipReply(reply FriendshipReply) error {
	infoCount := 0
	err := wire.Walk(reply.Body, func(field wire.Field) error {
		switch field.Number {
		case 1:
			if field.Type != 2 {
				return errors.New("invalid friendship reward bundle")
			}
			return wire.Walk(field.Value, func(wire.Field) error { return nil })
		case 2:
			if field.Type != 2 {
				return errors.New("invalid friendship response info")
			}
			infoCount++
			id, found, err := wire.Varint(field.Value, 1)
			if err != nil || !found || id == 0 || id > math.MaxInt32 {
				return errors.New("invalid friendship response costume")
			}
			level, found, err := wire.Varint(field.Value, 2)
			if err != nil || !found || level == 0 || level > math.MaxInt32 {
				return errors.New("invalid friendship response level")
			}
		case 3, 4:
			if field.Type != 0 || (reply.Code == 614 && field.Number == 4) {
				return errors.New("invalid friendship response scalar")
			}
			value, _, err := wire.Varint(reply.Body, field.Number)
			if err != nil || value > math.MaxInt32 || (reply.Code == 613 && field.Number == 3 && value > 1) {
				return errors.New("invalid friendship response value")
			}
		default:
			return errors.New("unexpected friendship response field")
		}
		return nil
	})
	if err != nil || infoCount != 1 {
		return errors.New("player: invalid friendship replay response")
	}
	return nil
}

func (s *CollectionStore) ApplyFriendship(ctx command.Context, state FriendshipState, daily *FriendshipDaily, replayKey string, reply FriendshipReply) error {

	next := cloneCollection(s.data)
	next.Friendships[friendshipStateKey(state.CostumeID)] = FriendshipEntry{State: &state}
	if daily != nil {
		next.Friendships["daily"] = FriendshipEntry{Daily: daily}
	}
	if _, exists := next.Friendships[replayKey]; exists {
		return errors.New("player: friendship request already applied")
	}
	next.Friendships[replayKey] = FriendshipEntry{Reply: &reply}
	if err := validateFriendshipEntries(next.Friendships); err != nil {
		return err
	}
	return s.commit(ctx, next)
}

func friendshipWire(state FriendshipState) []byte {
	b := wire.AppendVarint(nil, 1, state.CostumeID)
	b = wire.AppendVarint(b, 2, state.Level)
	if state.EXP != 0 {
		b = wire.AppendVarint(b, 3, state.EXP)
	}
	if state.LastCounselingDate != 0 {
		b = wire.AppendVarint(b, 4, state.LastCounselingDate)
	}
	return b
}

func (s *FriendshipService) info(entries map[string]FriendshipEntry) []byte {
	ids := make([]uint64, 0, len(s.design.Costumes))
	for id := range s.design.Costumes {
		if _, ok := s.owned(id); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var body []byte
	for _, id := range ids {
		state := FriendshipState{CostumeID: id, Level: 1}
		if entry := entries[friendshipStateKey(id)]; entry.State != nil {
			state = *entry.State
		}
		body = wire.AppendBytes(body, 1, friendshipWire(state))
		if len(state.Sessions) != 0 {
			counsel := wire.AppendVarint(nil, 1, id)
			for _, session := range state.Sessions {
				counsel = wire.AppendVarint(counsel, 2, session)
			}
			body = wire.AppendBytes(body, 2, counsel)
		}
	}
	return body
}

func (s *FriendshipService) gift(ctx command.Context, request []byte, state FriendshipState, max uint64, key, digest string) ([]byte, error) {
	if state.Level >= max {
		return nil, errors.New("player: friendship level is at its unlocked maximum")
	}
	items, err := assets.DecodeItemRequest(request, 3, "FriendshipGift")
	if err != nil {
		return nil, err
	}
	var exp uint64
	seen := map[uint64]bool{}
	for _, item := range items {
		if seen[item.InvenIndex] || item.InvenIndex > math.MaxInt64 || item.ID > math.MaxInt32 || item.Type > math.MaxInt32 || item.Count > math.MaxInt32 {
			return nil, errors.New("player: invalid friendship gift item")
		}
		seen[item.InvenIndex] = true
		rule, ok := s.design.Gifts[[2]uint64{item.Type, item.ID}]
		if !ok {
			return nil, errors.New("player: item is not a friendship gift")
		}
		unit := rule.Experience(state.CostumeID)
		if unit == 0 || item.Count > math.MaxInt32/unit || exp > math.MaxInt32-unit*item.Count {
			return nil, errors.New("player: invalid friendship gift experience")
		}
		exp += unit * item.Count
	}
	if err := s.inventory.CanConsume(ctx, items); err != nil {
		return nil, err
	}
	next, rewards, err := s.advance(state, max, exp)
	if err != nil {
		return nil, err
	}
	if err := s.inventory.Consume(ctx, items); err != nil {
		return nil, err
	}
	bundle, err := s.grant(ctx, key, rewards)
	if err != nil {
		return nil, err
	}
	body := wire.AppendBytes(nil, 1, bundle)
	body = wire.AppendBytes(body, 2, friendshipWire(next))
	body = wire.AppendVarint(body, 3, exp)
	if err := s.collection.ApplyFriendship(ctx, next, nil, key, FriendshipReply{Digest: digest, Code: 614, Body: body}); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *FriendshipService) counsel(ctx command.Context, request []byte, state FriendshipState, max uint64, entries map[string]FriendshipEntry, key, digest string) ([]byte, error) {
	session, found, err := wire.Varint(request, 3)
	if err != nil || !found || session == 0 || session > math.MaxInt32 {
		return nil, errors.New("player: invalid counseling session")
	}
	rule, exists := s.design.Sessions[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: session}]
	if !exists {
		return nil, errors.New("player: counseling session does not belong to costume")
	}
	choice, _, err := wire.Varint(request, 4)
	if err != nil || choice >= rule.ChoiceCount {
		return nil, errors.New("player: invalid counseling choice")
	}
	quick, _, err := wire.Varint(request, 5)
	if err != nil || quick > 1 {
		return nil, errors.New("player: invalid counseling quick flag")
	}
	if quick == 1 && (uint64(len(state.Sessions)) < s.design.Default.QuickCounselingUnlockCount || state.Level >= max || choice != 1) {
		return nil, errors.New("player: quick counseling is not available")
	}
	now := s.now()
	day := now.UTC().Format("2006-01-02")
	daily := s.daily(entries, day)
	free := state.Level >= s.design.Default.MaxLevels[2]
	if !free {
		if daily.Used >= s.design.Default.MaxCounselingAP {
			return nil, errors.New("player: no daily friendship AP remaining")
		}
		if state.CounselingDay == day && state.CounselingCount >= s.design.Default.MaxCounselingAPByCostume {
			return nil, errors.New("player: costume daily counseling limit reached")
		}
	}
	correct := quick == 1 || choice == s.design.Default.CorrectSelectDialogIndex
	exp := s.design.Default.IncorrectEXP
	if correct {
		exp = s.design.Default.CorrectEXP
	}
	if state.Level >= max {
		exp = 0
	}
	next, rewards, err := s.advance(state, max, exp)
	if err != nil {
		return nil, err
	}
	completed := slices.Contains(state.Sessions, session)
	if !free {
		rewards = append(rewards, s.design.Default.CounselingRewards...)
		daily.Used++
	} else if !completed && quick == 0 {
		// At the final cap the client allows unrestricted story playback. A
		// previously unseen story earns its default reward once; repeated
		// playback does not create a source of unlimited account currency.
		rewards = append(rewards, s.design.Default.CounselingRewards...)
	}
	if next.CounselingDay != day {
		next.CounselingDay = day
		next.CounselingCount = 0
	}
	next.CounselingCount++
	if now.UnixMilli() <= 0 {
		return nil, errors.New("player: invalid counseling time")
	}
	next.LastCounselingDate = uint64(now.UnixMilli())
	if quick == 0 {
		if !completed {
			next.Sessions = append(next.Sessions, session)
			slices.Sort(next.Sessions)
		}
	}
	bundle, err := s.grant(ctx, key, rewards)
	if err != nil {
		return nil, err
	}
	body := wire.AppendBytes(nil, 1, bundle)
	body = wire.AppendBytes(body, 2, friendshipWire(next))
	if correct {
		body = wire.AppendVarint(body, 3, 1)
	}
	if exp != 0 {
		body = wire.AppendVarint(body, 4, exp)
	}
	if err := s.collection.ApplyFriendship(ctx, next, &daily, key, FriendshipReply{Digest: digest, Code: 613, Body: body}); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *FriendshipService) grant(ctx command.Context, identity string, rewards []gamedata.Reward) ([]byte, error) {
	if len(rewards) == 0 {
		return nil, nil
	}
	var items []gamedata.BattleReward
	for _, reward := range rewards {
		if reward.Type == 0 || reward.Count == 0 || reward.Count > math.MaxInt32 {
			return nil, errors.New("player: invalid friendship reward")
		}
		switch reward.Type {
		case 2, 3, 4, 12, 20:
		default:
			if reward.ID == 0 || reward.ID > math.MaxInt32 {
				return nil, errors.New("player: invalid friendship item reward")
			}
			items = append(items, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
		}
	}
	if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", rewards); err != nil {
		return nil, err
	}
	granted, err := s.inventory.GrantOnce(ctx, identity+":items", items)
	if err != nil {
		return nil, err
	}
	var bundle []byte
	for _, item := range granted {
		bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
	}
	for _, reward := range rewards {
		view := assets.Item{ID: reward.ID, Type: reward.Type, Count: reward.Count}
		bundle = wire.AppendBytes(bundle, 6, assets.ItemWire(view))
		switch reward.Type {
		case 2, 3, 4, 12, 20:
			bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(view))
		}
	}
	return bundle, nil
}

func (s *CostumePotentialService) connect(ctx command.Context, request []byte) (int, []byte, bool, error) {

	fail := func(e error) (int, []byte, bool, error) { return 267, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || s.connectStore == nil || ctx.SessionID == "" {
		return fail(fmt.Errorf("player: invalid potential connection session/sequence"))
	}
	var receipts map[string]potentialConnectReceipt
	b, err := s.connectStore.Load(ctx.State, "potentialconnect")
	if err != nil {
		return fail(err)
	}
	if b != nil {
		if err = json.Unmarshal(b, &receipts); err != nil || receipts == nil {
			return fail(fmt.Errorf("player: invalid potential connection receipts"))
		}
	} else {
		receipts = map[string]potentialConnectReceipt{}
	}
	for _, r := range receipts {
		hash, e := hex.DecodeString(r.Digest)
		if e != nil || len(hash) != sha256.Size {
			return fail(fmt.Errorf("player: malformed potential connection receipt digest"))
		}
		if e = wire.Walk(r.Body, func(wire.Field) error { return nil }); e != nil {
			return fail(e)
		}
	}
	seqFields := 0
	if err = wire.Walk(request, func(f wire.Field) error {
		if f.Number == 1 {
			seqFields++
			if f.Type != 0 {
				return fmt.Errorf("player: invalid potential sequence wire")
			}
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	if seqFields != 1 {
		return fail(fmt.Errorf("player: duplicate potential sequence"))
	}
	key := fmt.Sprintf("%s:%d", ctx.SessionID, seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	if prior, ok := receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed potential connection replay"))
		}
		return 267, prior.Body, true, nil
	}
	var characters []Character
	seen := map[uint64]bool{}
	oldHP := map[uint64]uint64{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 2 || len(characters) >= 4096 {
			return fmt.Errorf("player: invalid potential connection list")
		}
		fields := map[int]bool{}
		if e := wire.Walk(f.Value, func(field wire.Field) error {
			if field.Number == 1 || field.Number == 2 {
				if field.Type != 0 || fields[field.Number] {
					return fmt.Errorf("player: duplicate or invalid connection scalar")
				}
				fields[field.Number] = true
			}
			return nil
		}); e != nil {
			return e
		}
		index, ok, e := wire.Varint(f.Value, 1)
		if e != nil || !ok || index == 0 || index > math.MaxInt64 || seen[index] {
			return fmt.Errorf("player: duplicate or invalid potential character")
		}
		costume, _, e := wire.Varint(f.Value, 2)
		if e != nil || costume == 0 || costume > math.MaxInt32 {
			return fmt.Errorf("player: potential connection requires a costume design ID")
		}
		c, owned := s.characters.Find(ctx, index)
		if !owned || IsStoryCharacter(c) || IsCharmCharacter(c) {
			return fmt.Errorf("player: potential character is not permanent owned")
		}
		unique := s.design.CharacterUnique[c.ID]
		characterType, knownType := s.design.CharacterTypes[c.ID]
		if unique == 0 || !knownType || characterType != 0 || !s.design.CostumeActive[costume] || s.design.CostumeUnique[costume] != unique || len(s.design.Nodes[costume]) == 0 {
			return fmt.Errorf("player: incompatible or unavailable potential costume")
		}
		ownedCostume := false
		for _, v := range s.collection.Costumes() {
			if v.ID == costume && v.UseChar == index {
				ownedCostume = true
				for _, id := range v.PotentialIDs {
					if _, ok := s.design.Nodes[costume][id]; !ok {
						return fmt.Errorf("player: invalid saved potential node")
					}
				}
				break
			}
		}
		if !ownedCostume {
			return fmt.Errorf("player: potential costume not owned by requested character")
		}
		hp, e := s.characters.CurrentHealth(ctx, index)
		if e != nil {
			return e
		}
		oldHP[index] = hp
		c.ConnectPotentialCostume = costume
		characters = append(characters, c)
		seen[index] = true
		return nil
	})
	if err != nil {
		return fail(err)
	}
	if len(characters) == 0 {
		return fail(fmt.Errorf("player: empty potential connection list"))
	}
	// All links are validated before writes. The parent account transaction
	// includes both character ownership domains, HP and the response receipt.
	for _, c := range characters {
		if err = s.characters.setPotentialConnection(ctx, c.InvenIndex, c.ConnectPotentialCostume); err != nil {
			return fail(err)
		}
	}
	var out []byte
	for _, c := range characters {
		maximum, e := s.characters.MaxHealth(ctx, c.InvenIndex)
		if e != nil {
			return fail(e)
		}
		hp := min(oldHP[c.InvenIndex], maximum)
		if e = s.characters.SetCurrentHealth(ctx, c.InvenIndex, hp); e != nil {
			return fail(e)
		}
		c.HP = hp
		out = wire.AppendBytes(out, 1, CharacterWire(c))
	}
	receipts[key] = potentialConnectReceipt{Digest: digest, Body: out}
	b, err = json.Marshal(receipts)
	if err != nil {
		return fail(err)
	}
	if err = s.connectStore.Save(ctx.State, "potentialconnect", b); err != nil {
		return fail(err)
	}
	return 267, out, true, nil
}

func OpenCollectionStore(ctx command.Context, store stateio.Store, base []Costume) (*CollectionStore, error) {
	if store == nil {
		return nil, errors.New("player: nil collection store")
	}
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok {
		return nil, errors.New("player: collection store requires atomic entry storage")
	}
	s := &CollectionStore{store: entries, base: append([]Costume(nil), base...), data: collectionSnapshot{
		Version: versionconfig.State(), NextCharacterIndex: 920000001, NextCostumeIndex: 930000001,
		BaseCostumeLevels: map[string]uint64{}, GachaSelections: map[string][]GachaSelection{}, GachaSelectionChanges: map[string]uint64{},
		CostumePotential:   map[string][]uint64{},
		CostumeBurstLevels: map[string]uint64{}, CostumeBurstUpgrades: map[string]CostumeBurstUpgradeRecord{},
		CharAwake:      map[string]CharAwakeProgress{},
		Friendships:    map[string]FriendshipEntry{},
		StepUpProgress: map[string]uint64{}, GachaUsers: map[string]GachaUserState{}, GachaFixed: map[string]GachaFixedState{},
		GachaApplied: map[string]bool{}, GachaPointExchange: map[string]GachaPointExchange{}, Grants: map[string]CollectionGrant{},
	}}
	b, err := store.Load(ctx.State, "collection")
	if err != nil {
		return nil, err
	}
	if b == nil {
		if err := stateio.RequireNoEntries(entries, ctx.State, collectionDomain, collectionEntryBuckets[:]...); err != nil {
			return nil, fmt.Errorf("player: invalid collection storage: %w", err)
		}
		return s, nil
	}
	if err := rejectInlineCollectionEntries(b); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("player: decode collection: %w", err)
	}
	if err := loadCollectionEntries(ctx, entries, &s.data); err != nil {
		return nil, err
	}
	if s.data.Version != versionconfig.State() || s.data.NextCharacterIndex < 920000001 || s.data.NextCostumeIndex < 930000001 {
		return nil, errors.New("player: invalid collection save")
	}
	if s.data.BaseCostumeLevels == nil {
		s.data.BaseCostumeLevels = map[string]uint64{}
	}
	if s.data.CostumePotential == nil {
		return nil, errors.New("player: collection save requires costume_potential; migrate the development save")
	}
	if s.data.CharAwake == nil {
		return nil, errors.New("player: collection save requires char_awake; migrate the development save")
	}
	if err := s.validateCostumeBurstStorage(); err != nil {
		return nil, err
	}
	if err := validateFriendshipEntries(s.data.Friendships); err != nil {
		return nil, err
	}
	for key, progress := range s.data.CharAwake {
		uniqueID, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || uniqueID == 0 || (progress.ImprintLevels == [3]uint64{} && !progress.IsAwake) {
			return nil, errors.New("player: invalid char_awake ledger")
		}
	}
	if s.data.GachaSelections == nil {
		s.data.GachaSelections = map[string][]GachaSelection{}
	}
	if s.data.GachaSelectionChanges == nil {
		s.data.GachaSelectionChanges = map[string]uint64{}
	}
	for key, count := range s.data.GachaSelectionChanges {
		groupID, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || groupID == 0 || key != strconv.FormatUint(groupID, 10) || count == 0 {
			return nil, errors.New("player: invalid gacha selection change ledger")
		}
	}
	if s.data.StepUpProgress == nil {
		s.data.StepUpProgress = map[string]uint64{}
	}
	if s.data.GachaUsers == nil {
		s.data.GachaUsers = map[string]GachaUserState{}
	}
	if s.data.GachaFixed == nil {
		s.data.GachaFixed = map[string]GachaFixedState{}
	}
	if s.data.GachaApplied == nil {
		s.data.GachaApplied = map[string]bool{}
	}
	if s.data.GachaPointExchange == nil {
		s.data.GachaPointExchange = map[string]GachaPointExchange{}
	}
	if marker, exists := s.data.Grants[FirstGachaCompletedIdentity]; exists && !emptyCollectionGrant(marker) {
		return nil, errors.New("player: invalid first-gacha completion marker")
	}
	if err := validateCharacters(s.data.Characters); err != nil && len(s.data.Characters) != 0 {
		return nil, err
	}
	s.persisted = true
	return s, nil
}

// ApplyCostumeBurst atomically advances one owned costume and stores the exact
// successful reply. expectedCurrent is a compare-and-swap guard against a
// stale request overwriting a newer level.
func (s *CollectionStore) ApplyCostumeBurst(ctx command.Context, invenIndex, expectedCurrent, target uint64, record CostumeBurstUpgradeRecord) error {
	if invenIndex == 0 || expectedCurrent == math.MaxUint64 || target != expectedCurrent+1 {
		return errors.New("player: invalid costume burst transition")
	}
	if record.Level != target || record.CostumeID == 0 {
		return errors.New("player: inconsistent costume burst record")
	}
	if err := validateCostumeBurstRecord(record); err != nil {
		return err
	}

	costume, found := s.costumeByIndexLocked(invenIndex)
	if !found {
		return fmt.Errorf("player: costume %d not found", invenIndex)
	}
	if costume.ID != record.CostumeID {
		return errors.New("player: costume burst record design mismatch")
	}
	if costume.BurstLevel != expectedCurrent {
		return errors.New("player: stale costume burst level")
	}
	ledgerKey := costumeBurstUpgradeKey(invenIndex, target)
	if _, exists := s.data.CostumeBurstUpgrades[ledgerKey]; exists {
		return errors.New("player: costume burst transition already recorded")
	}
	next := cloneCollection(s.data)
	next.CostumeBurstLevels[strconv.FormatUint(invenIndex, 10)] = target
	record.Body = append([]byte(nil), record.Body...)
	next.CostumeBurstUpgrades[ledgerKey] = record
	return s.commit(ctx, next)
}

func validateCostumeBurstRecord(record CostumeBurstUpgradeRecord) error {
	if record.CostumeID == 0 || record.Level == 0 || record.Code != 578 || len(record.Digest) != costumeBurstDigestHexSize {
		return errors.New("player: invalid costume burst upgrade record")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil {
		return errors.New("player: invalid costume burst upgrade digest")
	}
	level, found, err := wire.Varint(record.Body, 1)
	if err != nil || !found || level != record.Level {
		return errors.New("player: invalid costume burst upgrade response")
	}
	fieldCount := 0
	if err := wire.Walk(record.Body, func(field wire.Field) error {
		if field.Number != 1 || field.Type != 0 {
			return errors.New("unexpected costume burst response field")
		}
		fieldCount++
		return nil
	}); err != nil || fieldCount != 1 {
		return errors.New("player: invalid costume burst upgrade response")
	}
	return nil
}

func (s *CollectionStore) validateCostumeBurstStorage() error {
	if s.data.CostumeBurstLevels == nil || s.data.CostumeBurstUpgrades == nil {
		return errors.New("player: collection save requires costume burst ledgers")
	}
	owned := make(map[uint64]Costume, len(s.base)+len(s.data.Costumes))
	for _, costume := range append(append([]Costume(nil), s.base...), s.data.Costumes...) {
		if costume.InvenIndex == 0 || costume.ID == 0 {
			return errors.New("player: invalid costume burst ownership")
		}
		if _, exists := owned[costume.InvenIndex]; exists {
			return errors.New("player: duplicate costume burst inventory index")
		}
		owned[costume.InvenIndex] = costume
	}
	for key, level := range s.data.CostumeBurstLevels {
		index, err := strconv.ParseUint(key, 10, 64)
		costume, found := owned[index]
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) || level == 0 || !found || level < costume.BurstLevel {
			return fmt.Errorf("player: invalid costume burst level entry %q", key)
		}
	}
	for key, record := range s.data.CostumeBurstUpgrades {
		index, level, valid := parseCostumeBurstUpgradeKey(key)
		costume, found := owned[index]
		current := costume.BurstLevel
		if overlay, exists := s.data.CostumeBurstLevels[strconv.FormatUint(index, 10)]; exists {
			current = overlay
		}
		if !valid || !found || record.CostumeID != costume.ID || record.Level != level || level > current {
			return fmt.Errorf("player: invalid costume burst upgrade entry %q", key)
		}
		if err := validateCostumeBurstRecord(record); err != nil {
			return fmt.Errorf("player: invalid costume burst upgrade entry %q: %w", key, err)
		}
	}
	return nil
}
