package player

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"sync"
	"time"
)

type TalentUseContext func() (pack int, mapID uint64, battle bool, err error)
type TalentUseEffect func(identity string, character Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error)
type TalentUseEconomy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type talentUseState struct {
	End     int64
	Count   uint64
	Day     string
	Level   uint64
	Settled bool
}
type talentUseReceipt struct {
	Digest string
	Body   []byte
}
type talentUseSnapshot struct {
	Skills    map[uint64]talentUseState
	NPCs      map[string]int64
	Receipts  map[string]talentUseReceipt
	Discounts map[string]uint64
}
type TalentUseService struct {
	mu         sync.Mutex
	design     *gamedata.TalentUseDesign
	store      stateio.Store
	characters *CharacterStore
	inventory  *Inventory
	wallet     *Wallet
	economy    TalentUseEconomy
	context    TalentUseContext
	effects    map[uint64]TalentUseEffect
	session    string
	now        func() time.Time
}

func NewTalentUseService(d *gamedata.TalentUseDesign, store stateio.Store, characters *CharacterStore, inventory *Inventory, wallet *Wallet, economy TalentUseEconomy) (*TalentUseService, error) {
	if d == nil || store == nil || characters == nil || inventory == nil || wallet == nil || economy == nil {
		return nil, fmt.Errorf("player: missing talent use dependencies")
	}
	return &TalentUseService{design: d, store: store, characters: characters, inventory: inventory, wallet: wallet, economy: economy, effects: map[uint64]TalentUseEffect{}, now: time.Now}, nil
}
func (s *TalentUseService) AttachContext(f TalentUseContext)             { s.context = f }
func (s *TalentUseService) AttachEffect(class uint64, f TalentUseEffect) { s.effects[class] = f }
func (s *TalentUseService) BeginSession(id string)                       { s.mu.Lock(); defer s.mu.Unlock(); s.session = id }
func (s *TalentUseService) load() (talentUseSnapshot, error) {
	v := talentUseSnapshot{Skills: map[uint64]talentUseState{}, NPCs: map[string]int64{}, Receipts: map[string]talentUseReceipt{}, Discounts: map[string]uint64{}}
	b, e := s.store.Load("talentuse")
	if e != nil {
		return v, e
	}
	if b != nil {
		if e = stateio.RequireExactJSONObject(b, "Skills", "NPCs", "Receipts", "Discounts"); e != nil {
			return v, e
		}
		if e = json.Unmarshal(b, &v); e != nil || v.Skills == nil || v.NPCs == nil || v.Receipts == nil || v.Discounts == nil {
			return v, fmt.Errorf("player: invalid talent use state")
		}
	}
	return v, nil
}
func (s *TalentUseService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path == "/CharHealing" {
		return s.charHealing(request)
	}
	if path != "/TalentSkillUse" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(e error) (int, []byte, bool, error) { return 43, nil, true, e }
	seq, ok, err := wire.Varint(request, 1)
	if err != nil || !ok || seq == 0 || seq > math.MaxInt32 || s.session == "" {
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
	state, err := s.load()
	if err != nil {
		return fail(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	key := s.session + ":" + fmt.Sprint(seq)
	if prior, ok := state.Receipts[key]; ok {
		if prior.Digest != digest {
			return fail(fmt.Errorf("player: changed talent replay"))
		}
		return 43, prior.Body, true, nil
	}
	var character Character
	var meta gamedata.TalentUseCharacter
	var foodItem Item
	if food > 0 {
		for _, item := range s.inventory.All() {
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
		character, owned = s.characters.Find(index)
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
	pack, mapID, battle, err := s.context()
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
		if err = s.inventory.CanConsume([]Item{foodItem}); err != nil {
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
			bundle, e := s.economy.Apply(identity, nil, rewards)
			if e != nil {
				return fail(e)
			}
			extra, e = talentBundleFields(bundle)
			if e != nil {
				return fail(e)
			}
		}
		if rule.Class == 19 && len(npc.CharmCharacters) > 0 {
			b, e := s.applyCharm(identity, character, rule, targets)
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
				state.Discounts[s.session+":"+fmt.Sprintf("%d/%d", pack, npc.ID)] = uint64(rule.Values[1])
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
			b, e := effect(identity, character, rule, targets)
			if e != nil {
				return fail(e)
			}
			extra = append(extra, b...)
		}
	} else if effect := s.effects[rule.Class]; effect != nil {
		extra, err = effect(identity, character, rule, targets)
		if err != nil {
			return fail(err)
		}
	} else if rule.Class == 4 || rule.Class == 18 || rule.Class == 20 {
		return fail(fmt.Errorf("player: field talent executor unavailable"))
	} else if len(targets) > 0 && rule.Class != 3 && rule.Class != 11 {
		return fail(fmt.Errorf("player: unexpected talent targets"))
	}
	if food > 0 {
		if err = s.inventory.Consume([]Item{foodItem}); err != nil {
			return fail(err)
		}
	}
	if rule.Catalyst > 0 {
		if rule.Catalyst > math.MaxUint64/multiplier {
			return fail(fmt.Errorf("player: talent cost overflow"))
		}
		if _, err = s.wallet.SpendCatalystOnce(identity, rule.Catalyst*multiplier); err != nil {
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
			if _, e = s.characters.AddTalentExperience(index, gain, maximum); e != nil {
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
	if err = s.store.Save("talentuse", b); err != nil {
		return fail(err)
	}
	return 43, out, true, nil
}
func sha256Sum(b []byte) []byte        { v := sha256.Sum256(b); return v[:] }
func talentNPCClass(class uint64) bool { return class == 1 || class == 5 || class == 16 || class == 19 }
func talentHasCooldown(class uint64) bool {
	return class == 2 || class == 6 || class == 12 || class == 13 || class == 15 || class == 17
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
func (s *TalentUseService) experienceMaximum(c Character) (uint64, error) {
	if s.design.Growth == nil {
		return 0, fmt.Errorf("player: talent growth design missing")
	}
	meta, ok := s.design.Growth.Characters[c.ID]
	if !ok {
		return 0, fmt.Errorf("player: missing character growth")
	}
	var total uint64
	for level := uint64(1); level <= c.TalentLevel; level++ {
		v, ok := s.design.Growth.Levels[[2]uint64{meta.GrowthGroup, level}]
		if !ok {
			return 0, fmt.Errorf("player: missing talent growth level")
		}
		total += v.NeedExp
	}
	return total, nil
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
func (s *TalentUseService) PackInfo(pack int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
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

func (s *TalentUseService) nextNPCReset(reset uint64) time.Time {
	shift := 9*time.Hour - s.design.ResetSchedule.DailyReset
	clock := s.now().UTC().Add(shift)
	next := time.Date(clock.Year(), clock.Month(), clock.Day()+1, 0, 0, 0, 0, time.UTC)
	if reset == 2 {
		for next.Weekday() != s.design.ResetSchedule.WeeklyDay {
			next = next.AddDate(0, 0, 1)
		}
	}
	return next.Add(-shift)
}

func (s *TalentUseService) ShopDiscount(pack, npc uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.load()
	if e != nil {
		return 0, e
	}
	return v.Discounts[s.session+":"+fmt.Sprintf("%d/%d", pack, npc)], nil
}

// ConsumeOverwhelm authorizes the follow-up result for one successful cast.
// The one-minute handoff window is this server's request policy, not a claimed
// official skill duration; the field domain validates every actual monster.
func (s *TalentUseService) ConsumeOverwhelm(identity string, count uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity == "" || count == 0 {
		return fmt.Errorf("player: invalid overwhelm settlement")
	}
	v, err := s.load()
	if err != nil {
		return err
	}
	receiptKey := "overwhelm:" + identity
	digest := fmt.Sprint(count)
	if prior, ok := v.Receipts[receiptKey]; ok {
		if prior.Digest != digest {
			return fmt.Errorf("player: changed overwhelm settlement")
		}
		return nil
	}
	var chosen uint64
	var latest int64
	for group, state := range v.Skills {
		rule, ok := s.design.Rules[[2]uint64{group, state.Level}]
		if !ok || rule.Class != 3 || state.Settled || state.Count == 0 {
			continue
		}
		if state.End <= s.now().UnixMilli() && s.now().UnixMilli()-state.End <= 60000 && state.End > latest {
			chosen = group
			latest = state.End
		}
	}
	if chosen == 0 {
		return fmt.Errorf("player: overwhelm result has no pending successful cast")
	}
	state := v.Skills[chosen]
	state.Settled = true
	v.Skills[chosen] = state
	v.Receipts[receiptKey] = talentUseReceipt{Digest: digest}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.store.Save("talentuse", b)
}
