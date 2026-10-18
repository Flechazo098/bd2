package battle

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/protocol/wire"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
)

type battleReply struct {
	Digest   [32]byte
	Response []byte
	Code     int
}

type Completion struct {
	Identity     string
	Win          bool
	Participants []uint64
	Deck         []deck.DeckEntry
	Context      deck.RecommendContext
}

func (s *Service) AttachPreparation(f func(command.Context, Completion) error) { s.preparation = f }

func (s *Service) AttachExternalResume(f func(command.Context, uint64, string) ([]byte, error)) {
	s.externalResume = f
}

func (s *Service) AttachAbandonment(f func(command.Context, string) error) { s.abandonment = f }

func (s *Service) AttachCompletion(f func(command.Context, Completion) ([]byte, error)) {
	s.completion = f
}

func (s *Service) Restore(ctx command.Context) error { return s.restore(ctx) }

func (s *Service) restore(ctx command.Context) error {
	if ctx.State == nil {
		return nil
	}
	raw, found, err := ctx.State.LoadEntry("battle", "session", "active")
	if err != nil {
		return err
	}
	if s.states == nil {
		s.states = map[string]*battleState{}
	}
	v := &battleState{}
	if found && json.Unmarshal(raw, v) != nil {
		return fmt.Errorf("battle: invalid saved session")
	}
	s.states[ctx.SessionID] = v
	s.restored = v
	return nil
}

func (s *Service) persist(ctx command.Context) error {
	if ctx.State == nil {
		return nil
	}
	raw, err := json.Marshal(s.stateLocked(ctx))
	if err != nil {
		return err
	}
	return ctx.State.PutEntry("battle", "session", "active", raw)
}

func messages(raw []byte, number int) ([][]byte, error) {
	var values [][]byte
	err := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == number {
			if f.Type != 2 {
				return fmt.Errorf("battle: invalid message field %d", number)
			}
			values = append(values, bytes.Clone(f.Value))
		}
		return nil
	})
	return values, err
}

func one(raw []byte, number int) []byte {
	v, _ := messages(raw, number)
	if len(v) == 1 {
		return v[0]
	}
	return nil
}

func value(raw []byte, number int) uint64 { v, _, _ := wire.Varint(raw, number); return v }

