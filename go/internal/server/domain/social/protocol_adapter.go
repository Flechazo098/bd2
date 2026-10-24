package social

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

var packetCodes = map[string]int{
	"/SupporterReward": 442,
	// These current request callbacks ignore the success packet code; the client enum has no corresponding entries.
	"/GuildRaidSeasonRanking": 0, "/GuildRaidPresetDelete": 0, "/GuildRaidPresetInfoChange": 0,
	"/GuildRaidMainInfo": 291, "/GuildRaidBossBattleInfo": 293, "/GuildRaidBossQuickBattle": 294, "/GuildRaidMemberRanking": 309, "/GuildRaidSeasonReward": 310, "/GuildRaidDeckInfo": 312, "/GuildRaidDeckSave": 313, "/GuildRaidBossBattleEnter": 316, "/GuildRaidBossBattleHistory": 339, "/GuildRaidMemberPlayHistoryInfo": 375, "/GuildRaidPresetSlotAdd": 399, "/GuildRaidPresetInfo": 403, "/GuildRaidPresetSave": 404, "/GuildRaidPresetUse": 411, "/GuildRaidBossPracticeBattleEnter": 453,
	"/GuildCreate": 296, "/GuildInfo": 297, "/GuildLeave": 298, "/GuildRecommend": 299, "/GuildJoin": 300, "/GuildAccept": 301, "/GuildJoinRefuse": 302, "/GuildMemberBan": 303, "/GuildMemberRoleEdit": 304, "/GuildInfoEdit": 305, "/GuildSearch": 306, "/GuildSupporterInfoList": 308, "/GuildJoinSendCancel": 311, "/GuildJoinSendInfo": 314, "/GuildDeleteCancel": 315, "/GuildInitInfo": 332, "/GuildSupporterAdd": 333, "/GuildSupporterDelete": 334, "/GuildUpdateActionInfo": 335, "/GuildSupporterInfo": 341, "/GuildNoticeInfo": 381, "/GuildNoticeUpdate": 382, "/GuildNameChange": 570,
}

func Owns(path string) bool { _, ok := packetCodes[path]; return ok }
func Mutates(path string) bool {
	if strings.HasPrefix(path, "/GuildRaid") {
		switch path {
		case "/GuildRaidMainInfo", "/GuildRaidBossBattleInfo", "/GuildRaidMemberRanking", "/GuildRaidSeasonRanking", "/GuildRaidDeckInfo", "/GuildRaidBossBattleHistory", "/GuildRaidMemberPlayHistoryInfo", "/GuildRaidPresetInfo":
			return false
		default:
			return true
		}
	}
	switch path {
	case "/SupporterReward", "/GuildInfo", "/GuildCreate", "/GuildLeave", "/GuildJoin", "/GuildAccept", "/GuildJoinRefuse", "/GuildMemberBan", "/GuildMemberRoleEdit", "/GuildInfoEdit", "/GuildJoinSendCancel", "/GuildDeleteCancel", "/GuildSupporterAdd", "/GuildSupporterDelete", "/GuildUpdateActionInfo", "/GuildNoticeUpdate", "/GuildNameChange":
		return true
	}
	return false
}

