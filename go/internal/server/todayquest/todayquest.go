// Package todayquest implements the NPC commission board from TodayQuestTable.
package todayquest

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Economy interface {
	Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type Inventory interface {
	GrantOnce(string, []gamedata.BattleReward) ([]player.Item, error)
	GrantedItems(string) []player.Item
}
type Active struct {
	ID      int      `json:"id"`
	Value   int      `json:"value"`
	Objects []uint64 `json:"objects"`
}
type snapshot struct {
	Period      string            `json:"period"`
	Offered     []int             `json:"offered"`
	Active      map[int]Active    `json:"active"`
	Cleared     map[int]bool      `json:"cleared"`
	Responses   map[int][]byte    `json:"responses"`
	ScoreAwards map[string]uint64 `json:"score_awards"`
}
type Service struct {
	mu                  sync.Mutex
	store               stateio.Store
	design              *gamedata.TodayQuestCatalog
	economy             Economy
	inventory           Inventory
	now                 func() time.Time
	unlocked            func(int) bool
	CompleteReputation  func(string, int, uint64) ([]byte, error)
	CompleteAchievement func(string) error
}

// Request dispatch owns the account transaction; economy/inventory writes and
// this snapshot are committed together by session.BeginOperation.
func Open(store stateio.Store, design *gamedata.TodayQuestCatalog, economy Economy, inventory Inventory, unlocked func(int) bool) (*Service, error) {
	if store == nil || design == nil || len(design.Quests) == 0 || design.Limit <= 0 || design.PostCount <= 0 || economy == nil || inventory == nil || unlocked == nil {
		return nil, fmt.Errorf("todayquest: invalid configuration")
	}
	return &Service{store: store, design: design, economy: economy, inventory: inventory, unlocked: unlocked, now: time.Now}, nil
}
func (s *Service) load() (snapshot, error) {
	p, e := s.design.Reset.Period(3, s.now())
	if e != nil {
		return snapshot{}, e
	}
	raw, e := s.store.Load("today_quests")
	if e != nil {
		return snapshot{}, e
	}
	st := snapshot{Period: p, Active: map[int]Active{}, Cleared: map[int]bool{}, Responses: map[int][]byte{}, ScoreAwards: map[string]uint64{}}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "period", "offered", "active", "cleared", "responses", "score_awards"); e != nil {
			return st, e
		}
		if e = json.Unmarshal(raw, &st); e != nil {
			return st, e
		}
		if st.Period == "" || st.Active == nil || st.Cleared == nil || st.Responses == nil || st.ScoreAwards == nil {
			return st, fmt.Errorf("todayquest: invalid saved state")
		}
	}
	if st.Period != p {
		st = snapshot{Period: p, Active: map[int]Active{}, Cleared: map[int]bool{}, Responses: map[int][]byte{}, ScoreAwards: st.ScoreAwards}
	}
	for id, a := range st.Active {
		q, ok := s.design.Quests[id]
		if !ok || a.ID != id || st.Cleared[id] || a.Value < 0 || a.Value > q.ConditionCount || len(a.Objects) > q.ConditionCount {
			return st, fmt.Errorf("todayquest: invalid active progress")
		}
		seen := map[uint64]bool{}
		for _, object := range a.Objects {
			allowed := false
			for _, v := range q.MagicValues {
				if object == v {
					allowed = true
				}
			}
			if !allowed || seen[object] {
				return st, fmt.Errorf("todayquest: invalid saved object")
			}
			seen[object] = true
		}
		if q.PriorID != 0 && !st.Cleared[q.PriorID] {
			return st, fmt.Errorf("todayquest: missing prior clear")
		}
	}
	for id, cleared := range st.Cleared {
		if _, ok := s.design.Quests[id]; !ok || !cleared {
			return st, fmt.Errorf("todayquest: invalid cleared node")
		}
	}
	for id, b := range st.Responses {
		if _, ok := s.design.Quests[id]; !ok || len(b) == 0 {
			return st, fmt.Errorf("todayquest: invalid clear receipt")
		}
	}
	// Deterministic sampling is a local server policy. Persisted offers are stable
	// across restarts, with PostCount independent roots for every unlocked pack.
	byPack := map[int][]int{}
	for id, q := range s.design.Quests {
		if q.PriorID == 0 && s.unlocked(q.PackID) {
			byPack[q.PackID] = append(byPack[q.PackID], id)
		}
	}
	existing := map[int]bool{}
	for _, id := range st.Offered {
		q, ok := s.design.Quests[id]
		if !ok || q.PriorID != 0 {
			return st, fmt.Errorf("todayquest: invalid offered root")
		}
		existing[q.PackID] = true
	}
	packs := []int{}
	for p := range byPack {
		packs = append(packs, p)
	}
	sort.Ints(packs)
	for _, pack := range packs {
		if existing[pack] {
			continue
		}
		ids := byPack[pack]
		sort.Slice(ids, func(i, j int) bool {
			a := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", p, ids[i])))
			b := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", p, ids[j])))
			return string(a[:]) < string(b[:])
		})
		n := s.design.PostCount
		if n > len(ids) {
			n = len(ids)
		}
		st.Offered = append(st.Offered, ids[:n]...)
	}
	sort.Ints(st.Offered)
	return st, nil
}
func (s *Service) save(st snapshot) error {
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return s.store.Save("today_quests", b)
}
func (s *Service) identity(st snapshot, id int, part string) string {
	return fmt.Sprintf("todayquest:%s:%d:%s", st.Period, id, part)
}
func (s *Service) root(id int) int {
	for s.design.Quests[id].PriorID != 0 {
		id = s.design.Quests[id].PriorID
	}
	return id
}
func (s *Service) questWire(a Active) []byte {
	q := s.design.Quests[a.ID]
	b := wire.AppendVarint(nil, 1, uint64(a.ID))
	b = wire.AppendVarint(b, 2, uint64(a.Value))
	for _, id := range a.Objects {
		b = wire.AppendVarint(b, 3, id)
	}
	return wire.AppendVarint(b, 6, uint64(q.PackID))
}
func (s *Service) give(st snapshot, id int) ([]player.Item, error) {
	var rs []gamedata.BattleReward
	for _, item := range s.design.Quests[id].GiveItemIDs {
		rs = append(rs, gamedata.BattleReward{Type: 13, ID: item, Count: 1})
	}
	if len(rs) == 0 {
		return nil, nil
	}
	key := s.identity(st, id, "give")
	items, e := s.inventory.GrantOnce(key, rs)
	if e == nil && len(items) == 0 {
		items = s.inventory.GrantedItems(key)
	}
	return items, e
}
func (s *Service) secondsLeft(st snapshot) uint64 {
	base, _ := time.Parse("2006-01-02", st.Period)
	end := base.AddDate(0, 0, 7).Add(s.design.Reset.DailyReset - 9*time.Hour)
	n := int64(end.Sub(s.now()).Seconds())
	if n < 0 {
		return 0
	}
	return uint64(n)
}
func (s *Service) Info(pack int) ([][]byte, []int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, e := s.load()
	if e != nil {
		return nil, nil, e
	}
	if e = s.save(st); e != nil {
		return nil, nil, e
	}
	var rows [][]byte
	var cleared []int
	ids := []int{}
	for id := range st.Active {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if s.design.Quests[id].PackID == pack {
			rows = append(rows, s.questWire(st.Active[id]))
		}
	}
	for id := range st.Cleared {
		if s.design.Quests[id].PackID == pack {
			cleared = append(cleared, id)
		}
	}
	sort.Ints(cleared)
	return rows, cleared, nil
}
func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/TodayQuestInfo" && path != "/QuestAccept" && path != "/QuestUpdate" && path != "/QuestClear" && path != "/QuestGiveUp" {
		return 0, nil, false, nil
	}
	id := uint64(0)
	var e error
	if path != "/TodayQuestInfo" {
		id, _, e = wire.Varint(request, 2)
		if e != nil {
			return 0, nil, true, e
		}
		if _, ok := s.design.Quests[int(id)]; !ok {
			return 0, nil, false, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, e := wire.Varint(request, 1)
	if e != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("todayquest: missing sequence")
	}
	st, e := s.load()
	if e != nil {
		return 0, nil, true, e
	}
	if path == "/TodayQuestInfo" {
		var b []byte
		ids := []int{}
		for id := range st.Active {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			b = wire.AppendBytes(b, 1, s.questWire(st.Active[id]))
		}
		ids = nil
		for id := range st.Cleared {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			b = wire.AppendVarint(b, 2, uint64(id))
		}
		b = wire.AppendVarint(b, 3, s.secondsLeft(st))
		for _, id := range st.Offered {
			b = wire.AppendVarint(b, 4, uint64(id))
		}
		return 64, b, true, s.save(st)
	}
	q := s.design.Quests[int(id)]
	pack, _, e := wire.Varint(request, 3)
	if e != nil || int(pack) != q.PackID || !s.unlocked(q.PackID) {
		return 0, nil, true, fmt.Errorf("todayquest: unavailable pack")
	}
	a, active := st.Active[q.ID]
	switch path {
	case "/QuestAccept":
		level, _, e := wire.Varint(request, 4)
		opt, _, optErr := wire.Varint(request, 5)
		if e != nil || level != 0 || optErr != nil || opt != 0 {
			return 0, nil, true, fmt.Errorf("todayquest: invalid difficulty")
		}
		if !active {
			offered := false
			for _, x := range st.Offered {
				if x == q.ID {
					offered = true
				}
			}
			completed := 0
			for x := range st.Cleared {
				if s.design.Quests[x].NextID == 0 {
					completed++
				}
			}
			if q.PriorID != 0 || !offered || st.Cleared[q.ID] || completed+len(st.Active) >= s.design.Limit {
				return 0, nil, true, fmt.Errorf("todayquest: root not available or limit reached")
			}
			a = Active{ID: q.ID}
			st.Active[q.ID] = a
		}
		items, e := s.give(st, q.ID)
		if e != nil {
			return 0, nil, true, e
		}
		b := wire.AppendBytes(nil, 1, s.questWire(a))
		for _, it := range items {
			b = wire.AppendBytes(b, 4, player.ItemWire(it))
		}
		return 17, b, true, s.save(st)
	case "/QuestGiveUp":
		if !active {
			return 0, nil, true, fmt.Errorf("todayquest: quest is not active")
		}
		delete(st.Active, q.ID)
		root := s.root(q.ID)
		for x := range st.Cleared {
			if s.root(x) == root {
				delete(st.Cleared, x)
			}
		}
		return 20, wire.AppendVarint(nil, 1, id), true, s.save(st)
	case "/QuestUpdate":
		if !active {
			return 0, nil, true, fmt.Errorf("todayquest: quest is not active")
		}
		vs, e := values(request, 4)
		if e != nil || len(vs) == 0 {
			return 0, nil, true, fmt.Errorf("todayquest: missing progress")
		}
		if q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18 {
			for _, v := range vs {
				allowed := false
				for _, x := range q.MagicValues {
					if x == v {
						allowed = true
					}
				}
				if !allowed {
					return 0, nil, true, fmt.Errorf("todayquest: foreign quest object")
				}
				seen := false
				for _, x := range a.Objects {
					if x == v {
						seen = true
					}
				}
				if !seen {
					a.Objects = append(a.Objects, v)
				}
			}
		} else {
			if len(vs) != 1 || vs[0] > uint64(q.ConditionCount) {
				return 0, nil, true, fmt.Errorf("todayquest: invalid progress")
			}
			if int(vs[0]) > a.Value {
				a.Value = int(vs[0])
			}
		}
		st.Active[q.ID] = a
		return 19, wire.AppendBytes(wire.AppendVarint(nil, 1, id), 2, nil), true, s.save(st)
	case "/QuestClear":
		if b, ok := st.Responses[q.ID]; ok && st.Cleared[q.ID] {
			return 18, b, true, nil
		}
		if !active || (q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18) && len(a.Objects) < q.ConditionCount || !(q.ConditionType == 2 || q.ConditionType == 9 || q.ConditionType == 18) && a.Value < q.ConditionCount {
			return 0, nil, true, fmt.Errorf("todayquest: incomplete quest")
		}
		if q.ReputationCompleteID != 0 && s.CompleteReputation == nil {
			return 0, nil, true, fmt.Errorf("todayquest: reputation provider unavailable")
		}
		if q.NextID == 0 && s.CompleteAchievement == nil {
			return 0, nil, true, fmt.Errorf("todayquest: achievement provider unavailable")
		}
		bundle, e := s.economy.Apply(s.identity(st, q.ID, "reward"), nil, q.Rewards)
		if e != nil {
			return 0, nil, true, e
		}
		b := wire.AppendBytes(nil, 1, bundle)
		b = wire.AppendVarint(b, 3, id)
		delete(st.Active, q.ID)
		st.Cleared[q.ID] = true
		if q.ReputationCompleteID != 0 {
			rep, e := s.CompleteReputation(s.identity(st, q.ID, "reputation"), q.PackID, uint64(q.ReputationCompleteID))
			if e != nil {
				return 0, nil, true, e
			}
			b = wire.AppendBytes(b, 8, rep)
		}
		if q.NextID != 0 {
			next := Active{ID: q.NextID}
			st.Active[next.ID] = next
			b = wire.AppendBytes(b, 2, s.questWire(next))
			items, e := s.give(st, next.ID)
			if e != nil {
				return 0, nil, true, e
			}
			for _, it := range items {
				b = wire.AppendBytes(b, 6, player.ItemWire(it))
			}
		} else {
			b = wire.AppendBytes(b, 2, nil)
			if e = s.CompleteAchievement(s.identity(st, q.ID, "achievement")); e != nil {
				return 0, nil, true, e
			}
			key := s.identity(st, q.ID, "score")
			if _, ok := st.ScoreAwards[key]; !ok {
				st.ScoreAwards[key] = uint64(s.design.AchievementScore)
			}
		}
		st.Responses[q.ID] = b
		return 18, b, true, s.save(st)
	}
	return 0, nil, false, nil
}

// Score projects durable commission points. These are deliberately separate
// from user experience: no current QuestClear/Notify protocol supports an Exp
// update, and the board's score label alone does not establish that meaning.
func (s *Service) Score() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, e := s.load()
	if e != nil {
		return 0, e
	}
	var total uint64
	for _, n := range st.ScoreAwards {
		if n == 0 || n > 2147483647 || total > 2147483647-n {
			return 0, fmt.Errorf("todayquest: invalid score awards")
		}
		total += n
	}
	return total, nil
}
func values(raw []byte, number int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type != 0 && f.Type != 2 {
			return fmt.Errorf("todayquest: invalid progress type")
		}
		b := f.Value
		for len(b) > 0 {
			n, k := binary.Uvarint(b)
			if k <= 0 {
				return fmt.Errorf("todayquest: invalid packed progress")
			}
			out = append(out, n)
			b = b[k:]
			if f.Type == 0 && len(b) > 0 {
				return fmt.Errorf("todayquest: invalid scalar progress")
			}
		}
		return nil
	})
	return out, err
}
