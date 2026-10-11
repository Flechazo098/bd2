package room

import (
	"encoding/binary"
	"fmt"
	"time"

	"bd2server/internal/server/protocol/wire"
)

func (s *state) gameUser(u *member) []byte {
	b := integer(nil, 1, u.principal.OwnerIndex)
	if s.selection.Kind == Action {
		b = integer(b, 2, u.character)
		b = integer(b, 3, u.clientState)
		b = integer(b, 4, u.networkState)
		b = integer(b, 5, u.ping)
		if u.paused {
			b = integer(b, 6, 1)
		}
	}
	if s.selection.Kind == Defense {
		b = integer(b, 2, u.clientState)
		b = integer(b, 3, u.enemies)
		b = integer(b, 4, u.wave)
		b = integer(b, 5, u.kills)
		b = integer(b, 6, u.networkState)
		b = integer(b, 7, u.ping)
	}
	return b
}
func (s *state) fishingUser(u *member) []byte {
	b := blob(nil, 1, u.profile.UserInfo)
	b = integer(b, 2, u.profile.RodID)
	b = blob(b, 3, u.profile.AvatarUseInfo)
	if len(u.fishingState) > 0 {
		b = blob(b, 4, u.fishingState)
	}
	if len(u.fishingMotion) > 0 {
		b = blob(b, 5, u.fishingMotion)
	}
	return b
}
func (s *state) playerStates() []byte {
	var b []byte
	for _, u := range s.members {
		b = blob(b, 2, s.gameUser(u))
	}
	return b
}
func (s *state) tryStart() []Delivery {
	if s.roomState != 1 {
		return nil
	}
	for _, u := range s.members {
		if !u.ready {
			return nil
		}
	}
	s.roomState = 2
	s.start = s.owner.manager.config.Now()
	s.loadingSince = time.Time{}
	for _, u := range s.members {
		u.clientState = 2
	}
	if s.selection.Kind == Fishing {
		return s.broadcast(203, integer(nil, 2, s.start.Unix()), false, 0)
	}
	b := integer(nil, 2, ticks(s.start))
	typ := int32(47)
	if s.selection.Kind == Action {
		typ = 106
		b = integer(b, 3, s.host)
		for _, u := range s.members {
			b = blob(b, 4, s.gameUser(u))
		}
	}
	if s.selection.Kind == Defense {
		for _, u := range s.members {
			b = blob(b, 3, s.gameUser(u))
			v := integer(nil, 1, u.principal.OwnerIndex)
			v = text(v, 2, u.guid)
			v = integer(v, 3, ticks(u.entered))
			b = blob(b, 4, v)
		}
	}
	return s.broadcast(typ, b, false, 0)
}
func (s *state) finish(clear bool, now time.Time) []Delivery {
	if s.roomState == 3 {
		return nil
	}
	s.roomState = 3
	s.end = now
	s.clear = clear
	if s.start.IsZero() {
		s.start = now
	}
	s.evidence = s.playerStates()
	if len(s.monsterSnapshot) > 0 {
		s.evidence = blob(s.evidence, 3, s.monsterSnapshot)
	}
	var deliveries []Delivery
	switch s.selection.Kind {
	case Action:
		state := int64(0)
		if clear {
			state = 1
		}
		deliveries = s.broadcast(108, integer(integer(nil, 2, ticks(now)), 3, state), false, 0)
	case Defense:
		deliveries = s.broadcast(51, s.playerStates(), false, 0)
	default:
		deliveries = s.broadcast(204, integer(integer(nil, 2, now.Unix()), 3, 1), false, 0)
	}
	if s.owner.manager.config.PublishResult == nil {
		return deliveries
	}
	s.terminalDeliveries = deliveries
	s.publishResult()
	return nil
}

func (s *state) publishResult() {
	if s.resultQueued || s.owner.manager.config.PublishResult == nil {
		return
	}
	s.resultQueued = s.owner.manager.config.PublishResult(FinalResult{View: s.view(), Clear: s.clear, ScoreMilliseconds: s.end.Sub(s.start).Milliseconds(), Evidence: append([]byte(nil), s.evidence...)})
}