type ServiceConfig struct {
	RaidSeasons  []Season
	RaidState    func(context.Context, command.Identity) (RaidState, error)
	RewardBundle func(command.Context, string, []gamedata.Reward) ([]byte, error)
	BeginBattle  func(command.Context, uint64, uint64, string) error
	BattleRounds func(command.Context) uint64
	BattleBuffs  func(command.Context) ([]gamedata.PictorialBuffStat, error)
	Characters   *roster.CharacterStore
	Collection   *roster.CollectionStore
	Equipment    *assets.EquipmentInventory
	Presets      *gamedata.PresetDesign
	Owner        *Owner
	Profile      func(context.Context, command.Identity) (PublicMember, error)
	Qualify      func(context.Context, command.Identity, string) error
	Supporter    func(context.Context, command.Identity, uint64, uint64) (SupporterSlot, error)
	CheckText    func(context.Context, command.Identity, string, string) error
	Spend        func(command.Context, string, []gamedata.Reward) error
	Grant        func(command.Context, string, []gamedata.Reward) error
	Now          func() time.Time
	Day          func(time.Time) string
}
type Service struct{ config ServiceConfig }
type Plan struct {
	events        []command.Event
	ID            string
	local         func(command.Context) ([]byte, error)
	Identity      command.Identity
	Code          int
	Response      []byte
	Reservation   *Reservation
	RequestDigest string
}
type playerReceipt struct {
	Proof         CommitProof
	RequestDigest string
	Response      []byte
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Owner == nil || config.Profile == nil || config.Qualify == nil || config.CheckText == nil || config.Spend == nil || config.Grant == nil {
		return nil, fmt.Errorf("%w: guild service is missing a dependency", ErrInvalid)
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Day == nil {
		config.Day = func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	}
	seenSeasons := map[uint64]bool{}
	for _, season := range config.RaidSeasons {
		if season.ID == 0 || season.StartsMS <= 0 || season.EndsMS <= season.StartsMS || season.RulesVersion == "" || len(config.Owner.design.Raid.Bosses[season.BossGroupID]) == 0 || seenSeasons[season.ID] {
			return nil, fmt.Errorf("%w: raid season %+v is incomplete, duplicated or has no bosses", ErrInvalid, season)
		}
		seenSeasons[season.ID] = true
	}
	return &Service{config: config}, nil
}

type packet map[int][]wire.Field

func parsePacket(raw []byte) (packet, error) {
	p := packet{}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("%w: guild request is %d bytes", ErrInvalid, len(raw))
	}
	err := wire.Walk(raw, func(f wire.Field) error { p[f.Number] = append(p[f.Number], f); return nil })
	return p, err
}
func (p packet) integer(field int) (uint64, error) {
	v := p[field]
	if len(v) == 0 {
		return 0, nil
	}
	if len(v) != 1 || v[0].Type != 0 {
		return 0, fmt.Errorf("%w: field %d needs one varint, got %d values", wire.ErrMalformed, field, len(v))
	}
	n, k := binary.Uvarint(v[0].Value)
	if k <= 0 || n > math.MaxInt64 {
		return 0, fmt.Errorf("%w: field %d varint is out of range", wire.ErrMalformed, field)
	}
	return n, nil
}
func (p packet) text(field int) (string, error) {
	v := p[field]
	if len(v) == 0 {
		return "", nil
	}
	if len(v) != 1 || v[0].Type != 2 || !utf8.Valid(v[0].Value) {
		return "", fmt.Errorf("%w: field %d needs one UTF-8 string, got %d values", wire.ErrMalformed, field, len(v))
	}
	return string(v[0].Value), nil
}
func (p packet) integers(field int) ([]uint64, error) {
	var out []uint64
	for _, f := range p[field] {
		switch f.Type {
		case 0:
			n, k := binary.Uvarint(f.Value)
			if k <= 0 {
				return nil, fmt.Errorf("%w: field %d varint is truncated", wire.ErrMalformed, field)
			}
			out = append(out, n)
		case 2:
			for b := f.Value; len(b) > 0; {
				n, k := binary.Uvarint(b)
				if k <= 0 {
					return nil, fmt.Errorf("%w: field %d packed varint is truncated", wire.ErrMalformed, field)
				}
				out = append(out, n)
				b = b[k:]
			}
		default:
			return nil, fmt.Errorf("%w: field %d has wire type %d, want varints", wire.ErrMalformed, field, f.Type)
		}
	}
	return out, nil
}
func requestIdentity(identity command.Identity, path string, seq uint64, raw []byte) (string, string) {
	key, _ := json.Marshal(struct {
		Account, Session, Request, Path string
		Seq                             uint64
	}{identity.AccountID, identity.SessionID, identity.RequestID, path, seq})
	id := sha256.Sum256(key)
	d := sha256.Sum256(append(append([]byte(path), 0), raw...))
	return "guild:" + hex.EncodeToString(id[:]), hex.EncodeToString(d[:])
}

