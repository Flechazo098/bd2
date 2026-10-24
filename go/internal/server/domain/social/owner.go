package social

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	guildBucket            = "social.guilds"
	accountBucket          = "social.accounts"
	operationBucket        = "social.operations"
	pendingBucket          = "social.pending_operations"
	nameBucket             = "social.names"
	applicationBucket      = "social.applications"
	applicationGuildBucket = "social.guild_applications"
	deletionBucket         = "social.deletions"
	seasonBucket           = "social.seasons"
	raidEntryBucket        = "social.raid_entries"
	raidPendingBucket      = "social.raid_pending"
	bestBucket             = "social.member_best"
	resultBucket           = "social.raid_results"
	resultIdentityBucket   = "social.raid_result_ids"
	usageBucket            = "social.raid_usage"
	seasonGuildBucket      = "social.season_guilds"
	entitlementBucket      = "social.entitlements"
	boardBucket            = "social.boards"
	rentalBucket           = "social.rentals"
)

// Owner has no mutable cache: each scoped store transaction is the authority.
// Independent guilds do not queue behind an additional global domain mutex.
type Owner struct {
	store  Store
	design *gamedata.GuildDesign
}

func New(store Store, design *gamedata.GuildDesign) (*Owner, error) {
	if store == nil || design == nil || design.Guild.MaxMembers == 0 || design.Guild.MaxMembers > 256 {
		return nil, fmt.Errorf("%w: guild store or design missing, or max members outside 1..256", ErrInvalid)
	}
	return &Owner{store: store, design: design}, nil
}

func load(tx Tx, bucket, key string, value any) (bool, error) {
	raw, found, err := tx.Get(bucket, key)
	if err != nil || !found {
		return found, err
	}
	if err = json.Unmarshal(raw, value); err != nil {
		return false, fmt.Errorf("social: corrupt %s: %w", bucket, err)
	}
	return true, nil
}
func save(tx Tx, bucket, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Put(bucket, key, raw)
}
func remove(tx Tx, bucket, key string) error { _, err := tx.Delete(bucket, key); return err }
func numberKey(id uint64) string             { return fmt.Sprintf("%020d", id) }
func seasonKey(guild, season uint64) string  { return numberKey(guild) + ":" + numberKey(season) }
func accountPrefix(account string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(account)) + ":"
}
func applicationKey(guild uint64, account string) string {
	return accountPrefix(account) + numberKey(guild)
}
func saveApplication(tx Tx, app Application) error {
	if err := save(tx, applicationBucket, applicationKey(app.GuildID, app.Applicant.AccountID), app); err != nil {
		return err
	}
	return save(tx, applicationGuildBucket, numberKey(app.GuildID)+":"+accountPrefix(app.Applicant.AccountID), app)
}
func removeApplication(tx Tx, guild uint64, account string) error {
	if err := remove(tx, applicationBucket, applicationKey(guild, account)); err != nil {
		return err
	}
	return remove(tx, applicationGuildBucket, numberKey(guild)+":"+accountPrefix(account))
}
func memberBestKey(guild, season uint64, account string) string {
	return seasonKey(guild, season) + ":" + account
}
func operationDigest(op Operation) (string, error) {
	raw, err := json.Marshal(op)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:]), nil
}
func newGuildID() (uint64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := binary.LittleEndian.Uint64(raw[:]) & math.MaxInt64
	if id == 0 {
		id = 1
	}
	return id, nil
}
func readAccount(tx Tx, id string) (Account, error) {
	a := Account{}
	_, err := load(tx, accountBucket, id, &a)
	a.AccountID = id
	if a.Bans == nil {
		a.Bans = map[uint64]int64{}
	}
	if a.SeasonLocks == nil {
		a.SeasonLocks = map[string]int64{}
	}
	if a.Actions == nil {
		a.Actions = map[uint64]Action{}
	}
	return a, err
}
func readGuild(tx Tx, id uint64) (Guild, error) {
	g := Guild{}
	found, err := load(tx, guildBucket, numberKey(id), &g)
	if err != nil {
		return g, err
	}
	if !found {
		return g, fmt.Errorf("%w: guild %d", ErrNotFound, id)
	}
	if g.ID != id || g.Members == nil || g.Reserved == nil {
		return g, fmt.Errorf("%w: saved guild %d is record %d with members=%t reserved=%t", ErrInvalid, id, g.ID, g.Members != nil, g.Reserved != nil)
	}
	if g.Supporters == nil {
		g.Supporters = map[string]SupporterSlot{}
	}
	return g, nil
}
func readReservation(tx Tx, id string) (reservationRecord, bool, error) {
	r := reservationRecord{}
	ok, err := load(tx, operationBucket, id, &r)
	if err == nil && ok {
		digest, e := operationDigest(r.Operation)
		if e != nil {
			return r, false, e
		}
		if r.Operation.ID != id || r.Reservation.ID != id || r.Reservation.Digest != digest || r.Reservation.ActorAccount != r.Operation.Actor.AccountID || r.Reservation.Kind != r.Operation.Kind || r.Reservation.Status != Prepared && r.Reservation.Status != Confirmed && r.Reservation.Status != Cancelled {
			return r, false, fmt.Errorf("%w: saved reservation %q does not match its operation", ErrInvalid, id)
		}
	}
	return r, ok, err
}
func hoursAfter(at int64, hours uint64) (int64, error) {
	if hours > uint64(math.MaxInt64/(60*60*1000)) || at > math.MaxInt64-int64(hours)*60*60*1000 {
		return 0, fmt.Errorf("%w: %d hours after %d overflows", ErrInvalid, hours, at)
	}
	return at + int64(hours)*60*60*1000, nil
}
func memberLocked(a Account, at int64) bool {
	for _, until := range a.SeasonLocks {
		if at < until {
			return true
		}
	}
	return false
}
func memberPermission(g Guild, actor, target string) bool {
	a, ok := g.Members[actor]
	if !ok || actor == target {
		return false
	}
	b, ok := g.Members[target]
	if !ok {
		return false
	}
	return a.Role == Admin && b.Role != Admin || a.Role == SubAdmin && b.Role == Member
}

