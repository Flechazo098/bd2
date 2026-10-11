// Package social owns guild membership, reservations and guild-season records.
package social

import (
	"context"
	"errors"

	"bd2server/internal/server/design/gamedata"
)

var (
	ErrInvalid      = errors.New("social: invalid operation")
	ErrNotFound     = errors.New("social: not found")
	ErrConflict     = errors.New("social: operation identity conflict")
	ErrIneligible   = errors.New("social: ineligible")
	ErrCapacity     = errors.New("social: guild capacity reached")
	ErrPending      = errors.New("social: pending operation")
	ErrPermission   = errors.New("social: permission denied")
	ErrSeasonLocked = errors.New("social: season participation locks membership")
	ErrRejected     = errors.New("social: player transaction rejected without commit")
)

type Entry struct {
	Key   string
	Value []byte
}
type Tx interface {
	Get(bucket, key string) ([]byte, bool, error)
	Put(bucket, key string, value []byte) error
	Delete(bucket, key string) (bool, error)
	Scan(bucket, prefix, after string, limit int) ([]Entry, error)
}
type Store interface {
	Update(context.Context, func(Tx) error) error
	View(context.Context, func(Tx) error) error
}

type Role uint64

const (
	NoRole Role = iota
	Admin
	SubAdmin
	Member
)

type JoinType uint64

const (
	NoJoin JoinType = iota
	OpenJoin
	ApplicationJoin
	ClosedJoin
)

type Kind string

const (
	Create          Kind = "create"
	Join            Kind = "join"
	Accept          Kind = "accept"
	Leave           Kind = "leave"
	Ban             Kind = "ban"
	ChangeRole      Kind = "change_role"
	RaidEnter       Kind = "raid_enter"
	CancelDelete    Kind = "cancel_delete"
	CancelJoin      Kind = "cancel_join"
	Refuse          Kind = "refuse"
	Edit            Kind = "edit"
	Rename          Kind = "rename"
	Notice          Kind = "notice"
	Acknowledge     Kind = "acknowledge"
	SupporterAdd    Kind = "supporter_add"
	SupporterRemove Kind = "supporter_remove"
	Attendance      Kind = "attendance"
	RaidClaim       Kind = "raid_claim"
)

type Status string

const (
	Prepared  Status = "prepared"
	Confirmed Status = "confirmed"
	Cancelled Status = "cancelled"
)

type PublicMember struct {
	AccountID                             string `json:"account_id"`
	OwnerIndex                            uint64 `json:"owner_index"`
	Name                                  string `json:"name"`
	TitleID, PortraitID, PortraitDesignID uint64
	LastLoginMS                           int64
}
type Membership struct {
	PublicMember
	Role     Role
	Epoch    uint64
	JoinedMS int64
}
type Guild struct {
	ID                                uint64
	Name, NameKey, Message, IconColor string
	Icon                              uint64
	JoinType                          JoinType
	Revision                          uint64
	CreatedMS, UpdatedMS, DeleteAtMS  int64
	PendingOperation                  string
	Members                           map[string]Membership
	Reserved                          map[string]bool
	Notice                            string
	NoticeUpdatedMS                   int64
	PendingManagement                 string
	Supporters                        map[string]SupporterSlot
}
type Action struct {
	Type      uint64
	AtMS      int64
	GuildID   uint64
	GuildName string
	Notify    bool
	Role      Role
}
type Account struct {
	AccountID        string `json:"-"`
	GuildID, Epoch   uint64
	PendingOperation string
	LeaveUntilMS     int64
	Bans             map[uint64]int64
	SeasonLocks      map[string]int64
	Actions          map[uint64]Action
	AttendanceDay    string
}
type CreateDetails struct {
	Name, Message, IconColor string
	Icon                     uint64
	JoinType                 JoinType
}
type Operation struct {
	Raid                             *RaidTicket
	ID                               string
	Kind                             Kind
	Actor                            PublicMember
	TargetAccount                    string
	GuildID, ExpectedMembershipEpoch uint64
	Create                           CreateDetails
	Role                             Role
	Season                           uint64
	AtMS                             int64
	Day                              string
	UseSupporter                     bool
	RequestDigest                    string
	Text                             string
	ActionTypes                      []uint64
	Supporter                        SupporterSlot
}
type Reservation struct {
	Raid                         *RaidTicket
	ID                           string
	Kind                         Kind
	ActorAccount, SubjectAccount string
	Digest                       string
	Status                       Status
	GuildID, MembershipEpoch     uint64
	Charges                      []gamedata.Reward
	Grants                       []gamedata.Reward
	RequestDigest                string
	EntitlementIDs               []string
	Application                  bool
	PreparedMS                   int64
	View                         *Guild
}
type SupporterSlot struct {
	CharacterID                                               uint64
	AccountID                                                 string
	OwnerIndex, Slot, CharacterIndex, CostumeIndex, CostumeID uint64
	CostumeLevel, CostumeDesignID                             uint64
	Name                                                      string
	Bundle                                                    string
	Version                                                   string
	UseCount                                                  uint64
}
type reservationRecord struct {
	Operation   Operation
	Reservation Reservation
}

// CommitProof is produced only by the trusted committed-player outbox consumer.
type CommitProof struct{ OperationID, ActorAccount, Digest string }
type Application struct {
	GuildID              uint64
	Applicant            PublicMember
	CreatedMS, ExpiresMS int64
}
type Season struct {
	AchievedMS               int64
	GuildView                Guild
	GuildID, ID, BossGroupID uint64
	RulesVersion             string
	StartsMS, EndsMS         int64
	State                    string
	TotalScore               uint64
	Pending                  uint64
	FrozenMS                 int64
	FrozenMembers            []MemberBest
}
type RaidEntry struct {
	Supporters                       []SupporterSlot
	OperationID, AccountID, Digest   string
	Participant                      PublicMember
	GuildID, MembershipEpoch, Season uint64
	AtMS                             int64
	Day                              string
	UseSupporter                     bool
	Status                           string
}
type MemberBest struct {
	TitleID, PortraitID, PortraitDesignID uint64
	GuildID, Season                       uint64
	AccountID                             string
	OwnerIndex                            uint64
	Name                                  string
	Level, Score, Overkill                uint64
	ResultID                              string
}
type RaidResult struct {
	AtMS                                                     int64
	EntryID, AccountID, ResultID                             string
	GuildID, Season, MembershipEpoch, Level, Score, Overkill uint64
	RulesVersion                                             string
	Digest                                                   string
}

type GuildStanding struct {
	AchievedMS                    int64
	Guild                         Guild
	Season, Score, Overkill, Rank uint64
	TopPercent                    float64
	Members                       []MemberBest
}
type Entitlement struct {
	ID, AccountID   string
	GuildID, Season uint64
	Rewards         []gamedata.Reward
	Claimed         bool
}
