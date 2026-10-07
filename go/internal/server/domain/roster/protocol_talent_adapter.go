package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"time"
)

func (s *TalentUseService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/CharHealing" {
		return s.charHealing(ctx, request)
	}
	if path != "/TalentSkillUse" {
		return 0, nil, false, nil
	}

	fail := func(e error) (int, []byte, bool, error) { return 43, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || ctx.SessionID == "" {
		return fail(fmt.Errorf("player: invalid talent request sequence"))
	}
	index, _, err := wire.Varint(request, 2)
	if err != nil || index > math.MaxInt64 {
		return fail(fmt.Errorf("player: invalid talent caster"))
	}
	food, _, err := wire.Varint(request, 4)
	if err != nil || food > math.MaxInt64 || (index == 0) == (food == 0) {
		return fail(fmt.Errorf("player: talent requires a character or a skill food"))
	}
	var targets []uint64
	seen := map[uint64]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("player: invalid talent target wire")
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 || v == 0 || v > math.MaxInt32 || seen[v] || len(targets) >= 4096 {
				return fmt.Errorf("player: invalid or duplicate talent target")
			}
			raw = raw[n:]
			seen[v] = true
			targets = append(targets, v)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	state, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	key := ctx.SessionID + ":" + fmt.Sprint(seq)
	if prior, ok := state.Receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed talent replay"))
		}
		return 43, prior.Body, true, nil
	}
	var character Character
	var meta gamedata.TalentUseCharacter
	var foodItem assets.Item
	if food > 0 {
		for _, item := range s.inventory.All(ctx) {
			if item.InvenIndex == food && item.Type == 5 && item.Count > 0 {
				foodItem = item
				break
			}
		}
		group, exists := s.design.Foods[foodItem.ID]
		if !exists {
			return fail(fmt.Errorf("player: unavailable skill food"))
		}
		meta = gamedata.TalentUseCharacter{Group: group, MaxLevel: 1}
		character.TalentLevel = 1
	} else {
		var owned bool
		character, owned = s.characters.Find(ctx, index)
		if !owned || character.TalentLevel == 0 {
			return fail(fmt.Errorf("player: talent caster unavailable"))
		}
		var exists bool
		meta, exists = s.design.Characters[character.ID]
		if !exists || character.TalentLevel > meta.MaxLevel {
			return fail(fmt.Errorf("player: character talent design unavailable"))
		}
	}
	rule, ok := s.design.Rules[[2]uint64{meta.Group, character.TalentLevel}]
	if !ok {
		return fail(fmt.Errorf("player: talent level design unavailable"))
	}
	if s.context == nil {
		return fail(fmt.Errorf("player: talent field context unavailable"))
	}
	pack, mapID, battle, err := s.context(ctx)
	if err != nil {
		return fail(err)
	}
	if battle || meta.BannedPacks[pack] {
		return fail(fmt.Errorf("player: talent unavailable in current field"))
	}
	if rule.Class == 7 || rule.Class == 8 || rule.Class == 9 || rule.Class == 10 || rule.Class == 14 {
		return fail(fmt.Errorf("player: talent class %d uses its dedicated crafting/recovery packet", rule.Class))
	}
	day := s.now().UTC().Add(9*time.Hour - s.design.ResetSchedule.DailyReset).Format("2006-01-02")
	current := state.Skills[rule.Group]
	dailyLimit := rule.Class == 3 || rule.Class == 4 || rule.Class == 20
	if (rule.Reset == 1 || dailyLimit) && current.Day != day {
		current.Count = 0
		current.Day = day
	}
	if dailyLimit && (len(rule.Values) == 0 || current.Count >= uint64(rule.Values[0])) {
		return fail(fmt.Errorf("player: talent daily usage limit reached"))
	}
	cooldown := talentHasCooldown(rule.Class)
	if cooldown && current.End > s.now().UnixMilli() {
		return fail(fmt.Errorf("player: talent effect is already active"))
	}
	identity := "talent-use:" + hex.EncodeToString(sha256Sum([]byte(key)))
	multiplier := uint64(1)
	if rule.Class != 4 && rule.Class != 20 && len(targets) > 0 {
		multiplier = uint64(len(targets))
	}
	if food > 0 {
		rule.Catalyst = 0
		rule.Experience = 0
		foodItem.Count = 1
		if err = s.inventory.CanConsume(ctx, []assets.Item{foodItem}); err != nil {
			return fail(err)
		}
	}
	if rule.Catalyst > math.MaxUint64/multiplier || rule.Catalyst > 0 && !s.wallet.CanSpendCatalyst(rule.Catalyst*multiplier) {
		return fail(fmt.Errorf("player: insufficient talent catalyst"))
	}
	var extra []byte
	success := true
	if talentNPCClass(rule.Class) {
		if len(targets) != 1 || s.design.NPCs == nil {
			return fail(fmt.Errorf("player: talent requires one NPC"))
		}
		npcs, e := s.design.NPCs(pack)
		if e != nil {
			return fail(e)
		}
		npc, ok := npcs[targets[0]]
		if !ok || npc.MapID != mapID {
			return fail(fmt.Errorf("player: talent NPC outside current map"))
		}
		matched := -1
		for i, g := range npc.Groups {
			if g == rule.Group {
				matched = i
				break
			}
		}
		if matched < 0 {
			return fail(fmt.Errorf("player: NPC does not support talent"))
		}
		npcKey := fmt.Sprintf("%d/%d/%d", pack, npc.ID, rule.Group)
		if rule.Class != 16 && state.NPCs[npcKey] > s.now().UnixMilli() {
			return fail(fmt.Errorf("player: NPC talent cooldown active"))
		}
		if rule.Class == 1 || rule.Class == 16 {
			probability := 100.0
			if len(rule.Values) > 0 {
				probability = rule.Values[0]
			}
			draw, e := rand.Int(rand.Reader, big.NewInt(1000000))
			if e != nil {
				return fail(e)
			}
			success = float64(draw.Int64()) < probability*10000
		}
		if success && npc.Rewards[matched] > 0 {
			rewards, ok := s.design.Rewards[npc.Rewards[matched]]
			if !ok {
				return fail(fmt.Errorf("player: missing NPC talent reward"))
			}
			bundle, e := s.economy.Apply(ctx, identity, nil, rewards)
			if e != nil {
				return fail(e)
			}
			extra, e = talentBundleFields(bundle)
			if e != nil {
				return fail(e)
			}
		}
		if rule.Class == 19 && len(npc.CharmCharacters) > 0 {
			b, e := s.applyCharm(ctx, identity, character, rule, targets)
			if e != nil {
				return fail(e)
			}
			extra = append(extra, b...)
		}
		end := s.now().UnixMilli()
		if rule.Class == 16 {
			if len(rule.Values) < 2 || rule.Values[1] > 100 {
				return fail(fmt.Errorf("player: invalid bargain discount"))
			}
			if success {
				state.Discounts[ctx.SessionID+":"+fmt.Sprintf("%d/%d", pack, npc.ID)] = uint64(rule.Values[1])
			}
		} else if rule.Class == 1 && rule.Reset != 0 {
			end = s.nextNPCReset(rule.Reset).UnixMilli()
		} else if len(rule.Values) > 1 {
			end += int64(rule.Values[1]) * 1000
		}
		state.NPCs[npcKey] = end
		row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, npc.ID), 2, rule.Group), 3, uint64(end))
		extra = wire.AppendBytes(extra, 2, row)
		if !success && rule.Reputation > 0 {
			effect := s.effects[100]
			if effect == nil {
				return fail(fmt.Errorf("player: failed talent reputation executor unavailable"))
			}
			b, e := effect(ctx, identity, character, rule, targets)
			if e != nil {
				return fail(e)
			}
			extra = append(extra, b...)
		}
	} else if effect := s.effects[rule.Class]; effect != nil {
		extra, err = effect(ctx, identity, character, rule, targets)
		if err != nil {
			return fail(err)
		}
	} else if rule.Class == 4 || rule.Class == 18 || rule.Class == 20 {
		return fail(fmt.Errorf("player: field talent executor unavailable"))
	} else if len(targets) > 0 && rule.Class != 3 && rule.Class != 11 {
		return fail(fmt.Errorf("player: unexpected talent targets"))
	}
	if food > 0 {
		if err = s.inventory.Consume(ctx, []assets.Item{foodItem}); err != nil {
			return fail(err)
		}
	}
	if rule.Catalyst > 0 {
		if rule.Catalyst > math.MaxUint64/multiplier {
			return fail(fmt.Errorf("player: talent cost overflow"))
		}
		if _, err = s.wallet.SpendCatalystOnce(ctx, identity, rule.Catalyst*multiplier); err != nil {
			return fail(err)
		}
	}
	gain := rule.Experience
	if gain > 0 {
		maximum, e := s.experienceMaximum(character)
		if e != nil {
			return fail(e)
		}
		if character.TalentExp >= maximum {
			gain = 0
		} else if gain > maximum-character.TalentExp {
			gain = maximum - character.TalentExp
		}
		if gain > 0 {
			if _, e = s.characters.AddTalentExperience(ctx, index, gain, maximum); e != nil {
				return fail(e)
			}
		}
	}
	current.Count++
	current.Settled = false
	current.Level = rule.Level
	current.Day = day
	current.End = s.now().UnixMilli()
	if cooldown && len(rule.Values) > 1 {
		current.End += int64(rule.Values[1]) * 1000
	}
	state.Skills[rule.Group] = current
	out := append([]byte(nil), extra...)
	out = wire.AppendBytes(out, 1, talentUseWire(rule.Group, current, rule))
	if gain > 0 {
		out = wire.AppendVarint(out, 10, gain)
	}
	if success {
		out = wire.AppendVarint(out, 11, 1)
	}
	state.Receipts[key] = talentUseReceipt{Digest: digest, Body: out}
	b, err := json.Marshal(state)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save(ctx.State, "talentuse", b); err != nil {
		return fail(err)
	}
	return 43, out, true, nil
}

