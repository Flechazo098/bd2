package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

type SkyWaySchedule struct {
	Group, Bonus uint64
	Days         []uint64
}
type skyWayEconomy interface {
	CanApply(command.Context, []gamedata.Reward) error
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	AdditionalCurrencies(command.Context) (map[int]uint64, error)
}
type skyWayAP interface {
	HuntingAP(command.Context) (uint64, uint64, error)
}
type skyWayRuntime struct {
	design        *gamedata.SkyWayDesign
	store         stateio.Store
	economy       skyWayEconomy
	ap            skyWayAP
	schedules     []SkyWaySchedule
	now           func() time.Time
	graph         *gamedata.RewardGraph
	bossProgress  func(command.Context, uint64, uint64) error
	battleAttempt func(command.Context, uint64) error
}
type skyWayGroup struct {
	Current uint64
	Maximum int
	Auto    bool
}
type skyWayRun struct {
	Group, ID, Generation uint64
	Defeated              []uint64
}
type skyWayReceipt struct {
	Request         []byte
	Response, Bonus []byte
	Monsters        [][]byte
}
type skyWayState struct {
	Version  string                   `json:"version"`
	Groups   map[string]skyWayGroup   `json:"groups"`
	Run      skyWayRun                `json:"run"`
	Receipts map[string]skyWayReceipt `json:"receipts"`
}