func (s *Service) Handle(ctx command.Context, path string, request []byte) (resultCode int, resultBody []byte, wasHandled bool, returnedError error) {
	switch path {
	case "/BattleEnter", "/BattleStart", "/BattleRetry", "/BattleVerifyState", "/BattleEnd", "/BattleEndTest", "/BattleExit", "/BattlePhaseChange", "/BattleResume", "/BattleGiveUp", "/BattleRetryPreviousTurn", "/BattleRecordInfo", "/BattleRecordSave", "/BattleStatisticsInfo":
	default:
		return 0, nil, false, nil
	}
	if s.states == nil {
		s.states = map[string]*battleState{}
	}
	if err := s.restore(ctx); err != nil {
		return 0, nil, true, err
	}
	if err := checkSeq(request); err != nil {
		return 0, nil, true, err
	}
	before := *s.stateLocked(ctx)
	defer func() {
		if returnedError != nil {
			s.states[ctx.SessionID] = &before
			if ctx.State != nil {
				s.restored = &before
			}
		}
	}()
	if path == "/BattleEnter" && before.Entered {
		if bytes.Equal(before.EntryRequest, request) {
			return 52, bytes.Clone(before.EntryResponse), true, nil
		}
		return 52, nil, true, fmt.Errorf("battle: another encounter is active")
	}
	if len(request) > 8<<20 {
		return 0, nil, true, fmt.Errorf("battle: request too large")
	}
	var receiptKey string
	if ctx.State != nil && path != "/BattleResume" && path != "/BattleStatisticsInfo" && path != "/BattleRecordInfo" {
		receiptKey = fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, value(request, 1))
		raw, exists, e := ctx.State.LoadEntry("battle", "replies", receiptKey)
		if e != nil {
			return 0, nil, true, e
		}
		if exists {
			var saved battleReply
			if json.Unmarshal(raw, &saved) != nil || saved.Digest != sha256.Sum256(request) {
				return 0, nil, true, fmt.Errorf("battle: changed request retry")
			}
			return saved.Code, saved.Response, true, nil
		}
	}
	var code int
	var response []byte
	var handled bool
	var err error
	switch path {
	case "/BattleResume":
		code, response, handled, err = s.resume(ctx)
	case "/BattleRetryPreviousTurn":
		code, response, handled, err = s.previous(ctx, request)
	case "/BattleGiveUp":
		if !before.Entered {
			return 57, nil, true, fmt.Errorf("battle: give up without an active battle")
		}
		_, response, handled, err = s.handle(ctx, "/BattleExit", request)
		code = 57
		if isMonsterHunt(before.Mode) {
			if abandon, ok := s.monsterHunt.(interface {
				AbandonBattle(command.Context, string) ([]byte, error)
			}); ok && err == nil {
				response, err = abandon.AbandonBattle(ctx, before.EnterReceipt)
			}
		}
	case "/BattleRecordInfo", "/BattleRecordSave", "/BattleStatisticsInfo":
		return s.records(ctx, path, request)
	default:
		code, response, handled, err = s.handle(ctx, path, request)
	}
	if !handled || err != nil {
		s.states[ctx.SessionID] = &before
		if ctx.State != nil {
			s.restored = &before
		}
		return code, response, handled, err
	}
	state := s.stateLocked(ctx)
	if path == "/BattleEnter" {
		state.Attempt = 0
		state.EntryRequest, state.EntryResponse = bytes.Clone(request), bytes.Clone(response)
		state.EndSeq, state.EndRequest, state.EndReply = 0, nil, nil
		state.Checkpoint, state.LastBlue, state.PreviousBlue, state.EnteredDeck, state.Participating = nil, nil, nil, nil, nil
	}
	if path == "/BattleStart" {
		entries, e := messages(request, 3)
		if e != nil {
			return code, nil, true, e
		}
		if len(state.EnteredDeck) == 0 {
			state.EnteredDeck = entries
		}
		blue, _ := messages(request, 5)
		for _, character := range blue {
			index := value(character, 2)
			if index == 0 || value(character, 38) != 0 {
				continue
			}
			found := slices.Contains(state.Participating, index)
			if !found {
				state.Participating = append(state.Participating, index)
			}
		}
		if checkpoint := one(request, 100); len(checkpoint) != 0 {
			if err = s.checkpoint(state, checkpoint); err != nil {
				return code, nil, true, err
			}
		}
	}
	if path == "/BattleStart" && before.Round == 0 && s.preparation != nil {
		if err = s.preparation(ctx, state.completion(false)); err != nil {
			return code, nil, true, err
		}
	}

	if path == "/BattleRetry" {
		state.Attempt++
		state.Checkpoint, state.LastBlue, state.PreviousBlue = nil, nil, nil
		state.EnteredDeck, state.Participating = nil, nil
	}
	if path == "/BattleExit" || path == "/BattleGiveUp" {
		if before.Entered && s.abandonment != nil {
			if err = s.abandonment(ctx, before.completion(false).Identity); err != nil {
				return code, nil, true, err
			}
		}
		state.Checkpoint, state.LastBlue, state.PreviousBlue = nil, nil, nil
	}
	if (path == "/BattleEnd" || path == "/BattleEndTest") && before.Entered {
		if before.Round == 0 {
			return code, nil, true, fmt.Errorf("battle: settlement before battle start")
		}
		statisticsField := 10
		if path == "/BattleEndTest" {
			statisticsField = 8
		}
		if stats := one(request, statisticsField); stats != nil && ctx.State != nil {
			if err = s.saveStatistics(ctx, before.Mode, stats); err != nil {
				return code, nil, true, err
			}
		}
		if s.completion != nil {
			completion := before.completion(value(request, 2) == 1)
			var extra []byte
			extra, err = s.completion(ctx, completion)
			if err != nil {
				return code, nil, true, err
			}
			response = append(response, extra...)
			state.rememberEnd(request, response)
		}
		state.Checkpoint, state.LastBlue, state.PreviousBlue = nil, nil, nil
	}
	if err = s.persist(ctx); err != nil {
		return code, nil, true, err
	}
	if receiptKey != "" {
		raw, e := json.Marshal(battleReply{sha256.Sum256(request), response, code})
		if e != nil {
			return code, nil, true, e
		}
		if e = ctx.State.PutEntry("battle", "replies", receiptKey, raw); e != nil {
			return code, nil, true, e
		}
	}
	return code, response, true, nil
}