func (s *state) handleGame(c Connection, f Frame) ([]Delivery, error) {
	switch f.Type {
	case 25, 27, 30, 34, 55, 66, 68, 70, 72, 84:
		s.transientCommand = true
	}
	if s.closed || c.Channel != s.selection.Kind {
		return nil, ErrNotFound
	}
	u := s.find(c.Principal)
	if u == nil {
		return nil, ErrUnauthorized
	}
	if f.Type == 23 {
		if f.UDP || stringField(f.Payload, 2) != s.owner.code || s.roomState < 1 || s.roomState > 2 {
			return nil, ErrUnauthorized
		}
		if u.clientState == 4 {
			return nil, ErrNotReady
		}
		if !u.disconnected.IsZero() && s.owner.manager.config.Now().Sub(u.disconnected) > s.reconnectWindow() {
			return nil, ErrNotReady
		}
		u.gameConnection = c.ID
		u.disconnected = time.Time{}
		u.networkState = 1
		b := s.endpoint(nil, 2, 3, 0)
		b = text(b, 4, u.guid)
		return []Delivery{s.reply(c, f, 24, b, 0)}, nil
	}
	if f.Type == 55 {
		if !f.UDP || s.selection.Kind != Action || u.gameConnection != c.ID || stringField(f.Payload, 2) != s.owner.code {
			return nil, ErrUnauthorized
		}
		b := text(nil, 2, s.owner.manager.config.AdvertisedHost)
		b = text(b, 3, fmt.Sprint(s.owner.manager.config.UDPPort))
		b = text(b, 4, u.guid)
		return []Delivery{s.reply(c, f, 56, b, 0)}, nil
	}
	if f.Type == 195 || f.Type == 198 { // These notifications project a committed HTTP change.
		if s.selection.Kind != Fishing || u.gameConnection != c.ID || f.UDP {
			return nil, ErrUnauthorized
		}
		if f.Type == 195 {
			b := integer(nil, 2, u.principal.OwnerIndex)
			b = integer(b, 3, u.profile.RodID)
			b = blob(b, 4, u.profile.AvatarUseInfo)
			d := []Delivery{s.reply(c, f, 196, nil, 0)}
			return append(d, s.broadcast(197, b, false, u.principal.OwnerIndex)...), nil
		}
		return []Delivery{s.reply(c, f, 199, nil, 0)}, nil
	}
	if f.Type == 144 {
		if s.selection.Kind != Fishing || f.UDP || u.gameConnection != c.ID || s.roomState != 2 {
			return nil, ErrUnauthorized
		}
		info := bytesField(f.Payload, 2)
		if len(info) > 256 || number(info, 1) <= 0 {
			return nil, ErrUnauthorized
		}
		u.fishingMotion = append([]byte(nil), info...)
		d := []Delivery{s.reply(c, f, 145, nil, 0)}
		return append(d, s.broadcast(146, blob(integer(nil, 2, u.principal.OwnerIndex), 3, info), false, u.principal.OwnerIndex)...), nil
	}
	guid := stringField(f.Payload, 2)
	if f.Type == 37 || f.Type == 109 || f.Type == 201 {
		if f.UDP || guid != u.guid || u.clientState == 4 {
			return nil, ErrUnauthorized
		}
		return s.reconnect(c, f, u)
	}
	if _, err := s.require(c, guid); err != nil {
		return nil, err
	}
	if f.UDP && f.Type != 84 && f.Type != 68 && f.Type != 70 && f.Type != 72 && f.Type != 25 && f.Type != 34 && f.Type != 27 && f.Type != 30 {
		return nil, ErrUnauthorized
	}
	switch f.Type {
	case 25, 34:
		if !f.UDP || s.selection.Kind != Action {
			return nil, ErrUnauthorized
		}
		var b []byte
		for _, member := range s.members {
			v := integer(nil, 1, member.principal.OwnerIndex)
			v = text(v, 2, s.owner.manager.config.AdvertisedHost)
			v = text(v, 3, fmt.Sprint(s.owner.manager.config.UDPPort))
			v = integer(v, 4, 4)
			b = blob(b, 2, v)
		}
		return []Delivery{s.reply(c, f, f.Type+1, b, 0)}, nil
	case 84:
		if s.selection.Kind != Action || !f.UDP {
			return nil, ErrUnauthorized
		}
		return []Delivery{s.reply(c, f, 85, nil, 0)}, nil
	case 66, 68:
		b := integer(nil, 2, number(f.Payload, 3))
		return []Delivery{s.reply(c, f, f.Type+1, b, 0)}, nil
	case 43:
		v := number(f.Payload, 3)
		if v < 0 || v > 2 {
			return nil, ErrUnauthorized
		}
		u.networkState = v
		return []Delivery{s.reply(c, f, 44, integer(nil, 2, v), 0)}, nil
	case 45:
		if f.UDP || s.selection.Kind == Fishing || s.roomState != 1 && s.roomState != 2 {
			return nil, ErrNotReady
		}
		if u.ready {
			return []Delivery{s.reply(c, f, 46, integer(integer(nil, 2, u.principal.OwnerIndex), 3, u.clientState), 0)}, nil
		}
		u.ready = true
		u.clientState = 1
		d := s.broadcast(46, integer(integer(nil, 2, u.principal.OwnerIndex), 3, 1), false, 0)
		return append(d, s.tryStart()...), nil
	case 70, 72:
		return s.relay(c, f, u)
	case 27, 30:
		if s.selection.Kind != Action || !f.UDP {
			return nil, ErrUnauthorized
		}
		if f.Type == 27 {
			f.Type = 70
		} else {
			f.Type = 72
		}
		d, err := s.relay(c, f, u)
		for i := range d {
			if d[i].Frame.Type == 71 {
				d[i].Frame.Type = 28
			} else {
				d[i].Frame.Type = 31
			}
		}
		return d, err
	case 52, 114, 192:
		if f.Type == 52 && s.selection.Kind != Defense || f.Type == 114 && s.selection.Kind != Action || f.Type == 192 && s.selection.Kind != Fishing {
			return nil, ErrUnauthorized
		}
		u.clientState = 4
		d := []Delivery{s.reply(c, f, f.Type+1, nil, 0)}
		typ := int32(54)
		if s.selection.Kind == Action {
			typ = 116
		}
		if s.selection.Kind == Fishing {
			typ = 194
		}
		b := integer(nil, 2, u.principal.OwnerIndex)
		if s.selection.Kind == Defense {
			b = integer(b, 3, 4)
		}
		d = append(d, s.broadcast(typ, b, false, u.principal.OwnerIndex)...)
		if s.selection.Kind == Fishing && u.principal.OwnerIndex == s.host {
			d = append(d, s.finish(false, s.owner.manager.config.Now())...)
		}
		d = append(d, s.chooseHost()...)
		return d, nil
	case 117:
		if s.selection.Kind != Action {
			return nil, ErrUnauthorized
		}
		u.paused = true
		d := []Delivery{s.reply(c, f, 118, nil, 0)}
		d = append(d, s.broadcast(119, blob(nil, 2, s.gameUser(u)), false, u.principal.OwnerIndex)...)
		return append(d, s.chooseHost()...), nil
	case 120:
		if s.selection.Kind != Action || s.byOwner(number(f.Payload, 3)) == nil {
			return nil, ErrUnauthorized
		}
		return []Delivery{s.reply(c, f, 121, nil, 0)}, nil
	case 111:
		return s.actionState(c, f, u)
	case 48:
		return s.defenseState(c, f, u)
	case 210:
		if s.selection.Kind != Fishing || s.roomState != 2 {
			return nil, ErrNotReady
		}
		info := bytesField(f.Payload, 3)
		if number(info, 1) != u.principal.OwnerIndex {
			return nil, ErrUnauthorized
		}
		if nested := bytesField(info, 2); len(nested) > 0 && number(nested, 1) != u.principal.OwnerIndex {
			return nil, ErrUnauthorized
		}
		u.fishingState = append([]byte(nil), info...)
		if number(f.Payload, 4) == 0 {
			return nil, nil
		}
		return s.broadcast(211, blob(nil, 2, info), false, u.principal.OwnerIndex), nil
	case 212:
		if s.selection.Kind != Fishing || stringField(f.Payload, 3) != s.owner.code || s.roomState < 1 || s.roomState > 2 {
			return nil, ErrNotReady
		}
		info := bytesField(f.Payload, 4)
		if id := number(info, 1); id != 0 && id != u.principal.OwnerIndex {
			return nil, ErrUnauthorized
		}
		if nested := bytesField(info, 2); len(nested) > 0 && number(nested, 1) != u.principal.OwnerIndex {
			return nil, ErrUnauthorized
		}
		info, _, _ = wire.ReplaceVarint(info, 1, uint64(u.principal.OwnerIndex))
		u.fishingState = append([]byte(nil), info...)
		u.ready = true
		u.clientState = 2
		if s.roomState == 1 {
			s.roomState = 2
			s.start = s.owner.manager.config.Now()
			s.loadingSince = time.Time{}
		}
		var b []byte
		for _, v := range s.members {
			if v.ready {
				b = blob(b, 2, s.fishingUser(v))
			}
		}
		for _, v := range s.buffs {
			b = blob(b, 3, v)
		}
		b = integer(b, 4, s.start.Unix())
		d := []Delivery{s.reply(c, f, 213, b, 0)}
		return append(d, s.broadcast(214, blob(nil, 2, s.fishingUser(u)), false, u.principal.OwnerIndex)...), nil
	}
	return nil, ErrUnauthorized
}

