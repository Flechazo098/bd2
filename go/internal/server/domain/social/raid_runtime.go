package social

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

const RaidBattleMode uint64 = 23
const RaidPracticeMode uint64 = 26

type RaidDay struct{ Level, Score, SupporterUses uint64 }
type RaidPersonal struct {
	HighestLevel, Score, Overkill, RewardedLevel uint64
	Days                                         map[string]RaidDay
}
type RaidTicket struct {
	EntryID, Digest, AccountID, Day string
	Season                          Season
	Epoch, Level, Mode, DeckID      uint64
	Supporters                      []SupporterSlot
	Deck, Settings                  [][]byte
}
type RaidState struct {
	Seasons                       map[string]RaidPersonal
	Deck, SupporterDeck, Settings [][]byte
	Active                        *RaidTicket
	PresetSlots                   uint64
	Presets                       map[string][]byte
}

func LoadRaidState(store stateio.AtomicEntryStore) (RaidState, error) {
	var out RaidState
	if store == nil {
		return out, fmt.Errorf("%w: raid state has no store", ErrInvalid)
	}
	raw, found, e := store.LoadEntry("social", "raid", "core")
	if e != nil {
		return out, e
	}
	if found {
		if e = json.Unmarshal(raw, &out); e != nil {
			return out, e
		}
	}
	if out.Seasons == nil {
		out.Seasons = map[string]RaidPersonal{}
	}
	if out.Presets == nil {
		out.Presets = map[string][]byte{}
	}
	return out, nil
}
func saveRaidState(ctx command.Context, state RaidState) error {
	raw, e := json.Marshal(state)
	if e != nil {
		return e
	}
	return ctx.State.PutEntry("social", "raid", "core", raw)
}
func personal(state RaidState, season uint64) RaidPersonal {
	v := state.Seasons[strconv.FormatUint(season, 10)]
	if v.Days == nil {
		v.Days = map[string]RaidDay{}
	}
	return v
}
func (s *Service) applyLocal(ctx command.Context, plan *Plan) ([]byte, error) {
	if ctx.State == nil {
		return nil, fmt.Errorf("%w: raid plan %q has no state", ErrInvalid, plan.ID)
	}
	if raw, found, e := ctx.State.LoadEntry("social", "raid_replies", plan.ID); e != nil {
		return nil, e
	} else if found {
		var receipt playerReceipt
		if json.Unmarshal(raw, &receipt) != nil || receipt.RequestDigest != plan.RequestDigest {
			return nil, fmt.Errorf("%w: raid plan %q receipt does not match the request", ErrConflict, plan.ID)
		}
		return receipt.Response, nil
	}
	out, e := plan.local(ctx)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(playerReceipt{RequestDigest: plan.RequestDigest, Response: out})
	if e != nil {
		return nil, e
	}
	return out, ctx.State.PutEntry("social", "raid_replies", plan.ID, raw)
}
func (s *Service) raidSeason(at int64) (Season, error) {
	var current Season
	for _, v := range s.config.RaidSeasons {
		if v.StartsMS <= at && (current.ID == 0 || v.StartsMS > current.StartsMS) {
			current = v
		}
	}
	if current.ID == 0 {
		return current, fmt.Errorf("%w: no raid season has started at %d", ErrIneligible, at)
	}
	return current, nil
}
func (p packet) messages(field int) ([][]byte, error) {
	var out [][]byte
	for _, f := range p[field] {
		if f.Type != 2 {
			return nil, fmt.Errorf("%w: field %d has wire type %d, want messages", wire.ErrMalformed, field, f.Type)
		}
		out = append(out, append([]byte(nil), f.Value...))
	}
	return out, nil
}

