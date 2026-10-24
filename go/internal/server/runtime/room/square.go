package room

import (
	"context"
	"fmt"
	"slices"
	"time"

	"bd2server/internal/server/protocol/wire"
)

func (m *Manager) handleSquareChannel(ctx context.Context, c Connection, f Frame) ([]Delivery, error) {
	if f.UDP || len(f.Payload) > 4096 {
		return nil, fmt.Errorf("%w: square channel frame %d of %d bytes (udp=%t)", ErrUnauthorized, f.Type, len(f.Payload), f.UDP)
	}
	reply := func(body []byte, code int64) []Delivery {
		return []Delivery{{c.ID, Frame{Type: f.Type + 1, Sequence: f.Sequence, OwnerIndex: c.Principal.OwnerIndex, Payload: message(m.config.Now(), code, body)}}}
	}
	if m.config.SquareChannels == 0 {
		return reply(nil, 13), nil
	}
	if f.Type == 142 {
		body := integer(nil, 3, int64(m.config.SquareChannels))
		m.mu.Lock()
		for channel := 1; channel <= m.config.SquareChannels; channel++ {
			if o := m.rooms[squareCode(channel)]; o != nil {
				v := o.projection.Load()
				if v != nil && len(v.Members) > 0 {
					body = blob(body, 2, integer(integer(nil, 1, int64(channel)), 2, int64(len(v.Members))))
				}
			}
		}
		m.mu.Unlock()
		return reply(body, 0), nil
	}
	profile, err := m.rules.Profile(ctx, c.Principal)
	if err != nil {
		return nil, err
	}
	if number(profile.UserInfo, 1) != c.Principal.OwnerIndex || !profile.SquareUnlocked {
		return nil, fmt.Errorf("%w: square profile names owner %d for owner %d (unlocked=%t)", ErrUnauthorized, number(profile.UserInfo, 1), c.Principal.OwnerIndex, profile.SquareUnlocked)
	}
	m.squareJoinMu.Lock()
	defer m.squareJoinMu.Unlock()
	channel := int(number(f.Payload, 2))
	previous := m.lookup(c.Principal, Square)
	if f.Type == 205 {
		if previous != nil {
			channel = int(previous.projection.Load().Selection.SquareChannel)
		} else {
			m.mu.Lock()
			for id := 1; id <= m.config.SquareChannels; id++ {
				o := m.rooms[squareCode(id)]
				if o == nil || len(o.projection.Load().Members) < m.config.SquareChannelCapacity {
					channel = id
					break
				}
			}
			m.mu.Unlock()
		}
	}
	if f.Type == 205 && channel == 0 {
		return reply(nil, 11), nil
	}
	if channel < 1 || channel > m.config.SquareChannels {
		return reply(nil, 2), nil
	}
	m.mu.Lock()
	o := m.rooms[squareCode(channel)]
	if o == nil && !m.closed && len(m.rooms) < m.config.MaxRooms {
		o = &owner{manager: m, code: squareCode(channel), queue: make(chan operation, m.config.QueueCommands), done: make(chan struct{}), stop: make(chan struct{})}
		o.state = &state{owner: o, selection: Selection{Kind: Square, SquareChannel: int64(channel), Capacity: m.config.SquareChannelCapacity}, instance: randomID(), roomState: 2, sequences: map[string]int64{}, created: m.config.Now()}
		o.publishView()
		m.rooms[o.code] = o
		m.instances[o.state.instance] = o
		m.wg.Add(1)
		go o.run()
	}
	m.mu.Unlock()
	if o == nil {
		return reply(nil, 8), nil
	}
	if previous != o && len(o.projection.Load().Members) >= m.config.SquareChannelCapacity {
		return reply(nil, 11), nil
	}
	var deliveries []Delivery
	if previous != nil {
		_, d, err := previous.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
			u := s.find(c.Principal)
			if u == nil {
				u = s.byOwner(c.Principal.OwnerIndex)
				if u == nil || u.principal.AccountID != c.Principal.AccountID || u.principal.Generation > c.Principal.Generation {
					return nil, nil, fmt.Errorf("%w: owner %d square seat belongs to another or newer session", ErrUnauthorized, c.Principal.OwnerIndex)
				}
				return nil, s.leaveSquare(u), nil
			}
			if previous == o {
				return nil, nil, nil
			}
			return nil, s.leaveSquare(u), nil
		})
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d...)
	}
	_, d, err := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		u := s.find(c.Principal)
		if u == nil {
			if s.byOwner(c.Principal.OwnerIndex) != nil {
				return nil, nil, fmt.Errorf("%w: owner %d already holds a square seat from another session", ErrUnauthorized, c.Principal.OwnerIndex)
			}
			if len(s.members) >= s.selection.Capacity {
				return nil, reply(nil, 11), nil
			}
			u = &member{principal: c.Principal, profile: profile, guid: randomID(), entered: m.config.Now(), disconnected: m.config.Now()}
			s.members = append(s.members, u)
		}
		u.matchingConnection = c.ID
		u.profile = profile
		m.mu.Lock()
		m.memberships[memberKey(c.Principal, Square)] = o.code
		m.mu.Unlock()
		body := s.endpoint(text(nil, 2, o.code), 3, 4, 5)
		body = text(body, 6, u.guid)
		body = integer(body, 7, s.selection.SquareChannel)
		return nil, reply(body, 0), nil
	})
	return append(deliveries, d...), err
}

