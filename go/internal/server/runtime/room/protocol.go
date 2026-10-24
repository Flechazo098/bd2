package room

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"bd2server/internal/server/protocol/wire"
)

func integer(b []byte, n int, v int64) []byte { return wire.AppendVarint(b, n, uint64(v)) }
func text(b []byte, n int, v string) []byte   { return wire.AppendString(b, n, v) }
func blob(b []byte, n int, v []byte) []byte   { return wire.AppendBytes(b, n, v) }
func number(b []byte, n int) int64            { v, _, _ := wire.Varint(b, n); return int64(v) }
func bytesField(b []byte, n int) []byte       { v, _, _ := wire.Bytes(b, n); return v }
func stringField(b []byte, n int) string      { return string(bytesField(b, n)) }
func firstNumber(b []byte, n int) int64 {
	if packed := bytesField(b, n); len(packed) > 0 {
		v, read := binary.Uvarint(packed)
		if read > 0 {
			return int64(v)
		}
	}
	return number(b, n)
}
func repeated(b []byte, n int) [][]byte {
	var all [][]byte
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == n && f.Type == 2 {
			all = append(all, f.Value)
		}
		return nil
	})
	return all
}
func removeField(b []byte, n int) []byte {
	var out []byte
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			out = append(out, b[f.Start:f.End]...)
		}
		return nil
	})
	return out
}
func ticks(t time.Time) int64 {
	return t.Unix()*10000000 + int64(t.Nanosecond()/100) + 621355968000000000
}
func message(now time.Time, code int64, body []byte) []byte {
	h := integer(nil, 1, now.Unix())
	if code != 0 {
		h = integer(h, 2, code)
	}
	return append(blob(nil, 1, h), body...)
}
func (s *state) reply(c Connection, f Frame, typ int32, body []byte, code int64) Delivery {
	return Delivery{c.ID, Frame{Type: typ, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(s.owner.manager.config.Now(), code, body), UDP: f.UDP}}
}
func (s *state) broadcast(typ int32, body []byte, matching bool, except int64) []Delivery {
	var out []Delivery
	for _, u := range s.members {
		if u.principal.OwnerIndex == except {
			continue
		}
		id := u.gameConnection
		if matching {
			id = u.matchingConnection
		}
		if id != "" {
			out = append(out, Delivery{id, Frame{Type: typ, OwnerIndex: u.principal.OwnerIndex, Payload: message(s.owner.manager.config.Now(), 0, body)}})
		}
	}
	return out
}
func (s *state) matchingMember(u *member) []byte {
	b := blob(nil, 1, u.profile.UserInfo)
	if u.principal.OwnerIndex == s.host {
		b = integer(b, 2, 1)
	}
	b = integer(b, 3, ticks(u.entered))
	b = text(b, 4, u.guid)
	if s.selection.Kind == Action {
		b = integer(b, 5, u.character)
	}
	if s.selection.Kind == Fishing {
		b = blob(b, 5, u.profile.AvatarUseInfo)
		b = integer(b, 6, u.profile.RodID)
	}
	return b
}
func (s *state) matchingBody() []byte {
	var b []byte
	if s.selection.Kind == Fishing {
		b = text(b, 2, s.owner.code)
		for _, u := range s.members {
			b = blob(b, 3, s.matchingMember(u))
		}
		b = integer(b, 4, s.selection.MapID)
		b = integer(b, 5, s.selection.BoatLevel)
		b = integer(b, 6, s.selection.BoatSkinID)
	} else {
		b = integer(b, 2, s.matchingType)
		b = text(b, 3, s.owner.code)
		for _, u := range s.members {
			b = blob(b, 4, s.matchingMember(u))
		}
		if s.selection.Kind == Action {
			b = integer(b, 5, s.selection.MonsterID)
		}
	}
	return b
}

