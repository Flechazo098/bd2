package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
)

type gameplayTestSource struct {
	characters []player.Character
	costumes   []player.Costume
	equipment  []player.Equipment
	items      []player.Item
}

func (s *gameplayTestSource) RawAll() []player.Character { return s.characters }

type gameplayCostumes struct{ s *gameplayTestSource }

func (s gameplayCostumes) Costumes() []player.Costume { return s.s.costumes }

type gameplayEquipment struct{ s *gameplayTestSource }

func (s gameplayEquipment) All() []player.Equipment { return s.s.equipment }

type gameplayItems struct{ s *gameplayTestSource }

func (s gameplayItems) All() []player.Item { return s.s.items }
func TestTemporaryPartyMembersCannotGrantPermanentAcquisitionAchievements(t *testing.T) {
	source := &gameplayTestSource{}
	p := &OwnedGameplayAchievementProvider{Characters: source}
	before, e := p.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	source.characters = []player.Character{{ID: 10, InvenIndex: player.CharmCharacterIndexBase + 1, Level: 1}, {ID: 11, InvenIndex: player.StoryCharacterIndexBase + 1, Level: 1}, {ID: 12, InvenIndex: 77, Level: 1}}
	after, e := p.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	events, e := p.Events("/TalentSkillUse", nil, nil, before, after)
	if e != nil {
		t.Fatal(e)
	}
	if len(after.Characters) != 1 || len(events) != 1 || events[0].Type != 19 || events[0].Count != 1 {
		t.Fatalf("temporary ownership inflated achievements: %+v", events)
	}
}
func TestGameplayAchievementProjectsChangesWithoutFakeHistoricalGets(t *testing.T) {
	design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{701: {0}, 702: {0}, 703: {0}, 704: {0}, 705: {0}}, Conditions: map[int]gamedata.AchievementCondition{701: {Type: 7, SubType: 4}, 702: {Type: 22}, 703: {Type: 26}, 704: {Type: 55}, 705: {Type: 14, SubType: 909}}}
	service, err := NewAchievementService(design, stateio.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	source := &gameplayTestSource{characters: []player.Character{{ID: 1, InvenIndex: 1, Level: 3}}, costumes: []player.Costume{{ID: 2, InvenIndex: 2}}, equipment: []player.Equipment{{ID: 3, InvenIndex: 3}}, items: []player.Item{{Type: 5, ID: 8, Count: 10}}}
	provider := &OwnedGameplayAchievementProvider{Characters: source, Costumes: gameplayCostumes{source}, Equipment: gameplayEquipment{source}, Items: gameplayItems{source}, Design: design, EquipmentGrades: map[uint64]uint64{3: 4}}
	observer, err := NewGameplayAchievementObserver(service, provider)
	if err != nil {
		t.Fatal(err)
	}
	observer.BeginSession("test")
	req := wire.AppendVarint(nil, 1, 1)
	if err := observer.BeforeDispatch("/AchievementInfo", req); err != nil {
		t.Fatal(err)
	}
	notify, err := observer.AfterDispatch("/AchievementInfo", req, nil)
	if err != nil || len(notify) != 0 {
		t.Fatalf("invented historical counts notify=%x err=%v", notify, err)
	}
	if err := observer.BeforeDispatch("/CharGrowth", req); err != nil {
		t.Fatal(err)
	}
	source.characters[0].Level = 5
	notify, err = observer.AfterDispatch("/CharGrowth", req, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ := wire.Bytes(notify, 2)
	group, _, _ := wire.Varint(row, 1)
	value, _, _ := wire.Varint(row, 2)
	if group != 702 || value != 2 {
		t.Fatalf("absolute level update group=%d value=%d", group, value)
	}
	if err := observer.BeforeDispatch("/CharGrowth", req); err != nil {
		t.Fatal(err)
	}
	notify, err = observer.AfterDispatch("/CharGrowth", req, nil)
	if err != nil || len(notify) != 0 {
		t.Fatal("replay emitted increment")
	}
	req = wire.AppendVarint(nil, 1, 2)
	if err := observer.BeforeDispatch("/FieldObjectReward", req); err != nil {
		t.Fatal(err)
	}
	source.equipment = append(source.equipment, player.Equipment{ID: 3, InvenIndex: 4})
	if _, err := observer.AfterDispatch("/FieldObjectReward", req, nil); err != nil {
		t.Fatal(err)
	}
	values, err := service.CounterValues()
	if err != nil || values[701] != 1 {
		t.Fatalf("new equipment values=%v err=%v", values, err)
	}
}

func TestGameplayAchievementUsesChangedPackIDsAndDoesNotReplayChest(t *testing.T) {
	s := testService()
	s.storyCatalog = &gamedata.StoryCatalog{Packs: map[int]gamedata.StoryPack{808: {ID: 808, MainQuestIDs: []int{111, 222}}}}
	design := &gamedata.AchievementCounterDesign{Conditions: map[int]gamedata.AchievementCondition{99: {Type: 15, SubType: 808}}}
	p := s.GameplayAchievementProvider(design, gamedata.GameplayAchievementGrades{})
	if err := s.state.ClearQuest(111, 808, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.state.ClearQuest(222, 808, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conditions) != 1 || snapshot.Conditions[0].SubType != 808 || snapshot.Conditions[0].Type != 15 || snapshot.Conditions[0].Value != 1 {
		t.Fatalf("pack projection=%+v", snapshot.Conditions)
	}
	before := GameplayAchievementSnapshot{FieldObjects: map[string]gamedata.FieldRewardObject{}}
	after := GameplayAchievementSnapshot{FieldObjects: map[string]gamedata.FieldRewardObject{"chest": {Type: 2}}}
	p.Design = nil
	events, err := p.Events("/FieldObjectReward", nil, nil, before, after)
	if err != nil || len(events) != 1 || events[0].Type != 31 {
		t.Fatalf("chest events=%+v err=%v", events, err)
	}
	events, err = p.Events("/FieldObjectReward", nil, nil, after, after)
	if err != nil || len(events) != 0 {
		t.Fatal("chest replay increments")
	}
}

func TestGameplayGachaEventsFollowNewDurableGrantKeys(t *testing.T) {
	p := &OwnedGameplayAchievementProvider{}
	before := GameplayAchievementSnapshot{GachaGrants: map[string]uint64{"old": 10}}
	after := GameplayAchievementSnapshot{GachaGrants: map[string]uint64{"old": 10, "new": 3}}
	events, err := p.Events("/GachaBuy", nil, nil, before, after)
	if err != nil || len(events) != 1 || events[0].Type != 54 || events[0].Count != 3 {
		t.Fatalf("new grant=%+v err=%v", events, err)
	}
	events, err = p.Events("/GachaBuy", nil, []byte{1, 2, 3}, after, after)
	if err != nil || len(events) != 0 {
		t.Fatal("cached response counted again")
	}
}

type gameplayHistoryProvider struct {
	snapshot GameplayAchievementSnapshot
	OwnedGameplayAchievementProvider
}

func (p *gameplayHistoryProvider) Snapshot() (GameplayAchievementSnapshot, error) {
	return p.snapshot, nil
}
func TestGameplayRecordedHistoryAndFutureShareStableGrantReceipts(t *testing.T) {
	store := stateio.NewMemory()
	design := &gamedata.AchievementCounterDesign{Groups: map[int][]int{808: {0}}, Conditions: map[int]gamedata.AchievementCondition{808: {Type: 54}}}
	service, err := NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	provider := &gameplayHistoryProvider{snapshot: GameplayAchievementSnapshot{GachaGrants: map[string]uint64{"old": 10}}}
	observer, err := NewGameplayAchievementObserver(service, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.SyncRecordedHistory(); err != nil {
		t.Fatal(err)
	}
	observer.BeginSession("login1")
	req := wire.AppendVarint(nil, 1, 1)
	if err := observer.BeforeDispatch("/GachaBuy", req); err != nil {
		t.Fatal(err)
	}
	provider.snapshot = GameplayAchievementSnapshot{GachaGrants: map[string]uint64{"old": 10, "new": 3}}
	if _, err := observer.AfterDispatch("/GachaBuy", req, nil); err != nil {
		t.Fatal(err)
	}
	service, err = NewAchievementService(design, store)
	if err != nil {
		t.Fatal(err)
	}
	observer, err = NewGameplayAchievementObserver(service, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.SyncRecordedHistory(); err != nil {
		t.Fatal(err)
	}
	values, err := service.CounterValues()
	if err != nil || values[808] != 13 {
		t.Fatalf("history/future duplicated values=%v err=%v", values, err)
	}
}