func (s *Service) prepareRaid(ctx context.Context, plan *Plan, path string, p packet) (*Plan, error) {
	a, e := s.config.Owner.Account(ctx, plan.Identity.AccountID)
	if e != nil {
		return nil, e
	}
	now := s.config.Now()
	if path == "/GuildRaidSeasonReward" {
		if prior, e := s.config.Owner.Reservation(ctx, plan.ID); e == nil {
			if prior.RequestDigest != plan.RequestDigest || prior.ActorAccount != plan.Identity.AccountID || prior.Status == Cancelled {
				return nil, fmt.Errorf("%w: raid reward %q replays a different or cancelled operation", ErrConflict, plan.ID)
			}
			plan.Reservation = &prior
			return plan, nil
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		profile, e := s.config.Profile(ctx, plan.Identity)
		if e != nil {
			return nil, e
		}
		r, e := s.config.Owner.Prepare(ctx, Operation{ID: plan.ID, Actor: profile, Kind: RaidClaim, AtMS: now.UnixMilli(), ExpectedMembershipEpoch: a.Epoch, RequestDigest: plan.RequestDigest})
		if errors.Is(e, ErrIneligible) {
			return plan, nil
		}
		plan.Reservation = &r
		return plan, e
	}
	if s.config.RaidState == nil || len(s.config.RaidSeasons) == 0 {
		return nil, fmt.Errorf("%w: guild raids are not configured", ErrIneligible)
	}
	if e := s.config.Qualify(ctx, plan.Identity, path); e != nil {
		return nil, e
	}
	state, e := s.config.RaidState(ctx, plan.Identity)
	if e != nil {
		return nil, e
	}
	if a.GuildID == 0 {
		return nil, fmt.Errorf("%w: %s needs a guild", ErrIneligible, path)
	}
	g, e := s.config.Owner.Guild(ctx, a.GuildID)
	if e != nil {
		return nil, e
	}
	season, e := s.raidSeason(now.UnixMilli())
	if e != nil {
		return nil, e
	}
	season.GuildID = g.ID
	if now.UnixMilli() < season.EndsMS {
		if e = s.config.Owner.OpenSeason(ctx, season); e != nil {
			return nil, e
		}
	}
	member := personal(state, season.ID)
	board, e := s.config.Owner.Standings(ctx, season.ID)
	if e != nil {
		return nil, e
	}
	standing := GuildStanding{Guild: g, Season: season.ID}
	for _, row := range board {
		if row.Guild.ID == g.ID {
			standing = row
			break
		}
	}

	if path == "/GuildRaidBossQuickBattle" {
		if now.UnixMilli() >= season.EndsMS || member.HighestLevel == 0 {
			return nil, fmt.Errorf("%w: quick battle needs a cleared level before season %d ends at %d (now %d, highest %d)", ErrIneligible, season.ID, season.EndsMS, now.UnixMilli(), member.HighestLevel)
		}
		var r Reservation
		if old, e := s.config.Owner.Reservation(ctx, plan.ID); e == nil {
			if old.RequestDigest != plan.RequestDigest || old.ActorAccount != plan.Identity.AccountID || old.Status == Cancelled {
				return nil, fmt.Errorf("%w: raid quick battle %q replays a different or cancelled operation", ErrConflict, plan.ID)
			}
			r = old
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		} else {
			profile, e := s.config.Profile(ctx, plan.Identity)
			if e != nil {
				return nil, e
			}
			r, e = s.config.Owner.Prepare(ctx, Operation{ID: plan.ID, Actor: profile, Kind: RaidEnter, GuildID: g.ID, ExpectedMembershipEpoch: a.Epoch, AtMS: now.UnixMilli(), Day: s.config.Day(now), Season: season.ID, RequestDigest: plan.RequestDigest})
			if e != nil {
				return nil, e
			}
		}
		plan.Reservation = &r
		plan.local = func(c command.Context) ([]byte, error) {
			out, e := s.raidLocal(c, path, p, plan.ID, g, season, standing)
			if e != nil {
				return nil, e
			}
			result := RaidResult{AtMS: s.config.Now().UnixMilli(), EntryID: r.ID, AccountID: c.AccountID, ResultID: r.ID + ":result", GuildID: g.ID, Season: season.ID, MembershipEpoch: a.Epoch, Level: member.HighestLevel, Score: member.Score, Overkill: member.Overkill, RulesVersion: season.RulesVersion, Digest: r.Digest}
			payload, e := json.Marshal(result)
			if e != nil {
				return nil, e
			}
			plan.events = []command.Event{{ID: result.ResultID, Topic: "social.raid_settle", Payload: payload}}
			return out, nil
		}
		return plan, nil
	}
	if path == "/GuildRaidBossBattleEnter" || path == "/GuildRaidBossPracticeBattleEnter" {
		if s.config.BeginBattle == nil || s.config.BattleRounds == nil {
			return nil, fmt.Errorf("%w: raid battles are not configured", ErrInvalid)
		}
		if prior, e := s.config.Owner.Reservation(ctx, plan.ID); e == nil {
			if prior.RequestDigest != plan.RequestDigest || prior.ActorAccount != plan.Identity.AccountID || prior.Status == Cancelled || prior.Raid == nil {
				return nil, fmt.Errorf("%w: raid entry %q replays a different or cancelled operation", ErrConflict, plan.ID)
			}
			plan.Reservation = &prior
			ticket := *prior.Raid
			ticket.Digest = prior.Digest
			plan.local = s.raidEnterLocal(ticket)
			return plan, nil
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		if state.Active != nil {
			return nil, fmt.Errorf("%w: raid battle %q is still in progress", ErrPending, state.Active.EntryID)
		}
		level, e := p.integer(2)
		if e != nil {
			return nil, e
		}
		boss, ok := s.config.Owner.design.Raid.Bosses[season.BossGroupID][level]
		if !ok {
			return nil, fmt.Errorf("%w: raid level %d is not in boss group %d", ErrInvalid, level, season.BossGroupID)
		}
		mode := RaidBattleMode
		deck, settings, supporterDeck := state.Deck, state.Settings, state.SupporterDeck
		if path == "/GuildRaidBossPracticeBattleEnter" {
			mode = RaidPracticeMode
			deck, e = p.messages(3)
			if e != nil {
				return nil, e
			}
			supporterDeck, e = p.messages(4)
			if e != nil {
				return nil, e
			}
		}
		if len(deck)+len(supporterDeck) == 0 || len(deck)+len(supporterDeck) > 7 {
			return nil, fmt.Errorf("%w: raid deck has %d characters and %d supporters, want 1..7 in total", ErrInvalid, len(deck), len(supporterDeck))
		}
		if e = validateCombinedPositions(deck, supporterDeck); e != nil {
			return nil, e
		}
		supporters, e := resolveSupporters(g, plan.Identity.AccountID, supporterDeck)
		if e != nil {
			return nil, e
		}
		season.TotalScore = standing.Score
		ticket := RaidTicket{EntryID: plan.ID, AccountID: plan.Identity.AccountID, Day: s.config.Day(now), Season: season, Epoch: a.Epoch, Level: level, Mode: mode, DeckID: boss.DeckID, Supporters: supporters, Deck: deck, Settings: settings}
		if mode == RaidBattleMode {
			if now.UnixMilli() >= season.EndsMS {
				return nil, fmt.Errorf("%w: raid season %d ended at %d", ErrSeasonLocked, season.ID, season.EndsMS)
			}
			profile, e := s.config.Profile(ctx, plan.Identity)
			if e != nil {
				return nil, e
			}
			r, e := s.config.Owner.Prepare(ctx, Operation{ID: plan.ID, Actor: profile, Kind: RaidEnter, GuildID: g.ID, ExpectedMembershipEpoch: a.Epoch, AtMS: now.UnixMilli(), Day: ticket.Day, Season: season.ID, UseSupporter: len(supporters) > 0, RequestDigest: plan.RequestDigest, Raid: &ticket})
			if e != nil {
				return nil, e
			}
			plan.Reservation = &r
			ticket.Digest = r.Digest
		}
		plan.local = s.raidEnterLocal(ticket)
		return plan, nil
	}
	if Mutates(path) || path == "/GuildRaidDeckInfo" || path == "/GuildRaidPresetInfo" {
		plan.local = func(commandCtx command.Context) ([]byte, error) {
			return s.raidLocal(commandCtx, path, p, plan.ID, g, season, standing)
		}
		return plan, nil
	}
	plan.Response, e = s.raidRead(ctx, path, p, a, g, season, standing, board, member)
	return plan, e
}
func resolveSupporters(g Guild, actor string, deck [][]byte) ([]SupporterSlot, error) {
	seen := map[string]bool{}
	var out []SupporterSlot
	for _, raw := range deck {
		p, e := parsePacket(raw)
		if e != nil {
			return nil, e
		}
		owner, e := p.integer(1)
		if e != nil {
			return nil, e
		}
		slot, e := p.integer(2)
		if e != nil || slot > 1 {
			return nil, fmt.Errorf("%w: raid supporter slot %d, want 0 or 1: %v", ErrInvalid, slot, e)
		}
		found := false
		for _, v := range g.Supporters {
			if v.OwnerIndex == owner && v.Slot == slot {
				if v.AccountID == actor {
					return nil, fmt.Errorf("%w: %s cannot borrow their own supporter", ErrIneligible, actor)
				}
				key := accountPrefix(v.AccountID) + numberKey(slot)
				if seen[key] {
					return nil, fmt.Errorf("%w: raid supporter %s slot %d is used twice", ErrInvalid, v.AccountID, slot)
				}
				seen[key] = true
				out = append(out, v)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: guild has no supporter of owner %d in slot %d", ErrNotFound, owner, slot)
		}
	}
	return out, nil
}
func (s *Service) raidEnterLocal(ticket RaidTicket) func(command.Context) ([]byte, error) {
	return func(ctx command.Context) ([]byte, error) {
		state, e := LoadRaidState(ctx.State)
		if e != nil {
			return nil, e
		}
		if state.Active != nil && state.Active.EntryID != ticket.EntryID {
			return nil, fmt.Errorf("%w: raid battle %q is in progress, not %q", ErrPending, state.Active.EntryID, ticket.EntryID)
		}
		if e = s.validateRaidFormation(ctx, ticket.Deck, ticket.Settings); e != nil {
			return nil, e
		}
		ids := map[uint64]bool{}
		for _, raw := range ticket.Deck {
			p, e := parsePacket(raw)
			if e != nil {
				return nil, e
			}
			index, e := p.integer(1)
			if e != nil {
				return nil, e
			}
			char, ok := s.config.Characters.Find(ctx, index)
			if !ok {
				return nil, fmt.Errorf("%w: character %d is not owned", ErrIneligible, index)
			}
			if ids[char.ID] {
				return nil, fmt.Errorf("%w: character %d is in the raid deck twice", ErrInvalid, char.ID)
			}
			ids[char.ID] = true
		}
		for _, v := range ticket.Supporters {
			if v.CharacterID == 0 || ids[v.CharacterID] {
				return nil, fmt.Errorf("%w: raid supporter character %d is missing or already in the deck", ErrInvalid, v.CharacterID)
			}
			ids[v.CharacterID] = true
		}
		state.Active = &ticket
		if e = saveRaidState(ctx, state); e != nil {
			return nil, e
		}
		if e = s.config.BeginBattle(ctx, ticket.Mode, ticket.DeckID, ticket.EntryID); e != nil {
			return nil, e
		}
		out := wire.AppendVarint(nil, 4, 1)
		if s.config.BattleBuffs != nil {
			buffs, e := s.config.BattleBuffs(ctx)
			if e != nil {
				return nil, e
			}
			for _, v := range buffs {
				raw := wire.AppendVarint(nil, 1, v.StatType)
				raw = wire.AppendDouble(raw, 2, v.Value)
				raw = wire.AppendVarint(raw, 3, v.Category)
				out = wire.AppendBytes(out, 2, raw)
			}
		}
		for _, slot := range ticket.Supporters {
			raw, e := base64.StdEncoding.DecodeString(slot.Bundle)
			if e != nil {
				return nil, e
			}
			out = wire.AppendBytes(out, 3, raw)
		}
		return out, nil
	}
}
func (s *Service) validateRaidFormation(ctx command.Context, deck, settings [][]byte) error {
	if s.config.Characters == nil || s.config.Collection == nil {
		return fmt.Errorf("%w: raid formation checks are not configured", ErrInvalid)
	}
	characters := map[uint64]bool{}
	positions := map[uint64]bool{}
	sequences := map[uint64]bool{}
	for _, raw := range deck {
		p, e := parsePacket(raw)
		if e != nil {
			return e
		}
		index, e := p.integer(1)
		if e != nil || index == 0 {
			return fmt.Errorf("%w: raid formation character %d: %v", ErrInvalid, index, e)
		}
		pos, e := p.integer(2)
		if e != nil || pos >= 18 {
			return fmt.Errorf("%w: raid formation position %d, want below 18: %v", ErrInvalid, pos, e)
		}
		seq, e := p.integer(3)
		if e != nil || seq >= 7 {
			return fmt.Errorf("%w: raid formation sequence %d, want below 7: %v", ErrInvalid, seq, e)
		}
		if characters[index] || positions[pos] || sequences[seq] {
			return fmt.Errorf("%w: raid formation repeats character %d, position %d or sequence %d", ErrInvalid, index, pos, seq)
		}
		if _, found := s.config.Characters.Find(ctx, index); !found {
			return fmt.Errorf("%w: character %d is not owned", ErrNotFound, index)
		}
		characters[index], positions[pos], sequences[seq] = true, true, true
	}
	for _, raw := range settings {
		p, e := parsePacket(raw)
		if e != nil {
			return e
		}
		index, e := p.integer(1)
		if e != nil || !characters[index] {
			return fmt.Errorf("%w: raid setting names character %d which is not in the deck: %v", ErrInvalid, index, e)
		}
		refs, e := p.messages(2)
		if e != nil {
			return e
		}
		for _, ref := range refs {
			q, e := parsePacket(ref)
			if e != nil {
				return e
			}
			costume, e := q.integer(1)
			if e != nil {
				return e
			}
			c, found := s.config.Collection.CostumeByIndex(costume)
			if !found || c.UseChar != index {
				return fmt.Errorf("%w: costume %d is not for character %d", ErrIneligible, costume, index)
			}
		}
	}
	return nil
}

func (s *Service) HandlesBattle(mode uint64) bool {
	return mode == RaidBattleMode || mode == RaidPracticeMode
}
func (s *Service) EnterBattle(ctx command.Context, request []byte, receipt string) ([]byte, error) {
	return nil, fmt.Errorf("%w: guild raid battles enter through their own requests", ErrIneligible)
}
func (s *Service) CompleteBattle(ctx command.Context, request []byte, receipt string) ([]byte, error) {
	state, e := LoadRaidState(ctx.State)
	if e != nil {
		return nil, e
	}
	ticket := state.Active
	if ticket == nil || ticket.EntryID != receipt || ticket.AccountID != ctx.AccountID {
		return nil, fmt.Errorf("%w: raid battle receipt %q does not match the active battle", ErrConflict, receipt)
	}
	p, e := parsePacket(request)
	if e != nil {
		return nil, e
	}
	result, e := p.integer(2)
	if e != nil || result < 1 || result > 4 {
		return nil, fmt.Errorf("%w: raid battle result %d, want 1..4: %v", ErrInvalid, result, e)
	}
	rounds := s.config.BattleRounds(ctx)
	if rounds == 0 {
		return nil, fmt.Errorf("%w: raid battle has no rounds", ErrIneligible)
	}
	member := personal(state, ticket.Season.ID)
	score, base, turn, support := uint64(0), uint64(0), uint64(0), uint64(0)
	level := uint64(0)
	var rewards []gamedata.Reward
	if result == 1 {
		level = ticket.Level
		for n, boss := range s.config.Owner.design.Raid.Bosses[ticket.Season.BossGroupID] {
			if n <= level {
				if base > math.MaxInt32-boss.AddScore {
					return nil, fmt.Errorf("%w: raid level %d base score overflows", ErrInvalid, n)
				}
				base += boss.AddScore
			}
			if ticket.Mode == RaidBattleMode && n > member.RewardedLevel && n <= level {
				rewards = append(rewards, boss.Rewards...)
			}
		}
		bonus := math.Max(0, s.config.Owner.design.Raid.TurnAddScore-float64(rounds)*s.config.Owner.design.Raid.TurnScoreValue)
		turn = uint64(bonus)
		support = uint64(float64(len(ticket.Supporters)) * s.config.Owner.design.Raid.SupportScore)
		score = base + turn + support
		if score > math.MaxInt32 {
			return nil, fmt.Errorf("%w: raid score %d overflows", ErrInvalid, score)
		}
	}
	var bundle []byte
	if len(rewards) > 0 {
		if s.config.RewardBundle == nil {
			return nil, fmt.Errorf("%w: raid rewards need a reward bundle builder", ErrInvalid)
		}
		bundle, e = s.config.RewardBundle(ctx, ticket.EntryID+":boss_rewards", rewards)
		if e != nil {
			return nil, e
		}
	}
	overkill := uint64(0)
	// LocalText 2001675: only clears at overkillLevel or above record damage beyond the boss HP.
	if threshold := s.config.Owner.design.Raid.OverkillLevel; result == 1 && threshold > 0 && level >= threshold {
		total, e := raidTotalDamage(p)
		if e != nil {
			return nil, e
		}
		hp := s.config.Owner.design.Raid.Bosses[ticket.Season.BossGroupID][level].MaxHP
		if hp == 0 {
			return nil, fmt.Errorf("%w: raid level %d of boss group %d has no HP", ErrInvalid, level, ticket.Season.BossGroupID)
		}
		if total > hp {
			overkill = total - hp
		}
	}
	projectedTotal := ticket.Season.TotalScore
	if ticket.Mode == RaidBattleMode {
		if score > member.Score {
			projectedTotal += score - member.Score
			member.Score, member.Overkill = score, overkill
		} else if score == member.Score && overkill > member.Overkill {
			member.Overkill = overkill
		}
		if level > member.HighestLevel {
			member.HighestLevel = level
		}
		if level > member.RewardedLevel {
			member.RewardedLevel = level
		}
		day := member.Days[ticket.Day]
		if score > day.Score {
			day.Score = score
		}
		if level > day.Level {
			day.Level = level
		}
		if result == 1 && len(ticket.Supporters) > 0 {
			day.SupporterUses++
		}
		member.Days[ticket.Day] = day
		state.Seasons[strconv.FormatUint(ticket.Season.ID, 10)] = member
		settled := RaidResult{AtMS: s.config.Now().UnixMilli(), EntryID: ticket.EntryID, AccountID: ctx.AccountID, ResultID: ticket.EntryID + ":result", GuildID: ticket.Season.GuildID, Season: ticket.Season.ID, MembershipEpoch: ticket.Epoch, Level: level, Score: score, Overkill: overkill, RulesVersion: ticket.Season.RulesVersion, Digest: ticket.Digest}
		payload, e := json.Marshal(settled)
		if e != nil {
			return nil, e
		}
		if e = ctx.Publish(command.Event{ID: settled.ResultID, Topic: "social.raid_settle", Payload: payload}); e != nil {
			return nil, e
		}
	}
	state.Active = nil
	if e = saveRaidState(ctx, state); e != nil {
		return nil, e
	}
	out := wire.AppendBytes(nil, 5, bundle)
	scoreInfo := wire.AppendVarint(nil, 1, base)
	scoreInfo = wire.AppendVarint(scoreInfo, 2, turn)
	scoreInfo = wire.AppendVarint(scoreInfo, 3, support)
	scoreInfo = wire.AppendVarint(scoreInfo, 4, projectedTotal)
	out = wire.AppendBytes(out, 25, scoreInfo)
	out = wire.AppendVarint(out, 28, overkill)
	if e = saveBattleReply(ctx, request, out); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *Service) AbandonBattle(ctx command.Context) error {
	state, e := LoadRaidState(ctx.State)
	if e != nil {
		return e
	}
	ticket := state.Active
	if ticket == nil {
		return nil
	}
	if ticket.AccountID != ctx.AccountID {
		return fmt.Errorf("%w: raid battle belongs to %s, not %s", ErrPermission, ticket.AccountID, ctx.AccountID)
	}
	if ticket.Mode == RaidBattleMode {
		result := RaidResult{AtMS: s.config.Now().UnixMilli(), EntryID: ticket.EntryID, AccountID: ctx.AccountID, ResultID: ticket.EntryID + ":result", GuildID: ticket.Season.GuildID, Season: ticket.Season.ID, MembershipEpoch: ticket.Epoch, RulesVersion: ticket.Season.RulesVersion, Digest: ticket.Digest}
		payload, e := json.Marshal(result)
		if e != nil {
			return e
		}
		if e = ctx.Publish(command.Event{ID: result.ResultID, Topic: "social.raid_settle", Payload: payload}); e != nil {
			return e
		}
	}
	state.Active = nil
	return saveRaidState(ctx, state)
}

func (s *Service) CloseExpiredRaid(ctx command.Context) error {
	state, e := LoadRaidState(ctx.State)
	if e != nil {
		return e
	}
	if state.Active == nil || s.config.Now().UnixMilli() < state.Active.Season.EndsMS {
		return nil
	}
	return s.AbandonBattle(ctx)
}

type raidBattleReply struct{ Request, Response []byte }

func battleReplyID(ctx command.Context, request []byte) (string, error) {
	p, e := parsePacket(request)
	if e != nil {
		return "", e
	}
	seq, e := p.integer(1)
	if e != nil {
		return "", e
	}
	return ctx.SessionID + ":" + strconv.FormatUint(seq, 10), nil
}
func saveBattleReply(ctx command.Context, request, response []byte) error {
	id, e := battleReplyID(ctx, request)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(raidBattleReply{Request: request, Response: response})
	if e != nil {
		return e
	}
	return ctx.State.PutEntry("social", "raid_battle_replies", id, raw)
}
func (s *Service) BattleEndReplay(ctx command.Context, request []byte) ([]byte, bool, error) {
	id, e := battleReplyID(ctx, request)
	if e != nil {
		return nil, false, e
	}
	raw, found, e := ctx.State.LoadEntry("social", "raid_battle_replies", id)
	if e != nil || !found {
		return nil, found, e
	}
	var reply raidBattleReply
	if e = json.Unmarshal(raw, &reply); e != nil {
		return nil, false, e
	}
	if !bytes.Equal(request, reply.Request) {
		return nil, false, fmt.Errorf("%w: raid battle reply %q was requested with a different body", ErrConflict, id)
	}
	return reply.Response, true, nil
}
func raidTotalDamage(p packet) (uint64, error) {
	stats, e := p.messages(10)
	if e != nil || len(stats) != 1 {
		return 0, fmt.Errorf("%w: raid battle result carries %d stat blocks, want 1: %v", ErrInvalid, len(stats), e)
	}
	q, e := parsePacket(stats[0])
	if e != nil {
		return 0, e
	}
	damage, e := q.messages(1)
	if e != nil {
		return 0, e
	}
	var total uint64
	for _, raw := range damage {
		r, e := parsePacket(raw)
		if e != nil {
			return 0, e
		}
		n, e := r.integer(2)
		if e != nil || total > math.MaxInt64-n {
			return 0, fmt.Errorf("%w: raid damage %d: %v", ErrInvalid, n, e)
		}
		total += n
	}
	return total, nil
}