func (s *state) matchingResponse(typ int32) []byte {
	if typ != 149 {
		return s.matchingBody()
	}
	b := text(nil, 2, s.owner.code)
	for _, u := range s.members {
		b = blob(b, 3, s.matchingMember(u))
	}
	return b
}
func (s *state) complete() []Delivery {
	if s.roomState != 0 {
		return nil
	}
	s.roomState = 1
	s.owner.manager.mu.Lock()
	for key, o := range s.owner.manager.actionOffers {
		if o == s.owner {
			delete(s.owner.manager.actionOffers, key)
		}
	}
	s.owner.manager.mu.Unlock()
	s.loadingSince = s.owner.manager.config.Now()
	var b []byte
	b = text(b, 2, s.owner.code)
	for _, u := range s.members {
		b = blob(b, 3, s.matchingMember(u))
	}
	b = s.endpoint(b, 4, 5, 6)
	typ := int32(63)
	if s.selection.Kind == Action {
		typ = 101
		b = integer(b, 7, s.selection.MonsterID)
	}
	if s.selection.Kind == Fishing {
		typ = 171
		b = integer(b, 7, s.selection.MapID)
	}
	return s.broadcast(typ, b, true, 0)
}

func (m *Manager) Handle(ctx context.Context, c Connection, f Frame) (deliveries []Delivery, err error) {
	defer func() {
		code := int64(0)
		switch {
		case errors.Is(err, ErrNotFound):
			code = 3
		case errors.Is(err, ErrNotReady):
			code = 6
			if f.Type == 185 {
				code = 10
			}
		case errors.Is(err, ErrMailboxFull):
			code = 8
		}
		if code != 0 && responseType(f.Type) != 0 {
			deliveries = []Delivery{{c.ID, Frame{Type: responseType(f.Type), Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), code, nil), UDP: f.UDP}}}
			err = nil
		}
	}()
	if err := m.checkConnection(c); err != nil {
		return nil, err
	}
	if len(f.Payload) > 1280000 || f.Type < 1 || f.Type > 224 || f.OwnerIndex != 0 && f.OwnerIndex != c.Principal.OwnerIndex {
		return nil, fmt.Errorf("%w: frame %d of %d bytes names owner %d, connection owner is %d", ErrUnauthorized, f.Type, len(f.Payload), f.OwnerIndex, c.Principal.OwnerIndex)
	}
	if err := wire.Walk(f.Payload, func(wire.Field) error { return nil }); err != nil {
		return nil, err
	}
	if h := bytesField(f.Payload, 1); h != nil {
		if owner := number(bytesField(h, 3), 1); owner != 0 && owner != c.Principal.OwnerIndex {
			return nil, fmt.Errorf("%w: frame %d header names owner %d, connection owner is %d", ErrUnauthorized, f.Type, owner, c.Principal.OwnerIndex)
		}
	}
	f.Payload = append([]byte(nil), f.Payload...)
	if c.Channel == Matching && f.Type == 172 {
		if len(f.Payload) > 4096 {
			return nil, fmt.Errorf("%w: matching message is %d bytes, at most 4096", ErrUnauthorized, len(f.Payload))
		}
		text := stringField(bytesField(f.Payload, 2), 4)
		if text != "" && !m.allowRoomText(ctx, c.Principal, text) {
			return []Delivery{{c.ID, Frame{Type: 173, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), 2, nil)}}}, nil
		}
	}
	if (c.Channel == Fishing || c.Channel == Square) && (f.Type == 70 || f.Type == 72) {
		field := 3
		if f.Type == 72 {
			field = 4
		}
		data := bytesField(f.Payload, field)
		if len(data) >= 4 && binary.LittleEndian.Uint32(data) == 9004 {
			if !m.allowRoomText(ctx, c.Principal, stringField(data[4:], 2)) {
				return nil, nil
			}
		}
	}
	if f.Type == 1 || f.Type == 5 || f.Type == 11 {
		body := message(m.config.Now(), 0, nil)
		return []Delivery{{c.ID, Frame{Type: f.Type + 1, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: body}}}, nil
	}
	if c.Channel == Matching && (f.Type == 142 || f.Type == 124 || f.Type == 205) {
		return m.handleSquareChannel(ctx, c, f)
	}
	if c.Channel == Square {
		return m.handleSquare(ctx, c, f)
	}
	if c.Channel == Matching {
		if f.UDP {
			return nil, fmt.Errorf("%w: matching frame %d cannot arrive over UDP", ErrUnauthorized, f.Type)
		}
		if f.Type == 78 {
			b := integer(nil, 1, int64(m.config.Region))
			b = text(b, 2, m.config.AdvertisedHost)
			b = integer(b, 3, int64(m.config.TCPPort))
			return []Delivery{{c.ID, Frame{Type: 79, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), 0, blob(nil, 2, b))}}}, nil
		}
		switch f.Type {
		case 3, 13, 15, 177, 188, 190, 207:
			return []Delivery{{c.ID, Frame{Type: f.Type + 1, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), 13, nil)}}}, nil
		case 180:
			return []Delivery{{c.ID, Frame{Type: 181, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), 0, nil)}}}, nil
		}
		return m.handleMatching(ctx, c, f)
	}
	o := m.lookup(c.Principal, c.Channel)
	if o == nil {
		return nil, fmt.Errorf("%w: owner %d has no %s room", ErrNotFound, c.Principal.OwnerIndex, c.Channel)
	}
	_, d, e := o.submit(ctx, int64(len(f.Payload)), func(s *state) (any, []Delivery, error) { d, e := s.handleGame(c, f); return nil, d, e })
	return d, e
}