// Prepare performs shared reads and reservations before opening a player transaction.
func (s *Service) Prepare(ctx context.Context, identity command.Identity, path string, raw []byte) (*Plan, bool, error) {
	code, ok := packetCodes[path]
	if !ok {
		return nil, false, nil
	}
	fail := func(e error) (*Plan, bool, error) {
		if errors.Is(e, ErrInvalid) || errors.Is(e, wire.ErrMalformed) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrIneligible) || errors.Is(e, ErrCapacity) || errors.Is(e, ErrPending) || errors.Is(e, ErrPermission) || errors.Is(e, ErrSeasonLocked) {
			e = &command.GameError{PacketCode: code, ErrorType: 9003, Message: e.Error()}
		}
		return nil, true, e
	}
	if identity.AccountID == "" || identity.SessionID == "" {
		return nil, true, fmt.Errorf("%w: %s identity lacks account or session", ErrInvalid, path)
	}
	p, e := parsePacket(raw)
	if e != nil {
		return fail(e)
	}
	seq, e := p.integer(1)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return fail(fmt.Errorf("%w: %s sequence %d: %v", ErrInvalid, path, seq, e))
	}
	id, digest := requestIdentity(identity, path, seq, raw)
	plan := &Plan{ID: id, Identity: identity, Code: code, RequestDigest: digest}
	if strings.HasPrefix(path, "/GuildRaid") {
		plan, e = s.prepareRaid(ctx, plan, path, p)
		if e != nil {
			return fail(e)
		}
		return plan, true, e
	}
	if Mutates(path) {
		prior, e := s.config.Owner.Reservation(ctx, id)
		if e == nil {
			if prior.RequestDigest != digest || prior.ActorAccount != identity.AccountID || prior.Status == Cancelled {
				return fail(fmt.Errorf("%w: %s sequence %d replays operation %q with a different request or after cancellation", ErrConflict, path, seq, id))
			}
			plan.Reservation = &prior
			plan.Response, e = s.mutationResponse(ctx, path, prior, p)
			if e != nil {
				return fail(e)
			}
			return plan, true, e
		}
		if !errors.Is(e, ErrNotFound) {
			return fail(e)
		}
	}
	if e = s.config.Qualify(ctx, identity, path); e != nil {
		return fail(e)
	}
	profile, e := s.config.Profile(ctx, identity)
	if e != nil {
		return fail(e)
	}
	if profile.AccountID != identity.AccountID || profile.OwnerIndex == 0 {
		return fail(fmt.Errorf("%w: profile of owner %d is account %q, not %q", ErrPermission, profile.OwnerIndex, profile.AccountID, identity.AccountID))
	}
	a, e := s.config.Owner.Account(ctx, identity.AccountID)
	if e != nil {
		return fail(e)
	}
	now := s.config.Now()
	op := Operation{ID: id, Actor: profile, GuildID: a.GuildID, ExpectedMembershipEpoch: a.Epoch, AtMS: now.UnixMilli(), Day: s.config.Day(now), RequestDigest: digest}
	var g Guild
	if a.GuildID != 0 {
		g, e = s.config.Owner.Guild(ctx, a.GuildID)
		if e != nil {
			return fail(e)
		}
	}
	switch path {
	case "/SupporterReward":
		op.Kind = SupporterClaim
		op.GuildID = 0
		op.UsageIDs, e = p.integers(2)
		if e != nil {
			return fail(e)
		}
	case "/GuildInfo":
		target, e := p.integer(2)
		if e != nil {
			return fail(e)
		}
		if target == 0 {
			target = a.GuildID
		}
		if target != a.GuildID || target == 0 || a.AttendanceDay == op.Day || g.DeleteAtMS != 0 {
			plan.Response, e = s.info(ctx, target, a, now.UnixMilli())
			if e != nil {
				return fail(e)
			}
			return plan, true, e
		}
		op.Kind = Attendance
	case "/GuildCreate", "/GuildInfoEdit":
		op.Kind = Create
		shift := 0
		if path == "/GuildInfoEdit" {
			op.Kind = Edit
			shift = -1
		}
		if op.Kind == Create {
			op.Create.Name, e = p.text(2)
			if e != nil {
				return fail(e)
			}
			if e = s.config.CheckText(ctx, identity, "guild_name", op.Create.Name); e != nil {
				return fail(e)
			}
		}
		op.Create.Icon, e = p.integer(3 + shift)
		if e != nil {
			return fail(e)
		}
		op.Create.IconColor, e = p.text(4 + shift)
		if e != nil {
			return fail(e)
		}
		op.Create.Message, e = p.text(5 + shift)
		if e != nil {
			return fail(e)
		}
		join, e := p.integer(6 + shift)
		if e != nil {
			return fail(e)
		}
		op.Create.JoinType = JoinType(join)
		if e = s.config.CheckText(ctx, identity, "guild_message", op.Create.Message); e != nil {
			return fail(e)
		}
	case "/GuildJoin", "/GuildJoinSendCancel":
		op.Kind = Join
		if path == "/GuildJoinSendCancel" {
			op.Kind = CancelJoin
		}
		op.GuildID, e = p.integer(2)
		if e != nil {
			return fail(e)
		}
		if op.Kind == Join {
			until := max(a.LeaveUntilMS, a.Bans[op.GuildID])
			if until > op.AtMS {
				target, e := s.config.Owner.Guild(ctx, op.GuildID)
				if e != nil {
					return fail(e)
				}
				plan.Response = wire.AppendBytes(nil, 1, guildInfo(target, op.AtMS))
				delay := int64(s.config.Owner.design.Guild.LeaveDelayHours) * 3600000
				plan.Response = wire.AppendVarint(plan.Response, 3, uint64(until-delay))
				plan.Response = wire.AppendVarint(plan.Response, 4, 1)
				return plan, true, nil
			}
		}
	case "/GuildAccept", "/GuildJoinRefuse", "/GuildMemberBan", "/GuildMemberRoleEdit":
		op.Kind = map[string]Kind{"/GuildAccept": Accept, "/GuildJoinRefuse": Refuse, "/GuildMemberBan": Ban, "/GuildMemberRoleEdit": ChangeRole}[path]
		target, e := p.integer(2)
		if e != nil || target == 0 {
			return fail(fmt.Errorf("%w: %s target %d: %v", ErrInvalid, path, target, e))
		}
		if op.Kind == Accept || op.Kind == Refuse {
			apps, _, e := s.config.Owner.Applications(ctx, g.ID, identity.AccountID, "", 256)
			if e != nil {
				return fail(e)
			}
			for _, app := range apps {
				if app.Applicant.OwnerIndex == target {
					op.TargetAccount = app.Applicant.AccountID
					break
				}
			}
		} else {
			for account, m := range g.Members {
				if m.OwnerIndex == target {
					op.TargetAccount = account
					break
				}
			}
		}
		if op.TargetAccount == "" {
			return fail(fmt.Errorf("%w: %s target owner %d is not in guild %d", ErrNotFound, path, target, g.ID))
		}
		targetAccount, e := s.config.Owner.Account(ctx, op.TargetAccount)
		if e != nil {
			return fail(e)
		}
		op.ExpectedMembershipEpoch = targetAccount.Epoch
		if op.Kind == ChangeRole {
			role, e := p.integer(3)
			if e != nil {
				return fail(e)
			}
			op.Role = Role(role)
		}
	case "/GuildLeave":
		op.Kind = Leave
	case "/GuildDeleteCancel":
		op.Kind = CancelDelete
	case "/GuildNoticeUpdate", "/GuildNameChange":
		op.Kind = Notice
		scope := "guild_notice"
		if path == "/GuildNameChange" {
			op.Kind = Rename
			scope = "guild_name"
		}
		op.Text, e = p.text(2)
		if e != nil {
			return fail(e)
		}
		if e = s.config.CheckText(ctx, identity, scope, op.Text); e != nil {
			return fail(e)
		}
	case "/GuildUpdateActionInfo":
		op.Kind = Acknowledge
		op.ActionTypes, e = p.integers(2)
		if e != nil {
			return fail(e)
		}
	case "/GuildSupporterAdd", "/GuildSupporterDelete":
		op.Kind = SupporterRemove
		slot, e := p.integer(2)
		if e != nil {
			return fail(e)
		}
		op.Supporter = SupporterSlot{AccountID: identity.AccountID, Slot: slot}
		if path == "/GuildSupporterAdd" {
			op.Kind = SupporterAdd
			if s.config.Supporter == nil {
				return fail(fmt.Errorf("%w: guild supporters are not configured", ErrIneligible))
			}
			char, e := p.integer(3)
			if e != nil {
				return fail(e)
			}
			costume, e := p.integer(4)
			if e != nil {
				return fail(e)
			}
			op.Supporter, e = s.config.Supporter(ctx, identity, char, costume)
			if e != nil {
				return fail(e)
			}
			op.Supporter.Slot = slot
			if op.Supporter.CharacterIndex != char || op.Supporter.CostumeIndex != costume {
				return fail(fmt.Errorf("%w: supporter is character %d costume %d, request named %d and %d", ErrConflict, op.Supporter.CharacterIndex, op.Supporter.CostumeIndex, char, costume))
			}
		}
	default:
		plan.Response, e = s.read(ctx, path, p, a, g, now.UnixMilli())
		if e != nil {
			return fail(e)
		}
		return plan, true, e
	}
	r, e := s.config.Owner.Prepare(ctx, op)
	if e != nil {
		return fail(e)
	}
	plan.Reservation = &r
	plan.Response, e = s.mutationResponse(ctx, path, r, p)
	if e != nil {
		_, cancelErr := s.config.Owner.Cancel(ctx, proofFor(r))
		return fail(errors.Join(e, cancelErr))
	}
	return plan, true, nil
}

