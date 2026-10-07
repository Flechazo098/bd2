// Package calendar reads the project's independently distributed calendars.
package calendar

import (
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/commerce/gacha"
	"bd2server/internal/server/domain/events"
)

type Manifest struct {
	SchemaVersion   int
	Revision        string
	GameVersion     string
	GameDataVersion string
	Events          []Event
	Gacha           []Gacha
	StepUps         []Gacha
	Regular         *Regular
	MonsterHunt     *MonsterHunt
	CashProducts    []CashProduct
	EventHubs       []EventHub
	MiniGameHubs    []MiniGameHub
}
type Event struct {
	UID   uint64
	Type  uint64
	ID    uint64
	SubID uint64
	Start string
	End   string
}
type Gacha struct {
	GroupID        uint64
	Start          string
	End            string
	FreeCountBonus bool
	CashCountBonus bool
}
type Season struct {
	ID                uint64
	Start             string
	End               string
	RankRewardGroupID uint64
	Error             bool
	Return            bool
}
type Content struct {
	ID      uint64
	Current Season
	Next    Season
}
type Regular struct {
	CalculateMilliseconds uint64
	Contents              []Content
	Regular               []schedule.RegularSeason
}
type Hunt struct {
	Season            Season
	HuntID            uint64
	InfoOpenDay       uint64
	CalculateEndAt    string
	ErrorFlag         bool
	IndependentFlag   bool
	RankRewardGroupID uint64
	CostumeBanIDs     []uint64
	BurstBanIDs       []uint64
}
type HuntHistory struct {
	Season    uint64
	HuntID    uint64
	ErrorFlag bool
	Hidden    bool
}
type MonsterHunt struct {
	Seasons            []Hunt
	StartRegularSeason uint64
	History            []HuntHistory
}
type CashProduct struct {
	GroupID         uint64
	ProductID       uint64
	SaleGroup       uint64
	Start           string
	End             string
	EndDelayMinutes uint64
	EventIndex      uint64
}
type HubSetting struct {
	// Mini hubs bind PackEventListTable.SlotIndex and HubContentType here.
	// The response's slot ID is the separate static table Id, never SlotIndex.
	Slot         uint64
	ProgressType uint64
	EventUIDs    []uint64
}
type EventHub struct {
	UID      uint64
	HubID    uint64
	Start    string
	PlayEnd  string
	End      string
	Settings []HubSetting
}
type MiniGameHub struct {
	Slot         uint64
	EventUID     uint64
	ProgressType uint64
}
type Set struct {
	Events         []events.Schedule
	GachaSeed      *gacha.ScheduleSeed
	RegularService *schedule.Service
	MonsterHunt    *MonsterHunt
	CashProducts   []CashProduct
	Revisions      []string
	EventHubs      []EventHub
	MiniGameHubs   []MiniGameHub
}
