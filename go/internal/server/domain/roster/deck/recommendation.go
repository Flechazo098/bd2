package deck

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

type RecommendContext struct{ BattleMode, PackID, BattleIndex, Level, TeamCountType, DeckType uint64 }
type RecommendIdentity struct {
	GuildBase                                             []byte
	OwnerIndex                                            uint64
	UserID                                                string
	UserExp, PortraitCostumeID, PortraitDesignID, TitleID uint64
}
type RecommendOptions struct {
	IsSave    bool `json:"is_save"`
	OnlyOwned bool `json:"only_owned"`
}
type RecommendCostume struct {
	roster.Costume
	Potential []uint64
	Locked    []uint64
}
type RecommendRecord struct {
	Revision   uint64
	Context    RecommendContext
	Identity   RecommendIdentity
	Power      uint64
	Deck       []DeckEntry
	Characters []roster.Character
	Costumes   []RecommendCostume
	Equipment  []assets.Equipment
	Awake      map[uint64]roster.CharAwakeProgress
}

func (s *Store) AttachRecommendation(identity func(command.Context) (RecommendIdentity, error), power func(command.Context, []roster.Character) (uint64, error)) error {
	if identity == nil || power == nil {
		return fmt.Errorf("deck: incomplete recommendation runtime")
	}
	s.recommendIdentity = identity
	s.recommendPower = power
	return nil
}
func (s *Store) RecommendationOptions(ctx command.Context) (RecommendOptions, error) {
	raw, found, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_options", "options")
	if e != nil {
		return RecommendOptions{}, e
	}
	options := RecommendOptions{IsSave: true}
	if found {
		if json.Unmarshal(raw, &options) != nil {
			return options, fmt.Errorf("deck: malformed recommendation options")
		}
	}
	return options, nil
}
func (s *Store) RecommendationOptionWire(ctx command.Context) ([]byte, error) {
	o, e := s.RecommendationOptions(ctx)
	if e != nil {
		return nil, e
	}
	var b []byte
	if o.IsSave {
		b = wire.AppendVarint(b, 1, 1)
	}
	if o.OnlyOwned {
		b = wire.AppendVarint(b, 2, 1)
	}
	return b, nil
}
func (s *Store) PrepareRecommendation(ctx command.Context, identity string, scenario RecommendContext, entries []DeckEntry) error {
	if s.recommendIdentity == nil || s.recommendPower == nil || s.characters == nil || s.equipment == nil || s.collection == nil || identity == "" {
		return fmt.Errorf("deck: missing recommendation runtime")
	}
	options, e := s.RecommendationOptions(ctx)
	if e != nil {
		return e
	}
	if !options.IsSave {
		return nil
	}
	if _, done, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_completed", identity); e != nil || done {
		return e
	}
	if raw, found, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_prepared", identity); e != nil {
		return e
	} else if found {
		var prior RecommendRecord
		if json.Unmarshal(raw, &prior) != nil || prior.Context != scenario || !slices.Equal(prior.Deck, entries) {
			return fmt.Errorf("deck: changed recommendation entry replay")
		}
		return nil
	}
	if e := validDeck(entries); e != nil {
		return e
	}
	if e := s.validateOwnedDeckLocked(ctx, entries); e != nil {
		return e
	}
	if scenario.BattleMode > math.MaxInt32 || scenario.PackID > math.MaxInt32 || scenario.BattleIndex > math.MaxInt32 || scenario.Level > math.MaxInt32 || scenario.TeamCountType > math.MaxInt32 || scenario.DeckType > math.MaxInt32 {
		return fmt.Errorf("deck: invalid recommendation context")
	}
	r := RecommendRecord{Context: scenario, Deck: append([]DeckEntry(nil), entries...), Awake: map[uint64]roster.CharAwakeProgress{}}
	r.Identity, e = s.recommendIdentity(ctx)
	if e != nil {
		return e
	}
	if r.Identity.OwnerIndex == 0 || r.Identity.UserID == "" {
		return fmt.Errorf("deck: recommendation identity unavailable")
	}
	participants := map[uint64]bool{}
	for _, entry := range entries {
		c, ok := s.characters.Find(ctx, entry.CharacterInvenIndex)
		if !ok || roster.IsStoryCharacter(c) || roster.IsCharmCharacter(c) {
			return fmt.Errorf("deck: recommendation requires permanent owned characters")
		}
		r.Characters = append(r.Characters, c)
		participants[c.InvenIndex] = true
	}
	r.Power, e = s.recommendPower(ctx, r.Characters)
	if e != nil {
		return e
	}
	if r.Power > math.MaxInt32 {
		return fmt.Errorf("deck: recommendation power outside protocol range")
	}
	for _, c := range s.collection.Costumes() {
		if participants[c.UseChar] {
			r.Costumes = append(r.Costumes, RecommendCostume{Costume: c, Potential: append([]uint64(nil), c.PotentialIDs...), Locked: append([]uint64(nil), c.PotentialLockIDs...)})
		}
	}
	for _, equip := range s.equipment.All(ctx) {
		if participants[equip.UseChar] {
			r.Equipment = append(r.Equipment, equip)
		}
	}
	for unique, v := range s.collection.CharAwakeStates() {
		for _, c := range r.Characters {
			if s.recommendUnique[c.ID] == unique {
				r.Awake[unique] = v
				break
			}
		}
	}
	if e = r.Validate(); e != nil {
		return e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return s.storage.PutEntry(ctx.State, "deck", "recommend_prepared", identity, raw)
}