func (s *state) reconnect(c Connection, f Frame, u *member) ([]Delivery, error) {
	if f.Type == 37 && s.selection.Kind != Defense || f.Type == 109 && s.selection.Kind != Action || f.Type == 201 && s.selection.Kind != Fishing {
		return nil, ErrUnauthorized
	}
	if !u.disconnected.IsZero() && s.owner.manager.config.Now().Sub(u.disconnected) > s.reconnectWindow() {
		return nil, ErrNotReady
	}
	if s.roomState == 3 && s.owner.manager.config.PublishResult != nil && !s.resultPersisted {
		return nil, ErrNotReady
	}
	u.gameConnection = c.ID
	u.disconnected = time.Time{}
	u.networkState = 1
	u.paused = false
	var b []byte
	if s.selection.Kind == Fishing {
		b = integer(b, 2, u.clientState)
		b = integer(b, 3, s.roomState)
		for _, v := range s.members {
			if v.ready {
				b = blob(b, 4, s.fishingUser(v))
			}
		}
		for _, v := range s.buffs {
			b = blob(b, 5, v)
		}
		if !s.start.IsZero() {
			b = integer(b, 6, s.start.Unix())
		}
	} else {
		b = s.endpoint(b, 2, 3, 0)
		b = integer(b, 4, u.clientState)
		b = integer(b, 5, s.roomState)
		if s.selection.Kind == Action {
			for _, v := range s.members {
				if len(v.playerSnapshot) > 0 {
					b = blob(b, 6, v.playerSnapshot)
				}
			}
			if len(s.monsterSnapshot) > 0 {
				b = blob(b, 7, s.monsterSnapshot)
			}
			b = integer(b, 8, s.host)
		}
	}
	return append([]Delivery{s.reply(c, f, f.Type+1, b, 0)}, s.chooseHost()...), nil
}