func proofFor(r Reservation) CommitProof {
	return CommitProof{OperationID: r.ID, ActorAccount: r.ActorAccount, Digest: r.Digest}
}
func (s *Service) Apply(ctx command.Context, plan *Plan) (int, []byte, error) {
	if plan == nil || ctx.Identity != plan.Identity {
		return 0, nil, fmt.Errorf("%w: guild plan belongs to another identity", ErrPermission)
	}
	if plan.Reservation == nil {
		if plan.local != nil {
			out, e := s.applyLocal(ctx, plan)
			return plan.Code, out, e
		}
		return plan.Code, plan.Response, nil
	}
	r := *plan.Reservation
	if ctx.State == nil {
		return plan.Code, nil, fmt.Errorf("%w: guild operation %q has no state", ErrInvalid, r.ID)
	}
	if raw, found, e := ctx.State.LoadEntry("social", "receipts", r.ID); e != nil {
		return plan.Code, nil, e
	} else if found {
		receipt, e := decodeReceipt(raw)
		if e != nil {
			return plan.Code, nil, e
		}
		if receipt.RequestDigest != plan.RequestDigest || receipt.Proof != proofFor(r) {
			return plan.Code, nil, fmt.Errorf("%w: guild operation %q receipt does not match the plan", ErrConflict, r.ID)
		}
		return plan.Code, receipt.Response, nil
	}
	if r.Status == Confirmed {
		return plan.Code, nil, fmt.Errorf("%w: guild operation %q is already confirmed without a receipt", ErrConflict, r.ID)
	}
	if len(r.Charges) > 0 {
		if e := s.config.Spend(ctx, r.ID, r.Charges); e != nil {
			return plan.Code, nil, e
		}
	}
	if len(r.Grants) > 0 {
		if r.Kind == RaidClaim || r.Kind == SupporterClaim {
			if s.config.RewardBundle == nil {
				return plan.Code, nil, fmt.Errorf("%w: guild operation %q needs a reward bundle builder", ErrInvalid, r.ID)
			}
			bundle, e := s.config.RewardBundle(ctx, r.ID, r.Grants)
			if e != nil {
				return plan.Code, nil, e
			}
			if r.Kind == SupporterClaim {
				plan.Response = wire.AppendBytes(plan.Response, 2, bundle)
			} else {
				plan.Response = wire.AppendVarint(wire.AppendBytes(nil, 1, bundle), 2, 1)
			}
		} else if e := s.config.Grant(ctx, r.ID, r.Grants); e != nil {
			return plan.Code, nil, e
		}
	}
	if plan.local != nil {
		out, e := plan.local(ctx)
		if e != nil {
			return plan.Code, nil, e
		}
		plan.Response = out
	}
	receipt := playerReceipt{Proof: proofFor(r), RequestDigest: plan.RequestDigest, Response: plan.Response}
	raw, e := json.Marshal(receipt)
	if e != nil {
		return plan.Code, nil, e
	}
	if e = ctx.State.PutEntry("social", "receipts", r.ID, raw); e != nil {
		return plan.Code, nil, e
	}
	payload, e := json.Marshal(receipt.Proof)
	if e != nil {
		return plan.Code, nil, e
	}
	if e = ctx.Publish(command.Event{ID: r.ID, Topic: "social.confirm", Payload: payload}); e != nil {
		return plan.Code, nil, e
	}
	for _, event := range plan.events {
		if e = ctx.Publish(event); e != nil {
			return plan.Code, nil, e
		}
	}
	return plan.Code, plan.Response, nil
}
func decodeReceipt(raw []byte) (playerReceipt, error) {
	var r playerReceipt
	e := json.Unmarshal(raw, &r)
	if e == nil && (r.Proof.OperationID == "" || r.Proof.ActorAccount == "" || r.Proof.Digest == "" || r.RequestDigest == "") {
		e = fmt.Errorf("%w: guild receipt lacks its proof or digest", ErrInvalid)
	}
	return r, e
}
func CommittedProof(raw []byte) (CommitProof, error) { r, e := decodeReceipt(raw); return r.Proof, e }
func (s *Service) Confirm(ctx context.Context, plan *Plan) error {
	if plan == nil || plan.Reservation == nil {
		return nil
	}
	_, e := s.config.Owner.Confirm(ctx, proofFor(*plan.Reservation))
	return e
}
func (s *Service) Cancel(ctx context.Context, plan *Plan) error {
	if plan == nil || plan.Reservation == nil {
		return nil
	}
	_, e := s.config.Owner.Cancel(ctx, proofFor(*plan.Reservation))
	return e
}

