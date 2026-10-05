package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type fieldMonsterState struct {
	Generation              uint64
	Start, Respawn, LifeEnd int64
	Defeated                bool
	Period                  string
}
type fieldMonsterSnapshot struct {
	Monsters map[string]fieldMonsterState
	Claims   map[string]bool
	Requests map[string]fieldMonsterReply
}
type fieldMonsterReply struct{ Request, Response []byte }

func (s *Service) BeginSession(id string) { s.monsterSession = id }
func (s *Service) AttachFieldMonsterDamage(f func(int, uint64, string) ([][]byte, error)) {
	s.monsterDamage = f
}

func (s *Service) AttachFieldMonsterState(store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("world: missing monster state store")
	}
	s.monsterStore = store
	return nil
}
func (s *Service) monsterTime() time.Time {
	if s.monsterNow != nil {
		return s.monsterNow()
	}
	return time.Now()
}
func (s *Service) loadMonsterState() (fieldMonsterSnapshot, error) {
	v := fieldMonsterSnapshot{Monsters: map[string]fieldMonsterState{}, Claims: map[string]bool{}, Requests: map[string]fieldMonsterReply{}}
	if s.monsterStore == nil {
		return v, nil
	}
	b, e := s.monsterStore.Load("field_monster_runtime")
	if e != nil || b == nil {
		return v, e
	}
	if e = stateio.RequireExactJSONObject(b, "Monsters", "Claims", "Requests"); e != nil {
		return v, e
	}
	if e = json.Unmarshal(b, &v); e != nil || v.Monsters == nil || v.Claims == nil || v.Requests == nil {
		return v, fmt.Errorf("world: invalid field monster state")
	}
	for _, m := range v.Monsters {
		if m.Generation == 0 || m.Start <= 0 || m.Respawn < 0 || m.LifeEnd < 0 {
			return v, fmt.Errorf("world: invalid monster timestamps")
		}
	}
	return v, nil
}
func (s *Service) saveMonsterState(v fieldMonsterSnapshot) error {
	if s.monsterStore == nil {
		return fmt.Errorf("world: monster persistence unavailable")
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.monsterStore.Save("field_monster_runtime", b)
}
func monsterKey(pack, difficulty, id int) string {
	return fmt.Sprintf("%d/%d/%d", pack, difficulty, id)
}
func (s *Service) monsterPeriod(m gamedata.FieldMonsterDesign) (string, error) {
	if m.ResetType == 0 {
		return "own", nil
	}
	reset := 0
	if m.ResetType == 2 {
		reset = 3
	}
	return s.fieldReset.Period(reset, s.monsterTime())
}
func (s *Service) monsterState(v *fieldMonsterSnapshot, pack int, m gamedata.FieldMonsterDesign) (fieldMonsterState, error) {
	key := monsterKey(pack, s.questDifficulty(pack), m.ID)
	old, exists := v.Monsters[key]
	period, e := s.monsterPeriod(m)
	if e != nil {
		return old, e
	}
	now := s.monsterTime().UnixMilli()
	fresh := func(generation uint64, start int64) fieldMonsterState {
		r := fieldMonsterState{Generation: generation, Start: start, Period: period}
		if m.LifeSeconds > 0 {
			r.LifeEnd = start + int64(m.LifeSeconds)*1000
		}
		return r
	}
	if !exists {
		old = fresh(1, now)
	} else if old.Period != period {
		old = fresh(old.Generation+1, now)
	} else if old.Respawn > 0 && now >= old.Respawn {
		old = fresh(old.Generation+1, old.Respawn)
	} else if old.LifeEnd > 0 && now >= old.LifeEnd && !old.Defeated {
		old.Defeated = true
		old.Respawn = old.LifeEnd + int64(m.RegenSeconds)*1000
		if old.Respawn <= now {
			old = fresh(old.Generation+1, now)
		}
	}
	v.Monsters[key] = old
	return old, nil
}
func (s *Service) monsterEligible(pack int, m gamedata.FieldMonsterDesign) bool {
	return m.QuestID == 0 || s.state.QuestCleared(m.QuestID, pack, s.questDifficulty(pack))
}
func monsterWire(m gamedata.FieldMonsterDesign, state fieldMonsterState, eligible bool) []byte {
	b := wire.AppendVarint(nil, 1, uint64(m.ID))
	if m.BattleDeck != 0 {
		b = wire.AppendVarint(b, 2, m.BattleDeck)
	}
	if state.Respawn > 0 {
		b = wire.AppendVarint(b, 3, uint64(state.Respawn))
	}
	if state.LifeEnd > 0 {
		b = wire.AppendVarint(b, 4, uint64(state.LifeEnd))
	}
	b = wire.AppendVarint(b, 5, uint64(m.GroupID))
	if eligible {
		b = wire.AppendVarint(b, 6, 1)
	}
	return b
}
func (s *Service) monsterRows(pack int, filter map[int]bool) ([][]byte, error) {
	if s.monsterLoader == nil {
		return nil, fmt.Errorf("%w: missing field monster design", ErrInvalidRequest)
	}
	design, e := s.monsterLoader(pack)
	if e != nil {
		return nil, e
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return nil, e
	}
	var rows [][]byte
	changed := false
	for _, m := range design {
		if m.GroupID == 0 {
			continue
		}
		if filter != nil && !filter[m.ID] {
			continue
		}
		eligible := s.monsterEligible(pack, m)
		state := fieldMonsterState{}
		if eligible {
			state, e = s.monsterState(&v, pack, m)
			if e != nil {
				return nil, e
			}
			changed = true
		}
		rows = append(rows, monsterWire(m, state, eligible))
	}
	if changed && s.monsterStore != nil {
		if e = s.saveMonsterState(v); e != nil {
			return nil, e
		}
	}
	return rows, nil
}
func (s *Service) handleMonsterInfo(request []byte) (int, []byte, bool, error) {
	seq, present, e := wire.Varint(request, 1)
	if e != nil || !present || seq == 0 || seq > 0x7fffffff {
		return 0, nil, true, ErrInvalidRequest
	}
	groups, e := intsRequest(request, 2)
	if e != nil {
		return 0, nil, true, e
	}
	wanted := map[int]bool{}
	for _, g := range groups {
		if g == 0 || g > 0x7fffffff {
			return 0, nil, true, ErrInvalidRequest
		}
		wanted[int(g)] = true
	}
	pack, e := s.CurrentPackID()
	if e != nil || !s.packUnlocked(pack) || s.monsterLoader == nil {
		return 0, nil, true, ErrInvalidRequest
	}
	design, e := s.monsterLoader(pack)
	if e != nil {
		return 0, nil, true, e
	}
	filter := map[int]bool{}
	for _, m := range design {
		if wanted[m.GroupID] {
			filter[m.ID] = true
		}
	}
	rows, e := s.monsterRows(pack, filter)
	if e != nil {
		return 0, nil, true, e
	}
	var b []byte
	for _, r := range rows {
		b = wire.AppendBytes(b, 1, r)
	}
	return 51, b, true, nil
}
func (s *Service) attachFieldMonsterDesign(root, version string) {
	s.monsterLoader = func(pack int) ([]gamedata.FieldMonsterDesign, error) {
		return gamedata.LoadFieldMonsters(root, version, pack)
	}
	s.monsterRewards = func(pack int, deck uint64) ([]gamedata.BattleReward, error) {
		return gamedata.BattleDeckRewards(root, version, pack, deck)
	}
	s.monsterMaps = func(pack int) (map[int][]int, error) { return gamedata.LoadFieldMonsterMaps(root, version, pack) }
}
func (s *Service) authorizeMonsterMap(pack, id int) error {
	if s.monsterMaps == nil {
		return nil
	}
	maps, e := s.monsterMaps(pack)
	if e != nil {
		return e
	}
	current, e := s.currentFieldMap(pack)
	if e != nil {
		return e
	}
	for _, mapID := range maps[id] {
		if current == mapID {
			return nil
		}
	}
	return fmt.Errorf("world: monster outside current map")
}
func (s *Service) findFieldMonster(pack, id int) (gamedata.FieldMonsterDesign, bool, error) {
	if s.monsterLoader == nil {
		return gamedata.FieldMonsterDesign{}, false, nil
	}
	d, e := s.monsterLoader(pack)
	if e != nil {
		return gamedata.FieldMonsterDesign{}, false, e
	}
	for _, m := range d {
		if m.ID == id {
			return m, true, nil
		}
	}
	return gamedata.FieldMonsterDesign{}, false, nil
}

// BeginFieldMonsterBattle locks the regenerated instance for retry-safe rewards.
// Scripted monsters without regeneration return handled=false.
func (s *Service) BeginFieldMonsterBattle(pack int, id, deck uint64) (string, bool, error) {
	m, found, e := s.findFieldMonster(pack, int(id))
	if found && m.GroupID == 0 {
		return "", false, nil
	}
	if e != nil || !found {
		return "", found, e
	}
	if !s.packUnlocked(pack) || !s.monsterEligible(pack, m) {
		return "", true, ErrInvalidRequest
	}
	deckOK := deck == m.BattleDeck
	for _, allowed := range m.BattleDecks {
		if deck == allowed {
			deckOK = true
		}
	}
	if m.BattleDeck != 0 && !deckOK {
		return "", true, fmt.Errorf("world: field monster deck mismatch")
	}
	if e = s.authorizeMonsterMap(pack, int(id)); e != nil {
		return "", true, e
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return "", true, e
	}
	state, e := s.monsterState(&v, pack, m)
	if e != nil {
		return "", true, e
	}
	if state.Defeated || state.Respawn > s.monsterTime().UnixMilli() {
		return "", true, fmt.Errorf("world: monster is not spawned")
	}
	if e = s.saveMonsterState(v); e != nil {
		return "", true, e
	}
	return fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, s.questDifficulty(pack), m.ID), state.Generation), true, nil
}
func (s *Service) CompleteFieldMonsterBattle(pack int, id uint64, instance string) ([]byte, error) {
	m, found, e := s.findFieldMonster(pack, int(id))
	if e != nil {
		return nil, e
	}
	if !found {
		return nil, ErrInvalidRequest
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return nil, e
	}
	key := monsterKey(pack, s.questDifficulty(pack), m.ID)
	state, exists := v.Monsters[key]
	if !exists {
		return nil, ErrInvalidRequest
	}
	if !v.Claims[instance] {
		if instance != fmt.Sprintf("fieldmonster:%s:%d", key, state.Generation) {
			return nil, fmt.Errorf("world: stale monster battle")
		}
		state.Defeated = true
		state.Respawn = s.nextMonsterSpawn(m)
		v.Monsters[key] = state
		v.Claims[instance] = true
		if e = s.saveMonsterState(v); e != nil {
			return nil, e
		}
	}
	return monsterWire(m, state, s.monsterEligible(pack, m)), nil
}
func (s *Service) nextMonsterSpawn(m gamedata.FieldMonsterDesign) int64 {
	now := s.monsterTime()
	if m.ResetType == 0 {
		return now.UnixMilli() + int64(m.RegenSeconds)*1000
	}
	shift := 9*time.Hour - s.fieldReset.DailyReset
	t := now.UTC().Add(shift)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	if m.ResetType == 2 {
		delta := (int(s.fieldReset.WeeklyDay) - int(day.Weekday()) + 7) % 7
		day = day.AddDate(0, 0, delta)
	}
	return day.Add(-shift).UnixMilli()
}
func (s *Service) handleFieldMonsterRegen(request []byte) (int, []byte, bool, error) {
	id, e := requestPack(request)
	if e != nil {
		return 0, nil, true, e
	}
	pack, e := s.CurrentPackID()
	if e != nil || !s.packUnlocked(pack) {
		return 0, nil, true, ErrInvalidRequest
	}
	m, found, e := s.findFieldMonster(pack, id)
	if e != nil || !found {
		return 0, nil, true, ErrInvalidRequest
	}
	v, e := s.loadMonsterState()
	if e != nil {
		return 0, nil, true, e
	}
	seq, _, _ := wire.Varint(request, 1)
	identity := fmt.Sprintf("regen:%s:%d", s.monsterSession, seq)
	if s.monsterSession == "" {
		return 0, nil, true, fmt.Errorf("world: missing monster session")
	}
	if reply, ok := v.Requests[identity]; ok {
		if !bytes.Equal(request, reply.Request) {
			return 0, nil, true, ErrInvalidRequest
		}
		return 139, reply.Response, true, nil
	}
	state, e := s.monsterState(&v, pack, m)
	if e != nil {
		return 0, nil, true, e
	}
	if !state.Defeated {
		state.Defeated = true
		state.Respawn = s.nextMonsterSpawn(m)
		v.Monsters[monsterKey(pack, s.questDifficulty(pack), m.ID)] = state
	}
	response := wire.AppendBytes(nil, 1, monsterWire(m, state, s.monsterEligible(pack, m)))
	v.Requests[identity] = fieldMonsterReply{Request: append([]byte(nil), request...), Response: response}
	if e = s.saveMonsterState(v); e != nil {
		return 0, nil, true, e
	}
	return 139, response, true, nil
}
