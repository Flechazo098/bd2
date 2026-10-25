package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	party "bd2server/internal/server/domain/roster/formation"
	"bd2server/internal/server/protocol/wire"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

type Rules struct {
	Design  *gamedata.PvpDesign
	Seasons []Season
	Policy  ScorePolicy
}

func NewRules(d *gamedata.PvpDesign, seasons []Season, policy ScorePolicy) (*Rules, error) {
	if d == nil || d.Presets == nil || policy == nil || len(seasons) == 0 {
		return nil, fmt.Errorf("pvp: incomplete rules")
	}
	seen := map[string]bool{}
	for _, s := range seasons {
		key := fmt.Sprintf("%s:%d", s.Mode, s.ID)
		if s.ID == 0 || s.End <= s.Start || s.CalculateEnd < s.End || seen[key] {
			return nil, fmt.Errorf("pvp: invalid season %s", key)
		}
		seen[key] = true
		switch s.Mode {
		case Mirror:
			if _, err := d.MirrorRank(s.ID, d.Mirror.StartVP, 0); err != nil {
				return nil, err
			}
		case Colosseum:
			for _, kind := range []uint64{1, 2, 4, 9, 11, 13, 14, 15, 26, 30, 31} {
				values, err := d.ColosseumRule(s.ID, kind)
				if err != nil || len(values) == 0 && kind != 30 && kind != 31 {
					return nil, fmt.Errorf("pvp: incomplete colosseum season %d rule %d", s.ID, kind)
				}
			}
		default:
			return nil, fmt.Errorf("pvp: unsupported season mode")
		}
	}
	for i, a := range seasons {
		for _, b := range seasons[i+1:] {
			if a.Mode == b.Mode && a.Start < b.End && b.Start < a.End {
				return nil, fmt.Errorf("pvp: overlapping seasons")
			}
		}
	}
	return &Rules{Design: d, Seasons: append([]Season(nil), seasons...), Policy: policy}, nil
}

type formation struct {
	Version  uint64
	Deck     [][]byte
	Snapshot FullSnapshot
	// Power is the client's official deck battle power sent with the save.
	Power uint64
}
type progress struct {
	AttackBaseWins, AttackBaseLosses, DefenseBaseWins, DefenseBaseLosses                                                                       uint64
	VP, Attacks, Wins, Losses, DefenseWins, DefenseLosses, AttackReset, DefenseReset, DeckWins, DeckLosses, DeckDefenseWins, DeckDefenseLosses uint64
}
type challenge struct {
	Seeds                                                        []uint64
	ID                                                           string
	BattleIndex, Season, Seed, FirstAttack, Multiplier, IssuedAt uint64
	Attacker                                                     PublishedSnapshot
	Candidates                                                   []PublishedSnapshot
	Selected                                                     uint64
	Started                                                      bool
	MatchReply                                                   []byte
}
type modeState struct {
	TicketPeriod              string
	TicketReset               int64
	Attack, Defense           formation
	AttackBless, DefenseBless []uint64
	Progress                  map[string]progress
	Active                    *challenge
	Recent                    []string
	BuyDay, BuyCount          uint64
	Pending                   map[string]pendingResult
	ResultOrder               uint64
}
type pendingResult struct {
	Season, BattleIndex, Order uint64
	Delta                      int64
	Result                     uint64
}
type state struct {
	Version     int
	Modes       map[Mode]*modeState
	PresetSlots uint64
}
type Service struct {
	preparation func(command.Context, Mode, string, PublishedSnapshot, []byte) error
	completion  func(command.Context, Mode, string, bool, [][]byte) ([]byte, error)
	balance     BalanceFunc
	readProfile ProfileFunc
	rules       *Rules
	profile     Profile
	source      Source
	builder     SnapshotBuilder
	spend       SpendFunc
	grant       GrantFunc
	state       state
	now         func() time.Time
}

