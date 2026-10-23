package room

import (
	"bd2server/internal/common/util"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Manager struct {
	config       Config
	rules        Rules
	mu           sync.Mutex
	squareJoinMu sync.Mutex
	rooms        map[string]*owner
	instances    map[string]*owner
	memberships  map[string]string
	actionOffers map[string]*owner
	closed       bool
	wg           sync.WaitGroup
}

type operation struct {
	size int64
	call func(*state) (any, []Delivery, error)
	done chan outcome
}
type outcome struct {
	value      any
	deliveries []Delivery
	err        error
}
type owner struct {
	manager    *Manager
	code       string
	queue      chan operation
	bytes      atomic.Int64
	done       chan struct{}
	stop       chan struct{}
	stopOnce   sync.Once
	state      *state
	projection atomic.Pointer[View]
}
type member struct {
	principal          Principal
	profile            Profile
	character          int64
	guid               string
	entered            time.Time
	matchingConnection string
	gameConnection     string
	disconnected       time.Time
	ready              bool
	clientState        int64
	networkState       int64
	ping               int64
	paused             bool
	wave               int64
	enemies            int64
	kills              int64
	playerSnapshot     []byte
	fishingState       []byte
	fishingMotion      []byte
	squareMotion       []byte
	runGUID            string
	acceptedRun        bool
	projectionEvents   []string
}
type state struct {
	owner              *owner
	selection          Selection
	instance           string
	matchingType       int64
	roomState          int64
	host               int64
	hostEpoch          uint64
	members            []*member
	start              time.Time
	end                time.Time
	loadingSince       time.Time
	created            time.Time
	monsterSnapshot    []byte
	buffs              [][]byte
	clear              bool
	evidence           []byte
	sequences          map[string]int64
	closed             bool
	resultQueued       bool
	resultPersisted    bool
	terminalDeliveries []Delivery
	transientCommand   bool
}

func New(config Config, rules Rules) (*Manager, error) {
	if rules == nil || config.AdvertisedHost == "" || config.TCPPort <= 0 || config.TCPPort > 65535 || config.UDPPort < 0 || config.UDPPort > 65535 {
		return nil, errors.New("room: rules and advertised endpoint required")
	}
	if config.MaxRooms <= 0 {
		config.MaxRooms = 1024
	}
	if config.SquareChannels < 0 || config.SquareChannels > 10000 || config.SquareChannelCapacity < 0 || config.SquareChannelCapacity > 1000 || (config.SquareChannels == 0) != (config.SquareChannelCapacity == 0) {
		return nil, errors.New("room: invalid square channel design")
	}
	if config.QueueCommands <= 0 {
		config.QueueCommands = 256
	}
	if config.QueueBytes <= 0 {
		config.QueueBytes = 4 << 20
	}
	if config.ReconnectWindow <= 0 {
		config.ReconnectWindow = 15 * time.Second
	}
	if config.LoadingLimit <= 0 {
		config.LoadingLimit = 15 * time.Second
	}
	if config.MatchingLimit <= 0 {
		config.MatchingLimit = 5 * time.Minute
	}
	if config.RoomDuration <= 0 {
		config.RoomDuration = 6 * time.Hour
	}
	if config.ResultRetention <= 0 {
		config.ResultRetention = 10 * time.Minute
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Manager{config: config, rules: rules, rooms: map[string]*owner{}, instances: map[string]*owner{}, memberships: map[string]string{}, actionOffers: map[string]*owner{}}, nil
}

func randomID() string {
	value, err := util.RandomHex(16)
	if err != nil {
		panic(err)
	}
	return value
}
func memberKey(p Principal, k Kind) string { return p.AccountID + ":" + string(k) }
func validPrincipal(p Principal) bool {
	return p.AccountID != "" && p.OwnerIndex > 0 && p.SessionID != "" && p.Generation > 0
}
func samePrincipal(a, b Principal) bool { return a == b }

func (m *Manager) lookup(p Principal, k Kind) *owner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rooms[m.memberships[memberKey(p, k)]]
}

func (o *owner) submit(ctx context.Context, size int64, call func(*state) (any, []Delivery, error)) (any, []Delivery, error) {
	if size > o.manager.config.QueueBytes {
		return nil, nil, ErrMailboxFull
	}
	if o.bytes.Add(size) > o.manager.config.QueueBytes {
		o.bytes.Add(-size)
		return nil, nil, ErrMailboxFull
	}
	op := operation{size: size, call: call, done: make(chan outcome, 1)}
	select {
	case <-ctx.Done():
		o.bytes.Add(-size)
		return nil, nil, ctx.Err()
	case <-o.done:
		o.bytes.Add(-size)
		return nil, nil, ErrClosed
	default:
	}
	select {
	case o.queue <- op:
	default:
		o.bytes.Add(-size)
		return nil, nil, ErrMailboxFull
	}
	// Accepted commands finish even if the connection disappears.
	select {
	case r := <-op.done:
		return r.value, r.deliveries, r.err
	case <-o.done:
		select {
		case r := <-op.done:
			return r.value, r.deliveries, r.err
		default:
			return nil, nil, ErrClosed
		}
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}
func (o *owner) run() {
	defer o.manager.wg.Done()
	defer close(o.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case op := <-o.queue:
			o.bytes.Add(-op.size)
			if o.state.closed {
				op.done <- outcome{err: ErrClosed}
				continue
			}
			o.state.transientCommand = false
			v, d, e := op.call(o.state)
			if !o.state.transientCommand {
				o.publishView()
			}
			op.done <- outcome{v, d, e}
		case <-ticker.C:
			d := o.state.expire(o.manager.config.Now())
			o.publishView()
			if len(d) > 0 && o.manager.config.Publish != nil {
				o.manager.config.Publish(d)
			}
			if o.state.closed {
				o.manager.remove(o)
				return
			}
		case <-o.stop:
			for o.state.roomState == 3 && !o.state.resultQueued && o.manager.config.PublishResult != nil {
				o.state.publishResult()
				if !o.state.resultQueued {
					time.Sleep(10 * time.Millisecond)
				}
			}
			for {
				select {
				case op := <-o.queue:
					o.bytes.Add(-op.size)
					op.done <- outcome{err: ErrClosed}
				default:
					return
				}
			}
		}
	}
}

func (o *owner) publishView() {
	v := o.state.view()
	o.projection.Store(&v)
}

// Snapshot reads an immutable room projection without entering its mailbox.
func (m *Manager) Snapshot(p Principal, k Kind) (View, error) {
	o := m.lookup(p, k)
	if o == nil {
		return View{}, ErrNotFound
	}
	v := o.projection.Load()
	if v == nil {
		return View{}, ErrNotReady
	}
	for _, member := range v.Members {
		if samePrincipal(member.Principal, p) {
			return cloneView(*v), nil
		}
	}
	return View{}, ErrUnauthorized
}

func (m *Manager) SnapshotForSession(account, session string, k Kind) (View, error) {
	o := m.lookup(Principal{AccountID: account}, k)
	if o == nil {
		return View{}, ErrNotFound
	}
	v := o.projection.Load()
	if v == nil {
		return View{}, ErrNotReady
	}
	for _, member := range v.Members {
		if member.Principal.AccountID == account && member.Principal.SessionID == session {
			return cloneView(*v), nil
		}
	}
	return View{}, ErrUnauthorized
}

func (m *Manager) FishingRooms() []View {
	m.mu.Lock()
	owners := make([]*owner, 0, len(m.rooms))
	for _, o := range m.rooms {
		owners = append(owners, o)
	}
	m.mu.Unlock()
	var views []View
	for _, o := range owners {
		v := o.projection.Load()
		if v != nil && v.Kind == Fishing && v.RoomState == 0 && len(v.Members) < v.Selection.Capacity {
			views = append(views, cloneView(*v))
		}
	}
	return views
}

func cloneView(v View) View {
	v.Members = append([]MemberView(nil), v.Members...)
	for i := range v.Members {
		v.Members[i].Profile.UserInfo = append([]byte(nil), v.Members[i].Profile.UserInfo...)
		v.Members[i].Profile.AvatarUseInfo = append([]byte(nil), v.Members[i].Profile.AvatarUseInfo...)
		v.Members[i].Profile.AvatarMotions = append([]uint64(nil), v.Members[i].Profile.AvatarMotions...)
		v.Members[i].Profile.FishingMaps = append([]uint64(nil), v.Members[i].Profile.FishingMaps...)
	}
	return v
}
func (m *Manager) remove(o *owner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rooms[o.code] != o {
		return
	}
	delete(m.rooms, o.code)
	delete(m.instances, o.state.instance)
	for key, v := range m.actionOffers {
		if v == o {
			delete(m.actionOffers, key)
		}
	}
	for key, code := range m.memberships {
		if code == o.code {
			delete(m.memberships, key)
		}
	}
}
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	for _, o := range m.rooms {
		o.stopOnce.Do(func() { close(o.stop) })
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) View(ctx context.Context, p Principal, k Kind) (View, error) {
	o := m.lookup(p, k)
	if o == nil {
		return View{}, ErrNotFound
	}
	v, _, err := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		if s.find(p) == nil {
			return nil, nil, ErrUnauthorized
		}
		return s.view(), nil, nil
	})
	if err != nil {
		return View{}, err
	}
	return v.(View), nil
}
func (m *Manager) AcceptRun(ctx context.Context, p Principal, k Kind, instanceID, runGUID string) error {
	o := m.lookup(p, k)
	if o == nil {
		return ErrNotFound
	}
	_, _, err := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		u := s.find(p)
		if u == nil || s.instance != instanceID || (!u.ready && k != Fishing) || s.roomState < 1 || s.roomState > 3 || (s.roomState == 1 && k != Fishing) || runGUID == "" || runGUID != u.guid {
			return nil, nil, ErrNotReady
		}
		if u.acceptedRun && u.runGUID != runGUID {
			return nil, nil, ErrUnauthorized
		}
		u.runGUID = runGUID
		u.acceptedRun = true
		return nil, nil, nil
	})
	return err
}
func (m *Manager) Result(ctx context.Context, p Principal, k Kind, instanceID string) (FinalResult, error) {
	m.mu.Lock()
	o := m.instances[instanceID]
	m.mu.Unlock()
	if o == nil {
		return FinalResult{}, ErrNotFound
	}
	v, _, err := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		u := s.find(p)
		if u == nil || s.selection.Kind != k || s.instance != instanceID || !u.ready || !u.acceptedRun || s.roomState != 3 {
			return nil, nil, ErrNotReady
		}
		return FinalResult{View: s.view(), Clear: s.clear, ScoreMilliseconds: s.end.Sub(s.start).Milliseconds(), Evidence: append([]byte(nil), s.evidence...)}, nil, nil
	})
	if err != nil {
		return FinalResult{}, err
	}
	return v.(FinalResult), nil
}

