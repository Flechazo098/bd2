package dating

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
	"slices"
	"sort"
	"time"
)

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	CanApply(command.Context, []gamedata.Reward) error
}
type episode struct {
	Group, Point, Clear, MessageGroup, Message uint64
	Updated                                    int64
}
type choice struct{ Group, ID, Text uint64 }
type receipt struct {
	Digest [32]byte
	Reply  []byte
}
type snapshot struct {
	Version   string
	Episodes  map[uint64]*episode
	Choices   map[string]choice
	Rewards   map[string]choice
	Purchases uint64
	Day       string
	Receipts  map[string]receipt
}
type Service struct {
	store        stateio.Store
	design       *gamedata.DatingDesign
	economy      Economy
	state        snapshot
	now          func() time.Time
	resetSeconds int64
	firstMeeting func(command.Context, uint64) (bool, error)
}

func Open(ctx command.Context, store stateio.Store, d *gamedata.DatingDesign, e Economy) (*Service, error) {
	if store == nil || d == nil || e == nil {
		return nil, errors.New("dating: missing dependency")
	}
	s := &Service{store: store, design: d, economy: e, now: time.Now, resetSeconds: 9 * 3600}
	b, err := store.Load(ctx.State, "dating")
	if err != nil {
		return nil, err
	}
	s.state = snapshot{Version: versionconfig.State(), Episodes: map[uint64]*episode{}, Choices: map[string]choice{}, Rewards: map[string]choice{}, Receipts: map[string]receipt{}}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Version", "Episodes", "Choices", "Rewards", "Purchases", "Day", "Receipts"); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != versionconfig.State() || s.state.Episodes == nil || s.state.Choices == nil || s.state.Rewards == nil || s.state.Receipts == nil {
			return nil, errors.New("dating: invalid state")
		}
		for g, p := range s.state.Episodes {
			if p == nil || p.Group != g {
				return nil, errors.New("dating: invalid episode")
			}
			if _, ok := d.Episodes.Row(g, p.Clear); !ok {
				return nil, errors.New("dating: unknown episode")
			}
		}
	}
	return s, nil
}
func (s *Service) AttachFirstMeeting(f func(command.Context, uint64) (bool, error)) {
	s.firstMeeting = f
}
func (s *Service) SetResetSeconds(seconds int64) { s.resetSeconds = seconds }
func (s *Service) HasEpisode(group uint64) bool  { return s.state.Episodes[group] != nil }
func (s *Service) LastClear(group uint64) uint64 {
	if p := s.state.Episodes[group]; p != nil {
		return p.Clear
	}
	return 0
}

// ClearedEpisodeGroups counts the dating groups with a cleared episode.
func (s *Service) ClearedEpisodeGroups() uint64 {
	var count uint64
	for _, p := range s.state.Episodes {
		if p.Clear > 0 {
			count++
		}
	}
	return count
}
func (s *Service) day() string {
	return s.now().UTC().Add(time.Duration(9*3600-s.resetSeconds) * time.Second).Format("2006-01-02")
}
func (s *Service) save(ctx command.Context) error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "dating", b)
}
func val(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }
func key(g, id uint64) string    { return fmt.Sprintf("%d/%d", g, id) }
func epWire(p *episode) []byte {
	b := wire.AppendVarint(nil, 1, p.Group)
	b = wire.AppendVarint(b, 2, p.Point)
	b = wire.AppendVarint(b, 3, p.Clear)
	b = wire.AppendVarint(b, 4, p.MessageGroup)
	b = wire.AppendVarint(b, 5, p.Message)
	return wire.AppendVarint(b, 6, uint64(p.Updated))
}
func choiceWire(p choice) []byte {
	b := wire.AppendVarint(nil, 1, p.Group)
	b = wire.AppendVarint(b, 2, p.ID)
	if p.Text > 0 {
		b = wire.AppendVarint(b, 3, p.Text)
	}
	return b
}

var codes = map[string]int{"/DatingInfo": 361, "/DatingEpisodeClear": 362, "/DatingMessageUpdate": 363, "/DatingMessageReward": 527, "/DatingApBuy": 633}