func (s *Service) mutationResponse(ctx context.Context, path string, r Reservation, p packet) ([]byte, error) {
	if r.Kind == SupporterClaim {
		var out []byte
		for _, id := range r.UsageIDs {
			out = wire.AppendVarint(out, 1, id)
		}
		return out, nil
	}
	if r.View == nil {
		if r.Kind == Acknowledge {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: guild operation %v has no view", ErrInvalid, r.Kind)
	}
	g := *r.View
	switch path {
	case "/GuildCreate":
		return wire.AppendBytes(nil, 1, guildInfo(g, r.PreparedMS)), nil
	case "/GuildJoin":
		out := wire.AppendBytes(nil, 1, guildInfo(g, r.PreparedMS))
		if !r.Application {
			out = appendMembers(out, 2, g)
		}
		return out, nil
	case "/GuildMemberRoleEdit":
		return appendMembers(nil, 1, g), nil
	case "/GuildNoticeUpdate":
		return wire.AppendVarint(nil, 1, uint64(g.NoticeUpdatedMS)), nil
	case "/GuildInfo":
		a, e := s.config.Owner.Account(ctx, r.ActorAccount)
		if e != nil {
			return nil, e
		}
		out, e := s.infoGuild(ctx, g, a, r.PreparedMS)
		return wire.AppendVarint(out, 5, 1), e
	}
	return nil, nil
}

func (s *Service) read(ctx context.Context, path string, p packet, a Account, g Guild, at int64) ([]byte, error) {
	switch path {
	case "/GuildNoticeInfo":
		if g.ID == 0 {
			return nil, fmt.Errorf("%w: %s needs a guild", ErrIneligible, path)
		}
		return wire.AppendVarint(wire.AppendString(nil, 1, g.Notice), 2, uint64(g.NoticeUpdatedMS)), nil
	case "/GuildInitInfo":
		var out []byte
		var e error
		if g.ID != 0 {
			out, e = s.appendApplications(ctx, out, 1, g, a, at)
		}
		return appendActions(out, 2, a), e
	case "/GuildJoinSendInfo":
		return s.sent(ctx, a, at)
	case "/GuildRecommend", "/GuildSearch":
		return s.search(ctx, path, p, at)
	case "/GuildSupporterInfoList", "/GuildSupporterInfo":
		if g.ID == 0 {
			return nil, fmt.Errorf("%w: %s needs a guild", ErrIneligible, path)
		}
		target, slot := uint64(0), uint64(0)
		var e error
		if path == "/GuildSupporterInfo" {
			target, e = p.integer(2)
			if e != nil {
				return nil, e
			}
			slot, e = p.integer(3)
			if e != nil {
				return nil, e
			}
		}
		var out []byte
		keys := make([]string, 0, len(g.Supporters))
		for k := range g.Supporters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			v := g.Supporters[key]
			if target != 0 && (v.OwnerIndex != target || v.Slot != slot) {
				continue
			}
			v.UseCount = 0
			if season, err := s.raidSeason(at); err == nil {
				v.UseCount, err = s.config.Owner.RentalCount(ctx, g.ID, season.ID, v.AccountID)
				if err != nil {
					return nil, err
				}
			} else if !errors.Is(err, ErrIneligible) {
				return nil, err
			}
			out = wire.AppendBytes(out, 1, supporterInfo(v))
			if target != 0 {
				return out, nil
			}
		}
		if target != 0 {
			return nil, fmt.Errorf("%w: guild %d has no supporter of owner %d", ErrNotFound, g.ID, target)
		}
		return out, nil
	}
	return nil, fmt.Errorf("social: unknown read %s", path)
}

