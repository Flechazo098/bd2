// Package monsterhunt owns the configured Fiend Hunt seasons and local player progress.
package monsterhunt

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"strconv"

	"time"
)

const BattleMode uint64 = 8
const PracticeMode uint64 = 24

type Season struct {
	ID, Hunt, Start, End, Calculate, RankGroup uint64
	Independent                                bool
}
type user struct {
	Season, Hunt, Level, StartHP, HighestHP, CurrentDamage, DailyDamage, HighestDate, DailyLevel, DailyDate uint64
	Played, Claimed                                                                                         bool
	ClearLevel                                                                                              uint64
}
type reply struct{ Request, Response []byte }
type snapshot struct {
	Version  string            `json:"version"`
	Slots    uint64            `json:"slots"`
	Users    map[string]user   `json:"users"`
	Decks    [][]byte          `json:"decks"`
	Settings [][]byte          `json:"settings"`
	Presets  map[string][]byte `json:"presets"`
	Replies  map[string]reply  `json:"replies"`
}
type encounter struct{ Season, Hunt, Level, HP, Mode, BeforeLevel, BeforeHP, Team uint64 }
type Service struct {
	storage             stateio.Store
	seed                *readonly.Seed
	seasons             []Season
	state               snapshot
	inventory           *assets.Inventory
	wallet              *assets.Wallet
	characters          *roster.CharacterStore
	equipment           *assets.EquipmentInventory
	collection          *roster.CollectionStore
	presets             *gamedata.PresetDesign
	baseSlots, maxSlots uint64
	now                 func() time.Time
	active              map[string]encounter
	transientVersion    uint64
	load                func(uint64) (*gamedata.MonsterHunt, error)
	rewardGrant         func(ctx command.Context, _ string, _ []gamedata.Reward) ([]byte, error)
}

// RuleLoader returns shared immutable hunt rules.
type RuleLoader func(uint64) (*gamedata.MonsterHunt, error)

// Rules holds the shared season calendar and preset configuration.
type Rules struct {
	seasons []Season
	seed    *readonly.Seed
	presets *gamedata.PresetDesign
	load    RuleLoader
}

func NewRules(seasons []Season, seed *readonly.Seed, presets *gamedata.PresetDesign, load RuleLoader) (*Rules, error) {
	if len(seasons) == 0 || seed == nil || presets == nil || load == nil {
		return nil, fmt.Errorf("monsterhunt: incomplete rules")
	}
	for _, c := range seasons {
		if c.ID == 0 || c.Hunt == 0 || c.Start > c.End {
			return nil, fmt.Errorf("monsterhunt: invalid configured season")
		}
		if _, err := load(c.Hunt); err != nil {
			return nil, err
		}
	}
	return &Rules{seasons: append([]Season(nil), seasons...), seed: seed, presets: presets, load: load}, nil
}

func (s *Service) AttachRewards(grant func(ctx command.Context, _ string, _ []gamedata.Reward) ([]byte, error)) {
	s.rewardGrant = grant
}
func (s *Service) AttachCharacters(ctx command.Context, c *roster.CharacterStore) { s.characters = c }

func (s *Service) PresetSlotCount() uint64 { return s.state.Slots }

func (s *Service) TransientVersion() uint64 {

	return s.transientVersion
}

func (s *Service) current() Season {
	// Future rows are public calendar data, not an instruction to switch the
	// player's current hunt before its window begins. Regular client schedules
	// have a single slot; independent hunts use their separate client list.
	now := uint64(s.now().UnixMilli())
	var regular []Season
	for _, c := range s.seasons {
		if !c.Independent {
			regular = append(regular, c)
		}
	}
	if len(regular) == 0 {
		regular = s.seasons
	}
	return selectSeason(regular, now)
}

func selectSeason(rows []Season, now uint64) Season {
	var active, begun, future Season
	for _, c := range rows {
		if c.Start <= now {
			if begun.ID == 0 || c.Start > begun.Start || c.Start == begun.Start && c.ID > begun.ID {
				begun = c
			}
			if now < c.End && (active.ID == 0 || c.Start > active.Start || c.Start == active.Start && c.ID > active.ID) {
				active = c
			}
		} else if future.ID == 0 || c.Start < future.Start || c.Start == future.Start && c.ID < future.ID {
			future = c
		}
	}
	if active.ID != 0 {
		return active
	}
	if begun.ID != 0 {
		return begun
	}
	return future
}

func (s *Service) playing(c Season) bool {
	n := uint64(s.now().UnixMilli())
	return n >= c.Start && n < c.End
}
func (s *Service) getUser(c Season) (user, error) {
	u, ok := s.state.Users[strconv.FormatUint(c.ID, 10)]
	if ok {
		if u.DailyDate != 0 && u.DailyDate/86400000 != uint64(s.now().UnixMilli())/86400000 {
			u.DailyDamage = 0
			u.DailyLevel = 0
		}
		return u, nil
	}
	d, e := s.load(c.Hunt)
	if e != nil {
		return u, e
	}
	hp, e := d.HP(1)
	return user{Season: c.ID, Hunt: c.Hunt, Level: 1, StartHP: hp, HighestHP: hp}, e
}

func (s *Service) save(ctx command.Context, next snapshot) error {
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if e = s.storage.Save(ctx.State, "monsterhunt", b); e != nil {
		return e
	}
	s.state = next
	return nil
}
func (s *Service) clone() snapshot {
	raw, _ := json.Marshal(s.state)
	var n snapshot
	_ = json.Unmarshal(raw, &n)
	return n
}

var codes = map[string]int{"/MonsterHuntScheduleInfo": 0, "/MonsterHuntUserInfo": 196, "/MonsterHuntRankInfo": 197, "/MonsterHuntDeckInfo": 263, "/MonsterHuntDeckSave": 264, "/MonsterHuntChangeTeam": 265, "/MonsterHuntQuickBattle": 266, "/MonsterHuntPresetSlotAdd": 400, "/MonsterHuntPresetInfo": 405, "/MonsterHuntPresetSave": 406, "/MonsterHuntPresetUse": 410, "/MonsterHuntPresetDelete": 0, "/MonsterHuntPresetInfoChange": 0, "/MonsterHuntSeasonReward": 0}