func (state *battleState) completion(win bool) Completion {
	c := Completion{Identity: fmt.Sprintf("%s:attempt:%d", state.EnterReceipt, state.Attempt), Win: win, Context: deck.RecommendContext{BattleMode: state.Mode, PackID: uint64(state.Pack), BattleIndex: state.Deck, Level: 0}}
	if scenario := one(state.Checkpoint, 103); scenario != nil {
		c.Context = deck.RecommendContext{BattleMode: value(scenario, 2), PackID: value(scenario, 3), BattleIndex: value(scenario, 4), Level: value(scenario, 5), TeamCountType: value(scenario, 6), DeckType: value(scenario, 7)}
	}
	seen := map[uint64]bool{}
	for _, index := range state.Participating {
		c.Participants = append(c.Participants, index)
		seen[index] = true
	}
	for _, character := range state.InitialBlue {
		index := value(character, 2)
		if index != 0 && value(character, 38) == 0 && !seen[index] {
			c.Participants = append(c.Participants, index)
			seen[index] = true
		}
	}
	for _, raw := range state.EnteredDeck {
		index := value(raw, 1)
		c.Deck = append(c.Deck, deck.DeckEntry{CharacterInvenIndex: index, CostumeInvenIndex: value(raw, 2), Slot: value(raw, 3)})
	}
	return c
}

func (s *Service) checkpoint(state *battleState, raw []byte) error {
	common := one(raw, 1)
	if len(raw) > 4<<20 || value(common, 1) != state.Mode || value(common, 2) != state.Index || value(common, 3)+1 != state.Round {
		return fmt.Errorf("battle: checkpoint encounter mismatch: bytes=%d mode=%d expected_mode=%d index=%d expected_index=%d round=%d expected_round=%d", len(raw), value(common, 1), state.Mode, value(common, 2), state.Index, value(common, 3), state.Round-1)
	}
	if scenario := one(raw, 103); scenario != nil {
		pack := value(scenario, 3)
		if value(scenario, 2) != state.Mode || value(scenario, 4) != state.Index || pack != 0 && pack != uint64(state.Pack) {
			return fmt.Errorf("battle: recommendation context does not match encounter")
		}
	}
	encoded := one(raw, 8)
	result, err := base64.StdEncoding.DecodeString(string(encoded))
	blue, teamsErr := messages(result, 3)
	if err != nil || teamsErr != nil || len(blue) == 0 {
		return fmt.Errorf("battle: invalid checkpoint teams")
	}
	if err = wire.Walk(result, func(wire.Field) error { return nil }); err != nil {
		return err
	}
	state.Checkpoint = bytes.Clone(raw)
	// The plugin's field 101 carries IsBlueTurn, including phase battles whose local parity differs.
	if value(raw, 101) != 0 {
		if len(state.LastBlue) != 0 && value(one(state.LastBlue, 1), 3) < value(common, 3) {
			state.PreviousBlue = state.LastBlue
		}
		state.LastBlue = bytes.Clone(raw)
	}
	return nil
}