func (s *Service) AttachSkyWay(ctx command.Context, d *gamedata.SkyWayDesign, store stateio.Store, economy skyWayEconomy, ap skyWayAP, schedules []SkyWaySchedule, graph *gamedata.RewardGraph) error {
	if d == nil || store == nil || economy == nil || ap == nil || graph == nil {
		return fmt.Errorf("world: invalid SkyWay runtime")
	}
	seen := map[uint64]bool{}
	for _, r := range schedules {
		if r.Group == 0 || r.Group > 7 || seen[r.Group] || r.Bonus > math.MaxInt32 {
			return fmt.Errorf("world: invalid SkyWay schedule")
		}
		seen[r.Group] = true
		days := map[uint64]bool{}
		for _, day := range r.Days {
			if day > 6 || days[day] {
				return fmt.Errorf("world: invalid SkyWay bonus day")
			}
			days[day] = true
		}
	}
	for _, row := range d.Stages {
		if !seen[row.Group] {
			return fmt.Errorf("world: missing SkyWay group schedule")
		}
	}
	s.skyway = &skyWayRuntime{design: d, store: store, economy: economy, ap: ap, schedules: schedules, now: time.Now, graph: graph}
	_, err := s.skyway.load(ctx)
	return err
}
func (r *skyWayRuntime) load(ctx command.Context) (skyWayState, error) {
	v := skyWayState{Version: versionconfig.State(), Groups: map[string]skyWayGroup{}, Receipts: map[string]skyWayReceipt{}}
	b, err := r.store.Load(ctx.State, "skyway")
	if err != nil || b == nil {
		return v, err
	}
	if err = stateio.RequireExactJSONObject(b, "version", "groups", "run", "receipts"); err != nil {
		return v, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&v); err != nil {
		return v, err
	}
	if v.Version != versionconfig.State() || v.Groups == nil || v.Receipts == nil {
		return v, fmt.Errorf("world: incompatible SkyWay state")
	}
	for k, st := range v.Groups {
		g, e := strconv.ParseUint(k, 10, 64)
		stage, ok := r.design.Stage(g, st.Current)
		maximum := -1
		for _, row := range r.design.Stages {
			if row.Group == g {
				maximum = max(maximum, int(row.Difficulty))
			}
		}
		if e != nil || strconv.FormatUint(g, 10) != k || !ok || st.Maximum < -1 || st.Maximum > maximum || int(stage.Difficulty) > st.Maximum+1 {
			return v, fmt.Errorf("world: invalid SkyWay group progression")
		}
	}
	if v.Run.Group != 0 {
		stage, ok := r.design.Stage(v.Run.Group, v.Run.ID)
		if !ok || v.Run.Generation == 0 {
			return v, fmt.Errorf("world: invalid SkyWay run")
		}
		seen := map[uint64]bool{}
		for _, id := range v.Run.Defeated {
			if !slices.Contains(stage.Monsters, id) || seen[id] {
				return v, fmt.Errorf("world: invalid SkyWay defeated monster")
			}
			seen[id] = true
		}
	}
	return v, nil
}
func (r *skyWayRuntime) save(ctx command.Context, v skyWayState) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.store.Save(ctx.State, "skyway", b)
}
func (s *Service) skyWayPack(ctx command.Context, pack int) error {
	if s.skyway == nil || pack != s.skyway.design.Pack || !s.packUnlocked(ctx, pack) {
		return fmt.Errorf("world: SkyWay pack unavailable")
	}
	return nil
}
func (r *skyWayRuntime) costs(ctx command.Context, stage gamedata.SkyWayStage, count uint64) ([]gamedata.Reward, error) {
	if count == 0 {
		return nil, nil
	}
	var free, bonus uint64
	var err error
	freeType, bonusType := uint64(21), uint64(23)
	if stage.APType == 1 {
		free, bonus, err = r.ap.HuntingAP(ctx)
	} else {
		freeType, bonusType = 32, 33
		var values map[int]uint64
		values, err = r.economy.AdditionalCurrencies(ctx)
		free, bonus = values[36], values[37]
	}
	if err != nil {
		return nil, err
	}
	if free < count && bonus < count-free {
		return nil, fmt.Errorf("world: insufficient SkyWay AP")
	}
	n := min(free, count)
	var out []gamedata.Reward
	if n > 0 {
		out = append(out, gamedata.Reward{Type: freeType, Count: n})
	}
	if n < count {
		out = append(out, gamedata.Reward{Type: bonusType, Count: count - n})
	}
	return out, nil
}
func (s *Service) handleSkyWay(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path == "/SaveUserPosition" && s.skyway != nil {
		pack, err := requestPack(request)
		if err != nil {
			return 7, nil, true, err
		}
		if pack == s.skyway.design.Pack {
			raw, found, e := wire.Bytes(request, 3)
			var position progress.Position
			if e != nil || !found || json.Unmarshal(raw, &position) != nil || !s.skyway.design.SafeMaps[position.MapID] || !s.packUnlocked(ctx, pack) {
				return 7, nil, true, ErrInvalidRequest
			}
			if e = s.state.SaveUserPosition(ctx, request); e != nil {
				return 7, nil, true, e
			}
			v, e := s.skyway.load(ctx)
			if e != nil {
				return 7, nil, true, e
			}
			v.Run = skyWayRun{Generation: v.Run.Generation}
			e = s.skyway.save(ctx, v)
			return 7, nil, true, e
		}
	}
	code := map[string]int{"/SkyWayInfo": 160, "/SkyWayEnter": 161, "/SkyWayScheduleInfo": 162}[path]
	if code == 0 {
		return 0, nil, false, nil
	}
	if s.skyway == nil {
		return code, nil, true, fmt.Errorf("world: SkyWay runtime missing")
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, ErrInvalidRequest
	}
	r := s.skyway
	if path == "/SkyWayScheduleInfo" {
		var out []byte
		for _, schedule := range r.schedules {
			row := wire.AppendVarint(nil, 1, schedule.Group)
			for _, day := range schedule.Days {
				row = wire.AppendVarint(row, 2, day)
			}
			if schedule.Bonus > 0 {
				row = wire.AppendVarint(row, 3, schedule.Bonus)
			}
			out = wire.AppendBytes(out, 1, row)
		}
		return code, out, true, nil
	}
	pack, err := requestPack(request)
	if err != nil {
		return code, nil, true, err
	}
	if err = s.skyWayPack(ctx, pack); err != nil {
		return code, nil, true, err
	}
	v, err := r.load(ctx)
	if err != nil {
		return code, nil, true, err
	}
	if path == "/SkyWayInfo" {
		var out []byte
		seen := map[uint64]bool{}
		for _, stage := range r.design.Stages {
			if seen[stage.Group] {
				continue
			}
			seen[stage.Group] = true
			st, ok := v.Groups[strconv.FormatUint(stage.Group, 10)]
			if !ok {
				st = skyWayGroup{Current: stage.ID, Maximum: -1}
			}
			row := wire.AppendVarint(nil, 2, stage.Group)
			row = wire.AppendVarint(row, 3, st.Current)
			row = wire.AppendVarint(row, 4, uint64(int64(st.Maximum)))
			if st.Auto {
				row = wire.AppendVarint(row, 1, 1)
			}
			out = wire.AppendBytes(out, 1, row)
		}
		return code, out, true, nil
	}
	if ctx.SessionID == "" {
		return code, nil, true, ErrInvalidRequest
	}
	key := "enter:" + ctx.SessionID + ":" + strconv.FormatUint(seq, 10)
	if prior, ok := v.Receipts[key]; ok {
		if !bytes.Equal(prior.Request, request) {
			return code, nil, true, ErrInvalidRequest
		}
		return code, prior.Response, true, nil
	}
	group, _, err := wire.Varint(request, 3)
	if err != nil {
		return code, nil, true, err
	}
	id, _, err := wire.Varint(request, 4)
	if err != nil {
		return code, nil, true, err
	}
	auto, _, err := wire.Varint(request, 5)
	if err != nil || auto > 1 {
		return code, nil, true, ErrInvalidRequest
	}
	stage, ok := r.design.Stage(group, id)
	if !ok {
		return code, nil, true, ErrInvalidRequest
	}
	current, err := s.CurrentPackID(ctx)
	if err != nil || current != pack || s.battleActive != nil && s.battleActive(ctx) {
		return code, nil, true, fmt.Errorf("world: SkyWay enter outside current pack or during battle")
	}
	st, exists := v.Groups[strconv.FormatUint(group, 10)]
	if !exists {
		st.Maximum = -1
	}
	if int(stage.Difficulty) > st.Maximum+1 {
		return code, nil, true, fmt.Errorf("world: SkyWay difficulty requires previous boss clear")
	}
	if _, err = r.costs(ctx, stage, 1); err != nil {
		return code, nil, true, err
	}
	if v.Run.Generation == math.MaxUint64 {
		return code, nil, true, fmt.Errorf("world: SkyWay generation overflow")
	}
	v.Run = skyWayRun{Group: group, ID: id, Generation: v.Run.Generation + 1}
	st.Current, st.Auto = id, auto == 1
	v.Groups[strconv.FormatUint(group, 10)] = st
	var out []byte
	for _, row := range r.monsters(stage, v.Run) {
		out = wire.AppendBytes(out, 1, row)
	}
	v.Receipts[key] = skyWayReceipt{Request: slices.Clone(request), Response: out}
	err = r.save(ctx, v)
	return code, out, true, err
}
func (r *skyWayRuntime) monsters(stage gamedata.SkyWayStage, run skyWayRun) [][]byte {
	var out [][]byte
	for _, id := range stage.Monsters {
		out = append(out, r.monster(id, !slices.Contains(run.Defeated, id)))
	}
	if len(run.Defeated) == len(stage.Monsters) {
		out = append(out, r.monster(stage.Boss, true))
	}
	return out
}
func (r *skyWayRuntime) monster(id uint64, active bool) []byte {
	m := r.design.Monsters[id]
	out := wire.AppendVarint(nil, 1, id)
	out = wire.AppendVarint(out, 2, m.Decks[0])
	if active {
		out = wire.AppendVarint(out, 6, 1)
	}
	return out
}
func (s *Service) SkyWayBeginBattle(ctx command.Context, pack int, mode, monster, deck uint64) (string, error) {
	if err := s.skyWayPack(ctx, pack); err != nil {
		return "", err
	}
	v, err := s.skyway.load(ctx)
	if err != nil {
		return "", err
	}
	stage, ok := s.skyway.design.Stage(v.Run.Group, v.Run.ID)
	if !ok || mode != gamedata.SkyWayMode(stage.Group) || slices.Contains(v.Run.Defeated, monster) {
		return "", fmt.Errorf("world: invalid SkyWay encounter")
	}
	cost := uint64(0)
	member := false
	for i, id := range stage.Monsters {
		if id == monster {
			member = true
			cost = stage.AP[i]
		}
	}
	if monster == stage.Boss {
		member = len(v.Run.Defeated) == len(stage.Monsters)
		cost = stage.BossAP
	}
	if !member || !slices.Contains(s.skyway.design.Monsters[monster].Decks, deck) {
		return "", fmt.Errorf("world: SkyWay monster/deck unavailable")
	}
	if _, err = s.skyway.costs(ctx, stage, cost); err != nil {
		return "", err
	}
	return fmt.Sprintf("skyway:%d:%d:%d:%d", stage.Group, stage.ID, v.Run.Generation, monster), nil
}
func (s *Service) SkyWayCompleteBattle(ctx command.Context, pack int, mode, monster, deck uint64, instance, receipt string) ([]byte, []byte, [][]byte, error) {
	r := s.skyway
	if r == nil || receipt == "" {
		return nil, nil, nil, ErrInvalidRequest
	}
	v, err := r.load(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	fingerprint := fmt.Sprintf("%d:%d:%d:%d:%s", pack, mode, monster, deck, instance)
	key := "win:" + receipt
	if old, ok := v.Receipts[key]; ok {
		if !bytes.Equal(old.Request, []byte(fingerprint)) {
			return nil, nil, nil, ErrInvalidRequest
		}
		return old.Response, old.Bonus, old.Monsters, nil
	}
	current, err := s.SkyWayBeginBattle(ctx, pack, mode, monster, deck)
	if err != nil || current != instance {
		return nil, nil, nil, fmt.Errorf("world: stale SkyWay battle: %w", err)
	}
	stage, _ := r.design.Stage(v.Run.Group, v.Run.ID)
	cost := stage.BossAP
	if monster == stage.Boss && v.Run.Generation == math.MaxUint64 {
		return nil, nil, nil, ErrInvalidRequest
	}
	for i, id := range stage.Monsters {
		if id == monster {
			cost = stage.AP[i]
		}
	}
	costs, err := r.costs(ctx, stage, cost)
	if err != nil {
		return nil, nil, nil, err
	}
	resolved, err := r.graph.Resolve(r.design.Monsters[monster].Rewards[deck])
	if err != nil {
		return nil, nil, nil, err
	}
	rewards := []gamedata.Reward{}
	for _, reward := range resolved {
		rewards = append(rewards, gamedata.Reward(reward))
	}
	var extra []gamedata.Reward
	rate := r.bonusRate(stage.Group)
	if rate > 0 {
		for _, reward := range rewards {
			if reward.Count > math.MaxInt32/rate {
				return nil, nil, nil, fmt.Errorf("world: SkyWay bonus overflow")
			}
			reward.Count = reward.Count * rate / 100
			if reward.Count > 0 {
				extra = append(extra, reward)
			}
		}
	}
	bundle, err := r.economy.Apply(ctx, instance, costs, rewards)
	if err != nil {
		return nil, nil, nil, err
	}
	var bonus []byte
	if len(extra) > 0 {
		bonus, err = r.economy.Apply(ctx, instance+":bonus", nil, extra)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	updates := [][]byte{r.monster(monster, false)}
	if monster == stage.Boss {
		if r.bossProgress != nil {
			if err = r.bossProgress(ctx, stage.PositionGroup, stage.Difficulty); err != nil {
				return nil, nil, nil, err
			}
		}
		st := v.Groups[strconv.FormatUint(stage.Group, 10)]
		st.Maximum = max(st.Maximum, int(stage.Difficulty))
		v.Groups[strconv.FormatUint(stage.Group, 10)] = st
		v.Run.Generation++
		v.Run.Defeated = nil
		for _, id := range stage.Monsters {
			updates = append(updates, r.monster(id, true))
		}
	} else {
		v.Run.Defeated = append(v.Run.Defeated, monster)
		if len(v.Run.Defeated) == len(stage.Monsters) {
			updates = append(updates, r.monster(stage.Boss, true))
		}
	}
	v.Receipts[key] = skyWayReceipt{Request: []byte(fingerprint), Response: bundle, Bonus: bonus, Monsters: updates}
	if err = r.save(ctx, v); err != nil {
		return nil, nil, nil, err
	}
	return bundle, bonus, updates, nil
}
func (r *skyWayRuntime) bonusRate(group uint64) uint64 {
	day := uint64(r.now().UTC().Weekday())
	for _, schedule := range r.schedules {
		if schedule.Group == group && slices.Contains(schedule.Days, day) {
			return schedule.Bonus
		}
	}
	return 0
}
func (s *Service) SkyWayDispatchEligibility(ctx command.Context, d *gamedata.DispatchDesign) error {
	if err := s.skyWayPack(ctx, int(d.Pack)); err != nil {
		return err
	}
	if !s.packCompleteFor(int(d.Pack)) {
		return fmt.Errorf("world: SkyWay dispatch requires completed introductory quests")
	}
	stage, ok := s.skyway.design.Stage(d.TypeGroup, d.TypeID)
	if !ok {
		return ErrInvalidRequest
	}
	v, err := s.skyway.load(ctx)
	if err != nil {
		return err
	}
	st, ok := v.Groups[strconv.FormatUint(stage.Group, 10)]
	if !ok || st.Maximum < int(stage.Difficulty) {
		return fmt.Errorf("world: SkyWay dispatch requires selected boss clear")
	}
	return nil
}
func (s *Service) skyWayMap(ctx command.Context, pack int) (int, bool, error) {
	if s.skyway == nil || pack != s.skyway.design.Pack {
		return 0, false, nil
	}
	v, err := s.skyway.load(ctx)
	if err != nil {
		return 0, true, err
	}
	stage, ok := s.skyway.design.Stage(v.Run.Group, v.Run.ID)
	return int(stage.Map), ok, nil
}

func (s *Service) SkyWayDispatchCosts(ctx command.Context, d *gamedata.DispatchDesign, count uint64) ([]gamedata.Reward, error) {
	if s.skyway == nil {
		return nil, ErrInvalidRequest
	}
	stage, ok := s.skyway.design.Stage(d.TypeGroup, d.TypeID)
	if !ok || count > math.MaxInt32/d.AP {
		return nil, ErrInvalidRequest
	}
	return s.skyway.costs(ctx, stage, d.AP*count)
}

func (s *Service) AttachSkyWayProgress(boss func(command.Context, uint64, uint64) error, attempt func(command.Context, uint64) error) {
	s.skyway.bossProgress, s.skyway.battleAttempt = boss, attempt
}
func (s *Service) SkyWayBattleStarted(ctx command.Context, mode uint64, receipt string) error {
	if !gamedata.IsSkyWayMode(mode) {
		return nil
	}
	r := s.skyway
	if r == nil || receipt == "" {
		return ErrInvalidRequest
	}
	v, err := r.load(ctx)
	if err != nil {
		return err
	}
	key := "attempt:" + receipt
	if _, ok := v.Receipts[key]; ok {
		return nil
	}
	if r.battleAttempt != nil {
		if err = r.battleAttempt(ctx, mode-8); err != nil {
			return err
		}
	}
	v.Receipts[key] = skyWayReceipt{Request: []byte(strconv.FormatUint(mode, 10))}
	return r.save(ctx, v)
}
func (s *Service) SkyWayDispatchExchange(ctx command.Context, identity string, costs, rewards []gamedata.Reward) ([]byte, error) {
	return s.skyway.economy.Apply(ctx, identity, costs, rewards)
}
func (s *Service) SkyWayDispatchBonus(d *gamedata.DispatchDesign, rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	rate := s.skyway.bonusRate(d.TypeGroup)
	out := append([]gamedata.BattleReward(nil), rewards...)
	for i, r := range out {
		if r.Count > math.MaxInt32/(100+rate) {
			return nil, ErrInvalidRequest
		}
		out[i].Count = r.Count * (100 + rate) / 100
	}
	return out, nil
}