func (s *Service) Handle(ctx command.Context, path string, b []byte) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	seq := val(b, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, errors.New("dating: missing sequence")
	}
	if e := wire.Walk(b, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	rk := fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)
	digest := sha256.Sum256(append([]byte(path), b...))
	if path != "/DatingInfo" {
		if r, ok := s.state.Receipts[rk]; ok {
			if r.Digest != digest {
				return code, nil, true, errors.New("dating: changed replay")
			}
			return code, slices.Clone(r.Reply), true, nil
		}
	}
	before, _ := json.Marshal(s.state)
	if day := s.day(); day != s.state.Day {
		s.state.Day = day
		s.state.Purchases = 0
	}
	out, e := s.handle(ctx, path, b, "dating:"+rk)
	if e == nil {
		if path != "/DatingInfo" {
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
	g, id := val(b, 2), val(b, 3)
	switch path {
	case "/DatingInfo":
		var out []byte
		groups := make([]uint64, 0, len(s.state.Episodes))
		for g := range s.state.Episodes {
			groups = append(groups, g)
		}
		slices.Sort(groups)
		for _, g := range groups {
			out = wire.AppendBytes(out, 1, epWire(s.state.Episodes[g]))
		}
		for _, entry := range []struct {
			field  int
			values map[string]choice
		}{{2, s.state.Choices}, {3, s.state.Rewards}} {
			keys := make([]string, 0, len(entry.values))
			for k := range entry.values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out = wire.AppendBytes(out, entry.field, choiceWire(entry.values[k]))
			}
		}
		return wire.AppendVarint(out, 4, s.state.Purchases), nil
	case "/DatingEpisodeClear":
		r, ok := s.design.Episodes.Row(g, id)
		if !ok {
			return nil, errors.New("dating: unknown episode")
		}
		p := s.state.Episodes[g]
		if p != nil && id <= p.Clear {
			return wire.AppendBytes(nil, 2, epWire(p)), nil
		}
		if p == nil {
			if id != 1 || s.firstMeeting == nil {
				return nil, errors.New("dating: first meeting unavailable")
			}
			allowed, e := s.firstMeeting(ctx, g)
			if e != nil {
				return nil, e
			}
			if !allowed {
				return nil, errors.New("dating: first meeting not unlocked")
			}
			p = &episode{Group: g}
		} else if id != p.Clear+1 || r.V(2) == 0 || p.MessageGroup != r.V(2) || p.Message != last(s.design.Messages.Groups[r.V(2)]) {
			return nil, errors.New("dating: pre-message incomplete")
		}
		var costs []gamedata.Reward
		if id > 1 {
			cost := gamedata.Reward{Type: 41, Count: 1}
			if e := s.economy.CanApply(ctx, []gamedata.Reward{cost}); e != nil {
				if s.design.CostumeGroup[g].V(3) == 1 {
					return nil, e
				}
				cost.Type = 42
			}
			costs = append(costs, cost)
		}
		var rewards []gamedata.Reward
		if r.V(8) > 0 {
			rewards = append(rewards, gamedata.Reward{Type: r.V(11), ID: r.V(10), Count: r.V(8)})
		}
		bundle, e := s.economy.Apply(ctx, identity, costs, rewards)
		if e != nil {
			return nil, e
		}
		p.Clear = id
		p.Point += r.V(9)
		s.state.Episodes[g] = p
		out := wire.AppendBytes(nil, 1, bundle)
		return wire.AppendBytes(out, 2, epWire(p)), nil
	case "/DatingMessageUpdate":
		r, ok := s.design.Messages.Row(g, id)
		owner, owned := s.design.MessageEpisode[g]
		if !ok || !owned {
			return nil, errors.New("dating: unknown message")
		}
		p := s.state.Episodes[owner[0]]
		if p == nil {
			return nil, errors.New("dating: no first meeting")
		}
		text := val(b, 4)
		if r.V(5) == 1 && r.V(9) == 1 {
			// The client acknowledges a message with one text option using text
			// id zero: its answer button reports zero when either option is
			// empty (DatingChatOwnSelectAnswer.isSingleSelectOption).
			if r.V(3) == 0 || r.V(4) == 0 {
				if text != 0 {
					return nil, errors.New("dating: invalid answer")
				}
			} else if text == 0 || (text != r.V(3) && text != r.V(4)) {
				return nil, errors.New("dating: invalid answer")
			}
		} else if text != 0 {
			return nil, errors.New("dating: message has no answer")
		}
		if old, exists := s.state.Choices[key(g, id)]; exists {
			if old.Text != text {
				return nil, errors.New("dating: answer immutable")
			}
			out := wire.AppendBytes(nil, 1, epWire(p))
			return wire.AppendBytes(out, 2, choiceWire(old)), nil
		}
		if p.MessageGroup == g && id <= p.Message {
			return wire.AppendBytes(nil, 1, epWire(p)), nil
		}
		if !s.nextMessage(p, g, id) {
			return nil, errors.New("dating: message out of sequence")
		}
		if prev, exists := s.design.Messages.Row(p.MessageGroup, p.Message); exists && prev.V(5) == 3 {
			if _, claimed := s.state.Rewards[key(p.MessageGroup, p.Message)]; !claimed {
				return nil, errors.New("dating: gift pending")
			}
		}
		p.MessageGroup = g
		p.Message = id
		p.Updated = s.now().UnixMilli()
		out := wire.AppendBytes(nil, 1, epWire(p))
		if text > 0 {
			c := choice{g, id, text}
			s.state.Choices[key(g, id)] = c
			out = wire.AppendBytes(out, 2, choiceWire(c))
		}
		return out, nil
	case "/DatingMessageReward":
		r, ok := s.design.Messages.Row(g, id)
		owner, owned := s.design.MessageEpisode[g]
		if !ok || !owned || r.V(5) != 3 {
			return nil, errors.New("dating: message is not a gift")
		}
		if _, ok := s.state.Rewards[key(g, id)]; ok {
			return nil, nil
		}
		p := s.state.Episodes[owner[0]]
		if p == nil || owner[1] >= p.Clear && (p.MessageGroup != g || p.Message < id) {
			return nil, errors.New("dating: gift unread")
		}
		types, ids, counts := r.A(8), r.A(7), r.A(6)
		if len(types) != len(ids) || len(ids) != len(counts) {
			return nil, errors.New("dating: invalid gift design")
		}
		rewards := make([]gamedata.Reward, 0, len(ids))
		for i := range ids {
			if counts[i] > 0 {
				rewards = append(rewards, gamedata.Reward{Type: types[i], ID: ids[i], Count: counts[i]})
			}
		}
		bundle, e := s.economy.Apply(ctx, identity, nil, rewards)
		if e != nil {
			return nil, e
		}
		s.state.Rewards[key(g, id)] = choice{Group: g, ID: id}
		return wire.AppendBytes(nil, 1, bundle), nil
	case "/DatingApBuy":
		count := g
		if count == 0 || count > s.design.Default.V(1)-min(s.state.Purchases, s.design.Default.V(1)) {
			return nil, errors.New("dating: daily purchase limit")
		}
		item, ok, e := wire.Bytes(b, 3)
		if e != nil || !ok {
			return nil, errors.New("dating: missing payment")
		}
		typ, itemID, amount := val(item, 3), val(item, 2), val(item, 4)
		types, ids, counts := s.design.Default.A(4), s.design.Default.A(3), s.design.Default.A(2)
		valid := false
		for i, t := range types {
			if i < len(ids) && i < len(counts) && typ == t && itemID == ids[i] && amount == counts[i]*count {
				valid = true
			}
		}
		if !valid {
			return nil, errors.New("dating: invalid payment")
		}
		bundle, e := s.economy.Apply(ctx, identity, []gamedata.Reward{{Type: typ, ID: itemID, Count: amount}}, []gamedata.Reward{{Type: 42, Count: count}})
		if e != nil {
			return nil, e
		}
		s.state.Purchases += count
		out := wire.AppendBytes(nil, 1, bundle)
		return wire.AppendVarint(out, 2, s.state.Purchases), nil
	}
	return nil, errors.New("dating: unsupported command")
}
func last(v []uint64) uint64 {
	if len(v) > 0 {
		return v[len(v)-1]
	}
	return 0
}
func (s *Service) nextMessage(p *episode, g, id uint64) bool {
	if p.MessageGroup == g {
		return id == p.Message+1
	}
	if id != 1 {
		return false
	}
	current, _ := s.design.Episodes.Row(p.Group, p.Clear)
	if p.MessageGroup == 0 {
		return g == current.V(3)
	}
	if p.Message != last(s.design.Messages.Groups[p.MessageGroup]) {
		return false
	}
	if g == current.V(3) && p.MessageGroup == current.V(2) {
		return true
	}
	next, ok := s.design.Episodes.Row(p.Group, p.Clear+1)
	return ok && g == next.V(2) && p.MessageGroup == current.V(3)
}