func (s *TalentDispatchService) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	if path != "/DispatchInfo" && path != "/DispatchReward" {
		return 0, nil, false, nil
	}

	seq, ok, e := wire.Varint(req, 1)
	if e != nil || !ok || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, fmt.Errorf("dispatch: missing sequence")
	}
	st, e := s.load(ctx)
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/DispatchInfo" {
		var b []byte
		ids := []uint64{}
		for id, r := range st.Rows {
			if !r.Claimed {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			b = wire.AppendBytes(b, 1, s.rowWire(st.Rows[id]))
		}
		return 0, b, true, nil
	}
	if ctx.SessionID == "" {
		return 108, nil, true, fmt.Errorf("dispatch: claim session unavailable")
	}
	var ids []uint64
	e = wire.Walk(req, func(f wire.Field) error {
		if f.Number != 2 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("dispatch: invalid ids")
		}
		b := f.Value
		for len(b) > 0 {
			n, k := binary.Uvarint(b)
			if k <= 0 || n == 0 || n > math.MaxInt32 || len(ids) >= len(s.design) {
				return fmt.Errorf("dispatch: invalid packed ids")
			}
			ids = append(ids, n)
			b = b[k:]
		}
		return nil
	})
	if e != nil || len(ids) == 0 {
		return 0, nil, true, fmt.Errorf("dispatch: missing reward ids")
	}
	digest := sha256.Sum256(req)
	key := fmt.Sprintf("%s:%d", ctx.SessionID, seq)
	if receipt, ok := st.Claims[key]; ok {
		if receipt.Digest != fmt.Sprintf("%x", digest) {
			return 0, nil, true, fmt.Errorf("dispatch: conflicting claim sequence")
		}
		return 108, append([]byte(nil), receipt.Body...), true, nil
	}
	seen := map[uint64]bool{}
	for _, id := range ids {
		r, ok := st.Rows[id]
		if !ok || seen[id] || s.now().UnixMilli() < r.End {
			return 0, nil, true, fmt.Errorf("dispatch: reward not ready")
		}
		seen[id] = true
	}
	var body []byte
	for _, id := range ids {
		r := st.Rows[id]
		if r.Claimed {
			continue
		}
		if !r.Claimed {
			bundle, e := s.economy.Apply(ctx, r.Identity+fmt.Sprintf(":dispatch:%d", id), nil, r.Rewards)
			if e != nil {
				return 0, nil, true, e
			}
			r.Bundle = bundle
			r.Claimed = true
			st.Rows[id] = r
		}
		if e = wire.Walk(r.Bundle, func(f wire.Field) error {
			if f.Number == 1 && f.Type == 2 {
				body = wire.AppendBytes(body, 1, f.Value)
			}
			return nil
		}); e != nil {
			return 0, nil, true, e
		}
	}
	st.Claims[key] = talentDispatchClaim{Digest: fmt.Sprintf("%x", digest), Body: body}
	return 108, body, true, s.save(ctx, st)
}

