package cafeteria

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type note struct {
	Costume, Served uint64
	Claimed         bool
}
type receipt struct {
	Digest [32]byte
	Reply  []byte
}
type snapshot struct {
	Version                       string
	Level, Manage                 uint64
	RewardTime, SpawnTime         int64
	Facilities, Managers, Regular []uint64
	Rewarded                      map[uint64]bool
	Connection, DailyCoins        uint64
	Day                           string
	Introduction                  bool
	Notes                         map[uint64]*note
	LastEvent, LastRare           int64
	Receipts                      map[string]receipt
}
type Service struct {
	store        stateio.Store
	design       *gamedata.CafeteriaDesign
	economy      Economy
	state        snapshot
	now          func() time.Time
	resetSeconds int64
	owned        func() []uint64
	hasEpisode   func(uint64) bool
}

func Open(ctx command.Context, store stateio.Store, d *gamedata.CafeteriaDesign, e Economy) (*Service, error) {
	if store == nil || d == nil || e == nil || len(d.Levels.Groups[0]) == 0 {
		return nil, errors.New("cafeteria: missing dependency")
	}
	s := &Service{store: store, design: d, economy: e, now: time.Now, resetSeconds: 9 * 3600}
	s.state = snapshot{Version: versionconfig.State(), Level: d.Levels.Groups[0][0], Rewarded: map[uint64]bool{}, Notes: map[uint64]*note{}, Receipts: map[string]receipt{}}
	b, err := store.Load(ctx.State, "cafeteria")
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Version", "Level", "Manage", "RewardTime", "SpawnTime", "Facilities", "Managers", "Regular", "Rewarded", "Connection", "DailyCoins", "Day", "Introduction", "Notes", "LastEvent", "LastRare", "Receipts"); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != versionconfig.State() || s.state.Rewarded == nil || s.state.Notes == nil || s.state.Receipts == nil {
			return nil, errors.New("cafeteria: invalid state")
		}
		if _, ok := d.Levels.Row(0, s.state.Level); !ok {
			return nil, errors.New("cafeteria: unknown level")
		}
		for id, n := range s.state.Notes {
			if n == nil || n.Costume != id {
				return nil, errors.New("cafeteria: invalid note")
			}
		}
	} else {
		for _, id := range d.Manage.Groups[0] {
			r, _ := d.Manage.Row(0, id)
			if f := r.V(4); f > 0 {
				s.state.Facilities = append(s.state.Facilities, f)
			}
			if p := r.V(9); p > 0 {
				s.state.Managers = append(s.state.Managers, p)
			}
		}
	}
	return s, nil
}
func (s *Service) AttachOwnedCostumes(f func() []uint64)      { s.owned = f }
func (s *Service) AttachDatingHasEpisode(f func(uint64) bool) { s.hasEpisode = f }
func (s *Service) SetResetSeconds(v int64)                    { s.resetSeconds = v }
func (s *Service) day() string {
	return s.now().UTC().Add(time.Duration(9*3600-s.resetSeconds) * time.Second).Format("2006-01-02")
}
func (s *Service) save(ctx command.Context) error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "cafeteria", b)
}
func (s *Service) roll() error {
	if s.owned == nil || s.hasEpisode == nil {
		return errors.New("cafeteria: roster or dating unavailable")
	}
	now := s.now().UnixMilli()
	if s.state.RewardTime == 0 {
		s.state.RewardTime = now
	}
	if s.state.Day == s.day() {
		return nil
	}
	s.state.Day = s.day()
	s.state.SpawnTime = now
	s.state.Rewarded = map[uint64]bool{}
	s.state.Regular = nil
	s.state.Connection = 0
	s.state.DailyCoins = 0
	s.state.LastEvent = 0
	s.state.LastRare = 0
	owned := slices.Clone(s.owned())
	slices.Sort(owned)
	owned = slices.Compact(owned)
	rand.Shuffle(len(owned), func(i, j int) { owned[i], owned[j] = owned[j], owned[i] })
	level, _ := s.design.Levels.Row(0, s.state.Level)
	for _, id := range owned {
		if r, ok := s.design.Connections.Row(0, id); ok && !s.hasEpisode(r.V(2)) && s.state.Connection == 0 {
			s.state.Connection = id
			continue
		}
		if _, ok := s.design.Costumes.Row(0, id); ok && uint64(len(s.state.Regular)) < level.V(2) {
			s.state.Regular = append(s.state.Regular, id)
		}
	}
	return nil
}
func (s *Service) canPhone() bool {
	r, ok := s.design.Connections.Row(0, s.state.Connection)
	return ok && s.hasEpisode != nil && !s.hasEpisode(r.V(2))
}
func (s *Service) CanMeet(ctx command.Context, g uint64) (bool, error) {
	if e := s.roll(); e != nil {
		return false, e
	}
	r, ok := s.design.Connections.Row(0, s.state.Connection)
	if !ok || r.V(2) != g || !s.canPhone() {
		return false, nil
	}
	return true, s.save(ctx)
}
func (s *Service) infoWire() []byte {
	b := wire.AppendVarint(nil, 1, s.state.Level)
	b = wire.AppendVarint(b, 2, uint64(s.state.RewardTime))
	b = wire.AppendVarint(b, 3, uint64(s.state.SpawnTime))
	b = wire.AppendVarint(b, 4, s.state.Manage)
	for _, id := range s.state.Regular {
		b = wire.AppendVarint(b, 5, id)
		if s.state.Rewarded[id] {
			b = wire.AppendVarint(b, 6, id)
		}
	}
	b = wire.AppendVarint(b, 7, s.state.Connection)
	if s.canPhone() {
		b = wire.AppendVarint(b, 8, 1)
	}
	return wire.AppendVarint(b, 9, s.state.DailyCoins)
}
func noteWire(n *note) []byte {
	b := wire.AppendVarint(nil, 1, n.Costume)
	b = wire.AppendVarint(b, 2, n.Served)
	if n.Claimed {
		b = wire.AppendVarint(b, 3, 1)
	}
	return b
}
func val(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }

