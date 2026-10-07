// Package todayquest implements the NPC commission board from TodayQuestTable.
package todayquest

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"time"
)

type Economy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}
type Inventory interface {
	GrantOnce(ctx command.Context, _ string, _ []gamedata.BattleReward) ([]assets.Item, error)
	GrantedItems(string) []assets.Item
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
	store               stateio.Store
	design              *gamedata.TodayQuestCatalog
	economy             Economy
	inventory           Inventory
	now                 func() time.Time
	unlocked            func(ctx command.Context, _ int) bool
	CompleteReputation  func(ctx command.Context, _ string, _ int, _ uint64) ([]byte, error)
	CompleteAchievement func(command.Context, string) error
}

// Request dispatch owns the account transaction; economy/inventory writes and
// this snapshot are committed together by the explicit command capability.
func Open(store stateio.Store, design *gamedata.TodayQuestCatalog, economy Economy, inventory Inventory, unlocked func(ctx command.Context, _ int) bool) (*Service, error) {
	if store == nil || design == nil || len(design.Quests) == 0 || design.Limit <= 0 || design.PostCount <= 0 || economy == nil || inventory == nil || unlocked == nil {
		return nil, fmt.Errorf("todayquest: invalid configuration")
	}
	return &Service{store: store, design: design, economy: economy, inventory: inventory, unlocked: unlocked, now: time.Now}, nil
}
func (s *Service) load(ctx command.Context) (snapshot, error) {
	p, e := s.design.Reset.Period(3, s.now())
	if e != nil {
		return snapshot{}, e
	}
	raw, e := s.store.Load(ctx.State, "today_quests")
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
		if q.PriorID == 0 && s.unlocked(ctx, q.PackID) {
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
		n := min(s.design.PostCount, len(ids))
		st.Offered = append(st.Offered, ids[:n]...)
	}
	sort.Ints(st.Offered)
	return st, nil
}
func (s *Service) save(ctx command.Context, st snapshot) error {
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "today_quests", b)
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

func (s *Service) give(ctx command.Context, st snapshot, id int) ([]assets.Item, error) {
	var rs []gamedata.BattleReward
	for _, item := range s.design.Quests[id].GiveItemIDs {
		rs = append(rs, gamedata.BattleReward{Type: 13, ID: item, Count: 1})
	}
	if len(rs) == 0 {
		return nil, nil
	}
	key := s.identity(st, id, "give")
	items, e := s.inventory.GrantOnce(ctx, key, rs)
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

// Score projects durable commission points. These are deliberately separate
// from user experience: no current QuestClear/Notify protocol supports an Exp
// update, and the board's score label alone does not establish that meaning.
func (s *Service) Score(ctx command.Context) (uint64, error) {

	st, e := s.load(ctx)
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