func (m *Manager) AcknowledgeResult(ctx context.Context, instanceID string) error {
	m.mu.Lock()
	o := m.instances[instanceID]
	m.mu.Unlock()
	if o == nil {
		return ErrNotFound
	}
	_, deliveries, err := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		if s.roomState != 3 {
			return nil, nil, ErrNotReady
		}
		if s.resultPersisted {
			return nil, nil, nil
		}
		s.resultPersisted = true
		d := s.terminalDeliveries
		s.terminalDeliveries = nil
		for i := range d {
			if u := s.byOwner(d[i].Frame.OwnerIndex); u != nil {
				d[i].ConnectionID = u.gameConnection
			}
		}
		return nil, d, nil
	})
	if err == nil && len(deliveries) > 0 && m.config.Publish != nil {
		m.config.Publish(deliveries)
	}
	return err
}

// UpdateFishingProjection is called only after the player's HTTP transaction commits.
func (m *Manager) UpdateFishingProjection(ctx context.Context, p Principal, profile Profile, buffs [][]byte, itemID int64, eventID string) ([]Delivery, error) {
	if number(profile.UserInfo, 1) != p.OwnerIndex || eventID == "" {
		return nil, ErrUnauthorized
	}
	o := m.lookup(p, Fishing)
	if o == nil {
		return nil, ErrNotFound
	}
	_, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
		u := s.find(p)
		if u == nil || s.roomState != 2 {
			return nil, nil, ErrNotReady
		}
		if slices.Contains(u.projectionEvents, eventID) {
			return nil, nil, nil
		}
		u.profile = profile
		u.profile.UserInfo = append([]byte(nil), profile.UserInfo...)
		u.profile.AvatarUseInfo = append([]byte(nil), profile.AvatarUseInfo...)
		u.profile.FishingMaps = append([]uint64(nil), profile.FishingMaps...)
		u.projectionEvents = append(u.projectionEvents, eventID)
		if len(u.projectionEvents) > 128 {
			u.projectionEvents = u.projectionEvents[1:]
		}
		if itemID <= 0 {
			return nil, nil, nil
		}
		s.buffs = nil
		var b []byte
		for _, buff := range buffs {
			s.buffs = append(s.buffs, append([]byte(nil), buff...))
			b = blob(b, 2, buff)
		}
		b = integer(b, 3, itemID)
		b = integer(b, 4, p.OwnerIndex)
		return nil, s.broadcast(200, b, false, 0), nil
	})
	return d, e
}
func (m *Manager) Disconnect(ctx context.Context, c Connection) ([]Delivery, error) {
	var all []Delivery
	for _, k := range []Kind{Action, Defense, Fishing, Square} {
		o := m.lookup(c.Principal, k)
		if o == nil {
			continue
		}
		_, d, e := o.submit(ctx, 0, func(s *state) (any, []Delivery, error) {
			u := s.find(c.Principal)
			if u == nil {
				return nil, nil, nil
			}
			if c.Channel == Matching && u.matchingConnection == c.ID {
				u.matchingConnection = ""
				if s.roomState == 0 {
					u.disconnected = m.config.Now()
				}
			}
			if c.Channel == k && u.gameConnection == c.ID {
				u.gameConnection = ""
				u.disconnected = m.config.Now()
				u.networkState = 0
				if k == Square {
					return nil, s.broadcast(131, integer(nil, 2, u.principal.OwnerIndex), false, u.principal.OwnerIndex), nil
				}
				return nil, s.chooseHost(), nil
			}
			return nil, nil, nil
		})
		if e != nil && !errors.Is(e, ErrClosed) {
			return all, e
		}
		all = append(all, d...)
	}
	return all, nil
}

