package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"fmt"
	"sort"
	"strconv"
)

// GameplayAchievementCounter persists each event in the request transaction.
type GameplayAchievementCounter interface {
	RecordEvent(string, uint64, uint64, uint64) ([][]byte, error)
	SetCondition(uint64, uint64, uint64) ([][]byte, error)
	CounterValues() (map[int]uint64, error)
}

type gameplayAchievementProgressBatch interface {
	ApplyGameplayProgress([]GameplayAchievementCondition, []GameplayAchievementRecordedEvent) (map[int]uint64, map[int]uint64, error)
}
type GameplayAchievementEvent struct {
	Type, SubType, Count uint64
	Identity             string
	StableIdentity       bool
}
type GameplayAchievementCondition struct{ Type, SubType, Value uint64 }
type GameplayAchievementSnapshot struct {
	Characters   map[uint64]player.Character
	Costumes     map[uint64]player.Costume
	Equipment    map[uint64]player.Equipment
	Items        map[[2]uint64]uint64
	Conditions   []GameplayAchievementCondition
	FieldObjects map[string]gamedata.FieldRewardObject
	GachaGrants  map[string]uint64
}
type GameplayAchievementProvider interface {
	Snapshot() (GameplayAchievementSnapshot, error)
	Events(string, []byte, []byte, GameplayAchievementSnapshot, GameplayAchievementSnapshot) ([]GameplayAchievementEvent, error)
}
type GameplayAchievementObserver struct {
	counter  GameplayAchievementCounter
	provider GameplayAchievementProvider
	session  string
	before   GameplayAchievementSnapshot
	counters map[int]uint64
}

func NewGameplayAchievementObserver(counter GameplayAchievementCounter, provider GameplayAchievementProvider) (*GameplayAchievementObserver, error) {
	if counter == nil || provider == nil {
		return nil, fmt.Errorf("achievement: gameplay provider unavailable")
	}
	return &GameplayAchievementObserver{counter: counter, provider: provider}, nil
}
func (s *GameplayAchievementObserver) BeginSession(id string) { s.session = id; s.counters = nil }

// SyncRecordedHistory must run within the startup account transaction. Retained
// real grants establish a lower bound; inventory never substitutes for history.
func (s *GameplayAchievementObserver) SyncRecordedHistory() error {
	snapshot, err := s.provider.Snapshot()
	if err != nil {
		return err
	}
	var ids []string
	for identity := range snapshot.GachaGrants {
		ids = append(ids, identity)
	}
	sort.Strings(ids)
	var events []GameplayAchievementRecordedEvent
	for _, identity := range ids {
		count := snapshot.GachaGrants[identity]
		if count == 0 {
			continue
		}
		events = append(events, GameplayAchievementRecordedEvent{Identity: "gacha-grant:" + identity, Type: 54, Count: count})
	}
	if batch, ok := s.counter.(gameplayAchievementProgressBatch); ok {
		_, _, err := batch.ApplyGameplayProgress(snapshot.Conditions, events)
		return err
	}
	for _, condition := range snapshot.Conditions {
		if _, err := s.counter.SetCondition(condition.Type, condition.SubType, condition.Value); err != nil {
			return err
		}
	}
	for _, event := range events {
		if _, err := s.counter.RecordEvent(event.Identity, event.Type, event.SubType, event.Count); err != nil {
			return err
		}
	}
	return nil
}
func (s *GameplayAchievementObserver) BeforeDispatch(_ string, _ []byte) error {
	var err error
	s.before, err = s.provider.Snapshot()
	if err != nil {
		return err
	}
	if batch, ok := s.counter.(gameplayAchievementProgressBatch); ok {
		s.counters, _, err = batch.ApplyGameplayProgress(s.before.Conditions, nil)
		return err
	}
	s.counters, err = s.counter.CounterValues()
	if err != nil {
		return err
	}
	for _, condition := range s.before.Conditions {
		if _, err := s.counter.SetCondition(condition.Type, condition.SubType, condition.Value); err != nil {
			return err
		}
	}
	return nil
}
func (s *GameplayAchievementObserver) AfterDispatch(path string, request, response []byte) ([]byte, error) {
	after, err := s.provider.Snapshot()
	if err != nil {
		return nil, err
	}
	batch, batches := s.counter.(gameplayAchievementProgressBatch)
	if !batches {
		for _, condition := range after.Conditions {
			if _, err := s.counter.SetCondition(condition.Type, condition.SubType, condition.Value); err != nil {
				return nil, err
			}
		}
	}
	events, err := s.provider.Events(path, request, response, s.before, after)
	if err != nil {
		return nil, err
	}
	var recorded []GameplayAchievementRecordedEvent
	if len(events) > 0 {
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 || s.session == "" {
			return nil, fmt.Errorf("achievement: gameplay event sequence unavailable")
		}
		for i, event := range events {
			identity := fmt.Sprintf("%s/%s/%d/%d/%s", s.session, path, seq, i, event.Identity)
			if event.StableIdentity {
				identity = event.Identity
			}
			recorded = append(recorded, GameplayAchievementRecordedEvent{Identity: identity, Type: event.Type, SubType: event.SubType, Count: event.Count})
		}
	}
	var values map[int]uint64
	if batches {
		_, values, err = batch.ApplyGameplayProgress(after.Conditions, recorded)
	} else {
		for _, event := range recorded {
			if _, err = s.counter.RecordEvent(event.Identity, event.Type, event.SubType, event.Count); err != nil {
				return nil, err
			}
		}
		values, err = s.counter.CounterValues()
	}
	if err != nil {
		return nil, err
	}
	var groups []int
	for group, value := range values {
		if s.counters[group] != value {
			groups = append(groups, group)
		}
	}
	sort.Ints(groups)
	var notify []byte
	for _, group := range groups {
		row := wire.AppendVarint(nil, 1, uint64(group))
		row = wire.AppendVarint(row, 2, values[group])
		row = wire.AppendVarint(row, 3, 1)
		notify = wire.AppendBytes(notify, 2, row)
	}
	return notify, nil
}