func (s *Store) PrepareRecommendationSnapshot(ctx command.Context, identity string, record RecommendRecord) error {
	if identity == "" {
		return fmt.Errorf("deck: missing recommendation entry identity")
	}
	options, err := s.RecommendationOptions(ctx)
	if err != nil || !options.IsSave {
		return err
	}
	if _, done, err := s.storage.LoadEntry(ctx.State, "deck", "recommend_completed", identity); err != nil || done {
		return err
	}
	if raw, found, err := s.storage.LoadEntry(ctx.State, "deck", "recommend_prepared", identity); err != nil {
		return err
	} else if found {
		var prior RecommendRecord
		if json.Unmarshal(raw, &prior) != nil || prior.Context != record.Context || !slices.Equal(prior.Deck, record.Deck) {
			return fmt.Errorf("deck: changed recommendation entry replay")
		}
		return nil
	}
	if err = record.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.storage.PutEntry(ctx.State, "deck", "recommend_prepared", identity, raw)
}

const RecommendationTopic = "deck.recommendation"

type RecommendationPublication struct {
	Revision uint64
	Public   bool
	Record   *RecommendRecord
}

func (s *Store) AttachRecommendationCommunity(query func(command.Context, RecommendContext) ([]RecommendRecord, error), characterUnique map[uint64]uint64) error {
	if query == nil || len(characterUnique) == 0 {
		return fmt.Errorf("deck: incomplete recommendation community")
	}
	s.recommendQuery = query
	s.recommendUnique = characterUnique
	return nil
}
func (s *Store) publishRecommendation(ctx command.Context, identity string, public bool, record *RecommendRecord) error {
	raw, found, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_publication", "revision")
	if e != nil {
		return e
	}
	var revision uint64
	if found && json.Unmarshal(raw, &revision) != nil {
		return fmt.Errorf("deck: invalid recommendation publication revision")
	}
	if revision == math.MaxUint64 {
		return fmt.Errorf("deck: recommendation revision overflow")
	}
	revision++
	if record != nil {
		record.Revision = revision
	}
	payload, e := json.Marshal(RecommendationPublication{Revision: revision, Public: public, Record: record})
	if e != nil {
		return e
	}
	if e = ctx.Publish(command.Event{ID: identity, Topic: RecommendationTopic, Payload: payload}); e != nil {
		return e
	}
	raw, e = json.Marshal(revision)
	if e != nil {
		return e
	}
	return s.storage.PutEntry(ctx.State, "deck", "recommend_publication", "revision", raw)
}
func (s *Store) CompleteRecommendation(ctx command.Context, identity string, win bool) error {
	if identity == "" {
		return fmt.Errorf("deck: missing recommendation completion")
	}
	receipt, done, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_completed", identity)
	if e != nil {
		return e
	}
	if done {
		var before bool
		if json.Unmarshal(receipt, &before) != nil || before != win {
			return fmt.Errorf("deck: changed recommendation completion replay")
		}
		return nil
	}
	raw, found, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_prepared", identity)
	if e != nil {
		return e
	}
	options, e := s.RecommendationOptions(ctx)
	if e != nil {
		return e
	}
	if found && win && options.IsSave {
		var record RecommendRecord
		if json.Unmarshal(raw, &record) != nil {
			return fmt.Errorf("deck: invalid prepared recommendation")
		}
		if e = record.Validate(); e != nil {
			return e
		}
		if e = s.publishRecommendation(ctx, "battle:"+identity, true, &record); e != nil {
			return e
		}
		raw, e = json.Marshal(record)
		if e != nil {
			return e
		}
		if e = s.storage.PutEntry(ctx.State, "deck", "recommendations", identity, raw); e != nil {
			return e
		}
	}
	if found {
		if _, e = s.storage.DeleteEntry(ctx.State, "deck", "recommend_prepared", identity); e != nil {
			return e
		}
	}
	receipt, e = json.Marshal(win)
	if e != nil {
		return e
	}
	return s.storage.PutEntry(ctx.State, "deck", "recommend_completed", identity, receipt)
}
func (r RecommendRecord) Validate() error {
	c := r.Context
	id := r.Identity
	if c.BattleMode > math.MaxInt32 || c.PackID > math.MaxInt32 || c.BattleIndex > math.MaxInt32 || c.Level > math.MaxInt32 || c.TeamCountType > math.MaxInt32 || c.DeckType > math.MaxInt32 || id.OwnerIndex == 0 || id.OwnerIndex > math.MaxInt64 || id.UserID == "" || id.UserExp > math.MaxInt32 || id.PortraitCostumeID > math.MaxInt32 || id.PortraitDesignID > math.MaxInt32 || id.TitleID > math.MaxInt32 || r.Power > math.MaxInt32 || validDeck(r.Deck) != nil || len(r.Characters) != len(r.Deck) {
		return fmt.Errorf("deck: invalid recommendation snapshot")
	}
	chars := map[uint64]roster.Character{}
	for _, v := range r.Characters {
		if v.InvenIndex == 0 || v.ID == 0 || v.Level == 0 || roster.IsStoryCharacter(v) || roster.IsCharmCharacter(v) {
			return fmt.Errorf("deck: invalid recommendation character")
		}
		if _, found := chars[v.InvenIndex]; found {
			return fmt.Errorf("deck: duplicate recommendation character")
		}
		chars[v.InvenIndex] = v
	}
	for _, entry := range r.Deck {
		if _, ok := chars[entry.CharacterInvenIndex]; !ok {
			return fmt.Errorf("deck: recommendation deck ownership mismatch")
		}
	}
	costumes := map[uint64]bool{}
	for _, v := range r.Costumes {
		if v.InvenIndex == 0 || v.ID == 0 || costumes[v.InvenIndex] {
			return fmt.Errorf("deck: invalid recommendation costume")
		}
		if _, ok := chars[v.UseChar]; !ok {
			return fmt.Errorf("deck: recommendation costume ownership mismatch")
		}
		costumes[v.InvenIndex] = true
	}
	for _, v := range r.Characters {
		if v.UseCostume == 0 || !costumes[v.UseCostume] {
			return fmt.Errorf("deck: recommendation active costume missing")
		}
	}
	equips := map[uint64]bool{}
	for _, v := range r.Equipment {
		if v.InvenIndex == 0 || v.ID == 0 || equips[v.InvenIndex] {
			return fmt.Errorf("deck: invalid recommendation equipment")
		}
		if _, ok := chars[v.UseChar]; !ok {
			return fmt.Errorf("deck: recommendation equipment ownership mismatch")
		}
		equips[v.InvenIndex] = true
	}
	return nil
}
func (s *Store) ownsRecommendation(ctx command.Context, r RecommendRecord) bool {
	ownedUnique := map[uint64]bool{}
	ownedIDs := map[uint64]bool{}
	for _, c := range s.characters.All(ctx) {
		if roster.IsCharmCharacter(c) || roster.IsStoryCharacter(c) {
			continue
		}
		unique := s.recommendUnique[c.ID]
		if unique != 0 {
			ownedUnique[unique] = true
		}
		ownedIDs[c.ID] = true
	}
	for _, c := range r.Characters {
		unique := s.recommendUnique[c.ID]
		if unique != 0 {
			if !ownedUnique[unique] {
				return false
			}
		} else if !ownedIDs[c.ID] {
			return false
		}

	}
	return true
}
func recommendBase(r RecommendRecord) []byte {
	b := wire.AppendVarint(nil, 1, r.Power)
	for _, d := range r.Deck {
		v := wire.AppendVarint(nil, 1, d.CharacterInvenIndex)
		v = wire.AppendVarint(v, 2, d.CostumeInvenIndex)
		v = wire.AppendVarint(v, 3, d.Slot)
		b = wire.AppendBytes(b, 2, v)
	}
	for _, c := range r.Characters {
		b = wire.AppendBytes(b, 4, roster.CharacterWire(c))
	}
	for _, c := range r.Costumes {
		v := c.Costume
		v.PotentialIDs = c.Potential
		v.PotentialLockIDs = c.Locked
		b = wire.AppendBytes(b, 5, roster.CostumeWire(v))
	}
	for _, v := range r.Equipment {
		b = wire.AppendBytes(b, 6, assets.EquipmentWire(v))
	}
	ids := make([]uint64, 0, len(r.Awake))
	for id := range r.Awake {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		v := r.Awake[id]
		a := wire.AppendVarint(nil, 1, id)
		for i, n := range v.ImprintLevels {
			a = wire.AppendVarint(a, i+2, n)
		}
		if v.IsAwake {
			a = wire.AppendVarint(a, 5, 1)
		}
		a = wire.AppendVarint(a, 6, r.Identity.OwnerIndex)
		b = wire.AppendBytes(b, 7, a)
	}
	return b
}
func (s *Store) handleRecommendation(ctx command.Context, path string, raw []byte) (int, []byte, bool, error) {
	code := 396
	if path == "/RecommendDeckUserOptionSave" {
		code = 412
	}
	fail := func(e error) (int, []byte, bool, error) { return code, nil, true, e }
	if e := checkSeq(raw); e != nil {
		return fail(e)
	}
	if path == "/RecommendDeckUserOptionSave" {
		var nested []byte
		count := 0
		e := wire.Walk(raw, func(f wire.Field) error {
			if f.Number == 2 {
				if f.Type != 2 || count != 0 {
					return fmt.Errorf("deck: invalid recommendation options")
				}
				nested = f.Value
				count++
			}
			return nil
		})
		if e != nil || count != 1 {
			return fail(fmt.Errorf("deck: missing recommendation options"))
		}
		var options RecommendOptions
		seen := map[int]bool{}
		e = wire.Walk(nested, func(f wire.Field) error {
			if f.Number != 1 && f.Number != 2 {
				return nil
			}
			if f.Type != 0 || seen[f.Number] {
				return fmt.Errorf("deck: invalid recommendation option")
			}
			seen[f.Number] = true
			v, _, e := wire.Varint(nested, f.Number)
			if e != nil || v > 1 {
				return fmt.Errorf("deck: invalid recommendation boolean")
			}
			if f.Number == 1 {
				options.IsSave = v == 1
			} else {
				options.OnlyOwned = v == 1
			}
			return nil
		})
		if e != nil {
			return fail(e)
		}
		if ctx.SessionID == "" {
			return fail(fmt.Errorf("deck: recommendation options require session"))
		}
		seq, _, _ := wire.Varint(raw, 1)
		key := fmt.Sprintf("%s:%d", ctx.SessionID, seq)
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		prior, found, e := s.storage.LoadEntry(ctx.State, "deck", "recommend_option_replies", key)
		if e != nil {
			return fail(e)
		}
		if found {
			var old string
			if json.Unmarshal(prior, &old) != nil || old != digest {
				return fail(fmt.Errorf("deck: changed recommendation option replay"))
			}
			return code, nil, true, nil
		}
		previous, e := s.RecommendationOptions(ctx)
		if e != nil {
			return fail(e)
		}
		if previous.IsSave != options.IsSave {
			if e = s.publishRecommendation(ctx, "option:"+key, options.IsSave, nil); e != nil {
				return fail(e)
			}
		}
		payload, e := json.Marshal(options)
		if e != nil {
			return fail(e)
		}
		if e = s.storage.PutEntry(ctx.State, "deck", "recommend_options", "options", payload); e != nil {
			return fail(e)
		}
		payload, e = json.Marshal(digest)
		if e != nil {
			return fail(e)
		}
		if e = s.storage.PutEntry(ctx.State, "deck", "recommend_option_replies", key, payload); e != nil {
			return fail(e)
		}
		return code, nil, true, nil
	}
	values := map[int]uint64{}
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number < 2 || f.Number > 8 {
			return nil
		}
		if f.Type != 0 {
			return fmt.Errorf("deck: invalid recommendation query")
		}
		if _, ok := values[f.Number]; ok {
			return fmt.Errorf("deck: duplicate recommendation query")
		}
		v, _, e := wire.Varint(raw, f.Number)
		if e != nil || v > math.MaxInt32 {
			return fmt.Errorf("deck: invalid recommendation query value")
		}
		values[f.Number] = v
		return nil
	})
	if e != nil || values[8] > 2 {
		return fail(fmt.Errorf("deck: invalid recommendation query"))
	}
	scenario := RecommendContext{values[2], values[3], values[4], values[5], values[6], values[7]}
	options, e := s.RecommendationOptions(ctx)
	if e != nil {
		return fail(e)
	}
	var candidates []RecommendRecord
	if s.recommendQuery != nil {
		candidates, e = s.recommendQuery(ctx, scenario)
		if e != nil {
			return fail(e)
		}
	}
	rows, e := s.storage.ListEntries(ctx.State, "deck", "recommendations")
	if e != nil {
		return fail(e)
	}
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		var r RecommendRecord
		if json.Unmarshal(rows[key], &r) != nil {
			return fail(fmt.Errorf("deck: invalid saved recommendation"))
		}
		candidates = append(candidates, r)
	}
	selected := map[uint64]RecommendRecord{}
	for _, r := range candidates {
		if e = r.Validate(); e != nil {
			return fail(e)
		}
		if r.Context != scenario {
			continue
		}
		if options.OnlyOwned && !s.ownsRecommendation(ctx, r) {
			continue
		}
		prior, found := selected[r.Identity.OwnerIndex]
		if !found || r.Revision > prior.Revision {
			selected[r.Identity.OwnerIndex] = r
		}
	}
	owners := make([]uint64, 0, len(selected))
	for owner := range selected {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	records := make([]RecommendRecord, 0, len(owners))
	for _, owner := range owners {
		records = append(records, selected[owner])
	}
	if values[8] != 0 {
		slices.SortStableFunc(records, func(a, b RecommendRecord) int {
			if a.Power == b.Power {
				return 0
			}
			if a.Power < b.Power {
				if values[8] == 1 {
					return 1
				}
				return -1
			}
			if values[8] == 1 {
				return -1
			}
			return 1
		})
	}
	var out []byte
	for i, r := range records {
		if i >= 100 {
			break
		}
		v := wire.AppendVarint(nil, 1, r.Identity.OwnerIndex)
		v = wire.AppendBytes(v, 2, []byte(r.Identity.UserID))
		for _, f := range []struct {
			n int
			v uint64
		}{{3, r.Identity.UserExp}, {4, r.Identity.PortraitCostumeID}, {5, r.Identity.PortraitDesignID}, {6, r.Identity.TitleID}, {8, r.Power}} {
			v = wire.AppendVarint(v, f.n, f.v)
		}
		if len(r.Identity.GuildBase) != 0 {
			v = wire.AppendBytes(v, 7, r.Identity.GuildBase)
		}
		v = wire.AppendBytes(v, 9, []byte(base64.StdEncoding.EncodeToString(recommendBase(r))))
		out = wire.AppendBytes(out, 1, v)
	}
	return code, out, true, nil
}