func validText(s string, units int) bool {
	return utf8.ValidString(s) && len(utf16.Encode([]rune(s))) <= units
}
func validName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" {
		return false
	}
	count := 0
	for _, r := range name {
		switch {
		case r >= 0xac00 && r <= 0xd7a3, r >= 0x4e00 && r <= 0x9fff, r >= 0x3040 && r <= 0x30fc:
			count += 2
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', unicode.IsSpace(r):
			count++
		default:
			return false
		}
	}
	return count >= 4 && count <= 12
}
func (o *Owner) validDetails(d CreateDetails) bool {
	if d.JoinType < OpenJoin || d.JoinType > ClosedJoin || d.Icon > math.MaxInt32 || !o.design.Guild.Flags[d.Icon%100000] || !o.design.Guild.Emblems[d.Icon/100000] || !validText(d.Message, 30) {
		return false
	}
	if len(d.IconColor) != 9 || d.IconColor[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(d.IconColor[1:])
	return err == nil
}

func (o *Owner) Prepare(ctx context.Context, op Operation) (Reservation, error) {
	if op.ID == "" || len(op.ID) > 256 || op.Actor.AccountID == "" || len(op.Actor.AccountID) > 256 || op.Actor.OwnerIndex == 0 || op.AtMS <= 0 {
		return Reservation{}, fmt.Errorf("%w: operation %q by %q (owner %d) at %d is incomplete", ErrInvalid, op.ID, op.Actor.AccountID, op.Actor.OwnerIndex, op.AtMS)
	}
	digest, err := operationDigest(op)
	if err != nil {
		return Reservation{}, err
	}
	var out Reservation
	err = o.store.Update(ctx, func(tx Tx) error {
		prior, found, e := readReservation(tx, op.ID)
		if e != nil {
			return e
		}
		if found {
			if prior.Reservation.Digest != digest {
				return fmt.Errorf("%w: operation %q was replayed with a different body", ErrConflict, op.ID)
			}
			out = prior.Reservation
			return nil
		}
		subject := op.Actor.AccountID
		if op.Kind == Accept || op.Kind == Ban || op.Kind == ChangeRole || op.Kind == Refuse {
			subject = op.TargetAccount
		}
		if subject == "" || len(subject) > 256 {
			return fmt.Errorf("%w: operation %q subject %q", ErrInvalid, op.ID, subject)
		}
		a, e := readAccount(tx, subject)
		if e != nil {
			return e
		}
		if a.PendingOperation != "" {
			return fmt.Errorf("%w: account %s has pending operation %q", ErrPending, subject, a.PendingOperation)
		}
		if a.Epoch != op.ExpectedMembershipEpoch {
			return fmt.Errorf("%w: account %s membership epoch %d, operation expects %d", ErrConflict, subject, a.Epoch, op.ExpectedMembershipEpoch)
		}
		r := Reservation{ID: op.ID, Kind: op.Kind, ActorAccount: op.Actor.AccountID, SubjectAccount: subject, Digest: digest, RequestDigest: op.RequestDigest, Status: Prepared, GuildID: op.GuildID, MembershipEpoch: a.Epoch, PreparedMS: op.AtMS, Raid: op.Raid}
		var g Guild
		if op.Kind == SupporterClaim {
			r.GuildID = 0
			if e := o.prepareSupporterClaim(tx, op, &r); e != nil {
				return e
			}
		} else if op.Kind == Acknowledge || op.Kind == RaidClaim {
			if op.Kind == RaidClaim {
				r.GuildID = 0
				for after := ""; ; {
					rows, e := tx.Scan(entitlementBucket, accountPrefix(subject), after, 256)
					if e != nil {
						return e
					}
					for _, row := range rows {
						var ent Entitlement
						if e = json.Unmarshal(row.Value, &ent); e != nil {
							return e
						}
						if !ent.Claimed {
							r.EntitlementIDs = append(r.EntitlementIDs, row.Key)
							r.Grants = append(r.Grants, ent.Rewards...)
						}
					}
					if len(rows) < 256 {
						break
					}
					after = rows[len(rows)-1].Key
				}
				if len(r.EntitlementIDs) == 0 {
					return fmt.Errorf("%w: account %s has no guild creation entitlement", ErrIneligible, subject)
				}
			}
			for _, kind := range op.ActionTypes {
				if kind < 1 || kind > 9 {
					return fmt.Errorf("%w: guild action type %d, want 1..9", ErrInvalid, kind)
				}
			}
			if op.Kind == Acknowledge {
				r.GuildID = a.GuildID
			}
			if r.GuildID != 0 {
				g, e = readGuild(tx, a.GuildID)
				if e != nil {
					return e
				}
			}
		} else if op.Kind == Create {
			if a.GuildID != 0 || op.AtMS < a.LeaveUntilMS || !validName(op.Create.Name) || !o.validDetails(op.Create) {
				return fmt.Errorf("%w: account %s cannot create guild %q (guild %d, leave until %d, at %d)", ErrIneligible, subject, op.Create.Name, a.GuildID, a.LeaveUntilMS, op.AtMS)
			}
			key := cases.Fold().String(norm.NFKC.String(op.Create.Name))
			var reserved uint64
			if found, e := load(tx, nameBucket, key, &reserved); e != nil {
				return e
			} else if found {
				return fmt.Errorf("%w: guild name %q is taken", ErrConflict, op.Create.Name)
			}
			id, e := newGuildID()
			if e != nil {
				return e
			}
			if _, found, e := tx.Get(guildBucket, numberKey(id)); e != nil {
				return e
			} else if found {
				return fmt.Errorf("%w: guild %d already exists", ErrConflict, id)
			}
			g = Guild{ID: id, Name: op.Create.Name, NameKey: key, Message: op.Create.Message, IconColor: op.Create.IconColor, Icon: op.Create.Icon, JoinType: op.Create.JoinType, Revision: 1, CreatedMS: op.AtMS, UpdatedMS: op.AtMS, PendingOperation: op.ID, Members: map[string]Membership{}, Reserved: map[string]bool{op.ID: true}, Supporters: map[string]SupporterSlot{}}
			r.GuildID = id
			r.Charges = []gamedata.Reward{o.design.Guild.CreateCost}
			if e = save(tx, nameBucket, key, id); e != nil {
				return e
			}
		} else {
			g, e = readGuild(tx, op.GuildID)
			if e != nil {
				return e
			}
			if g.PendingOperation != "" || g.PendingManagement != "" || g.DeleteAtMS != 0 && op.Kind != CancelDelete {
				return fmt.Errorf("%w: guild %d is busy (operation %q, management %q, delete at %d)", ErrIneligible, g.ID, g.PendingOperation, g.PendingManagement, g.DeleteAtMS)
			}
			switch op.Kind {
			case Attendance:
				if a.GuildID != g.ID || g.Members[subject].Epoch != a.Epoch || a.AttendanceDay == op.Day {
					return fmt.Errorf("%w: account %s cannot attend guild %d on %s", ErrIneligible, subject, g.ID, op.Day)
				}
				if _, e := time.Parse("2006-01-02", op.Day); e != nil {
					return fmt.Errorf("%w: attendance day %q", ErrInvalid, op.Day)
				}
				r.Grants = []gamedata.Reward{o.design.Guild.Attendance}
			case Edit, Rename, Notice:
				actor, ok := g.Members[op.Actor.AccountID]
				if !ok || actor.Role != Admin && actor.Role != SubAdmin {
					return fmt.Errorf("%w: %s is not an administrator of guild %d", ErrPermission, op.Actor.AccountID, g.ID)
				}
				if op.Kind == Rename && actor.Role != Admin {
					return fmt.Errorf("%w: only the guild master renames guild %d", ErrPermission, g.ID)
				}
				if op.Kind == Rename {
					if !validName(op.Text) {
						return fmt.Errorf("%w: guild name %q", ErrInvalid, op.Text)
					}
					key := cases.Fold().String(norm.NFKC.String(op.Text))
					var id uint64
					if found, e := load(tx, nameBucket, key, &id); e != nil {
						return e
					} else if found && id != g.ID {
						return fmt.Errorf("%w: guild name %q belongs to guild %d", ErrConflict, op.Text, id)
					}
					if e = save(tx, nameBucket, key, g.ID); e != nil {
						return e
					}
					r.Charges = []gamedata.Reward{o.design.Guild.RenameCost}
				} else if op.Kind == Edit {
					if !o.validDetails(op.Create) {
						return fmt.Errorf("%w: guild %d details %+v", ErrInvalid, g.ID, op.Create)
					}
				} else if !validText(op.Text, 500) {
					return fmt.Errorf("%w: guild notice is longer than 500 characters or invalid", ErrInvalid)
				}
				g.PendingManagement = op.ID
			case SupporterAdd, SupporterRemove:
				if a.GuildID != g.ID {
					return fmt.Errorf("%w: account %s is in guild %d, not %d", ErrIneligible, subject, a.GuildID, g.ID)
				}
				if op.Supporter.Slot > 1 || op.Supporter.AccountID != subject {
					return fmt.Errorf("%w: supporter slot %d for %q by %q", ErrInvalid, op.Supporter.Slot, op.Supporter.AccountID, subject)
				}
				if op.Kind == SupporterAdd && (op.Supporter.OwnerIndex != op.Actor.OwnerIndex || op.Supporter.Bundle == "" || op.Supporter.Version == "" || op.Supporter.CharacterIndex == 0 || op.Supporter.CostumeIndex == 0) {
					return fmt.Errorf("%w: supporter %+v is incomplete", ErrInvalid, op.Supporter)
				}
				if op.Kind == SupporterAdd {
					for _, slot := range g.Supporters {
						if slot.AccountID == subject && slot.Slot != op.Supporter.Slot && slot.CharacterIndex == op.Supporter.CharacterIndex {
							return fmt.Errorf("%w: character %d already supports guild %d in slot %d", ErrConflict, slot.CharacterIndex, g.ID, slot.Slot)
						}
					}
				}
				if op.Kind == SupporterRemove {
					if _, ok := g.Supporters[accountPrefix(subject)+numberKey(op.Supporter.Slot)]; !ok {
						return fmt.Errorf("%w: %s has no supporter in slot %d", ErrNotFound, subject, op.Supporter.Slot)
					}
				}
			case CancelDelete:
				member, ok := g.Members[op.Actor.AccountID]
				if !ok || member.Role != Admin {
					return fmt.Errorf("%w: only the guild master cancels the deletion of guild %d", ErrPermission, g.ID)
				}
				if g.DeleteAtMS == 0 || op.AtMS >= g.DeleteAtMS {
					return fmt.Errorf("%w: guild %d is not being deleted at %d (delete at %d)", ErrIneligible, g.ID, op.AtMS, g.DeleteAtMS)
				}
			case CancelJoin, Refuse:
				if op.Kind == Refuse {
					actor, ok := g.Members[op.Actor.AccountID]
					if !ok || actor.Role != Admin && actor.Role != SubAdmin {
						return fmt.Errorf("%w: %s is not an administrator of guild %d", ErrPermission, op.Actor.AccountID, g.ID)
					}
				}
				var app Application
				found, e := load(tx, applicationBucket, applicationKey(g.ID, subject), &app)
				if e != nil {
					return e
				}
				if !found {
					return fmt.Errorf("%w: no application from %s to guild %d", ErrNotFound, subject, g.ID)
				}
			case Join, Accept:
				if a.GuildID != 0 || op.AtMS < a.LeaveUntilMS || op.AtMS < a.Bans[g.ID] {
					return fmt.Errorf("%w: account %s cannot join guild %d (guild %d, leave until %d, banned until %d, at %d)", ErrIneligible, subject, g.ID, a.GuildID, a.LeaveUntilMS, a.Bans[g.ID], op.AtMS)
				}
				if uint64(len(g.Members)+len(g.Reserved)) >= o.design.Guild.MaxMembers {
					return fmt.Errorf("%w: guild %d has %d members and %d reservations", ErrCapacity, g.ID, len(g.Members), len(g.Reserved))
				}
				if op.Kind == Join {
					if g.JoinType != OpenJoin && g.JoinType != ApplicationJoin {
						return fmt.Errorf("%w: guild %d join type %v", ErrIneligible, g.ID, g.JoinType)
					}
					r.Application = g.JoinType == ApplicationJoin
					if r.Application {
						if e = o.checkApplications(tx, subject, g.ID, op.AtMS); e != nil {
							return e
						}
					}
				} else {
					m, ok := g.Members[op.Actor.AccountID]
					if !ok || m.Role != Admin && m.Role != SubAdmin {
						return fmt.Errorf("%w: %s is not an administrator of guild %d", ErrPermission, op.Actor.AccountID, g.ID)
					}
					var app Application
					found, e := load(tx, applicationBucket, applicationKey(g.ID, subject), &app)
					if e != nil {
						return e
					}
					if !found || app.ExpiresMS <= op.AtMS {
						return fmt.Errorf("%w: application from %s to guild %d is missing or expired (present=%t, expires %d, at %d)", ErrIneligible, subject, g.ID, found, app.ExpiresMS, op.AtMS)
					}
				}
				if !r.Application {
					g.Reserved[op.ID] = true
				}
			case Leave, Ban, ChangeRole:
				if a.GuildID != g.ID {
					return fmt.Errorf("%w: account %s is in guild %d, not %d", ErrIneligible, subject, a.GuildID, g.ID)
				}
				m, ok := g.Members[subject]
				if !ok {
					return fmt.Errorf("%w: %s is not a member of guild %d", ErrIneligible, subject, g.ID)
				}
				if op.Kind == Leave || op.Kind == Ban {
					if memberLocked(a, op.AtMS) {
						return fmt.Errorf("%w: %s is season locked at %d", ErrSeasonLocked, subject, op.AtMS)
					}
				}
				if op.Kind == Leave && m.Role == Admin && (len(g.Members) != 1 || len(g.Reserved) != 0) {
					return fmt.Errorf("%w: the master of guild %d cannot leave while %d members and %d reservations remain", ErrIneligible, g.ID, len(g.Members), len(g.Reserved))
				}
				if op.Kind == Ban && !memberPermission(g, op.Actor.AccountID, subject) {
					return fmt.Errorf("%w: %s cannot ban %s from guild %d", ErrPermission, op.Actor.AccountID, subject, g.ID)
				}
				if op.Kind == ChangeRole {
					actor, ok := g.Members[op.Actor.AccountID]
					if !ok || actor.Role != Admin || op.Role < Admin || op.Role > Member || op.Actor.AccountID == subject {
						return fmt.Errorf("%w: %s cannot give %s role %v in guild %d", ErrPermission, op.Actor.AccountID, subject, op.Role, g.ID)
					}
					if m.Role == Admin {
						return fmt.Errorf("%w: %s is the master of guild %d", ErrPermission, subject, g.ID)
					}
				}
			case RaidEnter:
				if a.GuildID != g.ID || op.Season == 0 || op.Day == "" || len(op.Day) > 32 {
					return fmt.Errorf("%w: %s cannot enter guild %d raid season %d on %q (guild %d)", ErrIneligible, subject, g.ID, op.Season, op.Day, a.GuildID)
				}
				member, ok := g.Members[subject]
				if !ok || member.Epoch != a.Epoch {
					return fmt.Errorf("%w: %s membership in guild %d does not match epoch %d", ErrIneligible, subject, g.ID, a.Epoch)
				}
				var season Season
				found, e := load(tx, seasonBucket, seasonKey(g.ID, op.Season), &season)
				if e != nil {
					return e
				}
				if !found || season.State != "active" || op.AtMS < season.StartsMS || op.AtMS >= season.EndsMS {
					return fmt.Errorf("%w: guild %d raid season %d is not open at %d (present=%t, state %q, %d..%d)", ErrIneligible, g.ID, op.Season, op.AtMS, found, season.State, season.StartsMS, season.EndsMS)
				}
				if op.UseSupporter {
					if e = o.reserveSupporter(tx, op, g.ID, 1); e != nil {
						return e
					}
				}
				if season.Pending == math.MaxUint64 {
					return fmt.Errorf("%w: guild %d raid season %d pending count overflows", ErrInvalid, g.ID, op.Season)
				}
				season.Pending++
				if e = save(tx, seasonBucket, seasonKey(g.ID, op.Season), season); e != nil {
					return e
				}
				entry := RaidEntry{OperationID: op.ID, AccountID: subject, Digest: digest, Participant: member.PublicMember, GuildID: g.ID, MembershipEpoch: a.Epoch, Season: op.Season, AtMS: op.AtMS, Day: op.Day, UseSupporter: op.UseSupporter, Status: "prepared"}
				if op.Raid != nil {
					entry.Supporters = op.Raid.Supporters
				}
				if e = save(tx, raidEntryBucket, op.ID, entry); e != nil {
					return e
				}
			default:
				return fmt.Errorf("%w: unknown guild operation kind %v", ErrInvalid, op.Kind)
			}
			g.PendingManagement = op.ID
		}
		a.PendingOperation = op.ID
		if e = save(tx, accountBucket, subject, a); e != nil {
			return e
		}
		if g.ID != 0 {
			if e = save(tx, guildBucket, numberKey(g.ID), g); e != nil {
				return e
			}
		}
		if g.ID != 0 {
			view, err := projectGuild(tx, g, a, op, r)
			if err != nil {
				return err
			}
			r.View = &view
		}
		out = r
		if e = save(tx, pendingBucket, op.ID, true); e != nil {
			return e
		}
		return save(tx, operationBucket, op.ID, reservationRecord{Operation: op, Reservation: r})
	})
	return out, err
}

func (o *Owner) checkApplications(tx Tx, account string, guild uint64, at int64) error {
	var count uint64
	for after := ""; ; {
		rows, err := tx.Scan(applicationBucket, accountPrefix(account), after, 256)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var app Application
			if err = json.Unmarshal(row.Value, &app); err != nil {
				return err
			}
			if app.Applicant.AccountID == account && app.GuildID != guild && app.ExpiresMS > at {
				count++
			}
		}
		if len(rows) < 256 {
			break
		}
		after = rows[len(rows)-1].Key
	}
	if count >= o.design.Guild.ApplicationLimit {
		return fmt.Errorf("%w: %s has %d open guild applications", ErrCapacity, account, count)
	}
	return nil
}