type GameplayCharacterSource interface{ RawAll() []player.Character }
type GameplayGachaSource interface{ GachaGrantSummary() map[string]uint64 }
type GameplayCostumeSource interface{ Costumes() []player.Costume }
type GameplayEquipmentSource interface{ All() []player.Equipment }
type GameplayItemSource interface{ All() []player.Item }

// OwnedGameplayAchievementProvider uses before/after authoritative ownership.
// GET conditions are event counters: existing inventory is never a historical
// acquisition total. Conditions are supplied only by proven state projections.
type OwnedGameplayAchievementProvider struct {
	Gacha                            GameplayGachaSource
	Characters                       GameplayCharacterSource
	Costumes                         GameplayCostumeSource
	Equipment                        GameplayEquipmentSource
	Items                            GameplayItemSource
	Design                           *gamedata.AchievementCounterDesign
	CharacterGrades, EquipmentGrades map[uint64]uint64
	Conditions                       func() ([]GameplayAchievementCondition, error)
	FieldObjects                     func() (map[string]gamedata.FieldRewardObject, error)
}

func (p *OwnedGameplayAchievementProvider) Snapshot() (GameplayAchievementSnapshot, error) {
	s, err := p.InventorySnapshot()
	if err != nil {
		return s, err
	}
	s.Characters = map[uint64]player.Character{}
	if p.Characters != nil {
		for _, v := range p.Characters.RawAll() {
			if player.IsCharmCharacter(v) || player.IsStoryCharacter(v) {
				continue
			}
			s.Characters[v.InvenIndex] = v
		}
	}
	if p.Conditions != nil {
		s.Conditions, err = p.Conditions()
	}
	if err == nil && p.FieldObjects != nil {
		s.FieldObjects, err = p.FieldObjects()
	}
	if p.Gacha != nil {
		s.GachaGrants = p.Gacha.GachaGrantSummary()
	}
	return s, err
}

// InventorySnapshot projects the three authoritative domains used by event
// missions. It reads a fresh before/after view, without querying unrelated
// field-object, main-quest, character or gacha-history projections.
func (p *OwnedGameplayAchievementProvider) InventorySnapshot() (GameplayAchievementSnapshot, error) {
	s := GameplayAchievementSnapshot{Costumes: map[uint64]player.Costume{}, Equipment: map[uint64]player.Equipment{}, Items: map[[2]uint64]uint64{}}
	if p.Costumes != nil {
		for _, v := range p.Costumes.Costumes() {
			s.Costumes[v.InvenIndex] = v
		}
	}
	if p.Equipment != nil {
		for _, v := range p.Equipment.All() {
			s.Equipment[v.InvenIndex] = v
		}
	}
	if p.Items != nil {
		for _, v := range p.Items.All() {
			s.Items[[2]uint64{v.Type, v.ID}] += v.Count
		}
	}
	return s, nil
}
func (p *OwnedGameplayAchievementProvider) Events(path string, _ []byte, _ []byte, before, after GameplayAchievementSnapshot) ([]GameplayAchievementEvent, error) {
	var events []GameplayAchievementEvent
	conditions := map[[2]uint64]bool{}
	if p.Design != nil {
		for _, condition := range p.Design.Conditions {
			conditions[[2]uint64{condition.Type, condition.SubType}] = true
		}
	}
	emit := func(kind, sub, count uint64, id string) {
		if count == 0 {
			return
		}
		if p.Design != nil && !conditions[[2]uint64{kind, sub}] {
			return
		}
		events = append(events, GameplayAchievementEvent{Type: kind, SubType: sub, Count: count, Identity: id})
	}
	for index, v := range after.Characters {
		old, exists := before.Characters[index]
		id := "char:" + strconv.FormatUint(index, 10)
		if !exists {
			emit(19, 0, 1, id)
			if grade := p.CharacterGrades[v.ID]; grade != 0 {
				emit(20, grade, 1, id)
			}
		}
		if exists && v.Level > old.Level {
			emit(22, 0, v.Level-old.Level, id)
		}
	}
	for index, v := range after.Costumes {
		old, exists := before.Costumes[index]
		id := "costume:" + strconv.FormatUint(index, 10)
		if !exists {
			emit(26, 0, 1, id)
			emit(26, v.ID, 1, id)
		}
		if exists && v.Level > old.Level {
			emit(27, 0, v.Level-old.Level, id)
		}
	}
	for index, v := range after.Equipment {
		old, exists := before.Equipment[index]
		id := "equipment:" + strconv.FormatUint(index, 10)
		if !exists {
			emit(7, 0, 1, id)
			if grade := p.EquipmentGrades[v.ID]; grade != 0 {
				emit(7, grade, 1, id)
			}
		}
		if exists && v.UpgradeAttempts > old.UpgradeAttempts {
			successes := uint64(0)
			if v.Level > old.Level {
				successes = v.Level - old.Level
			}
			attempts := v.UpgradeAttempts - old.UpgradeAttempts
			if successes > attempts {
				return nil, fmt.Errorf("achievement: equipment upgrade delta exceeds attempts")
			}
			if successes > 0 {
				emit(9, p.EquipmentGrades[v.ID], successes, id)
			}
			emit(10, 0, attempts-successes, id)
		}
	}
	if path == "/EatFood" || path == "/EatFoodAuto" {
		for key, count := range before.Items {
			if key[0] == 5 && count > after.Items[key] {
				emit(55, 0, count-after.Items[key], "food:"+strconv.FormatUint(key[1], 10))
			}
		}
	}
	for key, obj := range after.FieldObjects {
		if _, exists := before.FieldObjects[key]; !exists {
			switch obj.Type {
			case 1:
				emit(28, 0, 1, key)
			case 2:
				emit(31, 0, 1, key)
			case 5:
				emit(30, 0, 1, key)
			}
		}
	}
	for identity, count := range after.GachaGrants {
		if _, exists := before.GachaGrants[identity]; !exists {
			emit(54, 0, count, "gacha-grant:"+identity)
			if len(events) > 0 && events[len(events)-1].Identity == "gacha-grant:"+identity {
				events[len(events)-1].StableIdentity = true
			}
		}
	}
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.SubType != b.SubType {
			return a.SubType < b.SubType
		}
		return a.Identity < b.Identity
	})
	return events, nil
}

