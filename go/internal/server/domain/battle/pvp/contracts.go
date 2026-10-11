// Package pvp owns player formations, frozen challenges and personal PVP entitlements.
package pvp

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

type Mode string

const (
	Mirror    Mode = "mirror"
	Colosseum Mode = "colosseum"
)

type Profile struct {
	OwnerID                                                string
	OwnerIndex, XP, PortraitCostume, PortraitDesign, Title uint64
	Name                                                   string
	Guild                                                  []byte
}

// FullSnapshot is resolved from authoritative own-player state. FullDeck uses
// the current mode's DeckFullInfo protocol; Contents preserves saved bindings.
type FullSnapshot struct {
	FullDeck []byte
	Contents [][]byte
}
type PublishedSnapshot struct {
	Profile                   Profile
	Mode                      Mode
	Season, Version, VP, Rank uint64
	TopPercent                float64
	Snapshot                  FullSnapshot
}
type CandidateQuery struct {
	Mode    Mode
	Season  uint64
	Owner   string
	VP      uint64
	Exclude []string
	Limit   int
}
type RankQuery struct {
	Mode         Mode
	Season       uint64
	Owner        string
	OwnerIndex   uint64
	Start, Limit int
}
type RankEntry struct {
	PublishedSnapshot
	Wins, Losses uint64
}
type ProgressQuery struct {
	Mode       Mode
	Season     uint64
	Owner      string
	PendingIDs []string
}
type PersonalProgress struct {
	Found                                                     bool
	VP, AttackCount, Wins, Losses, DefenseWins, DefenseLosses uint64
	AppliedIDs                                                []string
}
type HistoryQuery struct {
	Mode                Mode
	Season, BattleIndex uint64
	Owner               string
	Start, Limit        int
}
type SeasonEntitlement struct {
	ID                            string
	Mode                          Mode
	Season, Rank, VP, AttackCount uint64
	TopPercent                    float64
	Rewards                       []gamedata.BattleReward
}

// Source reads immutable shared views. Implementations must not wait for a
// player actor or mutate shared ownership while a player transaction is open.
type Source interface {
	Candidates(CandidateQuery) ([]PublishedSnapshot, error)
	Ranking(RankQuery) ([]RankEntry, error)
	Personal(ProgressQuery) (PersonalProgress, error)
	History(HistoryQuery) ([]BattleSettled, error)
	Entitlements(mode Mode, owner string, limit int) ([]SeasonEntitlement, error)
}
type FreezeRequest struct {
	Mode                      Mode
	Season                    uint64
	Deck, Contents            [][]byte
	AttackBless, DefenseBless []uint64
	Renew                     bool
	Previous                  FullSnapshot
}
type SnapshotBuilder interface {
	Freeze(command.Context, FreezeRequest) (FullSnapshot, error)
}
type BalanceFunc func(command.Context, uint64) (uint64, error)
type ProfileFunc func(command.Context) (Profile, error)

type SpendFunc func(command.Context, string, []gamedata.BattleReward) error
type GrantFunc func(command.Context, string, []gamedata.BattleReward) ([]byte, error)
type ScoreInput struct {
	Mode                                                                           Mode
	Season, AttackerVP, DefenderVP, AttackerRank, DefenderRank, Result, Multiplier uint64
}
type ScoreChange struct {
	Attacker, Defender int64
	Rewards            []gamedata.BattleReward
}
type ScorePolicy interface {
	Resolve(ScoreInput) (ScoreChange, error)
}
type Season struct {
	Regular                      bool
	Mode                         Mode
	ID, Start, End, CalculateEnd uint64
}

type DefensePublished struct {
	Version int
	Defense PublishedSnapshot
}
type BattleSettled struct {
	Version                                          int
	ChallengeID                                      string
	BattleIndex                                      uint64
	Mode                                             Mode
	Season                                           uint64
	Attacker, Defender                               PublishedSnapshot
	Result, Multiplier, Seed, FirstAttack, Timestamp uint64
	AttackerChange, DefenderChange                   int64
}
type SeasonClaimed struct {
	Version                int
	OwnerID, EntitlementID string
	Mode                   Mode
	Season                 uint64
}

type SeasonClosed struct {
	Version int
	OwnerID string
	Mode    Mode
	Season  uint64
}
