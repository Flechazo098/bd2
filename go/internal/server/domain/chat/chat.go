package chat

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"
	"unicode/utf8"
)

type Settings struct{ AutoTranslate, Global, Visible bool }
type Policy struct {
	AccountID                      string
	OwnerIndex, Version            uint64
	AutoTranslate, Global, Visible bool
	Blocked                        []uint64
	PenaltyUntilMS                 int64
}
type Block struct {
	Owner  uint64
	Name   string
	DateMS int64
}
type Report struct {
	Target, ReasonID      uint64
	Text, Reason          string
	ChatTime, SubmittedMS int64
}
type State struct {
	Settings    Settings
	Blocks      map[uint64]Block
	ReportCount uint64
	ResetMS     int64
	Reports     []Report
}
type Service struct {
	Design *gamedata.ChatDesign
	Owner  func(command.Context) (uint64, error)
	Target func(command.Context, uint64) (string, error)
	Now    func() time.Time
}

var codes = map[string]int{"/ChatSettingUpdate": 0, "/RoomChatReportAndBlockInfo": 521, "/RoomChatBlockAdd": 522, "/RoomChatBlockRemove": 523, "/RoomChatReport": 524}

func Owns(path string) bool { _, ok := codes[path]; return ok }
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) load(ctx command.Context) (State, error) {
	state := State{Settings: Settings{true, true, true}, Blocks: map[uint64]Block{}}
	raw, found, err := ctx.State.LoadEntry("directory", "chat", "state")
	if err != nil {
		return state, err
	}
	if found {
		if err := json.Unmarshal(raw, &state); err != nil {
			return state, err
		}
	}
	if state.Blocks == nil {
		state.Blocks = map[uint64]Block{}
	}
	now := s.now().UTC()
	if now.UnixMilli() >= state.ResetMS {
		state.ReportCount = 0
		state.ResetMS = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).UnixMilli()
		state.Reports = nil
	}
	return state, nil
}
func (s *Service) ChatSettings(ctx command.Context) (bool, bool, bool, error) {
	state, err := s.load(ctx)
	return state.Settings.AutoTranslate, state.Settings.Global, state.Settings.Visible, err
}
func (s *Service) PublishPolicy(ctx command.Context) error {
	state, err := s.load(ctx)
	if err != nil {
		return err
	}
	owner, err := s.Owner(ctx)
	if err != nil {
		return err
	}
	p := Policy{AccountID: ctx.AccountID, OwnerIndex: owner, Version: uint64(s.now().UnixNano()), AutoTranslate: state.Settings.AutoTranslate, Global: state.Settings.Global, Visible: state.Settings.Visible}
	for owner := range state.Blocks {
		p.Blocked = append(p.Blocked, owner)
	}
	slices.Sort(p.Blocked)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return ctx.Publish(command.Event{ID: fmt.Sprintf("chat-policy:%d", p.Version), Topic: "chat.policy", Payload: raw})
}
func (s *Service) Handle(ctx command.Context, path string, raw []byte) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	fail := func(message string) (int, []byte, bool, error) {
		return code, nil, true, &command.GameError{PacketCode: code, ErrorType: 9003, Message: message}
	}
	values := map[int]wire.Field{}
	if err := wire.Walk(raw, func(f wire.Field) error {
		if _, exists := values[f.Number]; exists {
			return wire.ErrMalformed
		}
		values[f.Number] = f
		return nil
	}); err != nil {
		return fail("Invalid chat request")
	}
	number := func(field int) (uint64, error) {
		v, found := values[field]
		if !found {
			return 0, nil
		}
		if v.Type != 0 {
			return 0, wire.ErrMalformed
		}
		n, size := binary.Uvarint(v.Value)
		if size <= 0 {
			return 0, wire.ErrMalformed
		}
		return n, nil
	}
	seq, err := number(1)
	if err != nil || seq == 0 || seq > math.MaxInt32 {
		return fail("Invalid chat sequence")
	}
	state, err := s.load(ctx)
	if err != nil {
		return code, nil, true, err
	}
	var out []byte
	if path == "/RoomChatReportAndBlockInfo" {
		owners := make([]uint64, 0, len(state.Blocks))
		for owner := range state.Blocks {
			owners = append(owners, owner)
		}
		slices.Sort(owners)
		for _, owner := range owners {
			b := state.Blocks[owner]
			row := wire.AppendVarint(nil, 1, b.Owner)
			row = wire.AppendString(row, 2, b.Name)
			row = wire.AppendVarint(row, 3, uint64(b.DateMS))
			out = wire.AppendBytes(out, 1, row)
		}
		report := wire.AppendVarint(nil, 1, state.ReportCount)
		report = wire.AppendVarint(report, 2, uint64(state.ResetMS))
		return code, wire.AppendBytes(out, 2, report), true, nil
	}
	if path == "/ChatSettingUpdate" {
		flags := make([]bool, 3)
		for i := range flags {
			n, e := number(i + 2)
			if e != nil || n > 1 {
				return fail("Invalid chat setting")
			}
			flags[i] = n == 1
		}
		state.Settings = Settings{flags[0], flags[1], flags[2]}
	} else {
		target, e := number(2)
		owner, ownerErr := s.Owner(ctx)
		if e != nil || ownerErr != nil || target == 0 || target > math.MaxInt64 || target == owner {
			return fail("Invalid chat target")
		}
		if path == "/RoomChatBlockRemove" {
			delete(state.Blocks, target)
		} else {
			name, e := s.Target(ctx, target)
			if e != nil {
				return fail("Player not found")
			}
			if path == "/RoomChatBlockAdd" {
				if _, exists := state.Blocks[target]; !exists {
					state.Blocks[target] = Block{target, name, s.now().UnixMilli()}
				}
			} else {
				reason, e := number(3)
				stamp, stampErr := number(6)
				text, extra := values[4], values[5]
				if e != nil || stampErr != nil || stamp == 0 || stamp > math.MaxInt64 || !s.Design.ReportIDs[reason] || text.Type != 2 || len(text.Value) == 0 || len(text.Value) > 8192 || !utf8.Valid(text.Value) || extra.Type != 2 || len(extra.Value) > 2048 || !utf8.Valid(extra.Value) {
					return fail("Invalid chat report")
				}
				if state.ReportCount >= s.Design.ReportLimit {
					return fail("Daily chat report limit reached")
				}
				state.ReportCount++
				state.Reports = append(state.Reports, Report{target, reason, string(text.Value), string(extra.Value), int64(stamp), s.now().UnixMilli()})
			}
		}
	}
	data, err := json.Marshal(state)
	if err == nil {
		err = ctx.State.PutEntry("directory", "chat", "state", data)
	}
	if err == nil {
		err = s.PublishPolicy(ctx)
	}
	return code, out, true, err
}