func (m *Manager) allowRoomText(ctx context.Context, principal Principal, text string) bool {
	checker, ok := m.rules.(TextRules)
	if !ok {
		return false
	}
	err := checker.CheckRoomText(ctx, principal, text)
	if err == nil {
		return true
	}
	decision := "unavailable"
	if errors.Is(err, ErrTextRejected) {
		decision = "rejected"
	}
	slog.Warn("room text was not delivered", "account_id", principal.AccountID, "decision", decision)
	return false
}

func responseType(typ int32) int32 {
	switch typ {
	case 23, 25, 32, 34, 37, 43, 52, 55, 64, 66, 68, 74, 76, 78, 80, 82, 84, 87, 90, 93, 95, 97, 102, 104, 109, 114, 117, 120, 149, 151, 153, 155, 157, 169, 172, 192, 195, 198, 201, 212, 215, 217, 219:
		return typ + 1
	case 124, 126, 129, 137, 140, 142, 144, 147, 182, 185, 205:
		return typ + 1
	}
	return 0
}

func matchKind(typ int32) Kind {
	switch {
	case typ == 76 || typ == 64 || typ == 74 || typ == 80 || typ == 217:
		return Defense
	case typ >= 82 && typ <= 83 || typ >= 87 && typ <= 105 || typ == 219:
		return Action
	case typ >= 149 && typ <= 174 || typ == 215:
		return Fishing
	}
	return ""
}
func (m *Manager) handleMatching(ctx context.Context, c Connection, f Frame) ([]Delivery, error) {
	k := matchKind(f.Type)
	if k == "" {
		return nil, fmt.Errorf("%w: matching frame %d names no room kind", ErrUnauthorized, f.Type)
	}
	if f.Type == 76 || f.Type == 82 || f.Type == 149 || f.Type == 93 || f.Type == 153 || f.Type == 90 || f.Type == 155 {
		return m.match(ctx, c, f, k)
	}
	o := m.lookup(c.Principal, k)
	if f.Type == 215 || f.Type == 217 || f.Type == 219 {
		m.mu.Lock()
		o = m.rooms[stringField(f.Payload, 2)]
		m.mu.Unlock()
	}
	if o == nil {
		return nil, fmt.Errorf("%w: owner %d has no %s room", ErrNotFound, c.Principal.OwnerIndex, k)
	}
	_, d, e := o.submit(ctx, int64(len(f.Payload)), func(s *state) (any, []Delivery, error) {
		if s.selection.Kind != k {
			return nil, nil, fmt.Errorf("%w: matching frame %d is for a %s room, room is %s", ErrUnauthorized, f.Type, k, s.selection.Kind)
		}
		if f.Type == 215 || f.Type == 217 || f.Type == 219 {
			var b []byte
			field := 2
			if k == Fishing {
				field = 4
				b = integer(b, 2, s.selection.MapID)
				b = integer(b, 3, s.selection.BoatLevel)
			}
			for _, u := range s.members {
				v := text(nil, 1, stringField(u.profile.UserInfo, 4))
				v = integer(v, 2, u.principal.OwnerIndex)
				b = blob(b, field, v)
			}
			return nil, []Delivery{s.reply(c, f, f.Type+1, b, 0)}, nil
		}
		u := s.find(c.Principal)
		if u == nil {
			return nil, nil, fmt.Errorf("%w: owner %d is not in the %s room", ErrUnauthorized, c.Principal.OwnerIndex, k)
		}
		u.matchingConnection = c.ID
		switch f.Type {
		case 80, 102, 169:
			if s.roomState > 1 {
				return nil, nil, fmt.Errorf("%w: matching frame %d in room state %d", ErrNotReady, f.Type, s.roomState)
			}
			return nil, []Delivery{s.reply(c, f, f.Type+1, s.matchingResponse(f.Type), 0)}, nil
		case 64, 87, 151:
			if s.roomState != 0 {
				return nil, nil, fmt.Errorf("%w: matching frame %d in room state %d, want 0", ErrNotReady, f.Type, s.roomState)
			}
			d := []Delivery{s.reply(c, f, f.Type+1, nil, 0)}
			d = append(d, s.leaveMatching(u)...)
			return nil, d, nil
		case 74, 104, 157:
			if s.host != c.Principal.OwnerIndex || s.roomState != 0 {
				return nil, nil, fmt.Errorf("%w: matching frame %d from owner %d needs the host %d in room state 0 (state %d)", ErrUnauthorized, f.Type, c.Principal.OwnerIndex, s.host, s.roomState)
			}
			d := []Delivery{s.reply(c, f, f.Type+1, nil, 0)}
			if k == Action {
				d = append(d, s.broadcast(100, nil, true, 0)...)
			}
			if k == Fishing {
				d = append(d, s.broadcast(159, nil, true, 0)...)
			}
			return nil, append(d, s.complete()...), nil
		case 95:
			if s.roomState != 0 || s.matchingType != 1 {
				return nil, nil, fmt.Errorf("%w: matching frame 95 needs an automatic room in state 0 (type %d, state %d)", ErrNotReady, s.matchingType, s.roomState)
			}
			s.matchingType = 2
			m.mu.Lock()
			for key, o := range m.actionOffers {
				if o == s.owner {
					delete(m.actionOffers, key)
				}
			}
			m.mu.Unlock()
			return nil, []Delivery{s.reply(c, f, 96, nil, 0)}, nil
		case 97:
			if s.roomState != 0 {
				return nil, nil, fmt.Errorf("%w: matching frame 97 in room state %d, want 0", ErrNotReady, s.roomState)
			}
			selection := s.selection
			selection.CharacterID = number(f.Payload, 2)
			if list, found, _ := wire.Bytes(f.Payload, 3); found {
				v, _ := binary.Uvarint(list)
				if v > 0 {
					selection.MonsterID = int64(v)
				}
			} else if v := number(f.Payload, 3); v > 0 {
				selection.MonsterID = v
			}
			approved, err := m.rules.ValidateSelection(u.profile, selection)
			if err != nil {
				return nil, nil, err
			}
			u.character = approved.CharacterID
			if s.host == u.principal.OwnerIndex {
				s.selection = approved
			}
			b := integer(nil, 3, s.selection.MonsterID)
			for _, u := range s.members {
				b = blob(b, 2, s.matchingMember(u))
			}
			d := []Delivery{s.reply(c, f, 98, nil, 0)}
			return nil, append(d, s.broadcast(99, b, true, 0)...), nil
		case 172:
			// Matching messages contain the official enum/text payload, bounded here.
			if len(f.Payload) > 4096 {
				return nil, nil, fmt.Errorf("%w: matching message is %d bytes, at most 4096", ErrUnauthorized, len(f.Payload))
			}
			b := removeField(f.Payload, 1)
			d := []Delivery{s.reply(c, f, 173, nil, 0)}
			return nil, append(d, s.broadcast(174, b, true, u.principal.OwnerIndex)...), nil
		}
		return nil, nil, fmt.Errorf("%w: unknown matching frame %d", ErrUnauthorized, f.Type)
	})
	return d, e
}

