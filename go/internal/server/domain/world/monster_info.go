package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"slices"
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

func (s *Service) AttachFieldMonsterDamage(f func(command.Context, int, uint64, string) ([][]byte, error)) {
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
func (s *Service) loadMonsterState(ctx command.Context) (fieldMonsterSnapshot, error) {
	v := fieldMonsterSnapshot{Monsters: map[string]fieldMonsterState{}, Claims: map[string]bool{}, Requests: map[string]fieldMonsterReply{}}
	if s.monsterStore == nil {
		return v, nil
	}
	b, e := s.monsterStore.Load(ctx.State, "field_monster_runtime")
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
func (s *Service) saveMonsterState(ctx command.Context, v fieldMonsterSnapshot) error {
	if s.monsterStore == nil {
		return fmt.Errorf("world: monster persistence unavailable")
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.monsterStore.Save(ctx.State, "field_monster_runtime", b)
}
func monsterKey(pack, id int) string {
	return fmt.Sprintf("%d/0/%d", pack, id)
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
	key := monsterKey(pack, m.ID)
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
	// NormalHunt regeneration is unlocked by normal story progress, independently
	// of the optional difficulty main quest that hides these field monsters.
	return m.QuestID == 0 || s.state.QuestCleared(m.QuestID, pack, 0)
}

func (s *Service) attachFieldMonsterDesign(source *gamedata.Source) {
	s.monsterLoader = func(pack int) ([]gamedata.FieldMonsterDesign, error) {
		return source.FieldMonsters(pack)
	}
	s.monsterRewards = func(pack int, deck uint64) ([]gamedata.BattleReward, error) {
		return source.BattleRewards(pack, deck)
	}
	s.monsterMaps = func(pack int) (map[int][]int, error) { return source.FieldMonsterMaps(pack) }
}
func (s *Service) authorizeMonsterMap(ctx command.Context, pack, id int) error {
	available, err := s.rewardMonsterAvailable(ctx, pack, id)
	if err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("world: reward monster not summoned")
	}
	if s.monsterMaps == nil {
		return nil
	}
	maps, e := s.monsterMaps(pack)
	if e != nil {
		return e
	}
	current, e := s.currentFieldMap(ctx, pack)
	if e != nil {
		return e
	}
	if slices.Contains(maps[id], current) {
		return nil
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
func (s *Service) BeginFieldMonsterBattle(ctx command.Context, pack int, id, deck uint64) (string, bool, error) {
	m, found, e := s.findFieldMonster(pack, int(id))
	if e == nil && found {
		available, err := s.rewardMonsterAvailable(ctx, pack, int(id))
		if err != nil {
			return "", true, err
		}
		if !available {
			return "", true, fmt.Errorf("world: reward monster not summoned")
		}
	}
	if found && m.GroupID == 0 {
		return "", false, nil
	}
	if e != nil || !found {
		return "", found, e
	}
	if !s.packUnlocked(ctx, pack) || !s.monsterEligible(pack, m) {
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
	if e = s.authorizeMonsterMap(ctx, pack, int(id)); e != nil {
		return "", true, e
	}
	v, e := s.loadMonsterState(ctx)
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
	if e = s.saveMonsterState(ctx, v); e != nil {
		return "", true, e
	}
	return fmt.Sprintf("fieldmonster:%s:%d", monsterKey(pack, m.ID), state.Generation), true, nil
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