func squareCode(channel int) string { return fmt.Sprintf("square:%d", channel) }

func (s *state) leaveSquare(u *member) []Delivery {
	d := s.broadcast(131, integer(nil, 2, u.principal.OwnerIndex), false, u.principal.OwnerIndex)
	for i, candidate := range s.members {
		if candidate == u {
			s.members = append(s.members[:i], s.members[i+1:]...)
			break
		}
	}
	s.owner.manager.mu.Lock()
	delete(s.owner.manager.memberships, memberKey(u.principal, Square))
	s.owner.manager.mu.Unlock()
	return d
}

func squareAvatarUse(u *member) []byte {
	return integer(removeField(u.profile.AvatarUseInfo, 1), 1, u.principal.OwnerIndex)
}

func squareEnterUser(u *member) []byte {
	body := blob(nil, 1, u.playerSnapshot)
	if len(u.squareMotion) > 0 {
		body = blob(body, 2, u.squareMotion)
	}
	body = blob(body, 3, u.profile.UserInfo)
	return blob(body, 4, squareAvatarUse(u))
}

func (s *state) squareSnapshot(reconnect bool) []byte {
	stateField, enterField := 2, 3
	var body []byte
	if reconnect {
		body = integer(integer(nil, 2, 2), 3, 2)
		stateField, enterField = 4, 5
	}
	for _, u := range s.members {
		if u.gameConnection == "" || !u.ready {
			continue
		}
		body = blob(body, stateField, u.playerSnapshot)
		body = blob(body, enterField, squareEnterUser(u))
	}
	return body
}

func squareState(raw []byte, owner int64) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, fmt.Errorf("%w: square state is %d bytes, want 1..4096", ErrUnauthorized, len(raw))
	}
	if err := wire.Walk(raw, func(wire.Field) error { return nil }); err != nil {
		return nil, err
	}
	if id := number(raw, 1); id != 0 && id != owner {
		return nil, fmt.Errorf("%w: square state names owner %d, sender is %d", ErrUnauthorized, id, owner)
	}
	return integer(removeField(raw, 1), 1, owner), nil
}