func (s *Store) ExpireCharacter(ctx command.Context, index uint64) ([]byte, error) {
	next := clone(s.state)
	next.Deck = slices.DeleteFunc(next.Deck, func(v DeckEntry) bool { return v.CharacterInvenIndex == index })
	for i := range next.Deck {
		next.Deck[i].Slot = uint64(i + 1)
	}
	next.FieldDeck = slices.DeleteFunc(next.FieldDeck, func(v FieldEntry) bool { return v.CharacterInvenIndex == index })
	for i := range next.FieldDeck {
		next.FieldDeck[i].Slot = uint64(i + 1)
	}
	delete(next.Costumes, index)
	if len(next.FieldDeck) == 0 {
		for _, c := range s.characters.All(ctx) {
			if c.InvenIndex == index || roster.IsCharmCharacter(c) || roster.IsStoryCharacter(c) {
				continue
			}
			next.FieldDeck = []FieldEntry{{Slot: 1, CharacterInvenIndex: c.InvenIndex, CostumeInvenIndex: c.UseCostume}}
			break
		}
		if len(next.FieldDeck) == 0 {
			return nil, fmt.Errorf("deck: expiry has no permanent field replacement")
		}
	}
	if e := s.commit(ctx, next); e != nil {
		return nil, e
	}
	return encodeDeck(next.Deck), nil
}