func (s *Service) sent(ctx context.Context, a Account, at int64) ([]byte, error) {
	var out []byte
	for cursor := ""; ; {
		apps, next, e := s.config.Owner.ApplicationsSent(ctx, a.AccountID, cursor, 256)
		if e != nil {
			return nil, e
		}
		for _, app := range apps {
			if app.ExpiresMS <= at {
				continue
			}
			g, e := s.config.Owner.Guild(ctx, app.GuildID)
			if errors.Is(e, ErrNotFound) {
				continue
			}
			if e != nil {
				return nil, e
			}
			out = wire.AppendBytes(out, 1, guildInfo(g, at))
		}
		if len(apps) < 256 || next == cursor {
			break
		}
		cursor = next
	}
	return appendActions(out, 2, a), nil
}

func (s *Service) info(ctx context.Context, id uint64, a Account, at int64) ([]byte, error) {
	if id == 0 {
		return appendActions(nil, 4, a), nil
	}
	g, e := s.config.Owner.Guild(ctx, id)
	if e != nil {
		return nil, e
	}
	return s.infoGuild(ctx, g, a, at)
}
func (s *Service) infoGuild(ctx context.Context, g Guild, a Account, at int64) ([]byte, error) {
	out := wire.AppendBytes(nil, 1, guildInfo(g, at))
	out = appendMembers(out, 2, g)
	var e error
	if a.GuildID == g.ID {
		out, e = s.appendApplications(ctx, out, 3, g, a, at)
		out = appendActions(out, 4, a)
	}
	return out, e
}
func (s *Service) appendApplications(ctx context.Context, out []byte, field int, g Guild, a Account, at int64) ([]byte, error) {
	m, ok := g.Members[a.AccountID]
	if !ok || m.Role != Admin && m.Role != SubAdmin {
		return out, nil
	}
	for cursor := ""; ; {
		apps, next, e := s.config.Owner.Applications(ctx, g.ID, a.AccountID, cursor, 256)
		if e != nil {
			return nil, e
		}
		for _, app := range apps {
			if app.ExpiresMS > at {
				out = wire.AppendBytes(out, field, userBase(app.Applicant, app.CreatedMS))
			}
		}
		if len(apps) < 256 || next == cursor {
			break
		}
		cursor = next
	}
	return out, nil
}

