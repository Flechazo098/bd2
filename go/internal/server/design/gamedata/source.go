package gamedata

import "sync"

type designEntry[V any] struct {
	once  sync.Once
	value V
	err   error
}
type designIndex[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*designEntry[V]
}

func (i *designIndex[K, V]) get(key K, load func() (V, error)) (V, error) {
	i.mu.Lock()
	if i.entries == nil {
		i.entries = make(map[K]*designEntry[V])
	}
	entry := i.entries[key]
	if entry == nil {
		entry = &designEntry[V]{}
		i.entries[key] = entry
	}
	i.mu.Unlock()
	entry.once.Do(func() { entry.value, entry.err = load() })
	if entry.err != nil {
		i.mu.Lock()
		if i.entries[key] == entry {
			delete(i.entries, key)
		}
		i.mu.Unlock()
	}
	return entry.value, entry.err
}

// Source owns version-scoped immutable rules shared by all player instances.
// Callers must copy a rule before changing any map, slice or pointed-to value.
type Source struct {
	root, version    string
	fieldObjects     designIndex[int, FieldObjectDesign]
	fieldResearch    designIndex[int, FieldResearchDesign]
	packDetail       designIndex[int, PackDetailDesign]
	fieldMonsters    designIndex[int, []FieldMonsterDesign]
	fieldMonsterMaps designIndex[int, map[int][]int]
	fieldTraps       designIndex[int, FieldTrapDesign]
	nPCReputation    designIndex[int, NPCReputationDesign]
	inns             designIndex[int, []InnRule]
	waypoint         designIndex[uint64, WaypointPack]
	huntingPack      designIndex[int, *HuntingPack]
	monsterHunt      designIndex[uint64, *MonsterHunt]
	dispatch         designIndex[[2]uint64, *DispatchDesign]
	eventGames       designIndex[[2]uint64, *EventGame]
	eventFields      designIndex[uint64, *EventField]
	overwhelm        designIndex[[2]int, OverwhelmQuestRule]
	recruit          designIndex[[2]uint64, RecruitNPC]
	battleRewards    designIndex[[2]uint64, []BattleReward]
}

func NewSource(root, version string) *Source { return &Source{root: root, version: version} }
func (s *Source) FieldObjects(key int) (FieldObjectDesign, error) {
	return s.fieldObjects.get(key, func() (FieldObjectDesign, error) { return LoadFieldObjects(s.root, s.version, key) })
}
func (s *Source) FieldResearch(key int) (FieldResearchDesign, error) {
	return s.fieldResearch.get(key, func() (FieldResearchDesign, error) { return LoadFieldResearch(s.root, s.version, key) })
}
func (s *Source) PackDetail(key int) (PackDetailDesign, error) {
	return s.packDetail.get(key, func() (PackDetailDesign, error) { return LoadPackDetailDesign(s.root, s.version, key) })
}
func (s *Source) FieldMonsters(key int) ([]FieldMonsterDesign, error) {
	return s.fieldMonsters.get(key, func() ([]FieldMonsterDesign, error) { return LoadFieldMonsters(s.root, s.version, key) })
}
func (s *Source) FieldMonsterMaps(key int) (map[int][]int, error) {
	return s.fieldMonsterMaps.get(key, func() (map[int][]int, error) { return LoadFieldMonsterMaps(s.root, s.version, key) })
}
func (s *Source) FieldTraps(key int) (FieldTrapDesign, error) {
	return s.fieldTraps.get(key, func() (FieldTrapDesign, error) { return LoadFieldTraps(s.root, s.version, key) })
}
func (s *Source) NPCReputation(key int) (NPCReputationDesign, error) {
	return s.nPCReputation.get(key, func() (NPCReputationDesign, error) { return LoadNPCReputation(s.root, s.version, key) })
}
func (s *Source) Inns(key int) ([]InnRule, error) {
	return s.inns.get(key, func() ([]InnRule, error) { return LoadInns(s.root, s.version, key) })
}
func (s *Source) Waypoint(key uint64) (WaypointPack, error) {
	return s.waypoint.get(key, func() (WaypointPack, error) { return LoadWaypointPack(s.root, s.version, key) })
}
func (s *Source) HuntingPack(key int) (*HuntingPack, error) {
	return s.huntingPack.get(key, func() (*HuntingPack, error) { return LoadHuntingPack(s.root, s.version, key) })
}
func (s *Source) MonsterHunt(key uint64) (*MonsterHunt, error) {
	return s.monsterHunt.get(key, func() (*MonsterHunt, error) { return LoadMonsterHunt(s.root, s.version, key) })
}

func (s *Source) DispatchDesign(group, id uint64) (*DispatchDesign, error) {
	return s.dispatch.get([2]uint64{group, id}, func() (*DispatchDesign, error) { return LoadDispatchDesign(s.root, s.version, group, id) })
}
func (s *Source) EventGame(kind, id uint64) (*EventGame, error) {
	return s.eventGames.get([2]uint64{kind, id}, func() (*EventGame, error) { return LoadEventGame(s.root, s.version, kind, id) })
}
func (s *Source) EventField(catalog *EventPlayCatalog, id uint64) (*EventField, error) {
	return s.eventFields.get(id, func() (*EventField, error) { return catalog.Field(id) })
}
func (s *Source) OverwhelmQuest(pack, quest int) (OverwhelmQuestRule, error) {
	return s.overwhelm.get([2]int{pack, quest}, func() (OverwhelmQuestRule, error) { return LoadOverwhelmQuest(s.root, s.version, pack, quest) })
}
func (s *Source) RecruitNPC(pack int, npc uint64) (RecruitNPC, error) {
	return s.recruit.get([2]uint64{uint64(pack), npc}, func() (RecruitNPC, error) { return LoadRecruitNPC(s.root, s.version, pack, npc) })
}
func (s *Source) BattleRewards(pack int, deck uint64) ([]BattleReward, error) {
	return s.battleRewards.get([2]uint64{uint64(pack), deck}, func() ([]BattleReward, error) { return BattleDeckRewards(s.root, s.version, pack, deck) })
}

func (s *Source) Root() string    { return s.root }
func (s *Source) Version() string { return s.version }