func (s *state) find(p Principal) *member {
	for _, u := range s.members {
		if samePrincipal(u.principal, p) {
			return u
		}
	}
	return nil
}
func (s *state) byOwner(id int64) *member {
	for _, u := range s.members {
		if u.principal.OwnerIndex == id {
			return u
		}
	}
	return nil
}
func (s *state) view() View {
	v := View{RoomCode: s.owner.code, InstanceID: s.instance, Kind: s.selection.Kind, Selection: s.selection, MatchingType: s.matchingType, RoomState: s.roomState, HostOwnerIndex: s.host, HostEpoch: s.hostEpoch, StartTime: s.start, EndTime: s.end}
	for _, u := range s.members {
		p := u.profile
		p.UserInfo = append([]byte(nil), p.UserInfo...)
		p.AvatarUseInfo = append([]byte(nil), p.AvatarUseInfo...)
		p.AvatarMotions = append([]uint64(nil), p.AvatarMotions...)
		p.FishingMaps = append([]uint64(nil), p.FishingMaps...)
		v.Members = append(v.Members, MemberView{Principal: u.principal, Profile: p, CharacterID: u.character, Ready: u.ready, ClientState: u.clientState, Connected: u.gameConnection != "", RunGUID: u.runGUID, AcceptedRun: u.acceptedRun, NativeGUID: u.guid, Wave: u.wave, Kills: u.kills})
	}
	return v
}
func (s *state) chooseHost() []Delivery {
	if s.selection.Kind != Action || s.roomState == 3 {
		return nil
	}
	if u := s.byOwner(s.host); u != nil && u.gameConnection != "" && u.clientState != 4 && !u.paused {
		return nil
	}
	for _, u := range s.members {
		if u.gameConnection != "" && u.clientState != 4 && !u.paused {
			if s.host != u.principal.OwnerIndex {
				s.host = u.principal.OwnerIndex
				s.hostEpoch++
				s.sequences = map[string]int64{}
				return s.broadcast(107, integer(nil, 2, s.host), false, 0)
			}
			return nil
		}
	}
	return nil
}
func (s *state) expire(now time.Time) []Delivery {
	if s.selection.Kind == Square {
		for _, u := range append([]*member(nil), s.members...) {
			if !u.disconnected.IsZero() && now.Sub(u.disconnected) > s.reconnectWindow() {
				s.leaveSquare(u)
			}
		}
		return nil
	}
	var d []Delivery
	if s.roomState == 0 && now.Sub(s.created) >= s.owner.manager.config.MatchingLimit {
		s.closed = true
		return nil
	}
	for _, u := range append([]*member(nil), s.members...) {
		if !u.disconnected.IsZero() && now.Sub(u.disconnected) > s.reconnectWindow() && u.clientState != 4 {
			if s.roomState == 0 {
				d = append(d, s.leaveMatching(u)...)
				continue
			}
			u.clientState = 4
			if s.selection.Kind == Fishing && u.principal.OwnerIndex == s.host {
				d = append(d, s.finish(false, now)...)
			}
		}
	}
	loadingLimit := s.selection.LoadingLimit
	if loadingLimit <= 0 {
		loadingLimit = s.owner.manager.config.LoadingLimit
	}
	if s.roomState == 1 && !s.loadingSince.IsZero() && now.Sub(s.loadingSince) > loadingLimit {
		d = append(d, s.finish(false, now)...)
	}
	duration := s.selection.Duration
	if duration <= 0 {
		duration = s.owner.manager.config.RoomDuration
	}
	if s.roomState == 2 && now.Sub(s.start) >= duration {
		d = append(d, s.finish(false, now)...)
	}
	if s.roomState == 2 {
		allGone := true
		for _, u := range s.members {
			if u.clientState == 2 || u.clientState == 3 && u.gameConnection != "" {
				allGone = false
			}
		}
		if allGone {
			d = append(d, s.finish(false, now)...)
		}
	}
	if s.roomState == 3 {
		s.publishResult()
	}
	if s.roomState == 3 && (s.owner.manager.config.PublishResult == nil || s.resultPersisted) && now.Sub(s.end) >= s.owner.manager.config.ResultRetention {
		s.closed = true
	}
	return d
}