func (o *Owner) Confirm(ctx context.Context, proof CommitProof) (Reservation, error) {
	var out Reservation
	err := o.store.Update(ctx, func(tx Tx) error {
		record, found, err := readReservation(tx, proof.OperationID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: guild operation %q", ErrNotFound, proof.OperationID)
		}
		r, op := record.Reservation, record.Operation
		if r.ActorAccount != proof.ActorAccount || r.Digest != proof.Digest {
			return fmt.Errorf("%w: guild operation %q proof does not match its reservation", ErrConflict, proof.OperationID)
		}
		if r.Status != Prepared {
			out = r
			if r.Status == Cancelled {
				return fmt.Errorf("%w: guild operation %q was cancelled", ErrConflict, r.ID)
			}
			return nil
		}
		a, err := readAccount(tx, r.SubjectAccount)
		if err != nil {
			return err
		}
		if a.PendingOperation != r.ID || a.Epoch != r.MembershipEpoch {
			return fmt.Errorf("%w: account %s pending %q epoch %d, reservation %q epoch %d", ErrConflict, r.SubjectAccount, a.PendingOperation, a.Epoch, r.ID, r.MembershipEpoch)
		}
		var g Guild
		if r.GuildID != 0 {
			g, err = readGuild(tx, r.GuildID)
			if err != nil {
				return err
			}
		}
		switch r.Kind {
		case SupporterClaim:
			if err := o.confirmSupporterClaim(tx, op); err != nil {
				return err
			}
		case RaidClaim:
			for _, key := range r.EntitlementIDs {
				var ent Entitlement
				found, e := load(tx, entitlementBucket, key, &ent)
				if e != nil {
					return e
				}
				if !found || ent.AccountID != r.SubjectAccount || ent.Claimed {
					return fmt.Errorf("%w: entitlement %q of %s is missing, foreign or claimed", ErrConflict, key, r.SubjectAccount)
				}
				ent.Claimed = true
				if e = save(tx, entitlementBucket, key, ent); e != nil {
					return e
				}
			}
		case Attendance:
			a.AttendanceDay = op.Day
		case Acknowledge:
			for _, kind := range op.ActionTypes {
				action, found := a.Actions[kind]
				if !found {
					continue
				}
				action.Notify = false
				a.Actions[kind] = action
			}
		case Edit:
			g.Icon, g.IconColor, g.Message, g.JoinType = op.Create.Icon, op.Create.IconColor, op.Create.Message, op.Create.JoinType
			g.PendingManagement = ""
		case Notice:
			g.Notice, g.NoticeUpdatedMS = op.Text, op.AtMS
			g.PendingManagement = ""
		case Rename:
			key := cases.Fold().String(norm.NFKC.String(op.Text))
			if key != g.NameKey {
				if err = remove(tx, nameBucket, g.NameKey); err != nil {
					return err
				}
			}
			g.Name, g.NameKey = op.Text, key
			g.PendingManagement = ""
		case SupporterAdd:
			g.Supporters[accountPrefix(r.SubjectAccount)+numberKey(op.Supporter.Slot)] = op.Supporter
		case SupporterRemove:
			delete(g.Supporters, accountPrefix(r.SubjectAccount)+numberKey(op.Supporter.Slot))
		case CancelDelete:
			if err = remove(tx, deletionBucket, deletionKey(g.DeleteAtMS, g.ID)); err != nil {
				return err
			}
			g.DeleteAtMS = 0
		case CancelJoin, Refuse:
			if err = removeApplication(tx, g.ID, r.SubjectAccount); err != nil {
				return err
			}
		case Create, Join, Accept:
			if r.Application {
				until, e := hoursAfter(op.AtMS, o.design.Guild.ApplicationHours)
				if e != nil {
					return e
				}
				if err = saveApplication(tx, Application{GuildID: g.ID, Applicant: op.Actor, CreatedMS: op.AtMS, ExpiresMS: until}); err != nil {
					return err
				}
			} else {
				if !g.Reserved[r.ID] || a.GuildID != 0 || a.Epoch == math.MaxUint64 {
					return fmt.Errorf("%w: guild %d join %q no longer applies to %s (guild %d)", ErrConflict, g.ID, r.ID, r.SubjectAccount, a.GuildID)
				}
				profile := op.Actor
				if r.Kind == Accept {
					var app Application
					if _, err = load(tx, applicationBucket, applicationKey(g.ID, r.SubjectAccount), &app); err != nil {
						return err
					}
					profile = app.Applicant
				}
				a.GuildID = g.ID
				a.Epoch++
				role := Member
				if r.Kind == Create {
					role = Admin
					g.PendingOperation = ""
				}
				g.Members[r.SubjectAccount] = Membership{PublicMember: profile, Role: role, Epoch: a.Epoch, JoinedMS: op.AtMS}
				delete(g.Reserved, r.ID)
				if err = clearApplications(tx, r.SubjectAccount); err != nil {
					return err
				}
			}
		case Leave, Ban:
			if r.Kind == Leave && g.Members[r.SubjectAccount].Role == Admin {
				if g.DeleteAtMS, err = hoursAfter(op.AtMS, o.design.Guild.DeleteDelayHours); err != nil {
					return err
				}
				if err = save(tx, deletionBucket, deletionKey(g.DeleteAtMS, g.ID), g.ID); err != nil {
					return err
				}
			} else {
				if a.Epoch == math.MaxUint64 {
					return fmt.Errorf("%w: account %s membership epoch overflows", ErrInvalid, r.SubjectAccount)
				}
				delete(g.Members, r.SubjectAccount)
				for key, slot := range g.Supporters {
					if slot.AccountID == r.SubjectAccount {
						delete(g.Supporters, key)
					}
				}
				a.GuildID = 0
				a.Epoch++
				until, e := hoursAfter(op.AtMS, o.design.Guild.LeaveDelayHours)
				if e != nil {
					return e
				}
				if r.Kind == Ban {
					a.Bans[g.ID] = until
				} else {
					a.LeaveUntilMS = until
				}
			}
		case ChangeRole:
			m := g.Members[r.SubjectAccount]
			m.Role = op.Role
			g.Members[r.SubjectAccount] = m
			if op.Role == Admin {
				m = g.Members[op.Actor.AccountID]
				m.Role = Member
				g.Members[op.Actor.AccountID] = m
			}
		case RaidEnter:
			var season Season
			if _, err = load(tx, seasonBucket, seasonKey(g.ID, op.Season), &season); err != nil {
				return err
			}
			a.SeasonLocks[seasonKey(g.ID, op.Season)] = season.EndsMS
			var entry RaidEntry
			if _, err = load(tx, raidEntryBucket, r.ID, &entry); err != nil {
				return err
			}
			entry.Status = "confirmed"
			if err = save(tx, raidPendingBucket, r.ID, true); err != nil {
				return err
			}
			if err = save(tx, raidEntryBucket, r.ID, entry); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown guild operation kind %v", ErrInvalid, r.Kind)
		}
		a.PendingOperation = ""
		if g.PendingManagement == r.ID {
			g.PendingManagement = ""
		}
		if kind := actionType(r.Kind); kind != 0 {
			a.Actions[kind] = Action{Type: kind, AtMS: op.AtMS, GuildID: g.ID, GuildName: g.Name, Notify: true, Role: g.Members[r.SubjectAccount].Role}
		}
		g.Revision++
		g.UpdatedMS = op.AtMS
		r.Status = Confirmed
		r.MembershipEpoch = a.Epoch
		if g.ID != 0 {
			snapshot := g
			r.View = &snapshot
		}
		if err = save(tx, accountBucket, r.SubjectAccount, a); err != nil {
			return err
		}
		if g.ID != 0 {
			if err = save(tx, guildBucket, numberKey(g.ID), g); err != nil {
				return err
			}
		}
		record.Reservation = r
		out = r
		if err = remove(tx, pendingBucket, r.ID); err != nil {
			return err
		}
		return save(tx, operationBucket, r.ID, record)
	})
	return out, err
}