// GameplayAchievementProvider exposes only authoritative owned instances and
// persisted quest/object state. It does not manufacture past acquisition counts.
func (s *Service) GameplayAchievementProvider(design *gamedata.AchievementCounterDesign, grades gamedata.GameplayAchievementGrades) *OwnedGameplayAchievementProvider {
	p := &OwnedGameplayAchievementProvider{Design: design, CharacterGrades: grades.Characters, EquipmentGrades: grades.Equipment}
	if s.characters != nil {
		p.Characters = s.characters
	}
	if s.collection != nil {
		p.Costumes = s.collection
		p.Gacha = s.collection
	}
	if s.equipment != nil {
		p.Equipment = s.equipment
	}
	if s.inventory != nil {
		p.Items = s.inventory
	}
	p.Conditions = func() ([]GameplayAchievementCondition, error) {
		var conditions []GameplayAchievementCondition
		seen := map[[2]uint64]bool{}
		if s.storyCatalog == nil {
			return nil, nil
		}
		for _, c := range design.Conditions {
			if c.Type < 14 || c.Type > 16 {
				continue
			}
			key := [2]uint64{c.Type, c.SubType}
			if seen[key] {
				continue
			}
			seen[key] = true
			pack, known := s.storyCatalog.Packs[int(c.SubType)]
			if !known || len(pack.MainQuestIDs) == 0 {
				continue
			}
			complete := true
			for _, qid := range pack.MainQuestIDs {
				if !s.state.QuestCleared(qid, pack.ID, int(c.Type-14)) {
					complete = false
					break
				}
			}
			if complete {
				conditions = append(conditions, GameplayAchievementCondition{Type: c.Type, SubType: c.SubType, Value: 1})
			}
		}
		return conditions, nil
	}
	p.FieldObjects = func() (map[string]gamedata.FieldRewardObject, error) {
		objects := map[string]gamedata.FieldRewardObject{}
		openedPeriods, err := s.state.OpenedFieldRewardPeriods()
		if err != nil {
			return nil, err
		}
		// Opened IDs are persisted independently of quest difficulty. Only loaded
		// packs with real opened entries require their reward design to be resolved.
		packs := map[int]bool{}
		for id := range s.packs {
			packs[id] = true
		}
		for id := range s.fieldPacks {
			packs[id] = true
		}
		for pack := range packs {
			periods := openedPeriods[pack]
			if len(periods) == 0 {
				continue
			}
			d, err := s.fieldObjectDesign(pack)
			if err != nil {
				return nil, err
			}
			for id, openedPeriod := range periods {
				obj, known := d.Objects[id]
				if !known {
					return nil, fmt.Errorf("achievement: opened field design absent")
				}
				period, err := s.fieldObjectPeriodFor(pack, obj)
				if err != nil {
					continue
				}
				if openedPeriod == period {
					objects[fmt.Sprintf("field:%d:%d:%s", pack, id, period)] = obj
				}
			}
		}
		return objects, nil
	}
	return p
}