func Open(ctx command.Context, rules *Rules, profile Profile, source Source, builder SnapshotBuilder, spend SpendFunc, grant GrantFunc) (*Service, error) {
	if ctx.State == nil || rules == nil || source == nil || builder == nil || spend == nil || grant == nil || profile.OwnerID == "" || profile.OwnerIndex == 0 || profile.OwnerID != ctx.AccountID {
		return nil, fmt.Errorf("pvp: incomplete player runtime")
	}
	s := &Service{rules: rules, profile: profile, source: source, builder: builder, spend: spend, grant: grant, now: time.Now, state: state{Version: 1, Modes: map[Mode]*modeState{}, PresetSlots: rules.Design.Presets.BaseCount}}
	raw, _, err := ctx.State.LoadEntry("pvp", "state", "core")
	if err != nil {
		return nil, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &s.state); err != nil || s.state.Version != 1 || s.state.Modes == nil {
			return nil, fmt.Errorf("pvp: invalid player state")
		}
	}
	for _, mode := range []Mode{Mirror, Colosseum} {
		if s.state.Modes[mode] == nil {
			s.state.Modes[mode] = &modeState{Progress: map[string]progress{}, Pending: map[string]pendingResult{}}
		}
		if s.state.Modes[mode].Progress == nil || s.state.Modes[mode].Pending == nil {
			return nil, fmt.Errorf("pvp: invalid progress state")
		}
	}
	return s, nil
}
func (s *Service) PresetSlotCount() uint64 { return s.state.PresetSlots }
func (s *Service) current(mode Mode) (Season, error) {
	n := uint64(s.now().UnixMilli())
	var active, begun, future Season
	for _, c := range s.rules.Seasons {
		if c.Mode != mode {
			continue
		}
		if c.Start <= n {
			if begun.ID == 0 || c.Start > begun.Start {
				begun = c
			}
			if n < c.End && (active.ID == 0 || c.Start > active.Start) {
				active = c
			}
		} else if future.ID == 0 || c.Start < future.Start {
			future = c
		}
	}
	if active.ID != 0 {
		return active, nil
	}
	if begun.ID != 0 {
		return begun, nil
	}
	if future.ID != 0 {
		return future, nil
	}
	return Season{}, fmt.Errorf("pvp: mode has no configured season")
}
func (s *Service) activeSeason(mode Mode) (Season, error) {
	c, e := s.current(mode)
	n := uint64(s.now().UnixMilli())
	if e != nil || n < c.Start || n >= c.End {
		return c, fmt.Errorf("pvp: season is not accepting battles")
	}
	return c, nil
}
func (s *Service) progress(mode Mode, season uint64) (progress, error) {
	m := s.state.Modes[mode]
	p := m.Progress[strconv.FormatUint(season, 10)]
	query := ProgressQuery{Mode: mode, Season: season, Owner: s.profile.OwnerID}
	for id, pending := range m.Pending {
		if pending.Season == season {
			query.PendingIDs = append(query.PendingIDs, id)
		}
	}
	shared, e := s.source.Personal(query)
	if e != nil {
		return p, e
	}
	if shared.Found {
		p.VP, p.Attacks, p.Wins, p.Losses, p.DefenseWins, p.DefenseLosses = shared.VP, shared.AttackCount, shared.Wins, shared.Losses, shared.DefenseWins, shared.DefenseLosses
	} else {
		p.Attacks, p.Wins, p.Losses, p.DefenseWins, p.DefenseLosses = 0, 0, 0, 0, 0
		if mode == Mirror {
			p.VP = s.rules.Design.Mirror.StartVP
		} else {
			values, e := s.rules.Design.ColosseumRule(season, 26)
			if e != nil || len(values) != 1 {
				return p, fmt.Errorf("pvp: missing starting VP")
			}
			p.VP = values[0]
		}
	}
	if p.VP > math.MaxInt32 {
		return p, fmt.Errorf("pvp: invalid shared score")
	}
	applied := map[string]bool{}
	for _, id := range shared.AppliedIDs {
		applied[id] = true
	}
	minimum := s.rules.Design.Mirror.MinimumVP
	if mode == Colosseum {
		minimum = s.rules.Design.Colosseum.MinimumVP
	}
	var pendingIDs []string
	for id, pending := range m.Pending {
		if !applied[id] && pending.Season == season {
			pendingIDs = append(pendingIDs, id)
		}
	}
	sort.Slice(pendingIDs, func(i, j int) bool { return m.Pending[pendingIDs[i]].Order < m.Pending[pendingIDs[j]].Order })
	for _, id := range pendingIDs {
		pending := m.Pending[id]
		p.VP = applyDelta(p.VP, pending.Delta, minimum)
		p.Attacks++
		switch pending.Result {
		case 1:
			p.Wins++
		case 2:
			p.Losses++
		}
	}
	p.DeckWins = p.Wins - min(p.Wins, p.AttackBaseWins)
	p.DeckLosses = p.Losses - min(p.Losses, p.AttackBaseLosses)
	p.DeckDefenseWins = p.DefenseWins - min(p.DefenseWins, p.DefenseBaseWins)
	p.DeckDefenseLosses = p.DefenseLosses - min(p.DefenseLosses, p.DefenseBaseLosses)
	return p, nil
}
func (s *Service) reconcile() error {
	for mode, m := range s.state.Modes {
		bySeason := map[uint64][]string{}
		for id, pending := range m.Pending {
			bySeason[pending.Season] = append(bySeason[pending.Season], id)
		}
		for season, ids := range bySeason {
			shared, e := s.source.Personal(ProgressQuery{Mode: mode, Season: season, Owner: s.profile.OwnerID, PendingIDs: ids})
			if e != nil {
				return e
			}
			for _, id := range shared.AppliedIDs {
				delete(m.Pending, id)
			}
		}
	}
	return nil
}
func (s *Service) save(ctx command.Context) error {
	if err := s.reconcile(); err != nil {
		return err
	}
	raw, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return ctx.State.PutEntry("pvp", "state", "core", raw)
}
func publish(ctx command.Context, id, topic string, payload any) error {
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	return ctx.Publish(command.Event{ID: id, Topic: topic, Payload: raw})
}
func randomID() (uint64, error) {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return 0, e
	}
	return binary.LittleEndian.Uint64(b[:])&math.MaxInt64 | 1, nil
}
func cloneSnapshot(p PublishedSnapshot) PublishedSnapshot {
	p.Snapshot.FullDeck = append([]byte(nil), p.Snapshot.FullDeck...)
	p.Snapshot.Contents = cloneMessages(p.Snapshot.Contents)
	p.Profile.Guild = append([]byte(nil), p.Profile.Guild...)
	return p
}
func cloneMessages(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = append([]byte(nil), in[i]...)
	}
	return out
}
func (s *Service) rank(mode Mode, season uint64) (RankEntry, error) {
	rows, e := s.source.Ranking(RankQuery{Mode: mode, Season: season, Owner: s.profile.OwnerID, Limit: 1})
	if e != nil {
		return RankEntry{}, e
	}
	for _, r := range rows {
		if r.Profile.OwnerID == s.profile.OwnerID {
			return r, nil
		}
	}
	return RankEntry{}, nil
}
func (s *Service) own(mode Mode, season uint64, f formation, p progress) (PublishedSnapshot, error) {
	rank, e := s.rank(mode, season)
	if e != nil {
		return PublishedSnapshot{}, e
	}
	return PublishedSnapshot{Profile: s.profile, Mode: mode, Season: season, Version: f.Version, VP: p.VP, Rank: rank.Rank, TopPercent: rank.TopPercent, Power: f.Power, Snapshot: f.Snapshot}, nil
}