func (s *ItemCraftService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	var recipes map[uint64]gamedata.ItemCraftRecipe
	var code int
	switch path {
	case "/Cooking":
		recipes = s.design.Cooking
		code = 48
	case "/Alchemy":
		recipes = s.design.Alchemy
		code = 49
	case "/AlchemyBatch":
		recipes = s.design.Alchemy
		code = 262
	default:
		return 0, nil, false, nil
	}

	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if ctx.SessionID == "" || s.context == nil {
		return fail(fmt.Errorf("craft: session/context unavailable"))
	}
	values := map[int]uint64{}
	err := wire.Walk(request, func(f wire.Field) error {
		if f.Number >= 1 && f.Number <= 4 {
			if f.Type != 0 {
				return fmt.Errorf("craft: invalid scalar")
			}
			if _, exists := values[f.Number]; exists {
				return fmt.Errorf("craft: duplicate scalar")
			}
			v, _, err := wire.Varint(request, f.Number)
			if err != nil {
				return err
			}
			values[f.Number] = v
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	seq, index, recipeID, count := values[1], values[2], values[3], values[4]
	if seq == 0 || seq > math.MaxInt32 || index == 0 || index > math.MaxInt64 || recipeID == 0 || recipeID > math.MaxInt32 || count == 0 || count > math.MaxInt32 {
		return fail(fmt.Errorf("craft: invalid request"))
	}
	keyHash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)))
	key := hex.EncodeToString(keyHash[:])
	digestHash := sha256.Sum256(request)
	digest := hex.EncodeToString(digestHash[:])
	var receipts map[string]itemCraftReceipt
	raw, err := s.store.Load(ctx.State, "itemcraft")
	if err != nil {
		return fail(err)
	}
	if raw != nil && json.Unmarshal(raw, &receipts) != nil {
		return fail(fmt.Errorf("craft: invalid receipt state"))
	}
	if receipts == nil {
		receipts = map[string]itemCraftReceipt{}
	}
	if r, exists := receipts[key]; exists {
		if r.Digest != digest {
			return fail(fmt.Errorf("craft: changed request replay"))
		}
		return code, r.Body, true, nil
	}
	pack, battle, err := s.context(ctx)
	if err != nil {
		return fail(err)
	}
	if battle || pack <= 0 {
		return fail(fmt.Errorf("craft: unavailable in current scene"))
	}
	recipe, exists := recipes[recipeID]
	if !exists {
		return fail(fmt.Errorf("craft: unknown recipe"))
	}
	if recipe.Class == 7 && !s.known(ctx, recipeID) {
		return fail(fmt.Errorf("craft: recipe not learned"))
	}
	character, owned := s.characters.Find(ctx, index)
	if !owned || IsStoryCharacter(character) {
		return fail(fmt.Errorf("craft: unavailable producer"))
	}
	talent, exists := s.talents.Characters[character.ID]
	if !exists || talent.BannedPacks[pack] {
		return fail(fmt.Errorf("craft: producer talent blocked in pack"))
	}
	current := s.talents.Rules[[2]uint64{talent.Group, character.TalentLevel}]
	limitIndex := 0
	if recipe.Class == 7 {
		limitIndex = 1
	}
	// Batch count is the missing intermediate quantity requested by equipment
	// making (EquipmentMakingUI.OnClickUI), not the AlchemyUI craft slider.
	// Its full material graph and int32 quantities are validated below.
	if path != "/AlchemyBatch" && (len(current.Values) <= limitIndex || current.Values[limitIndex] < 1 || math.IsNaN(current.Values[limitIndex]) || math.IsInf(current.Values[limitIndex], 0) || count > uint64(current.Values[limitIndex])) {
		return fail(fmt.Errorf("craft: count exceeds talent limit"))
	}
	gain, catalyst, maximum, err := s.talents.CraftTalent(character.ID, character.TalentLevel, recipe.Class, recipe.TalentLevel, count, character.TalentExp)
	if err != nil {
		return fail(err)
	}
	materials, err := assets.DecodeItemRequest(request, 5, path)
	if err != nil {
		return fail(err)
	}
	if path == "/AlchemyBatch" {
		gain, catalyst, maximum, err = s.prepareAlchemyBatch(character, recipe, count, materials)
	} else {
		err = assets.ValidateMakingMaterials(recipe.Costs, count, materials)
		// Conversion recipes charge per produced resource, as AlchemyUI.GetNeededCurrency does.
		if err == nil && recipe.Class == 8 && recipe.Category == 2 {
			if recipe.Result.Count == 0 || catalyst > math.MaxInt32/recipe.Result.Count {
				err = fmt.Errorf("craft: catalyst overflow")
			} else {
				catalyst *= recipe.Result.Count
			}
		}
	}
	if err != nil {
		return fail(err)
	}
	if err = s.items.CanConsume(ctx, materials); err != nil {
		return fail(err)
	}
	if catalyst > 0 && !s.wallet.CanSpendCatalyst(catalyst) {
		return fail(fmt.Errorf("craft: insufficient catalyst"))
	}
	if recipe.Result.Count > math.MaxInt32/count {
		return fail(fmt.Errorf("craft: result quantity overflow"))
	}
	result := recipe.Result
	result.Count *= count
	if path == "/AlchemyBatch" {
		result.Count = count
	}
	if err = s.items.Consume(ctx, materials); err != nil {
		return fail(err)
	}
	if catalyst > 0 {
		if _, err = s.wallet.SpendCatalystOnce(ctx, "itemcraft:"+key, catalyst); err != nil {
			return fail(err)
		}
	}
	granted, err := s.items.GrantOnce(ctx, "itemcraft:"+key, []gamedata.BattleReward{result})
	if err != nil {
		return fail(err)
	}
	if gain > 0 {
		if _, err = s.characters.AddTalentExperience(ctx, index, gain, maximum); err != nil {
			return fail(err)
		}
	}
	var response []byte
	for _, item := range granted {
		response = wire.AppendBytes(response, 1, assets.ItemWire(item))
	}
	if gain > 0 {
		response = wire.AppendVarint(response, 2, gain)
	}
	receipts[key] = itemCraftReceipt{Digest: digest, Body: response}
	raw, err = json.Marshal(receipts)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save(ctx.State, "itemcraft", raw); err != nil {
		return fail(err)
	}
	return code, response, true, nil
}