func (s *Service) search(ctx context.Context, path string, p packet, at int64) ([]byte, error) {
	name, e := p.text(2)
	min, max, join := uint64(0), s.config.Owner.design.Guild.MaxMembers, uint64(0)
	if path == "/GuildRecommend" {
		min, e = p.integer(2)
		if e != nil {
			return nil, e
		}
		max, e = p.integer(3)
		if e != nil {
			return nil, e
		}
		join, e = p.integer(4)
		if e != nil || join > uint64(ClosedJoin) {
			return nil, fmt.Errorf("%w: guild join type %d: %v", ErrInvalid, join, e)
		}
	} else if e != nil {
		return nil, e
	}
	if max < min || max > s.config.Owner.design.Guild.MaxMembers {
		return nil, fmt.Errorf("%w: guild search member range %d..%d, guilds hold at most %d", ErrInvalid, min, max, s.config.Owner.design.Guild.MaxMembers)
	}
	var out []byte
	count := 0
	for after := ""; ; {
		rows, next, e := s.config.Owner.Guilds(ctx, after, 256)
		if e != nil {
			return nil, e
		}
		for _, g := range rows {
			if g.DeleteAtMS != 0 || uint64(len(g.Members)) < min || uint64(len(g.Members)) > max || join != 0 && uint64(g.JoinType) != join || path == "/GuildSearch" && !strings.Contains(strings.ToLower(g.Name), strings.ToLower(name)) {
				continue
			}
			out = wire.AppendBytes(out, 1, guildInfo(g, at))
			count++
			if uint64(count) >= s.config.Owner.design.Guild.ListCount {
				return out, nil
			}
		}
		if next == "" || next == after {
			return out, nil
		}
		after = next
	}
}
func guildInfo(g Guild, at int64) []byte {
	base := wire.AppendVarint(nil, 1, g.ID)
	base = wire.AppendString(base, 2, g.Name)
	base = wire.AppendVarint(base, 3, g.Icon)
	base = wire.AppendString(base, 4, g.IconColor)
	out := wire.AppendBytes(nil, 1, base)
	out = wire.AppendVarint(out, 2, uint64(g.JoinType))
	out = wire.AppendString(out, 3, g.Message)
	out = wire.AppendVarint(out, 4, uint64(g.UpdatedMS))
	out = wire.AppendVarint(out, 5, uint64(g.CreatedMS))
	out = wire.AppendVarint(out, 6, uint64(len(g.Members)))
	if g.DeleteAtMS > at {
		out = wire.AppendVarint(out, 7, uint64(g.DeleteAtMS-at))
	}
	return wire.AppendVarint(out, 8, uint64(g.NoticeUpdatedMS))
}
func appendMembers(out []byte, field int, g Guild) []byte {
	members := make([]Membership, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, m)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].OwnerIndex < members[j].OwnerIndex })
	for _, m := range members {
		out = wire.AppendBytes(out, field, MemberInfo(g, m))
	}
	return out
}
func MemberInfo(g Guild, m Membership) []byte {
	out := wire.AppendVarint(nil, 1, g.ID)
	out = wire.AppendVarint(out, 2, m.OwnerIndex)
	out = wire.AppendString(out, 3, m.Name)
	out = wire.AppendVarint(out, 4, m.TitleID)
	out = wire.AppendVarint(out, 5, m.PortraitID)
	out = wire.AppendVarint(out, 6, m.PortraitDesignID)
	out = wire.AppendVarint(out, 7, uint64(m.Role))
	slots := make([]SupporterSlot, 0, 2)
	for _, v := range g.Supporters {
		if v.AccountID == m.AccountID {
			slots = append(slots, v)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Slot < slots[j].Slot })
	nextSlot := uint64(0)
	for _, v := range slots {
		for nextSlot < v.Slot {
			out = wire.AppendBytes(out, 9, nil)
			nextSlot++
		}
		costume := wire.AppendVarint(nil, 1, v.CostumeID)
		costume = wire.AppendVarint(costume, 2, v.CostumeLevel)
		costume = wire.AppendVarint(costume, 3, v.CostumeDesignID)
		out = wire.AppendBytes(out, 9, costume)
		nextSlot = v.Slot + 1
	}
	out = wire.AppendVarint(out, 10, uint64(m.LastLoginMS))
	out = wire.AppendVarint(out, 11, uint64(g.UpdatedMS))
	return wire.AppendVarint(out, 12, uint64(m.JoinedMS))
}
func userBase(m PublicMember, at int64) []byte {
	out := wire.AppendVarint(nil, 1, m.OwnerIndex)
	out = wire.AppendVarint(out, 2, m.PortraitID)
	out = wire.AppendVarint(out, 3, m.PortraitDesignID)
	out = wire.AppendString(out, 4, m.Name)
	out = wire.AppendVarint(out, 5, m.TitleID)
	return wire.AppendVarint(out, 6, uint64(at))
}
func supporterInfo(v SupporterSlot) []byte {
	out := wire.AppendVarint(nil, 1, v.OwnerIndex)
	out = wire.AppendVarint(out, 2, v.Slot)
	out = wire.AppendString(out, 3, v.Name)
	out = wire.AppendVarint(out, 4, v.UseCount)
	return wire.AppendString(out, 5, v.Bundle)
}
func appendActions(out []byte, field int, a Account) []byte {
	keys := make([]uint64, 0, len(a.Actions))
	for k := range a.Actions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := a.Actions[k]
		b := wire.AppendVarint(nil, 1, v.Type)
		b = wire.AppendVarint(b, 2, uint64(v.AtMS))
		b = wire.AppendVarint(b, 3, v.GuildID)
		b = wire.AppendString(b, 4, v.GuildName)
		if v.Notify {
			b = wire.AppendVarint(b, 5, 1)
		}
		b = wire.AppendVarint(b, 6, uint64(v.Role))
		out = wire.AppendBytes(out, field, b)
	}
	return out
}