func (s *state) actionState(c Connection, f Frame, u *member) ([]Delivery, error) {
	if s.selection.Kind != Action || s.roomState != 2 || !u.ready {
		return nil, ErrNotReady
	}
	ping := number(f.Payload, 3)
	if ping < 0 || ping > 60000 {
		return nil, ErrUnauthorized
	}
	u.ping = ping
	u.paused = false
	for _, v := range repeated(f.Payload, 4) {
		info := bytesField(v, 1)
		id := number(info, 1)
		if id != u.principal.OwnerIndex {
			continue
		}
		state := number(info, 3)
		if state < 2 || state > 5 {
			return nil, ErrUnauthorized
		}
		if u.clientState == 4 || u.clientState == 5 {
			continue
		}
		if state == 5 { // A clear is accepted only from the host's monster terminal state.
			state = 2
		}
		u.clientState = state
		p := bytesField(v, 2)
		if number(p, 3) != id {
			return nil, ErrUnauthorized
		}
		u.playerSnapshot = append([]byte(nil), p...)
	}
	monster := bytesField(f.Payload, 5)
	if u.principal.OwnerIndex == s.host && len(monster) > 0 {
		if number(monster, 3) != s.selection.MonsterID {
			return nil, ErrUnauthorized
		}
		s.monsterSnapshot = append([]byte(nil), monster...)
		if number(monster, 10) == 0 && u.acceptedRun {
			for _, v := range s.members {
				if v.clientState == 2 {
					v.clientState = 5
				}
			}
			return s.finish(true, s.owner.manager.config.Now()), nil
		}
	}
	d := s.broadcast(112, s.playerStates(), false, 0)
	allDead := true
	for _, v := range s.members {
		if v.clientState == 2 {
			allDead = false
		}
	}
	if allDead {
		terminal := s.finish(false, s.owner.manager.config.Now())
		if s.owner.manager.config.PublishResult != nil {
			s.terminalDeliveries = append(d, s.terminalDeliveries...)
			return nil, nil
		}
		d = append(d, terminal...)
	}
	return d, nil
}
func (s *state) defenseState(c Connection, f Frame, u *member) ([]Delivery, error) {
	if s.selection.Kind != Defense || s.roomState != 2 || !u.ready {
		return nil, ErrNotReady
	}
	state, enemies, wave, kills, ping := number(f.Payload, 3), number(f.Payload, 4), number(f.Payload, 5), number(f.Payload, 6), number(f.Payload, 7)
	if state < 2 || state > 5 || enemies < 0 || enemies > 100 || wave < u.wave || wave > s.selection.DefenseLastWave || kills < u.kills || kills > 100000000 || ping < 0 || ping > 60000 {
		return nil, ErrUnauthorized
	}
	if state == 5 && (wave != s.selection.DefenseLastWave || enemies != 0) {
		return nil, ErrUnauthorized
	}
	if (state == 3 || state == 5) && !u.acceptedRun {
		return nil, ErrNotReady
	}
	if u.clientState == 3 || u.clientState == 4 || u.clientState == 5 {
		return nil, nil
	}
	u.clientState = state
	u.enemies = enemies
	u.wave = wave
	u.kills = kills
	u.ping = ping
	d := s.broadcast(49, s.playerStates(), false, 0)
	allDone, clear := true, false
	for _, v := range s.members {
		if v.clientState == 2 {
			allDone = false
		}
		if v.clientState == 5 {
			clear = true
		}
	}
	if allDone {
		terminal := s.finish(clear, s.owner.manager.config.Now())
		if s.owner.manager.config.PublishResult != nil {
			s.terminalDeliveries = append(d, s.terminalDeliveries...)
			return nil, nil
		}
		d = append(d, terminal...)
	}
	return d, nil
}