func (s *FoodService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/EatFoodAuto" {
		return s.eatFoodAuto(ctx, request)
	}
	if path != "/EatFood" {
		return 0, nil, false, nil
	}
	var seq, pack, index uint64
	seenHeaders := map[int]bool{}
	parseErr := wire.Walk(request, func(field wire.Field) error {
		if field.Number == 4 {
			return nil
		}
		target := map[int]*uint64{1: &seq, 2: &pack, 3: &index}[field.Number]
		if target == nil || field.Type != 0 || seenHeaders[field.Number] {
			return errors.New("player: invalid EatFood header")
		}
		seenHeaders[field.Number] = true
		*target, _ = binary.Uvarint(field.Value)
		return nil
	})
	if parseErr != nil || seq == 0 || seq > math.MaxInt32 || pack > math.MaxInt32 || index == 0 || index > math.MaxInt64 {
		return 0, nil, true, errors.New("player: invalid EatFood request")
	}
	items, err := assets.DecodeItemRequest(request, 4, "EatFood")
	if err != nil || len(items) == 0 {
		return 0, nil, true, errors.New("player: EatFood requires food stacks")
	}

	if ctx.SessionID == "" || s.currentPack == nil || s.battleActive == nil {
		return 0, nil, true, errors.New("player: EatFood session or context unavailable")
	}
	keyDigest := sha256.Sum256([]byte(ctx.SessionID + ":EatFood:" + strconv.FormatUint(seq, 10)))
	key := hex.EncodeToString(keyDigest[:])
	digest := sha256.Sum256(request)
	digestString := hex.EncodeToString(digest[:])
	prior, found, err := s.store.LoadEntry(ctx.State, "characters", "food_requests", key)
	if err != nil {
		return 0, nil, true, err
	}
	if found {
		var reply foodReply
		if json.Unmarshal(prior, &reply) != nil || reply.Digest != digestString {
			return 0, nil, true, errors.New("player: EatFood sequence reused with different request")
		}
		return 22, append([]byte(nil), reply.Body...), true, nil
	}
	currentPack, err := s.currentPack(ctx)
	if err != nil || currentPack <= 0 || (pack != 0 && pack != uint64(currentPack)) {
		return 0, nil, true, errors.New("player: EatFood pack unavailable")
	}
	if s.battleActive(ctx) {
		return 0, nil, true, errors.New("player: cannot eat food during battle")
	}
	character, err := s.recoverCharacter(ctx, index, items)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.CanConsume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	body := wire.AppendBytes(nil, 1, CharacterWire(character))
	if err := s.inventory.Consume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	healthRaw, _ := json.Marshal(character.HP)
	replyRaw, _ := json.Marshal(foodReply{Digest: digestString, Body: body})
	if err := s.store.SaveWithEntries(ctx.State, "characters", nil, []stateio.EntryMutation{{Bucket: "current_hp", Key: strconv.FormatUint(index, 10), Payload: healthRaw}, {Bucket: "food_requests", Key: key, Payload: replyRaw}}); err != nil {
		return 0, nil, true, err
	}
	return 22, body, true, nil
}

func talentUseWire(group uint64, state talentUseState, rule gamedata.TalentUseRule) []byte {
	b := wire.AppendVarint(nil, 1, group)
	if state.End > 0 {
		b = wire.AppendVarint(b, 2, uint64(state.End))
	}
	if talentHasCooldown(rule.Class) && len(rule.Values) > 1 {
		b = wire.AppendVarint(b, 3, uint64(rule.Values[1]))
	}
	return wire.AppendVarint(b, 4, state.Count)
}

func talentBundleFields(bundle []byte) ([]byte, error) {
	var out []byte
	err := wire.Walk(bundle, func(f wire.Field) error {
		to := map[int]int{1: 3, 2: 5, 3: 6, 4: 4}[f.Number]
		if to > 0 {
			out = wire.AppendBytes(out, to, f.Value)
		}
		return nil
	})
	return out, err
}

func (s *TalentUseService) PackInfo(ctx command.Context, pack int) ([]byte, error) {

	v, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	var groups []uint64
	for g := range v.Skills {
		groups = append(groups, g)
	}
	slices.Sort(groups)
	var b []byte
	for _, g := range groups {
		state := v.Skills[g]
		rule := s.design.Rules[[2]uint64{g, state.Level}]
		day := s.now().UTC().Add(9*time.Hour - s.design.ResetSchedule.DailyReset).Format("2006-01-02")
		if (rule.Reset == 1 || rule.Class == 3 || rule.Class == 4 || rule.Class == 20) && state.Day != day {
			state.Count = 0
		}
		b = wire.AppendBytes(b, 13, talentUseWire(g, state, rule))
	}
	var npcKeys []string
	for key := range v.NPCs {
		npcKeys = append(npcKeys, key)
	}
	sort.Strings(npcKeys)
	for _, key := range npcKeys {
		end := v.NPCs[key]
		var p int
		var npc, g uint64
		if _, e = fmt.Sscanf(key, "%d/%d/%d", &p, &npc, &g); e != nil {
			return nil, e
		}
		if p == pack {
			row := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, npc), 2, g), 3, uint64(end))
			b = wire.AppendBytes(b, 5, row)
		}
	}
	return b, nil
}