func (s *Service) resume(ctx command.Context) (int, []byte, bool, error) {
	state := s.stateLocked(ctx)
	if !state.Entered {
		return 540, nil, true, nil
	}
	if len(state.Checkpoint) == 0 {
		if state.Round != 0 {
			return 540, nil, true, fmt.Errorf("battle: client checkpoint unavailable")
		}
		common := wire.AppendVarint(nil, 1, state.Mode)
		common = wire.AppendVarint(common, 2, state.Deck)
		out := wire.AppendBytes(nil, 1, common)
		if state.Monster != 0 {
			out = wire.AppendVarint(out, 4, state.Monster)
		}
		if state.Quest != 0 {
			out = wire.AppendVarint(out, 5, state.Quest)
		}
		out = wire.AppendVarint(out, 18, value(state.EntryRequest, 8))
		out = wire.AppendVarint(out, 19, value(state.EntryRequest, 9))
		out = wire.AppendVarint(out, 20, value(state.EntryRequest, 10))
		out = wire.AppendVarint(out, 26, value(state.EntryRequest, 11))
		if isMonsterHunt(state.Mode) {
			user := one(state.EntryResponse, 5)
			out = wire.AppendVarint(out, 3, value(user, 3))
			out = wire.AppendVarint(out, 12, 1)
			out = wire.AppendVarint(out, 13, value(user, 4))
			out = wire.AppendVarint(out, 14, value(user, 4))
			out = wire.AppendVarint(out, 17, value(user, 1))
		}
		if (state.Mode == 23 || state.Mode == 26) && s.externalResume != nil {
			extra, err := s.externalResume(ctx, state.Mode, state.EnterReceipt)
			if err != nil {
				return 540, nil, true, err
			}
			out = append(out, extra...)
		}
		return 540, wire.AppendVarint(out, 22, 1), true, nil
	}
	out, _, err := wire.ReplaceVarint(state.Checkpoint, 27, boolNumber(len(state.previousSnapshot()) != 0))
	if err != nil {
		return 540, nil, true, err
	}
	if state.Quest != 0 {
		out, _, err = wire.ReplaceVarint(out, 5, state.Quest)
		if err != nil {
			return 540, nil, true, err
		}
	}
	for _, field := range []struct{ from, to int }{{8, 18}, {9, 19}, {10, 20}, {11, 26}} {
		out, _, err = wire.ReplaceVarint(out, field.to, value(state.EntryRequest, field.from))
		if err != nil {
			return 540, nil, true, err
		}
	}
	state.restoreCheckpointCursor(state.Checkpoint)
	return 540, out, true, err
}

func boolNumber(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

func (s *Service) previous(ctx command.Context, request []byte) (int, []byte, bool, error) {
	state := s.stateLocked(ctx)
	if !state.Entered || value(request, 2) != state.Index || len(state.previousSnapshot()) == 0 {
		return 287, nil, true, fmt.Errorf("battle: previous blue turn unavailable")
	}
	snapshot := state.previousSnapshot()
	var out []byte
	for from, to := range map[int]int{8: 1, 9: 2, 10: 3, 12: 4, 15: 5, 16: 6, 28: 9, 29: 10, 30: 11, 31: 12, 32: 13} {
		if err := wire.Walk(snapshot, func(f wire.Field) error {
			if f.Number != from {
				return nil
			}
			switch f.Type {
			case 2:
				out = wire.AppendBytes(out, to, f.Value)
			case 0:
				v, _ := binary.Uvarint(f.Value)
				out = wire.AppendVarint(out, to, v)
			}
			return nil
		}); err != nil {
			return 287, nil, true, err
		}
	}
	turn := value(one(snapshot, 1), 3)

	out = wire.AppendVarint(out, 7, turn)
	out = wire.AppendVarint(out, 14, value(one(snapshot, 1), 2))
	state.Checkpoint, state.LastBlue, state.PreviousBlue = snapshot, snapshot, nil
	state.restoreCheckpointCursor(snapshot)
	return 287, out, true, nil
}

func (state *battleState) previousSnapshot() []byte {
	if value(one(state.Checkpoint, 1), 3) < 2 {
		return nil
	}
	if value(state.Checkpoint, 101) == 0 {
		return state.LastBlue
	}
	return state.PreviousBlue
}

func (state *battleState) restoreCheckpointCursor(snapshot []byte) {
	common := one(snapshot, 1)
	state.Round = value(common, 3)
	state.Index = value(common, 2)
	state.Deck = state.Index
	for i, phase := range state.Phases {
		if phase.DeckID == state.Index {
			state.Phase = i
			state.PhaseStarted = true
			break
		}
	}
	state.PhaseSeq, state.PhaseReply = 0, nil
}