func (s *state) reconnectWindow() time.Duration {
	if s.selection.ReconnectWindow > 0 {
		return s.selection.ReconnectWindow
	}
	return s.owner.manager.config.ReconnectWindow
}
func (m *Manager) checkConnection(c Connection) error {
	if !validPrincipal(c.Principal) || c.ID == "" {
		return ErrUnauthorized
	}
	if c.Channel != Matching && c.Channel != Action && c.Channel != Defense && c.Channel != Fishing && c.Channel != Square {
		return ErrUnauthorized
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return nil
}
func (s *state) endpoint(b []byte, hostField, tcpField, udpField int) []byte {
	c := s.owner.manager.config
	b = text(b, hostField, c.AdvertisedHost)
	b = text(b, tcpField, strconv.Itoa(c.TCPPort))
	if udpField > 0 {
		b = text(b, udpField, strconv.Itoa(c.UDPPort))
	}
	return b
}
func (s *state) require(c Connection, guid string) (*member, error) {
	u := s.find(c.Principal)
	if u == nil || u.gameConnection != c.ID || guid != u.guid {
		return nil, ErrUnauthorized
	}
	return u, nil
}
func (s *state) sequenceKey(owner int64, typ int64) string {
	return fmt.Sprintf("%d:%d:%d", s.hostEpoch, owner, typ)
}