func (s *CharacterStore) talentSkillUpgrade(ctx command.Context, request []byte) (int, []byte, bool, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: TalentSkillUpgrade missing sequence")
	}
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 || index > math.MaxInt64 {
		return 0, nil, true, errors.New("player: TalentSkillUpgrade missing character")
	}
	materials, err := assets.DecodeItemRequest(request, 3, "TalentSkillUpgrade")
	if err != nil {
		return 0, nil, true, err
	}
	for _, material := range materials {
		if material.InvenIndex > math.MaxInt64 || material.ID > math.MaxInt32 || material.Type > math.MaxInt32 || material.Count > math.MaxInt32 ||
			material.KeepFlag > math.MaxInt32 || material.TimeValue > math.MaxInt64 || material.ExpiryTime > math.MaxInt64 ||
			material.SortID > math.MaxInt32 || material.UseCount > math.MaxInt32 {
			return 0, nil, true, errors.New("player: TalentSkillUpgrade material exceeds protocol range")
		}
	}

	digestBytes := sha256.Sum256(request)
	digest := hex.EncodeToString(digestBytes[:])
	sessionID := ctx.SessionID
	if sessionID == "" {
		return 0, nil, true, errors.New("player: TalentSkillUpgrade requires an authenticated session")
	}
	if s.talentReplies[sessionID] == nil {
		s.talentReplies[sessionID] = make(map[string]talentUpgradeReply)
	}
	replies := s.talentReplies[sessionID]
	cacheKey := "seq:" + strconv.FormatUint(seq, 10)
	if reply, ok := replies[cacheKey]; ok {
		if reply.Digest != digest {
			return 0, nil, true, errors.New("player: TalentSkillUpgrade sequence reused with different request")
		}
		return reply.Code, append([]byte(nil), reply.Body...), true, nil
	}
	if s.talentGrowth == nil || s.wallet == nil || s.inventory == nil {
		return 0, nil, true, errors.New("player: talent upgrade unavailable")
	}

	position := -1
	var current Character
	fromCollection := false
	for i, character := range s.characters {
		if character.InvenIndex == index {
			position, current = i, character
			break
		}
	}
	if position < 0 && s.collection != nil {
		current, fromCollection = s.collection.FindCharacter(index)
	}
	if position < 0 && !fromCollection {
		return 0, nil, true, fmt.Errorf("player: unknown talent character inventory index %d", index)
	}
	previousLedgerKey := strconv.FormatUint(index, 10) + ":" + strconv.FormatUint(current.TalentLevel, 10)
	if reply, ok := s.talentApplied[previousLedgerKey]; ok && reply.Digest == digest {
		replies[cacheKey] = reply
		return reply.Code, append([]byte(nil), reply.Body...), true, nil
	}
	rule, err := s.talentGrowth.UpgradeRule(current.ID, current.TalentLevel)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: resolve talent upgrade: %w", err)
	}
	if current.TalentExp < rule.RequiredTotalExp {
		return 0, nil, true, fmt.Errorf("player: character %d talent experience %d is below required %d", index, current.TalentExp, rule.RequiredTotalExp)
	}

	items, gold, err := validateTalentUpgradeMaterials(rule.Costs, materials)
	if err != nil {
		return 0, nil, true, err
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for talent upgrade")
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate talent upgrade items: %w", err)
		}
	}

	previousID := current.ID
	current.TalentLevel++
	ledgerKey := strconv.FormatUint(index, 10) + ":" + strconv.FormatUint(current.TalentLevel, 10)
	if _, exists := s.talentApplied[ledgerKey]; exists {
		return 0, nil, true, fmt.Errorf("player: talent upgrade ledger already contains target %s", ledgerKey)
	}
	if fromCollection {
		if err := s.collection.CanUpdateCharacter(ctx, previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate collection talent upgrade: %w", err)
		}
	}
	identity := "talent-upgrade:" + sessionID + ":" + cacheKey + ":character:" + strconv.FormatUint(index, 10) + ":level:" + strconv.FormatUint(rule.CurrentLevel, 10)
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(ctx, identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume talent upgrade gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(ctx, items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume talent upgrade items: %w", err)
		}
	}
	reply := talentUpgradeReply{Digest: digest, Code: talentSkillUpgradePacketCode}
	ledgerPayload, err := json.Marshal(reply)
	if err != nil {
		return 0, nil, true, err
	}
	ledgerChange := stateio.EntryMutation{Bucket: "talent_upgrades", Key: ledgerKey, Payload: ledgerPayload}
	if fromCollection {
		if err := s.collection.UpdateCharacter(ctx, previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist collection talent upgrade: %w", err)
		}
		if err := s.store.SaveWithEntries(ctx.State, "characters", nil, []stateio.EntryMutation{ledgerChange}); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist talent upgrade replay ledger: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persistWithChanges(ctx, next, []stateio.EntryMutation{ledgerChange}); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist talent upgrade: %w", err)
		}
		s.characters = next
	}
	// TalentSkillUpgradeResponse.item_info is an optional grant list. Current
	// GameData defines no refund, so the correct protobuf response is empty.
	s.talentApplied[ledgerKey] = reply
	replies[cacheKey] = reply
	return talentSkillUpgradePacketCode, nil, true, nil
}

func (s *TalentUseService) charHealing(ctx command.Context, request []byte) (int, []byte, bool, error) {
	return s.healing(ctx, request, "healing")
}