func (m *Manager) match(ctx context.Context, c Connection, f Frame, k Kind) ([]Delivery, error) {
	profile, err := m.rules.Profile(ctx, c.Principal)
	if err != nil {
		return nil, err
	}
	if number(profile.UserInfo, 1) != c.Principal.OwnerIndex {
		return nil, fmt.Errorf("%w: room profile names owner %d, connection owner is %d", ErrUnauthorized, number(profile.UserInfo, 1), c.Principal.OwnerIndex)
	}
	selection := Selection{Kind: k, BoatLevel: profile.BoatLevel, BoatSkinID: profile.BoatSkinID}
	mode := int64(2)
	code := ""
	automatic := false
	switch f.Type {
	case 76:
		selection.EventScheduleID = number(f.Payload, 2)
		mode = number(f.Payload, 3)
		code = stringField(f.Payload, 4)
		automatic = mode == 1
	case 82:
		selection.EventScheduleID = number(f.Payload, 2)
		mode = number(f.Payload, 4)
		selection.CharacterID = number(f.Payload, 5)
		selection.MonsterID = firstNumber(f.Payload, 6)
	case 90:
		mode = 3
		code = stringField(f.Payload, 2)
		selection.CharacterID = number(f.Payload, 3)
		selection.MonsterID = number(f.Payload, 4)
	case 93:
		mode = 1
		automatic = true
	case 149:
		selection.BoatLevel = number(f.Payload, 3)
		selection.MapID = number(f.Payload, 4)
		selection.BoatSkinID = number(f.Payload, 5)
	case 153:
		mode = 1
		automatic = true
		selection.MapID = number(f.Payload, 2)
	case 155:
		mode = 3
		code = stringField(f.Payload, 2)
	}
	// Action auto-match uses the room already created by request82.
	if f.Type == 93 {
		o := m.lookup(c.Principal, k)
		if o == nil {
			return nil, fmt.Errorf("%w: owner %d has no %s room", ErrNotFound, c.Principal.OwnerIndex, k)
		}
		v, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
			u := s.find(c.Principal)
			if u == nil || s.roomState != 0 {
				return nil, nil, fmt.Errorf("%w: owner %d is not in a waiting %s room (state %d)", ErrNotReady, c.Principal.OwnerIndex, k, s.roomState)
			}
			s.matchingType = 1
			u.matchingConnection = c.ID
			if len(s.members) == s.selection.Capacity {
				return nil, append([]Delivery{s.reply(c, f, 94, s.matchingBody(), 0)}, s.complete()...), nil
			}
			key := fmt.Sprintf("%d:%d", s.selection.EventScheduleID, s.selection.MonsterID)
			m.mu.Lock()
			candidate := m.actionOffers[key]
			if candidate == nil || candidate == o {
				m.actionOffers[key] = o
				candidate = nil
			} else {
				delete(m.actionOffers, key)
			}
			m.mu.Unlock()
			return candidate, []Delivery{s.reply(c, f, 94, s.matchingBody(), 0)}, nil
		})
		if e != nil {
			return nil, e
		}
		if candidate, ok := v.(*owner); ok && candidate != nil {
			view, e := m.View(ctx, c.Principal, k)
			if e != nil {
				return nil, e
			}
			joined, e := m.joinTransferred(ctx, candidate, c, f, profile, view.Selection, o.code)
			if e != nil {
				return nil, e
			}
			_, _, e = o.submit(ctx, 0, func(s *state) (any, []Delivery, error) { s.closed = true; return nil, nil, nil })
			if e != nil && !errors.Is(e, ErrClosed) {
				return nil, e
			}
			return joined, nil
		}
		return d, nil
	}
	if mode < 1 || mode > 4 {
		return nil, fmt.Errorf("%w: room join mode %d, want 1..4", ErrUnauthorized, mode)
	}
	selection.Solo = mode == 4
	if previous := m.lookup(c.Principal, k); previous != nil {
		_, _, e := previous.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
			if s.roomState == 3 {
				m.mu.Lock()
				if m.memberships[memberKey(c.Principal, k)] == previous.code {
					delete(m.memberships, memberKey(c.Principal, k))
				}
				m.mu.Unlock()
			}
			return nil, nil, nil
		})
		if e != nil && !errors.Is(e, ErrClosed) {
			return nil, e
		}
	}
	if o := m.lookup(c.Principal, k); o != nil {
		_, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
			u := s.find(c.Principal)
			if u == nil {
				return nil, nil, fmt.Errorf("%w: owner %d is not in the %s room", ErrUnauthorized, c.Principal.OwnerIndex, k)
			}
			if s.roomState != 0 {
				return nil, nil, fmt.Errorf("%w: room is in state %d, want 0", ErrNotReady, s.roomState)
			}
			u.matchingConnection = c.ID
			return nil, []Delivery{s.reply(c, f, f.Type+1, s.matchingBody(), 0)}, nil
		})
		return d, e
	}
	if mode == 3 {
		m.mu.Lock()
		o := m.rooms[code]
		m.mu.Unlock()
		if o == nil {
			return nil, fmt.Errorf("%w: room code %q", ErrNotFound, code)
		}
		return m.join(ctx, o, c, f, profile, selection)
	}
	if !automatic || k != Fishing {
		approved, err := m.rules.ValidateSelection(profile, selection)
		if err != nil {
			return nil, err
		}
		selection = approved
	}
	if automatic {
		m.mu.Lock()
		candidates := make([]*owner, 0, len(m.rooms))
		for _, o := range m.rooms {
			candidates = append(candidates, o)
		}
		m.mu.Unlock()
		for _, o := range candidates {
			d, e := m.join(ctx, o, c, f, profile, selection)
			if e == nil {
				return d, nil
			}
			if !errors.Is(e, ErrNotReady) && !errors.Is(e, ErrNotFound) {
				return nil, e
			}
		}
	}
	if automatic && k == Fishing {
		approved, err := m.rules.ValidateSelection(profile, selection)
		if err != nil {
			return nil, err
		}
		selection = approved
	}
	if selection.Capacity < 1 || selection.Capacity > 5 {
		return nil, errors.New("room: invalid approved capacity")
	}
	o := &owner{manager: m, code: randomID()[:8], queue: make(chan operation, m.config.QueueCommands), done: make(chan struct{}), stop: make(chan struct{})}
	s := &state{owner: o, selection: selection, instance: randomID(), matchingType: mode, host: c.Principal.OwnerIndex, hostEpoch: 1, sequences: map[string]int64{}, created: m.config.Now()}
	u := &member{principal: c.Principal, profile: profile, character: selection.CharacterID, guid: randomID(), entered: m.config.Now(), matchingConnection: c.ID}
	s.members = []*member{u}
	o.state = s
	o.publishView()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: room manager is closed", ErrClosed)
	}
	if len(m.rooms) >= m.config.MaxRooms {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %d rooms are open, at most %d", ErrMailboxFull, len(m.rooms), m.config.MaxRooms)
	}
	if m.memberships[memberKey(c.Principal, k)] != "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: owner %d is already in a %s room", ErrNotReady, c.Principal.OwnerIndex, k)
	}
	for m.rooms[o.code] != nil {
		o.code = randomID()[:8]
	}
	m.rooms[o.code] = o
	m.instances[s.instance] = o
	m.memberships[memberKey(c.Principal, k)] = o.code
	m.wg.Add(1)
	m.mu.Unlock()
	go o.run()
	_, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		d := []Delivery{s.reply(c, f, f.Type+1, s.matchingResponse(f.Type), 0)}
		if (automatic || selection.Solo) && selection.Capacity == 1 {
			d = append(d, s.complete()...)
		}
		return nil, d, nil
	})
	return d, e
}