func (s *state) relay(c Connection, f Frame, u *member) ([]Delivery, error) {
	if s.roomState != 2 || !u.ready {
		return nil, ErrNotReady
	}
	field := 3
	recipient := int64(0)
	typ := int32(71)
	if f.Type == 72 {
		field = 4
		recipient = number(f.Payload, 3)
		typ = 73
		if s.byOwner(recipient) == nil {
			return nil, ErrUnauthorized
		}
	}
	data := bytesField(f.Payload, field)
	if len(data) < 4 || len(data) > 60000 {
		return nil, ErrUnauthorized
	}
	innerType := int64(binary.LittleEndian.Uint32(data))
	payload := data[4:]
	if err := wire.Walk(payload, func(wire.Field) error { return nil }); err != nil {
		return nil, err
	}
	switch s.selection.Kind {
	case Action:
		switch innerType {
		case 1011:
			if number(payload, 3) != u.principal.OwnerIndex {
				return nil, ErrUnauthorized
			}
		case 1012:
			if u.principal.OwnerIndex != s.host || number(payload, 3) != s.selection.MonsterID {
				return nil, ErrUnauthorized
			}
		default:
			if innerType != 1007 && innerType != 1010 || number(payload, 1) != u.principal.OwnerIndex {
				return nil, ErrUnauthorized
			}
		}
		if innerType == 1011 || innerType == 1012 {
			if number(payload, 1) < 0 || number(payload, 1) > 32 {
				return nil, ErrUnauthorized
			}
			seq := number(payload, 2)
			if seq > 0 {
				key := s.sequenceKey(u.principal.OwnerIndex, innerType*1000+number(payload, 1))
				if last, exists := s.sequences[key]; exists && seq <= last {
					return nil, nil
				}
				s.sequences[key] = seq
			}
		}
	case Defense:
		if innerType < 1002 || innerType > 1009 {
			return nil, ErrUnauthorized
		}
		if id := number(payload, 1); id != 0 && id != u.principal.OwnerIndex {
			return nil, ErrUnauthorized
		}
	case Fishing:
		if innerType == 1013 {
			if number(payload, 2) != u.principal.OwnerIndex {
				return nil, ErrUnauthorized
			}
		} else if innerType >= 9002 && innerType <= 9004 {
			if number(payload, 1) != u.principal.OwnerIndex || len(payload) > 2048 {
				return nil, ErrUnauthorized
			}
		} else {
			return nil, ErrUnauthorized
		}
	}
	b := integer(nil, 2, u.principal.OwnerIndex)
	b = blob(b, 3, data)
	var out []Delivery
	for _, v := range s.members {
		if v.principal.OwnerIndex == u.principal.OwnerIndex || v.gameConnection == "" || recipient != 0 && v.principal.OwnerIndex != recipient {
			continue
		}
		out = append(out, Delivery{v.gameConnection, Frame{Type: typ, OwnerIndex: v.principal.OwnerIndex, Payload: message(s.owner.manager.config.Now(), 0, b), UDP: f.UDP}})
	}
	return out, nil
}