func (s *TalentUseService) healing(ctx command.Context, request []byte, operation string) (int, []byte, bool, error) {

	fail := func(e error) (int, []byte, bool, error) { return 62, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || ctx.SessionID == "" {
		return fail(fmt.Errorf("player: invalid healing sequence"))
	}
	index, _, err := wire.Varint(request, 2)
	if err != nil || index > math.MaxInt64 {
		return fail(fmt.Errorf("player: invalid healing caster"))
	}
	food, _, err := wire.Varint(request, 4)
	if err != nil || food > math.MaxInt64 || (index == 0) == (food == 0) {
		return fail(fmt.Errorf("player: healing requires caster or food"))
	}
	state, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	key := ctx.SessionID + ":" + operation + ":" + fmt.Sprint(seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	if prior, ok := state.Receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed healing replay"))
		}
		return 62, prior.Body, true, nil
	}
	var ids []uint64
	seen := map[uint64]bool{}
	err = wire.Walk(request, func(f wire.Field) error {
		if f.Number != 3 {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("player: invalid healing targets")
		}
		for raw := f.Value; len(raw) > 0; {
			id, n := binary.Uvarint(raw)
			if n <= 0 || id == 0 || id > math.MaxInt64 || seen[id] || len(ids) >= 4096 {
				return fmt.Errorf("player: invalid healing target")
			}
			raw = raw[n:]
			seen[id] = true
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return fail(fmt.Errorf("player: missing healing targets"))
	}
	if s.context == nil {
		return fail(fmt.Errorf("player: healing field context unavailable"))
	}
	pack, _, battle, err := s.context(ctx)
	if err != nil {
		return fail(err)
	}
	if battle {
		return fail(fmt.Errorf("player: healing unavailable during battle"))
	}
	var character Character
	var rule gamedata.TalentUseRule
	var item assets.Item
	if food > 0 {
		for _, v := range s.inventory.All(ctx) {
			if v.InvenIndex == food && v.Type == 5 {
				item = v
				break
			}
		}
		g, ok := s.design.Foods[item.ID]
		if !ok {
			return fail(fmt.Errorf("player: invalid revival food"))
		}
		rule, ok = s.design.Rules[[2]uint64{g, 1}]
		if !ok {
			return fail(fmt.Errorf("player: missing revival food talent"))
		}
		item.Count = uint64(len(ids))
		if err = s.inventory.CanConsume(ctx, []assets.Item{item}); err != nil {
			return fail(err)
		}
	} else {
		var owned bool
		character, owned = s.characters.Find(ctx, index)
		meta, ok := s.design.Characters[character.ID]
		if !owned || !ok || meta.BannedPacks[pack] {
			return fail(fmt.Errorf("player: invalid healing caster"))
		}
		rule, ok = s.design.Rules[[2]uint64{meta.Group, character.TalentLevel}]
		if !ok {
			return fail(fmt.Errorf("player: missing recovery level"))
		}
	}
	if rule.Class != 10 || len(rule.Values) == 0 || rule.Values[0] > 100 {
		return fail(fmt.Errorf("player: invalid fatigue recovery skill"))
	}
	var targets []Character
	for _, id := range ids {
		c, owned := s.characters.Find(ctx, id)
		if !owned {
			return fail(fmt.Errorf("player: revival target not owned"))
		}
		hp, e := s.characters.CurrentHealth(ctx, id)
		if e != nil {
			return fail(e)
		}
		if hp != 0 {
			return fail(fmt.Errorf("player: revival target is not fatigued"))
		}
		maximum, e := s.characters.MaxHealth(ctx, id)
		if e != nil {
			return fail(e)
		}
		c.HP = 1
		if rule.Values[0] > 0 {
			c.HP = uint64(float64(maximum) * rule.Values[0] / 100)
			if c.HP == 0 {
				c.HP = 1
			}
		}
		targets = append(targets, c)
	}
	identity := "talent-healing:" + key
	if food > 0 {
		if err = s.inventory.Consume(ctx, []assets.Item{item}); err != nil {
			return fail(err)
		}
	} else {
		if rule.Catalyst > math.MaxUint64/uint64(len(ids)) {
			return fail(fmt.Errorf("player: recovery cost overflow"))
		}
		if rule.Catalyst > 0 {
			if rule.Catalyst > 0 {
				if _, err = s.wallet.SpendCatalystOnce(ctx, identity, rule.Catalyst*uint64(len(ids))); err != nil {
					return fail(err)
				}
			}
		}
	}
	var out []byte
	for _, c := range targets {
		if err = s.characters.SetCurrentHealth(ctx, c.InvenIndex, c.HP); err != nil {
			return fail(err)
		}
		out = wire.AppendBytes(out, 1, CharacterWire(c))
	}
	if food == 0 && rule.Experience > 0 {
		maximum, e := s.experienceMaximum(character)
		if e != nil {
			return fail(e)
		}
		gain := rule.Experience * uint64(len(ids))
		if character.TalentExp >= maximum {
			gain = 0
		} else if gain > maximum-character.TalentExp {
			gain = maximum - character.TalentExp
		}
		if gain > 0 {
			if _, e = s.characters.AddTalentExperience(ctx, index, gain, maximum); e != nil {
				return fail(e)
			}
			out = wire.AppendVarint(out, 2, gain)
		}
	}
	state.Receipts[key] = talentUseReceipt{Digest: digest, Body: out}
	b, err := json.Marshal(state)
	if err != nil {
		return fail(err)
	}
	if err = s.store.Save(ctx.State, "talentuse", b); err != nil {
		return fail(err)
	}
	return 62, out, true, nil
}

func (s *TalentDispatchService) rowWire(r talentDispatchRow) []byte {
	b := wire.AppendVarint(nil, 1, r.ID)
	b = wire.AppendVarint(b, 2, uint64(s.now().UnixMilli()))
	return wire.AppendVarint(b, 3, uint64(r.End))
}

// Start returns extra TalentSkillUseResponse fields. Its caller owns talent
// cost/experience/cooldown and the encompassing account transaction.
func (s *TalentDispatchService) Start(ctx command.Context, identity string, character Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error) {

	if identity == "" || rule.Class != 18 || character.InvenIndex == 0 || len(targets) == 0 {
		return nil, fmt.Errorf("dispatch: invalid start")
	}
	st, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	if b, ok := st.Starts[identity]; ok {
		return append([]byte(nil), b...), nil
	}
	allowed := map[uint64]bool{}
	for _, v := range rule.Values {
		if v > 0 {
			allowed[uint64(v)] = true
		}
	}
	prefabs := map[string]bool{}
	for id, r := range st.Rows {
		if !r.Claimed {
			prefabs[s.design[id].Prefab] = true
		}
	}
	var rows []talentDispatchRow
	for _, id := range targets {
		d, ok := s.design[id]
		if !ok || !allowed[id] || prefabs[d.Prefab] {
			return nil, fmt.Errorf("dispatch: unavailable dispatch %d", id)
		}
		prefabs[d.Prefab] = true
		rs, e := d.Roll(s.draw)
		if e != nil {
			return nil, e
		}
		r := talentDispatchRow{ID: id, Start: s.now().UnixMilli(), Identity: identity}
		r.End = d.EndTime(time.UnixMilli(r.Start)).UnixMilli()
		for _, reward := range rs {
			r.Rewards = append(r.Rewards, gamedata.Reward(reward))
		}
		rows = append(rows, r)
	}
	var b []byte
	for _, r := range rows {
		st.Rows[r.ID] = r
		b = wire.AppendBytes(b, 8, s.rowWire(r))
	}
	st.Starts[identity] = b
	return b, s.save(ctx, st)
}

// Charm instances are field companions. They live only in the character domain
// with an expiry, never grant a permanent collection character or costume.
func (s *TalentUseService) applyCharm(ctx command.Context, _ string, _ Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error) {
	pack, _, _, err := s.context(ctx)
	if err != nil {
		return nil, err
	}
	npcs, err := s.design.NPCs(pack)
	if err != nil {
		return nil, err
	}
	npc := npcs[targets[0]]
	if s.design.CharmCharacter == nil || len(rule.Values) < 2 {
		return nil, fmt.Errorf("player: charm character design unavailable")
	}
	var chars []Character
	expiry := uint64(s.now().UnixMilli() + int64(rule.Values[1])*1000)
	for _, id := range npc.CharmCharacters {
		d, e := s.design.CharmCharacter(id)
		if e != nil {
			return nil, e
		}
		if d.CharacterID == 0 || d.CostumeID == 0 || d.HP == 0 {
			return nil, fmt.Errorf("player: invalid charm companion design")
		}
		if pack <= 0 || pack >= 65536 || d.CharacterID >= 1<<32 {
			return nil, fmt.Errorf("player: charm instance namespace overflow")
		}
		index := CharmCharacterIndexBase | uint64(pack)<<32 | d.CharacterID
		chars = append(chars, Character{InvenIndex: index, ID: d.CharacterID, Level: d.Level, HP: d.HP, CostumeID: d.CostumeID, TalentLevel: 1, ExpiryTime: expiry})
	}
	if err = s.characters.ensureCharmCharacters(ctx, chars); err != nil {
		return nil, err
	}
	var out []byte
	for _, c := range chars {
		out = wire.AppendBytes(out, 5, CharacterWire(c))
	}
	return out, nil
}

func OpenFoodService(ctx command.Context, store stateio.Store, design *gamedata.FoodDesign, inventory *assets.Inventory, characters *CharacterStore) (*FoodService, error) {
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok || design == nil || inventory == nil || characters == nil {
		return nil, errors.New("player: incomplete food configuration")
	}
	s := &FoodService{store: entries, design: design, inventory: inventory, characters: characters}
	health, err := entries.ListEntries(ctx.State, "characters", "current_hp")
	if err != nil {
		return nil, err
	}
	for key, raw := range health {
		index, err := strconv.ParseUint(key, 10, 64)
		var hp uint64
		if err != nil || index == 0 || json.Unmarshal(raw, &hp) != nil {
			return nil, errors.New("player: malformed saved current health")
		}
		if _, err := characters.MaxHealth(ctx, index); err != nil {
			return nil, err
		}
	}
	replies, err := entries.ListEntries(ctx.State, "characters", "food_requests")
	if err != nil {
		return nil, err
	}
	for key, raw := range replies {
		var reply foodReply
		decodedKey, keyErr := hex.DecodeString(key)
		if keyErr != nil || len(decodedKey) != sha256.Size || json.Unmarshal(raw, &reply) != nil || len(reply.Body) == 0 {
			return nil, errors.New("player: malformed saved food replay")
		}
		digest, err := hex.DecodeString(reply.Digest)
		if err != nil || len(digest) != sha256.Size {
			return nil, errors.New("player: malformed saved food replay digest")
		}
		if err := wire.Walk(reply.Body, func(field wire.Field) error { return nil }); err != nil {
			return nil, errors.New("player: malformed saved food replay body")
		}
	}
	return s, nil
}

func (s *FoodService) eatFoodAuto(ctx command.Context, request []byte) (int, []byte, bool, error) {
	var seq uint64
	var targets []struct {
		index uint64
		items []assets.Item
	}
	seenSequence := false
	err := wire.Walk(request, func(field wire.Field) error {
		switch field.Number {
		case 1:
			if field.Type != 0 || seenSequence {
				return errors.New("player: invalid EatFoodAuto sequence")
			}
			seenSequence = true
			seq, _ = binary.Uvarint(field.Value)
		case 2:
			if field.Type != 2 {
				return errors.New("player: invalid EatFoodAuto target")
			}
			var index uint64
			seenIndex := false
			if err := wire.Walk(field.Value, func(inner wire.Field) error {
				if inner.Number == 2 {
					return nil
				}
				if inner.Number != 1 || inner.Type != 0 || seenIndex {
					return errors.New("player: invalid EatFoodAuto character")
				}
				seenIndex = true
				index, _ = binary.Uvarint(inner.Value)
				return nil
			}); err != nil {
				return err
			}
			if index == 0 || index > math.MaxInt64 {
				return errors.New("player: invalid EatFoodAuto character index")
			}
			items, err := assets.DecodeItemRequest(field.Value, 2, "EatFoodAuto")
			if err != nil {
				return err
			}
			if len(items) == 0 {
				return errors.New("player: EatFoodAuto character requires food stacks")
			}
			targets = append(targets, struct {
				index uint64
				items []assets.Item
			}{index, items})
		default:
			return errors.New("player: unknown EatFoodAuto field")
		}
		return nil
	})
	if err != nil || seq == 0 || seq > math.MaxInt32 || len(targets) == 0 {
		return 0, nil, true, errors.New("player: invalid EatFoodAuto request")
	}

	if ctx.SessionID == "" || s.currentPack == nil || s.battleActive == nil {
		return 0, nil, true, errors.New("player: EatFoodAuto context unavailable")
	}
	keyDigest := sha256.Sum256([]byte(ctx.SessionID + ":EatFoodAuto:" + strconv.FormatUint(seq, 10)))
	key := hex.EncodeToString(keyDigest[:])
	digest := sha256.Sum256(request)
	digestString := hex.EncodeToString(digest[:])
	prior, found, err := s.store.LoadEntry(ctx.State, "characters", "food_requests", key)
	if err != nil {
		return 0, nil, true, err
	}
	if found {
		var reply foodReply
		if json.Unmarshal(prior, &reply) != nil || reply.Digest != digestString {
			return 0, nil, true, errors.New("player: EatFoodAuto sequence reused with different request")
		}
		return 27, append([]byte(nil), reply.Body...), true, nil
	}
	pack, err := s.currentPack(ctx)
	if err != nil || pack <= 0 || s.battleActive(ctx) {
		return 0, nil, true, errors.New("player: EatFoodAuto unavailable during battle or outside pack")
	}
	seenCharacters := map[uint64]bool{}
	var items []assets.Item
	var body []byte
	var changes []stateio.EntryMutation
	for _, target := range targets {
		if seenCharacters[target.index] {
			return 0, nil, true, errors.New("player: duplicate EatFoodAuto character")
		}
		seenCharacters[target.index] = true
		character, err := s.recoverCharacter(ctx, target.index, target.items)
		if err != nil {
			return 0, nil, true, err
		}
		items = append(items, target.items...)
		raw, _ := json.Marshal(character.HP)
		changes = append(changes, stateio.EntryMutation{Bucket: "current_hp", Key: strconv.FormatUint(target.index, 10), Payload: raw})
		info := wire.AppendVarint(nil, 1, target.index)
		info = wire.AppendVarint(info, 2, character.HP)
		body = wire.AppendBytes(body, 1, info)
	}
	// CanConsume accounts for a shared stack requested by several characters.
	if err := s.inventory.CanConsume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	if err := s.inventory.Consume(ctx, items); err != nil {
		return 0, nil, true, err
	}
	replyRaw, _ := json.Marshal(foodReply{Digest: digestString, Body: body})
	changes = append(changes, stateio.EntryMutation{Bucket: "food_requests", Key: key, Payload: replyRaw})
	if err := s.store.SaveWithEntries(ctx.State, "characters", nil, changes); err != nil {
		return 0, nil, true, err
	}
	return 27, body, true, nil
}

// AutoRecover selects a living permanent fatigue-recovery caster. The selected
// caster is preferred; another owned caster can take over when it is fatigued.
func (s *TalentUseService) AutoRecover(ctx command.Context, seq, caster uint64, targets []uint64) (AutoRecoveryResult, error) {
	r := AutoRecoveryResult{Caster: caster, Catalyst: s.wallet.CatalystBalance(ctx)}
	if s.context == nil {
		return r, fmt.Errorf("player: automatic recovery context unavailable")
	}
	pack, _, battle, err := s.context(ctx)
	if err != nil {
		return r, err
	}
	if battle {
		return r, fmt.Errorf("player: automatic recovery during battle")
	}
	all := s.characters.RawAll()
	sort.SliceStable(all, func(i, j int) bool {
		left, right := all[i].InvenIndex == caster, all[j].InvenIndex == caster
		if left != right {
			return left
		}
		return all[i].InvenIndex < all[j].InvenIndex
	})
	var selected Character
	var cost uint64
	for _, c := range all {
		if IsStoryCharacter(c) || IsCharmCharacter(c) {
			continue
		}
		meta, ok := s.design.Characters[c.ID]
		if !ok || meta.BannedPacks[pack] {
			continue
		}
		rule, ok := s.design.Rules[[2]uint64{meta.Group, c.TalentLevel}]
		if !ok || rule.Class != 10 {
			continue
		}
		hp, e := s.characters.CurrentHealth(ctx, c.InvenIndex)
		if e != nil {
			return r, e
		}
		if hp == 0 {
			continue
		}
		selected = c
		if len(targets) > 0 && rule.Catalyst > math.MaxUint64/uint64(len(targets)) {
			return r, fmt.Errorf("player: automatic recovery cost overflow")
		}
		cost = rule.Catalyst * uint64(len(targets))
		break
	}
	if selected.InvenIndex == 0 {
		r.Disabled = 1
		return r, nil
	}
	r.Caster = selected.InvenIndex
	if len(targets) == 0 {
		return r, nil
	}
	if cost > r.Catalyst {
		r.Disabled = 2
		return r, nil
	}
	request := wire.AppendVarint(nil, 1, seq)
	request = wire.AppendVarint(request, 2, r.Caster)
	for _, id := range targets {
		request = wire.AppendVarint(request, 3, id)
	}
	_, body, _, err := s.healing(ctx, request, "auto-recovery")
	if err != nil {
		return r, err
	}
	r.Experience, _, err = wire.Varint(body, 2)
	if err != nil {
		return r, err
	}
	r.Catalyst = s.wallet.CatalystBalance(ctx)
	for _, id := range targets {
		c, ok := s.characters.Find(ctx, id)
		if !ok {
			return r, fmt.Errorf("player: recovered character unavailable")
		}
		hp, e := s.characters.CurrentHealth(ctx, id)
		if e != nil {
			return r, e
		}
		c.HP = hp
		r.Characters = append(r.Characters, c)
	}
	return r, nil
}