func (o *Owner) Cancel(ctx context.Context, proof CommitProof) (Reservation, error) {
	var out Reservation
	err := o.store.Update(ctx, func(tx Tx) error {
		record, found, err := readReservation(tx, proof.OperationID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: guild operation %q", ErrNotFound, proof.OperationID)
		}
		r, op := record.Reservation, record.Operation
		if r.ActorAccount != proof.ActorAccount || r.Digest != proof.Digest {
			return fmt.Errorf("%w: guild operation %q proof does not match its reservation", ErrConflict, proof.OperationID)
		}
		if r.Status != Prepared {
			out = r
			return nil
		}
		a, err := readAccount(tx, r.SubjectAccount)
		if err != nil {
			return err
		}
		if a.PendingOperation != r.ID {
			return fmt.Errorf("%w: account %s pending %q, reservation %q", ErrConflict, r.SubjectAccount, a.PendingOperation, r.ID)
		}
		var g Guild
		if r.GuildID != 0 {
			g, err = readGuild(tx, r.GuildID)
			if err != nil {
				return err
			}
		}
		delete(g.Reserved, r.ID)
		if g.PendingManagement == r.ID {
			g.PendingManagement = ""
		}
		if r.Kind == Rename {
			key := cases.Fold().String(norm.NFKC.String(op.Text))
			if key != g.NameKey {
				if err = remove(tx, nameBucket, key); err != nil {
					return err
				}
			}
		}
		a.PendingOperation = ""
		if r.Kind == Create {
			if err = remove(tx, nameBucket, g.NameKey); err != nil {
				return err
			}
			if err = remove(tx, guildBucket, numberKey(g.ID)); err != nil {
				return err
			}
		} else if g.ID != 0 {
			if err = save(tx, guildBucket, numberKey(g.ID), g); err != nil {
				return err
			}
		}
		if r.Kind == RaidEnter {
			var season Season
			if _, err = load(tx, seasonBucket, seasonKey(g.ID, op.Season), &season); err != nil {
				return err
			}
			if season.Pending == 0 {
				return fmt.Errorf("%w: guild %d raid season %d has no pending entries", ErrInvalid, g.ID, op.Season)
			}
			season.Pending--
			if err = save(tx, seasonBucket, seasonKey(g.ID, op.Season), season); err != nil {
				return err
			}
			if op.UseSupporter {
				if err = o.reserveSupporter(tx, op, g.ID, -1); err != nil {
					return err
				}
			}
			if err = remove(tx, raidEntryBucket, r.ID); err != nil {
				return err
			}
			if err = remove(tx, raidPendingBucket, r.ID); err != nil {
				return err
			}
		}
		if err = save(tx, accountBucket, r.SubjectAccount, a); err != nil {
			return err
		}
		r.Status = Cancelled
		record.Reservation = r
		out = r
		if err = remove(tx, pendingBucket, r.ID); err != nil {
			return err
		}
		return save(tx, operationBucket, r.ID, record)
	})
	return out, err
}

func actionType(kind Kind) uint64 {
	switch kind {
	case Create:
		return 1
	case Join:
		return 2
	case Leave:
		return 3
	case Ban:
		return 4
	case Accept:
		return 5
	case Refuse:
		return 6
	case Edit, Notice, Rename:
		return 7
	case ChangeRole:
		return 8
	}
	return 0
}

func (o *Owner) reserveSupporter(tx Tx, op Operation, guild uint64, change int) error {
	key := numberKey(guild) + ":" + strconv.Quote(op.Actor.AccountID) + ":" + op.Day
	var count uint64
	if _, err := load(tx, usageBucket, key, &count); err != nil {
		return err
	}
	if change > 0 {
		if count >= o.design.Raid.DailySupporterUses {
			return fmt.Errorf("%w: %s used %d guild supporters on %s", ErrCapacity, op.Actor.AccountID, count, op.Day)
		}
		count++
	} else {
		if count == 0 {
			return fmt.Errorf("%w: %s has no guild supporter use to return on %s", ErrInvalid, op.Actor.AccountID, op.Day)
		}
		count--
	}
	return save(tx, usageBucket, key, count)
}
