package ranking

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle/pvp"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/matchmaking"
	"bd2server/internal/server/protocol/wire"
)

type pvpStanding struct {
	Published bool
	Defense   pvp.PublishedSnapshot
	Progress  pvp.PersonalProgress
	Applied   map[string]bool
	History   []pvp.BattleSettled
}
type pvpBoard struct {
	Members           map[string]pvpStanding
	Order, Candidates []string
	Owners            map[uint64]string
	Frozen, Closing   bool
	Required, Closed  map[string]bool
	Entitlements      map[string][]pvp.SeasonEntitlement
	Claimed           map[string]bool
}
type pvpSeasonState struct {
	Frozen, Closing  bool
	Required, Closed map[string]bool
	Entitlements     map[string][]pvp.SeasonEntitlement
}
type PVP struct {
	store   Store
	design  *gamedata.PvpDesign
	seasons []pvp.Season
	mu      sync.RWMutex
	writer  sync.Mutex
	boards  map[string]*pvpBoard
	rater   matchmaking.System
	ratings map[pvp.Mode]map[string]matchmaking.Rating
	models  map[pvp.Mode]matchmaking.Model
}

func pvpBoardKey(mode pvp.Mode, season uint64) string { return fmt.Sprintf("%s:%020d", mode, season) }
func pvpMemberKey(owner string) string                { return base64.RawURLEncoding.EncodeToString([]byte(owner)) }
func emptyPVPBoard() *pvpBoard {
	return &pvpBoard{Members: map[string]pvpStanding{}, Owners: map[uint64]string{}, Required: map[string]bool{}, Closed: map[string]bool{}, Entitlements: map[string][]pvp.SeasonEntitlement{}, Claimed: map[string]bool{}}
}
func (p *PVP) board(mode pvp.Mode, season uint64) *pvpBoard {
	if b := p.boards[pvpBoardKey(mode, season)]; b != nil {
		return b
	}
	return emptyPVPBoard()
}
func (p *PVP) configured(mode pvp.Mode, id uint64) (pvp.Season, error) {
	for _, s := range p.seasons {
		if s.Mode == mode && s.ID == id {
			return s, nil
		}
	}
	return pvp.Season{}, errors.New("unconfigured PVP season")
}
func NewPVP(ctx context.Context, store Store, design *gamedata.PvpDesign, seasons []pvp.Season) (*PVP, error) {
	if store == nil || design == nil || len(seasons) == 0 {
		return nil, errors.New("shared PVP requires persistence, design and seasons")
	}
	rater, err := newPVPRater()
	if err != nil {
		return nil, err
	}
	p := &PVP{store: store, design: design, seasons: slices.Clone(seasons), boards: map[string]*pvpBoard{}, rater: rater, ratings: map[pvp.Mode]map[string]matchmaking.Rating{}, models: map[pvp.Mode]matchmaking.Model{}}
	for _, s := range seasons {
		p.boards[pvpBoardKey(s.Mode, s.ID)] = emptyPVPBoard()
	}
	err = store.View(ctx, func(tx Tx) error {
		if err := p.loadRatings(tx); err != nil {
			return err
		}
		for cursor := ""; ; {
			page, err := tx.Scan("pvp.members", "", cursor, 256)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			for _, entry := range page {
				var row pvpStanding
				if err := json.Unmarshal(entry.Value, &row); err != nil {
					return err
				}
				key := pvpBoardKey(row.Defense.Mode, row.Defense.Season)
				b := p.boards[key]
				if b == nil && (row.Defense.Mode == pvp.Mirror || row.Defense.Mode == pvp.Colosseum) && row.Defense.Season != 0 {
					b = emptyPVPBoard()
					p.boards[key] = b
				}
				if b == nil || row.Defense.Profile.OwnerID == "" {
					return errors.New("invalid saved PVP standing")
				}
				b.Members[row.Defense.Profile.OwnerID] = row
				cursor = entry.Key
			}
		}
		for key, b := range p.boards {
			raw, exists, err := tx.Get("pvp.seasons", key)
			if err != nil {
				return err
			}
			if exists {
				var state pvpSeasonState
				if err = json.Unmarshal(raw, &state); err != nil {
					return err
				}
				b.Frozen, b.Closing = state.Frozen, state.Closing
				b.Required, b.Closed, b.Entitlements = state.Required, state.Closed, state.Entitlements
				if b.Required == nil || b.Closed == nil || b.Entitlements == nil {
					return errors.New("invalid PVP season barrier")
				}
			}
			for cursor := ""; ; {
				page, err := tx.Scan("pvp.claimed", key+":", cursor, 256)
				if err != nil {
					return err
				}
				if len(page) == 0 {
					break
				}
				for _, row := range page {
					b.Claimed[string(row.Value)] = true
					cursor = row.Key
				}
			}
			p.reindex(b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}
func (p *PVP) initial(mode pvp.Mode, season uint64) (uint64, uint64, error) {
	if mode == pvp.Mirror {
		return p.design.Mirror.StartVP, p.design.Mirror.MinimumVP, nil
	}
	if mode != pvp.Colosseum {
		return 0, 0, errors.New("unknown PVP mode")
	}
	values, err := p.design.ColosseumRule(season, 26)
	if err != nil || len(values) != 1 {
		return 0, 0, errors.New("missing arena starting score")
	}
	return values[0], p.design.Colosseum.MinimumVP, nil
}
func applyPVPDelta(score uint64, delta int64, minimum uint64) (uint64, error) {
	if score > math.MaxInt32 || minimum > math.MaxInt32 || delta > math.MaxInt32 || delta < -math.MaxInt32 {
		return 0, errors.New("PVP score overflow")
	}
	next := max(int64(score)+delta, int64(minimum))
	if next > math.MaxInt32 {
		return 0, errors.New("PVP score overflow")
	}
	return uint64(next), nil
}
func (p *PVP) score(row pvpStanding) uint64 {
	if row.Progress.Found {
		return row.Progress.VP
	}
	start, _, _ := p.initial(row.Defense.Mode, row.Defense.Season)
	return start
}
func (p *PVP) less(b *pvpBoard, a, c string) bool {
	ar, cr := b.Members[a], b.Members[c]
	av, cv := p.score(ar), p.score(cr)
	if av != cv {
		return av > cv
	}
	return ar.Defense.Profile.OwnerIndex < cr.Defense.Profile.OwnerIndex
}
func (p *PVP) reindex(b *pvpBoard) {
	b.Order = nil
	b.Candidates = nil
	b.Owners = map[uint64]string{}
	for owner, row := range b.Members {
		b.Owners[row.Defense.Profile.OwnerIndex] = owner
		if row.Progress.Found {
			b.Order = append(b.Order, owner)
		}
		if row.Published && len(row.Defense.Snapshot.FullDeck) > 0 {
			b.Candidates = append(b.Candidates, owner)
		}
	}
	sort.Slice(b.Order, func(i, j int) bool { return p.less(b, b.Order[i], b.Order[j]) })
	sort.Slice(b.Candidates, func(i, j int) bool { return p.less(b, b.Candidates[i], b.Candidates[j]) })
}
func (p *PVP) rank(b *pvpBoard, owner string) uint64 {
	if !b.Members[owner].Progress.Found {
		return 0
	}
	at := sort.Search(len(b.Order), func(i int) bool { return !p.less(b, b.Order[i], owner) })
	if at < len(b.Order) && b.Order[at] == owner {
		return uint64(at + 1)
	}
	return 0
}
func (p *PVP) updateRow(b *pvpBoard, owner string, row pvpStanding) {
	old, exists := b.Members[owner]
	remove := func(list []string) []string {
		if !exists {
			return list
		}
		at := sort.Search(len(list), func(i int) bool { return !p.less(b, list[i], owner) })
		if at < len(list) && list[at] == owner {
			return slices.Delete(list, at, at+1)
		}
		return list
	}
	b.Order = remove(b.Order)
	b.Candidates = remove(b.Candidates)
	if exists && old.Defense.Profile.OwnerIndex != row.Defense.Profile.OwnerIndex {
		delete(b.Owners, old.Defense.Profile.OwnerIndex)
	}
	b.Members[owner] = row
	b.Owners[row.Defense.Profile.OwnerIndex] = owner
	insert := func(list []string) []string {
		at := sort.Search(len(list), func(i int) bool { return !p.less(b, list[i], owner) })
		return slices.Insert(list, at, owner)
	}
	if row.Progress.Found {
		b.Order = insert(b.Order)
	}
	if row.Published && len(row.Defense.Snapshot.FullDeck) > 0 {
		b.Candidates = insert(b.Candidates)
	}
}
func savePVPStanding(tx Tx, key string, row pvpStanding) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return tx.Put("pvp.members", key+":"+pvpMemberKey(row.Defense.Profile.OwnerID), raw)
}
func seasonState(b *pvpBoard) pvpSeasonState {
	return pvpSeasonState{Frozen: b.Frozen, Closing: b.Closing, Required: b.Required, Closed: b.Closed, Entitlements: b.Entitlements}
}
func savePVPSeason(tx Tx, key string, state pvpSeasonState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return tx.Put("pvp.seasons", key, raw)
}
func (p *PVP) Consume(ctx context.Context, source string, event command.Event) error {
	if source == "" || event.ID == "" || event.Topic == "" || !json.Valid(event.Payload) {
		return errors.New("invalid PVP event")
	}
	envelope := struct {
		Source string
		Event  command.Event
	}{source, event}
	identity, err := json.Marshal([3]string{source, event.Topic, event.ID})
	if err != nil {
		return err
	}
	eventKey := sha256.Sum256(identity)
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	eventDigest := sha256.Sum256(raw)

	p.writer.Lock()
	defer p.writer.Unlock()
	var commit func()
	applyEvent := func(tx Tx) error {
		switch envelope.Event.Topic {
		case "pvp.defense.published":
			var update pvp.DefensePublished
			if err := json.Unmarshal(envelope.Event.Payload, &update); err != nil {
				return err
			}
			s := update.Defense
			if update.Version != 1 || s.Profile.OwnerID != envelope.Source || s.Profile.OwnerIndex == 0 || s.Version == 0 {
				return errors.New("invalid published defense")
			}
			if _, err := p.configured(s.Mode, s.Season); err != nil {
				return err
			}
			key := pvpBoardKey(s.Mode, s.Season)
			b := p.boards[key]
			if b.Frozen || b.Closed[envelope.Source] {
				return nil
			}
			row := b.Members[envelope.Source]
			if row.Published && row.Defense.Version >= s.Version {
				return nil
			}
			if owner := b.Owners[s.Profile.OwnerIndex]; owner != "" && owner != envelope.Source {
				return errors.New("PVP owner index collision")
			}
			row.Defense = s
			_, hasDeck, decodeErr := wire.Bytes(s.Snapshot.FullDeck, 1)
			if decodeErr != nil {
				return decodeErr
			}
			row.Published = hasDeck
			if err := savePVPStanding(tx, key, row); err != nil {
				return err
			}
			commit = func() { p.updateRow(b, envelope.Source, row) }
			return nil
		case "pvp.battle.settled":
			var battle pvp.BattleSettled
			if err := json.Unmarshal(envelope.Event.Payload, &battle); err != nil {
				return err
			}
			if battle.Version != 1 || battle.ChallengeID == "" || envelope.Event.ID != "pvp:result:"+battle.ChallengeID || battle.BattleIndex == 0 || battle.Attacker.Profile.OwnerID != envelope.Source || battle.Defender.Profile.OwnerID == envelope.Source || battle.Defender.Profile.OwnerID == "" || battle.Result < 1 || battle.Result > 3 {
				return errors.New("invalid shared battle result")
			}
			season, err := p.configured(battle.Mode, battle.Season)
			if err != nil {
				return err
			}
			if battle.Timestamp < season.Start || battle.Timestamp >= season.End {
				return errors.New("battle result outside acceptance deadline")
			}
			key := pvpBoardKey(battle.Mode, battle.Season)
			b := p.boards[key]
			digest := sha256.Sum256(envelope.Event.Payload)
			receipt := hex.EncodeToString(digest[:])
			battleKey := key + ":" + pvpMemberKey(battle.ChallengeID)
			if prior, found, err := tx.Get("pvp.battles", battleKey); err != nil {
				return err
			} else if found {
				if string(prior) != receipt {
					return errors.New("conflicting settled challenge")
				}
				return nil
			}
			if b.Frozen || b.Closed[envelope.Source] {
				return errors.New("result follows source season closure")
			}
			start, minimum, err := p.initial(battle.Mode, battle.Season)
			if err != nil {
				return err
			}
			updates := map[string]pvpStanding{}
			for index, snapshot := range []pvp.PublishedSnapshot{battle.Attacker, battle.Defender} {
				owner := snapshot.Profile.OwnerID
				if snapshot.Mode != battle.Mode || snapshot.Season != battle.Season || snapshot.Profile.OwnerIndex == 0 {
					return errors.New("battle snapshot season mismatch")
				}
				if indexed := b.Owners[snapshot.Profile.OwnerIndex]; indexed != "" && indexed != owner {
					return errors.New("PVP owner index collision")
				}
				row := b.Members[owner]
				if row.Defense.Profile.OwnerID == "" {
					row.Defense = snapshot
				}
				if !row.Progress.Found {
					row.Progress = pvp.PersonalProgress{Found: true, VP: start}
				}
				delta := battle.AttackerChange
				if index == 1 {
					delta = battle.DefenderChange
				}
				row.Progress.VP, err = applyPVPDelta(row.Progress.VP, delta, minimum)
				if err != nil {
					return err
				}
				if index == 0 {
					row.Progress.AttackCount++
					switch battle.Result {
					case 1:
						row.Progress.Wins++
					case 2:
						row.Progress.Losses++
					}
				} else {
					switch battle.Result {
					case 1:
						row.Progress.DefenseLosses++
					case 2:
						row.Progress.DefenseWins++
					}
				}
				if index == 0 {
					row.Applied = make(map[string]bool, len(row.Applied)+1)
					maps.Copy(row.Applied, b.Members[owner].Applied)
					row.Applied[envelope.Event.ID] = true
				}
				row.History = append(slices.Clone(row.History), battle)
				limit := p.design.Mirror.HistoryLimit
				if battle.Mode == pvp.Colosseum {
					limit = p.design.Colosseum.HistoryLimit
				}
				limit = min(max(limit*2, 1), 1000)
				if uint64(len(row.History)) > limit {
					row.History = row.History[len(row.History)-int(limit):]
				}
				if err := savePVPStanding(tx, key, row); err != nil {
					return err
				}
				updates[owner] = row
			}
			rated, err := p.settleRatings(tx, battle)
			if err != nil {
				return err
			}
			if err := tx.Put("pvp.battles", battleKey, []byte(receipt)); err != nil {
				return err
			}
			commit = func() {
				for owner, row := range updates {
					p.updateRow(b, owner, row)
				}
				rated()
			}
			return nil
		case "pvp.season.closed":
			var closed pvp.SeasonClosed
			if err := json.Unmarshal(envelope.Event.Payload, &closed); err != nil {
				return err
			}
			if closed.Version != 1 || closed.OwnerID != envelope.Source {
				return errors.New("invalid PVP source closure")
			}
			if _, err := p.configured(closed.Mode, closed.Season); err != nil {
				return err
			}
			key := pvpBoardKey(closed.Mode, closed.Season)
			b := p.boards[key]
			if b.Closed[closed.OwnerID] {
				return nil
			}
			state := seasonState(b)
			state.Closed = map[string]bool{}
			maps.Copy(state.Closed, b.Closed)
			state.Closed[closed.OwnerID] = true
			if err := savePVPSeason(tx, key, state); err != nil {
				return err
			}
			commit = func() { b.Closed = state.Closed }
			return nil
		case "pvp.season.claimed":
			var claim pvp.SeasonClaimed
			if err := json.Unmarshal(envelope.Event.Payload, &claim); err != nil {
				return err
			}
			if claim.Version != 1 || claim.OwnerID != envelope.Source || claim.EntitlementID == "" {
				return errors.New("invalid season claim")
			}
			b := p.board(claim.Mode, claim.Season)
			found := false
			for _, ent := range b.Entitlements[claim.OwnerID] {
				found = found || ent.ID == claim.EntitlementID
			}
			if !b.Frozen || !found {
				return errors.New("unissued season entitlement")
			}
			if err := tx.Put("pvp.claimed", pvpBoardKey(claim.Mode, claim.Season)+":"+pvpMemberKey(claim.EntitlementID), []byte(claim.EntitlementID)); err != nil {
				return err
			}
			commit = func() { b.Claimed[claim.EntitlementID] = true }
			return nil
		default:
			return errors.New("unknown PVP shared event")
		}
	}
	err = p.store.Update(ctx, func(tx Tx) error {
		key := hex.EncodeToString(eventKey[:])
		prior, found, e := tx.Get("pvp.inbox", key)
		if e != nil {
			return e
		}
		if found {
			if string(prior) != string(eventDigest[:]) {
				return errors.New("conflicting PVP event identity")
			}
			return nil
		}
		if e = applyEvent(tx); e != nil {
			return e
		}
		return tx.Put("pvp.inbox", key, eventDigest[:])
	})
	if err == nil && commit != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		commit()
	}
	return err
}

// BeginClosing records the complete account-directory snapshot taken after End.
// Closure events from every account are required even when their enrollment
// event is still waiting in an outbox.
func (p *PVP) BeginClosing(ctx context.Context, season pvp.Season, owners []string) error {
	p.writer.Lock()
	defer p.writer.Unlock()
	configured, err := p.configured(season.Mode, season.ID)
	if err != nil || configured != season {
		return errors.New("invalid closing season")
	}
	if uint64(time.Now().UnixMilli()) < season.End {
		return errors.New("cannot close a live season")
	}
	b := p.boards[pvpBoardKey(season.Mode, season.ID)]
	if b.Closing || b.Frozen {
		return nil
	}
	state := seasonState(b)
	state.Closing = true
	state.Required = map[string]bool{}
	for _, owner := range owners {
		if owner == "" {
			return errors.New("empty season participant")
		}
		state.Required[owner] = true
	}
	if err := p.store.Update(ctx, func(tx Tx) error { return savePVPSeason(tx, pvpBoardKey(season.Mode, season.ID), state) }); err != nil {
		return err
	}
	p.mu.Lock()
	b.Closing, b.Required = true, state.Required
	p.mu.Unlock()
	return nil
}

type Closure struct {
	Owner  string
	Mode   pvp.Mode
	Season uint64
}

func (p *PVP) RequiredClosures(limit int) []Closure {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if limit < 1 {
		return nil
	}
	limit = min(limit, 256)
	var out []Closure
	for _, season := range p.seasons {
		b := p.boards[pvpBoardKey(season.Mode, season.ID)]
		if !b.Closing || b.Frozen {
			continue
		}
		owners := make([]string, 0, len(b.Required))
		for owner := range b.Required {
			if !b.Closed[owner] {
				owners = append(owners, owner)
			}
		}
		sort.Strings(owners)
		for _, owner := range owners {
			out = append(out, Closure{owner, season.Mode, season.ID})
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}
func (p *PVP) FreezeDue(ctx context.Context, now time.Time) error {
	p.writer.Lock()
	defer p.writer.Unlock()
	for _, season := range p.seasons {
		b := p.boards[pvpBoardKey(season.Mode, season.ID)]
		if b.Frozen || !b.Closing || uint64(now.UnixMilli()) < season.CalculateEnd {
			continue
		}
		ready := true
		for owner := range b.Required {
			if !b.Closed[owner] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		state := seasonState(b)
		state.Frozen = true
		state.Entitlements = map[string][]pvp.SeasonEntitlement{}
		for _, owner := range b.Order {
			row := b.Members[owner]
			if row.Progress.AttackCount == 0 {
				continue
			}
			rank := p.rank(b, owner)
			percent := float64(rank) * 100 / float64(len(b.Order))
			var rewards []gamedata.BattleReward
			if season.Mode == pvp.Mirror {
				r, err := p.design.MirrorRank(season.ID, row.Progress.VP, rank)
				if err != nil {
					return err
				}
				rewards = slices.Clone(r.SeasonRewards)
			} else {
				r, err := p.design.ColosseumRank(season.ID, percent)
				if err != nil {
					return err
				}
				rewards = slices.Clone(r.Rewards)
			}
			ent := pvp.SeasonEntitlement{ID: fmt.Sprintf("%s:%d:%s", season.Mode, season.ID, owner), Mode: season.Mode, Season: season.ID, Rank: rank, VP: row.Progress.VP, AttackCount: row.Progress.AttackCount, TopPercent: percent, Rewards: rewards}
			state.Entitlements[owner] = []pvp.SeasonEntitlement{ent}
		}
		if err := p.store.Update(ctx, func(tx Tx) error { return savePVPSeason(tx, pvpBoardKey(season.Mode, season.ID), state) }); err != nil {
			return err
		}
		p.mu.Lock()
		b.Frozen, b.Entitlements = true, state.Entitlements
		p.mu.Unlock()
	}
	return nil
}
