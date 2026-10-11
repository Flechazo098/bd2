// Package room owns native multiplayer membership and transient gameplay state.
package room

import (
	"context"
	"errors"
	"time"
)

type Kind string

const (
	Action   Kind = "action"
	Defense  Kind = "defense"
	Fishing  Kind = "fishing"
	Matching Kind = "matching"
)

var (
	ErrClosed       = errors.New("room service closed")
	ErrMailboxFull  = errors.New("room mailbox full")
	ErrUnauthorized = errors.New("room authority denied")
	ErrNotFound     = errors.New("room not found")
	ErrNotReady     = errors.New("room result not accepted")
	ErrTextRejected = errors.New("room text rejected")
)

type Principal struct {
	AccountID  string
	OwnerIndex int64
	SessionID  string
	Generation uint64
}

type Connection struct {
	ID        string
	Principal Principal
	Channel   Kind
}

type Frame struct {
	Type       int32
	Sequence   int32
	OwnerIndex int64
	Payload    []byte
	UDP        bool
}

type Delivery struct {
	ConnectionID string
	Frame        Frame
}

// Profile is an immutable projection of committed player state.
type Profile struct {
	UserInfo      []byte
	AvatarUseInfo []byte
	RodID         int64
	BoatLevel     int64
	BoatSkinID    int64
	FishingMaps   []uint64
}

type Selection struct {
	Kind            Kind
	EventScheduleID int64
	CharacterID     int64
	MonsterID       int64
	GroupID         int64
	StageID         int64
	MapID           int64
	BoatLevel       int64
	BoatSkinID      int64
	Capacity        int
	DefenseLastWave int64
	Solo            bool
	LoadingLimit    time.Duration
	Duration        time.Duration
	ReconnectWindow time.Duration
}

type Rules interface {
	Profile(context.Context, Principal) (Profile, error)
	ValidateSelection(Profile, Selection) (Selection, error)
}

type TextRules interface {
	CheckRoomText(context.Context, Principal, string) error
}

type ParticipantRules interface {
	ValidateParticipant(Profile, Selection) error
}

type Config struct {
	AdvertisedHost  string
	TCPPort         int
	UDPPort         int
	Region          int
	MaxRooms        int
	QueueCommands   int
	QueueBytes      int64
	ReconnectWindow time.Duration
	LoadingLimit    time.Duration
	MatchingLimit   time.Duration
	RoomDuration    time.Duration
	ResultRetention time.Duration
	Now             func() time.Time
	// Publish must enqueue without blocking; false drops a slow connection.
	Publish func([]Delivery)
	// PublishResult queues a terminal projection. Acknowledgment follows durable commit.
	PublishResult func(FinalResult) bool
}

type MemberView struct {
	Principal   Principal
	Profile     Profile
	CharacterID int64
	Ready       bool
	ClientState int64
	Connected   bool
	RunGUID     string
	AcceptedRun bool
	NativeGUID  string
	Wave        int64
	Kills       int64
}

type View struct {
	RoomCode       string
	InstanceID     string
	Kind           Kind
	Selection      Selection
	MatchingType   int64
	RoomState      int64
	HostOwnerIndex int64
	HostEpoch      uint64
	StartTime      time.Time
	EndTime        time.Time
	Members        []MemberView
}

type FinalResult struct {
	View              View
	Clear             bool
	ScoreMilliseconds int64
	// Evidence is a room-owned snapshot, never an HTTP result supplied by a client.
	Evidence []byte
}
