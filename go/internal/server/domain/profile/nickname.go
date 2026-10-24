package profile

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"

	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type NicknameService struct {
	store     stateio.Store
	items     *assets.Inventory
	checkText TextCheck
	ticket    uint64
	name      string
}

type nicknameState struct {
	Name string `json:"name"`
}

func OpenNickname(ctx command.Context, store stateio.Store, items *assets.Inventory, checkText TextCheck, ticket uint64, initial string) (*NicknameService, error) {
	if store == nil || items == nil || checkText == nil || ticket == 0 || initial == "" {
		return nil, fmt.Errorf("profile: incomplete nickname dependencies")
	}
	s := &NicknameService{store: store, items: items, checkText: checkText, ticket: ticket, name: initial}
	raw, err := store.Load(ctx.State, "nickname")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		if err := stateio.RequireExactJSONObject(raw, "name"); err != nil {
			return nil, err
		}
		var state nicknameState
		if err := json.Unmarshal(raw, &state); err != nil || state.Name == "" || !utf8.ValidString(state.Name) {
			return nil, fmt.Errorf("profile: invalid nickname state")
		}
		s.name = state.Name
	}
	return s, nil
}

func (s *NicknameService) CurrentNickname() string { return s.name }

func (s *NicknameService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/UserNicknameChange" {
		return 0, nil, false, nil
	}
	const code = 74
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	var seq uint64
	var name string
	var seen [2]bool
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number < 1 || field.Number > 2 {
			return nil
		}
		if seen[field.Number-1] {
			return fmt.Errorf("%w: nickname field %d repeats", wire.ErrMalformed, field.Number)
		}
		seen[field.Number-1] = true
		if field.Number == 1 {
			if field.Type != 0 {
				return fmt.Errorf("%w: nickname sequence has wire type %d", wire.ErrMalformed, field.Type)
			}
			seq, _ = binary.Uvarint(field.Value)
		} else {
			if field.Type != 2 {
				return fmt.Errorf("%w: nickname field %d has wire type %d", wire.ErrMalformed, field.Number, field.Type)
			}
			name = string(field.Value)
		}
		return nil
	})
	if err != nil || seq == 0 || seq > math.MaxInt32 || !seen[1] {
		return fail(fmt.Errorf("profile: invalid nickname request"))
	}
	if !validNickname(name) {
		return fail(fmt.Errorf("profile: nickname violates client character or byte-length limits"))
	}
	if name == s.name {
		return fail(&command.GameError{PacketCode: code, ErrorType: 1, Message: "nickname is already in use by this player"})
	}
	consumed, err := s.items.SelectMutable(ctx, 14, s.ticket, 1)
	if err != nil {
		return fail(fmt.Errorf("profile: nickname change ticket required: %w", err))
	}
	if err := s.checkText(ctx, NicknameText, name); err != nil {
		return fail(err)
	}
	if err := s.items.Consume(ctx, consumed); err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(nicknameState{Name: name})
	if err != nil {
		return fail(err)
	}
	if err := s.store.Save(ctx.State, "nickname", raw); err != nil {
		return fail(err)
	}
	s.name = name
	return code, nil, true, nil
}

func validNickname(name string) bool {
	if !utf8.ValidString(name) || len(name) < 4 || len(name) > 24 {
		return false
	}
	for _, r := range name {
		r = unicode.ToLower(r)
		allowed := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 0xAC00 && r <= 0xD7A3 || r >= 0x4E00 && r <= 0x9FD5 || r >= 0x3041 && r <= 0x30FE || r >= 0x0180 && r <= 0x024F
		if !allowed {
			return false
		}
	}
	return true
}