func (m *Manager) join(ctx context.Context, o *owner, c Connection, f Frame, profile Profile, selection Selection) ([]Delivery, error) {
	return m.joinTransferred(ctx, o, c, f, profile, selection, "")
}
func (m *Manager) joinTransferred(ctx context.Context, o *owner, c Connection, f Frame, profile Profile, selection Selection, previousCode string) ([]Delivery, error) {
	_, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		if s.selection.Kind != selection.Kind || s.roomState != 0 || len(s.members) >= s.selection.Capacity {
			return nil, nil, fmt.Errorf("%w: %s room in state %d with %d of %d members cannot take owner %d", ErrNotReady, s.selection.Kind, s.roomState, len(s.members), s.selection.Capacity, c.Principal.OwnerIndex)
		}
		auto := f.Type == 76 && number(f.Payload, 3) == 1 || f.Type == 153
		if auto && (s.matchingType != 1 || (selection.MapID != 0 && s.selection.MapID != selection.MapID) || s.selection.EventScheduleID != selection.EventScheduleID) {
			return nil, nil, fmt.Errorf("%w: automatic join by owner %d does not match the room's map or event", ErrNotReady, c.Principal.OwnerIndex)
		}
		if s.byOwner(c.Principal.OwnerIndex) != nil {
			return nil, nil, fmt.Errorf("%w: owner %d is already in the room", ErrUnauthorized, c.Principal.OwnerIndex)
		}
		candidate := s.selection
		candidate.CharacterID = selection.CharacterID
		approved := candidate
		if candidate.Kind == Fishing {
			if rules, ok := m.rules.(ParticipantRules); ok {
				if err := rules.ValidateParticipant(profile, candidate); err != nil {
					return nil, nil, err
				}
			}
		} else {
			var err error
			approved, err = m.rules.ValidateSelection(profile, candidate)
			if err != nil {
				return nil, nil, err
			}
		}
		m.mu.Lock()
		key := memberKey(c.Principal, s.selection.Kind)
		if m.closed || m.memberships[key] != previousCode {
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("%w: owner %d membership moved from room %q (closed=%t)", ErrNotReady, c.Principal.OwnerIndex, previousCode, m.closed)
		}
		m.memberships[key] = o.code
		m.mu.Unlock()
		u := &member{principal: c.Principal, profile: profile, character: approved.CharacterID, guid: randomID(), entered: m.config.Now(), matchingConnection: c.ID}
		s.members = append(s.members, u)
		d := []Delivery{s.reply(c, f, f.Type+1, s.matchingBody(), 0)}
		if s.selection.Kind == Defense {
			b := blob(nil, 2, s.matchingMember(u))
			d = append(d, s.broadcast(61, b, true, c.Principal.OwnerIndex)...)
		}
		if s.selection.Kind == Action {
			d = append(d, s.broadcast(92, s.matchingBody(), true, c.Principal.OwnerIndex)...)
		}
		if s.selection.Kind == Fishing {
			b := text(nil, 2, s.owner.code)
			for _, u := range s.members {
				b = blob(b, 3, s.matchingMember(u))
			}
			d = append(d, s.broadcast(161, b, true, c.Principal.OwnerIndex)...)
		}
		if s.matchingType == 1 && len(s.members) == s.selection.Capacity {
			d = append(d, s.complete()...)
		}
		return nil, d, nil
	})
	return d, e
}
func (s *state) leaveMatching(u *member) []Delivery {
	var d []Delivery
	if s.selection.Kind == Fishing && s.host == u.principal.OwnerIndex {
		d = s.broadcast(162, nil, true, u.principal.OwnerIndex)
		s.closed = true
	}
	for i, v := range s.members {
		if v == u {
			s.members = append(s.members[:i], s.members[i+1:]...)
			break
		}
	}
	s.owner.manager.mu.Lock()
	delete(s.owner.manager.memberships, memberKey(u.principal, s.selection.Kind))
	s.owner.manager.mu.Unlock()
	if len(s.members) == 0 {
		s.closed = true
		return d
	}
	if s.host == u.principal.OwnerIndex {
		s.host = s.members[0].principal.OwnerIndex
		s.hostEpoch++
	}
	b := integer(nil, 2, u.principal.OwnerIndex)
	if s.selection.Kind != Fishing {
		b = integer(b, 3, s.host)
	}
	typ := int32(62)
	if s.selection.Kind == Action {
		typ = 89
	}
	if s.selection.Kind == Fishing {
		typ = 160
	}
	return append(d, s.broadcast(typ, b, true, 0)...)
}

// RelayEndpoint returns native bootstrap fields, with the HTTP string port format.
func (m *Manager) RelayEndpoint() (region int, host, tcpPort, udpPort string) {
	return m.config.Region, m.config.AdvertisedHost, strconv.Itoa(m.config.TCPPort), strconv.Itoa(m.config.UDPPort)
}