func (m *Manager) handleSquare(ctx context.Context, c Connection, f Frame) ([]Delivery, error) {
	if f.UDP || len(f.Payload) > 65536 {
		return nil, fmt.Errorf("%w: square frame %d of %d bytes (udp=%t)", ErrUnauthorized, f.Type, len(f.Payload), f.UDP)
	}
	o := m.lookup(c.Principal, Square)
	if o == nil {
		return nil, fmt.Errorf("%w: owner %d has no square room", ErrNotFound, c.Principal.OwnerIndex)
	}
	var profile Profile
	if f.Type == 140 || f.Type == 147 || f.Type == 182 {
		var err error
		profile, err = m.rules.Profile(ctx, c.Principal)
		if err != nil {
			return nil, err
		}
		if number(profile.UserInfo, 1) != c.Principal.OwnerIndex {
			return nil, fmt.Errorf("%w: square profile names owner %d, connection owner is %d", ErrUnauthorized, number(profile.UserInfo, 1), c.Principal.OwnerIndex)
		}
	}
	_, deliveries, err := o.submit(ctx, int64(len(f.Payload)), func(s *state) (any, []Delivery, error) {
		u := s.find(c.Principal)
		if u == nil {
			return nil, nil, fmt.Errorf("%w: owner %d is not in the square", ErrUnauthorized, c.Principal.OwnerIndex)
		}
		if f.Type == 23 || f.Type == 185 {
			if f.Type == 23 && stringField(f.Payload, 2) != o.code || f.Type == 185 && stringField(f.Payload, 2) != u.guid {
				return nil, nil, fmt.Errorf("%w: square frame %d carries another room code or guid", ErrUnauthorized, f.Type)
			}
			if !u.disconnected.IsZero() && m.config.Now().Sub(u.disconnected) > s.reconnectWindow() {
				return nil, nil, fmt.Errorf("%w: owner %d square reconnect window has passed", ErrNotReady, u.principal.OwnerIndex)
			}
			if f.Type == 185 && !u.ready {
				return nil, nil, fmt.Errorf("%w: owner %d reconnects to the square before entering it", ErrNotReady, u.principal.OwnerIndex)
			}
			u.gameConnection = c.ID
			u.disconnected = time.Time{}
			if f.Type == 23 {
				return nil, []Delivery{s.reply(c, f, 24, text(s.endpoint(nil, 2, 3, 0), 4, u.guid), 0)}, nil
			}
			d := []Delivery{s.reply(c, f, 186, s.squareSnapshot(true), 0)}
			return nil, append(d, s.broadcast(128, blob(nil, 2, squareEnterUser(u)), false, u.principal.OwnerIndex)...), nil
		}
		if u.gameConnection != c.ID {
			return nil, nil, fmt.Errorf("%w: square frame %d arrives on connection %q, the game connection is %q", ErrUnauthorized, f.Type, c.ID, u.gameConnection)
		}
		if f.Type == 126 {
			if stringField(f.Payload, 2) != u.guid || stringField(f.Payload, 3) != o.code {
				return nil, nil, fmt.Errorf("%w: square frame 126 carries another guid or room code", ErrUnauthorized)
			}
			state, err := squareState(bytesField(f.Payload, 4), u.principal.OwnerIndex)
			if err != nil {
				return nil, nil, err
			}
			wasReady := u.ready
			u.playerSnapshot, u.ready, u.clientState = state, true, 2
			d := []Delivery{s.reply(c, f, 127, s.squareSnapshot(false), 0)}
			if !wasReady {
				d = append(d, s.broadcast(128, blob(nil, 2, squareEnterUser(u)), false, u.principal.OwnerIndex)...)
			}
			return nil, d, nil
		}
		if !u.ready {
			return nil, nil, fmt.Errorf("%w: square frame %d before owner %d entered", ErrNotReady, f.Type, u.principal.OwnerIndex)
		}
		switch f.Type {
		case 122, 175:
			if stringField(f.Payload, 2) != u.guid {
				return nil, nil, fmt.Errorf("%w: square frame %d carries another guid", ErrUnauthorized, f.Type)
			}
			state, err := squareState(bytesField(f.Payload, 3), u.principal.OwnerIndex)
			if err != nil {
				return nil, nil, err
			}
			u.playerSnapshot = state
			typ := int32(123)
			if f.Type == 175 {
				typ = 176
				if number(f.Payload, 4) == 0 {
					return nil, nil, nil
				}
			}
			return nil, s.broadcast(typ, blob(nil, 2, state), false, u.principal.OwnerIndex), nil
		case 129:
			if stringField(f.Payload, 2) != u.guid {
				return nil, nil, fmt.Errorf("%w: square frame 129 carries another guid", ErrUnauthorized)
			}
			return nil, append([]Delivery{s.reply(c, f, 130, nil, 0)}, s.leaveSquare(u)...), nil
		case 137:
			if stringField(f.Payload, 2) != o.code {
				return nil, nil, fmt.Errorf("%w: square frame 137 carries another room code", ErrUnauthorized)
			}
			var body []byte
			for _, member := range s.members {
				if member.ready && member.gameConnection != "" {
					body = blob(body, 2, squareAvatarUse(member))
				}
			}
			return nil, []Delivery{s.reply(c, f, 138, body, 0)}, nil
		case 140:
			u.profile = profile
			d := []Delivery{s.reply(c, f, 141, nil, 0)}
			return nil, append(d, s.broadcast(139, blob(nil, 2, squareAvatarUse(u)), false, 0)...), nil
		case 144:
			motion := bytesField(f.Payload, 2)
			id := number(motion, 1)
			if len(motion) > 256 || id < 0 || id != 0 && !slices.Contains(u.profile.AvatarMotions, uint64(id)) {
				return nil, nil, fmt.Errorf("%w: square motion %d (%d bytes) is not owned", ErrUnauthorized, id, len(motion))
			}
			if err := wire.Walk(motion, func(wire.Field) error { return nil }); err != nil {
				return nil, nil, err
			}
			motion = integer(integer(nil, 1, id), 2, ticks(m.config.Now()))
			u.squareMotion = append([]byte(nil), motion...)
			d := []Delivery{s.reply(c, f, 145, nil, 0)}
			body := blob(integer(nil, 2, u.principal.OwnerIndex), 3, motion)
			return nil, append(d, s.broadcast(146, body, false, u.principal.OwnerIndex)...), nil
		case 147:
			u.profile = profile
			return nil, []Delivery{s.reply(c, f, 148, nil, 0)}, nil
		case 182:
			u.profile = profile
			d := []Delivery{s.reply(c, f, 183, nil, 0)}
			header := blob(integer(nil, 1, m.config.Now().Unix()), 3, profile.UserInfo)
			for _, member := range s.members {
				if member.gameConnection != "" && member != u {
					d = append(d, Delivery{member.gameConnection, Frame{Type: 184, OwnerIndex: member.principal.OwnerIndex, Payload: blob(nil, 1, header)}})
				}
			}
			return nil, d, nil
		case 66, 68:
			if stringField(f.Payload, 2) != u.guid {
				return nil, nil, fmt.Errorf("%w: square frame %d carries another guid", ErrUnauthorized, f.Type)
			}
			return nil, []Delivery{s.reply(c, f, f.Type+1, integer(nil, 2, number(f.Payload, 3)), 0)}, nil
		case 70, 72:
			if stringField(f.Payload, 2) != u.guid {
				return nil, nil, fmt.Errorf("%w: square frame %d carries another guid", ErrUnauthorized, f.Type)
			}
			d, err := s.relay(c, f, u)
			return nil, d, err
		}
		return nil, nil, fmt.Errorf("%w: unknown square frame %d", ErrUnauthorized, f.Type)
	})
	return deliveries, err
}