var codes = map[string]int{"/CafeteriaInfo": 351, "/CafeteriaSpawnReset": 352, "/CafeteriaLevelUp": 353, "/CafeteriaManageItemAdd": 354, "/CafeteriaRegularCostumeInteractionReward": 355, "/CafeteriaRegularCostumeInteractionAllReward": 356, "/CafeteriaCumulativeReward": 357, "/CafeteriaRegularCostumeNoteInfo": 358, "/CafeteriaRegularCostumeNoteReward": 359, "/CafeteriaRegularCostumeNoteAllReward": 360, "/CafeteriaIntroductionStoryReward": 371, "/CafeteriaDailyConnectionCostumeRefresh": 376, "/CafeteriaRareNpcInteractionReward": 413, "/CafeteriaEventNpcInteractionReward": 414}

func (s *Service) Handle(ctx command.Context, path string, b []byte) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	seq := val(b, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, errors.New("cafeteria: missing sequence")
	}
	if e := wire.Walk(b, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	query := path == "/CafeteriaInfo" || path == "/CafeteriaRegularCostumeNoteInfo"
	rk := fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)
	digest := sha256.Sum256(append([]byte(path), b...))
	if !query {
		if r, ok := s.state.Receipts[rk]; ok {
			if r.Digest != digest {
				return code, nil, true, errors.New("cafeteria: changed replay")
			}
			return code, slices.Clone(r.Reply), true, nil
		}
	}
	before, _ := json.Marshal(s.state)
	e := s.roll()
	var out []byte
	if e == nil {
		out, e = s.handle(ctx, path, b, "cafeteria:"+rk)
	}
	if e == nil {
		if !query {
			s.state.Receipts[rk] = receipt{digest, slices.Clone(out)}
		}
		e = s.save(ctx)
	}
	if e != nil {
		_ = json.Unmarshal(before, &s.state)
		return code, nil, true, e
	}
	return code, out, true, nil
}
func (s *Service) handle(ctx command.Context, path string, b []byte, identity string) ([]byte, error) {
	switch path {
	case "/CafeteriaInfo":
		out := wire.AppendBytes(nil, 1, s.infoWire())
		for _, id := range s.state.Facilities {
			out = wire.AppendBytes(out, 2, wire.AppendVarint(nil, 1, id))
		}
		for _, id := range s.state.Managers {
			out = wire.AppendBytes(out, 3, wire.AppendVarint(nil, 1, id))
		}
		return out, nil
	case "/CafeteriaSpawnReset":
		out := wire.AppendVarint(nil, 1, s.state.Connection)
		if s.canPhone() {
			out = wire.AppendVarint(out, 2, 1)
		}
		for _, id := range s.state.Regular {
			out = wire.AppendVarint(out, 3, id)
			if s.state.Rewarded[id] {
				out = wire.AppendVarint(out, 4, id)
			}
		}
		return wire.AppendVarint(out, 5, s.state.DailyCoins), nil
	case "/CafeteriaDailyConnectionCostumeRefresh":
		if s.state.Connection == 0 || !s.canPhone() {
			owned := slices.Clone(s.owned())
			slices.Sort(owned)
			for _, id := range owned {
				r, ok := s.design.Connections.Row(0, id)
				if ok && !s.hasEpisode(r.V(2)) && !slices.Contains(s.state.Regular, id) {
					s.state.Connection = id
					break
				}
			}
		}
		out := wire.AppendVarint(nil, 1, s.state.Connection)
		if s.canPhone() {
			out = wire.AppendVarint(out, 2, 1)
		}
		return out, nil
	case "/CafeteriaIntroductionStoryReward":
		if s.state.Introduction {
			return nil, nil
		}
		d := s.design.Default
		bundle, e := s.economy.Apply(ctx, identity, nil, []gamedata.Reward{{Type: d.V(31), ID: d.V(30), Count: d.V(29)}})
		if e != nil {
			return nil, e
		}
		s.state.Introduction = true
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/CafeteriaManageItemAdd":
		id := s.state.Manage + 1
		r, ok := s.design.Manage.Row(s.state.Level, id)
		if !ok {
			return nil, errors.New("cafeteria: no management upgrade")
		}
		var costs []gamedata.Reward
		if r.V(3) > 0 {
			costs = append(costs, gamedata.Reward{Type: r.V(2), ID: r.V(1), Count: r.V(3)})
		}
		if _, e := s.economy.Apply(ctx, identity, costs, nil); e != nil {
			return nil, e
		}
		s.state.Manage = id
		out := wire.AppendVarint(nil, 1, id)
		if f := r.V(4); f > 0 {
			s.state.Facilities = append(s.state.Facilities, f)
			out = wire.AppendBytes(out, 2, wire.AppendVarint(nil, 1, f))
		}
		if p := r.V(9); p > 0 {
			s.state.Managers = append(s.state.Managers, p)
			out = wire.AppendBytes(out, 3, wire.AppendVarint(nil, 1, p))
		}
		return out, nil
	case "/CafeteriaLevelUp":
		ids := s.design.Manage.Groups[s.state.Level]
		if len(ids) == 0 || s.state.Manage != ids[len(ids)-1] {
			return nil, errors.New("cafeteria: management incomplete")
		}
		if _, ok := s.design.Levels.Row(0, s.state.Level+1); !ok {
			return nil, errors.New("cafeteria: maximum level")
		}
		bundle, e := s.cumulative(ctx, identity, false)
		if e != nil {
			return nil, e
		}
		s.state.Level++
		s.state.Manage = 0
		out := wire.AppendBytes(nil, 1, s.infoWire())
		return wire.AppendBytes(out, 2, bundle), nil
	case "/CafeteriaCumulativeReward":
		bundle, e := s.cumulative(ctx, identity, true)
		if e != nil {
			return nil, e
		}
		out := wire.AppendBytes(nil, 1, bundle)
		return wire.AppendVarint(out, 2, uint64(s.state.RewardTime)), nil
	case "/CafeteriaRegularCostumeInteractionReward", "/CafeteriaRegularCostumeInteractionAllReward":
		return s.serve(ctx, b, identity, path == "/CafeteriaRegularCostumeInteractionAllReward")
	case "/CafeteriaRegularCostumeNoteInfo":
		var out []byte
		for _, id := range sortedNotes(s.state.Notes) {
			out = wire.AppendBytes(out, 1, noteWire(s.state.Notes[id]))
		}
		return out, nil
	case "/CafeteriaRegularCostumeNoteReward", "/CafeteriaRegularCostumeNoteAllReward":
		return s.claimNotes(ctx, b, identity, path == "/CafeteriaRegularCostumeNoteAllReward")
	case "/CafeteriaEventNpcInteractionReward":
		r, ok := s.design.Events.Row(val(b, 2), val(b, 3))
		if !ok {
			return nil, errors.New("cafeteria: unknown event")
		}
		now := s.now().UnixMilli()
		if s.state.LastEvent > 0 && now-s.state.LastEvent < int64(s.design.Default.V(3))*1000 {
			return nil, errors.New("cafeteria: interaction cooldown")
		}
		reward := gamedata.Reward{Type: r.V(13), ID: r.V(12), Count: r.V(11)}
		s.capCoins(&reward)
		bundle, e := s.grant(ctx, identity, []gamedata.Reward{reward})
		if e != nil {
			return nil, e
		}
		s.state.LastEvent = now
		out := wire.AppendBytes(nil, 1, bundle)
		return wire.AppendVarint(out, 2, s.state.DailyCoins), nil
	case "/CafeteriaRareNpcInteractionReward":
		id := val(b, 2)
		r, ok := s.design.NPCs.Row(0, id)
		spawn, exists := s.design.UniqueNPCs.Row(0, s.state.Level)
		if !ok || !exists || !slices.Contains(spawn.A(5), id) {
			return nil, errors.New("cafeteria: invalid rare NPC")
		}
		now := s.now().UnixMilli()
		if s.state.LastRare > 0 && now-s.state.LastRare < int64(spawn.V(4))*1000 {
			return nil, errors.New("cafeteria: rare NPC cooldown")
		}
		reward := gamedata.Reward{Type: r.V(9), ID: r.V(8), Count: r.V(7)}
		s.capCoins(&reward)
		bundle, e := s.grant(ctx, identity, []gamedata.Reward{reward})
		if e != nil {
			return nil, e
		}
		s.state.LastRare = now
		return wire.AppendBytes(nil, 1, bundle), nil
	}
	return nil, errors.New("cafeteria: unsupported command")
}
func (s *Service) grant(ctx command.Context, id string, rewards []gamedata.Reward) ([]byte, error) {
	filtered := make([]gamedata.Reward, 0, len(rewards))
	for _, r := range rewards {
		if r.Count > 0 {
			filtered = append(filtered, r)
		}
	}
	return s.economy.Apply(ctx, id, nil, filtered)
}
func (s *Service) serve(ctx command.Context, b []byte, identity string, all bool) ([]byte, error) {
	ids := []uint64{val(b, 2)}
	if all {
		ids = slices.Clone(s.state.Regular)
	}
	var rewards []gamedata.Reward
	var served []*note
	for _, id := range ids {
		if !slices.Contains(s.state.Regular, id) {
			return nil, errors.New("cafeteria: costume is not today's customer")
		}
		if s.state.Rewarded[id] {
			continue
		}
		r, ok := s.design.Costumes.Row(0, id)
		if !ok {
			return nil, errors.New("cafeteria: customer missing design")
		}
		rewards = append(rewards, gamedata.Reward{Type: r.V(4), ID: r.V(3), Count: r.V(5)})
		n := s.state.Notes[id]
		if n == nil {
			n = &note{Costume: id}
			s.state.Notes[id] = n
		}
		n.Served++
		s.state.Rewarded[id] = true
		served = append(served, n)
	}
	bundle, e := s.grant(ctx, identity, rewards)
	if e != nil {
		return nil, e
	}
	if !all {
		out := wire.AppendBytes(nil, 1, bundle)
		if len(served) > 0 {
			out = wire.AppendBytes(out, 2, noteWire(served[0]))
		}
		return out, nil
	}
	out := wire.AppendBytes(nil, 2, bundle)
	for _, n := range served {
		out = wire.AppendVarint(out, 1, n.Costume)
		out = wire.AppendBytes(out, 3, noteWire(n))
	}
	return out, nil
}
func (s *Service) claimNotes(ctx command.Context, b []byte, identity string, all bool) ([]byte, error) {
	ids := []uint64{val(b, 2)}
	if all {
		ids = sortedNotes(s.state.Notes)
	}
	var claimed []uint64
	var rewards []gamedata.Reward
	d := s.design.Default
	for _, id := range ids {
		n := s.state.Notes[id]
		if n == nil || n.Served < d.V(7) {
			if all {
				continue
			}
			return nil, errors.New("cafeteria: note incomplete")
		}
		if n.Claimed {
			continue
		}
		claimed = append(claimed, id)
		rewards = append(rewards, gamedata.Reward{Type: d.V(20), ID: d.V(19), Count: d.V(21)})
	}
	bundle, e := s.grant(ctx, identity, rewards)
	if e != nil {
		return nil, e
	}
	out := wire.AppendBytes(nil, 1, bundle)
	for _, id := range claimed {
		s.state.Notes[id].Claimed = true
		if all {
			out = wire.AppendVarint(out, 2, id)
		}
	}
	return out, nil
}
func (s *Service) capCoins(r *gamedata.Reward) {
	d := s.design.Default
	if r.Type == d.V(12) && r.ID == d.V(11) {
		r.Count = min(r.Count, d.V(8)-min(d.V(8), s.state.DailyCoins))
		s.state.DailyCoins += r.Count
	}
}
func (s *Service) cumulative(ctx command.Context, identity string, minimum bool) ([]byte, error) {
	elapsed := max(int64(0), s.now().UnixMilli()-s.state.RewardTime) / 1000
	d := s.design.Default
	if minimum && elapsed < int64(d.V(17))*60 {
		return nil, errors.New("cafeteria: settlement not ready")
	}
	elapsed = min(elapsed, int64(d.V(16))*60)
	r, _ := s.design.Levels.Row(0, s.state.Level)
	counts, types, terms, ids := r.A(9), r.A(10), r.A(11), r.A(12)
	if len(counts) != len(types) || len(types) != len(terms) || len(terms) != len(ids) {
		return nil, errors.New("cafeteria: invalid production design")
	}
	var rewards []gamedata.Reward
	for i, n := range counts {
		if n > 0 && terms[i] > 0 {
			count := n * (uint64(elapsed) / terms[i])
			if count > 0 {
				rewards = append(rewards, gamedata.Reward{Type: types[i], ID: ids[i], Count: count})
			}
		}
	}
	bundle, e := s.grant(ctx, identity, rewards)
	if e != nil {
		return nil, e
	}
	s.state.RewardTime = s.now().UnixMilli()
	return bundle, nil
}
func sortedNotes(m map[uint64]*note) []uint64 {
	ids := make([]uint64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
