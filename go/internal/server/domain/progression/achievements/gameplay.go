package achievements

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"fmt"
	"sort"
	"strconv"
)

// GameplayAchievementCounter persists each event in the request transaction.
type GameplayAchievementCounter interface {
	RecordEvent(ctx command.Context, _ string, _ uint64, _ uint64, _ uint64) ([][]byte, error)
	SetCondition(ctx command.Context, _ uint64, _ uint64, _ uint64) ([][]byte, error)
	CounterValues(ctx command.Context) (map[int]uint64, error)
}

type gameplayAchievementProgressBatch interface {
	ApplyGameplayProgress(ctx command.Context, _ []GameplayAchievementCondition, _ []GameplayAchievementRecordedEvent) (map[int]uint64, map[int]uint64, error)
}
type GameplayAchievementEvent struct {
	Type, SubType, Count uint64
	Identity             string
	StableIdentity       bool
}
type GameplayAchievementCondition struct{ Type, SubType, Value uint64 }
type GameplayAchievementSnapshot struct {
	Characters   map[uint64]roster.Character
	Costumes     map[uint64]roster.Costume
	Equipment    map[uint64]assets.Equipment
	Items        map[[2]uint64]uint64
	Conditions   []GameplayAchievementCondition
	FieldObjects map[string]gamedata.FieldRewardObject
	GachaGrants  map[string]uint64
}
type GameplayAchievementProvider interface {
	Snapshot(ctx command.Context) (GameplayAchievementSnapshot, error)
	Events(string, []byte, []byte, GameplayAchievementSnapshot, GameplayAchievementSnapshot) ([]GameplayAchievementEvent, error)
}
type GameplayAchievementObserver struct {
	counter  GameplayAchievementCounter
	provider GameplayAchievementProvider

	before          GameplayAchievementSnapshot
	counters        map[int]uint64
	conditionValues map[int]uint64
	version         string
	ready           bool
}

func NewGameplayAchievementObserver(counter GameplayAchievementCounter, provider GameplayAchievementProvider) (*GameplayAchievementObserver, error) {
	if counter == nil || provider == nil {
		return nil, fmt.Errorf("achievement: gameplay provider unavailable")
	}
	return &GameplayAchievementObserver{counter: counter, provider: provider}, nil
}
func (s *GameplayAchievementObserver) BeginLogin(ctx command.Context) {
	s.ready = false
}

func (s *GameplayAchievementObserver) observationVersion() string {
	if provider, ok := s.provider.(interface{ ObservationVersion() string }); ok {
		return provider.ObservationVersion()
	}
	return ""
}

type GameplayCharacterSource interface{ RawAll() []roster.Character }
type GameplayGachaSource interface{ GachaGrantSummary() map[string]uint64 }
type GameplayCostumeSource interface{ Costumes() []roster.Costume }
type GameplayEquipmentSource interface {
	All(ctx command.Context) []assets.Equipment
}
type GameplayItemSource interface {
	All(ctx command.Context) []assets.Item
}

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
	Conditions                       func(command.Context) ([]GameplayAchievementCondition, error)
	FieldObjects                     func(command.Context) (map[string]gamedata.FieldRewardObject, error)
	StateVersion                     func() uint64
	TransientVersion                 func() string
}

func (p *OwnedGameplayAchievementProvider) ObservationVersion() string {
	if p.StateVersion == nil {
		return ""
	}
	transient := ""
	if p.TransientVersion != nil {
		transient = p.TransientVersion()
	}
	var costumes uint64
	if source, ok := p.Costumes.(interface{ ObservationVersion() uint64 }); ok {
		costumes = source.ObservationVersion()
	}
	return fmt.Sprintf("%d/%d/%s", p.StateVersion(), costumes, transient)
}

func (p *OwnedGameplayAchievementProvider) Snapshot(ctx command.Context) (GameplayAchievementSnapshot, error) {
	s, err := p.InventorySnapshot(ctx)
	if err != nil {
		return s, err
	}
	s.Characters = map[uint64]roster.Character{}
	if p.Characters != nil {
		for _, v := range p.Characters.RawAll() {
			if roster.IsCharmCharacter(v) || roster.IsStoryCharacter(v) {
				continue
			}
			s.Characters[v.InvenIndex] = v
		}
	}
	if p.Conditions != nil {
		s.Conditions, err = p.Conditions(ctx)
	}
	if err == nil && p.FieldObjects != nil {
		s.FieldObjects, err = p.FieldObjects(ctx)
	}
	if p.Gacha != nil {
		s.GachaGrants = p.Gacha.GachaGrantSummary()
	}
	return s, err
}

// InventorySnapshot projects the three authoritative domains used by event
// missions. It reads a fresh before/after view, without querying unrelated
// field-object, main-quest, character or gacha-history projections.
func (p *OwnedGameplayAchievementProvider) InventorySnapshot(ctx command.Context) (GameplayAchievementSnapshot, error) {
	s := GameplayAchievementSnapshot{Costumes: map[uint64]roster.Costume{}, Equipment: map[uint64]assets.Equipment{}, Items: map[[2]uint64]uint64{}}
	if p.Costumes != nil {
		for _, v := range p.Costumes.Costumes() {
			s.Costumes[v.InvenIndex] = v
		}
	}
	if p.Equipment != nil {
		for _, v := range p.Equipment.All(ctx) {
			s.Equipment[v.InvenIndex] = v
		}
	}
	if p.Items != nil {
		for _, v := range p.Items.All(ctx) {
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