// saveFormation keeps the previous battle power when power is nil.
func (s *Service) saveFormation(ctx command.Context, mode Mode, side uint64, deck, contents [][]byte, renew bool, power *uint64) error {
	if side > 1 {
		return fmt.Errorf("pvp: invalid deck type")
	}
	season, e := s.activeSeason(mode)
	if e != nil {
		return e
	}
	m := s.state.Modes[mode]
	old := m.Attack
	if side == 1 {
		old = m.Defense
	}
	if renew {
		deck = old.Deck
	}
	if e = s.validateDeck(mode, season.ID, deck); e != nil {
		return e
	}
	frozen, e := s.builder.Freeze(ctx, FreezeRequest{Mode: mode, Season: season.ID, Deck: deck, Contents: contents, Renew: renew, Previous: old.Snapshot, AttackBless: m.AttackBless, DefenseBless: m.DefenseBless})
	if e != nil {
		return e
	}
	if e = validateFullDeck(deck, frozen.FullDeck); e != nil {
		return e
	}
	next := formation{Version: old.Version + 1, Deck: cloneMessages(deck), Snapshot: FullSnapshot{FullDeck: append([]byte(nil), frozen.FullDeck...), Contents: cloneMessages(frozen.Contents)}, Power: old.Power}
	if power != nil {
		next.Power = *power
	}
	if side == 1 {
		m.Defense = next
	} else {
		m.Attack = next
	}
	if mode == Colosseum {
		m.Attack = next
		m.Defense = next
	}
	if side == 1 || mode == Colosseum {
		p, e := s.progress(mode, season.ID)
		if e != nil {
			return e
		}
		own, e := s.own(mode, season.ID, next, p)
		if e != nil {
			return e
		}
		if e = publish(ctx, fmt.Sprintf("pvp:%s:%s:%d:defense:%d", mode, s.profile.OwnerID, season.ID, next.Version), "pvp.defense.published", DefensePublished{Version: 1, Defense: own}); e != nil {
			return e
		}
	}
	if mode == Mirror {
		progress, e := s.progress(mode, season.ID)
		if e != nil {
			return e
		}
		own, e := s.own(mode, season.ID, next, progress)
		if e != nil {
			return e
		}
		if e = publish(ctx, fmt.Sprintf("pvp:%s:%d:popularity:%d:%d", s.profile.OwnerID, season.ID, side, next.Version), "pvp.popularity", PopularityPublished{Timestamp: uint64(s.now().UnixMilli()), Side: side, Snapshot: own}); e != nil {
			return e
		}
	}
	return s.save(ctx)
}
func (s *Service) validateDeck(mode Mode, season uint64, deck [][]byte) error {
	maximum, grids := uint64(party.StandardCapacity), uint64(party.StandardCells)
	if mode == Colosseum {
		size, e := s.rules.Design.ColosseumRule(season, 1)
		if e != nil || len(size) != 2 || size[0] == 0 || size[1] == 0 || size[0] > 32 || size[1] > 32 {
			return fmt.Errorf("pvp: invalid colosseum grid")
		}
		count, e := s.rules.Design.ColosseumRule(season, 2)
		if e != nil || len(count) != 1 {
			return fmt.Errorf("pvp: invalid colosseum team limit")
		}
		maximum, grids = count[0], size[0]*size[1]
	}
	members := make([]party.Member, 0, len(deck))
	for _, entry := range deck {
		member, err := party.DecodeDeck(entry)
		costume, costumeErr := scalar(entry, 4)
		if err != nil || costumeErr != nil || costume == 0 || costume > math.MaxInt64 {
			return fmt.Errorf("pvp: invalid formation entry")
		}
		members = append(members, member)
	}
	return (party.Rules{Capacity: maximum, Cells: grids}).Validate(members)
}
func validateFullDeck(deck [][]byte, raw []byte) error {
	resolved, e := messages(raw, 1)
	if e != nil || len(resolved) != len(deck) {
		return fmt.Errorf("pvp: invalid frozen deck")
	}
	for i := range deck {
		a, _ := scalar(deck[i], 1)
		b, _ := scalar(resolved[i], 1)
		if a != b {
			return fmt.Errorf("pvp: frozen deck changed character identity")
		}
	}
	return nil
}
func scalar(raw []byte, field int) (uint64, error) { v, _, e := wire.Varint(raw, field); return v, e }
func messages(raw []byte, field int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == field {
			if f.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
			}
			out = append(out, append([]byte(nil), f.Value...))
		}
		return nil
	})
	return out, e
}
func ints(raw []byte, field int) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number != field {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("%w: field %d has wire type %d, want packed varints", wire.ErrMalformed, f.Number, f.Type)
		}
		for b := f.Value; len(b) > 0; {
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return fmt.Errorf("%w: field %d packed varint is truncated", wire.ErrMalformed, f.Number)
			}
			out = append(out, v)
			b = b[n:]
		}
		return nil
	})
	return out, e
}
func addMessages(raw []byte, field int, values [][]byte) []byte {
	for _, v := range values {
		raw = wire.AppendBytes(raw, field, v)
	}
	return raw
}
func requestKey(ctx command.Context, path string, seq uint64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)))
	return hex.EncodeToString(h[:])
}

func (s *Service) AttachPlayerViews(balance BalanceFunc, profile ProfileFunc) error {
	if balance == nil || profile == nil {
		return fmt.Errorf("pvp: missing live player view")
	}
	s.balance, s.readProfile = balance, profile
	return nil
}
func (s *Service) refreshProfile(ctx command.Context) error {
	if s.readProfile == nil {
		return fmt.Errorf("pvp: missing profile source")
	}
	profile, err := s.readProfile(ctx)
	if err != nil {
		return err
	}
	if profile.OwnerID != s.profile.OwnerID || profile.OwnerIndex != s.profile.OwnerIndex {
		return fmt.Errorf("pvp: profile identity changed")
	}
	s.profile = profile
	return nil
}
