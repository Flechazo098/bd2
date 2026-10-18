// Package eventplay owns event stories and client-simulated minigame runs.
package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/protocol/staticdata"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"time"
)

type Rewards interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}
type Run struct {
	UID, Game, Stage, Mode, Started, Score, HP uint64
	Family, Session                            string
	Objects, Skills                            []uint64
	FieldObjects                               map[uint64]bool
	Coin, Exp, Char, MapGroup                  uint64
	SkillLevels                                map[uint64]uint64
	MaxHP, Rerolls, Level, SkillCredits        uint64
	Offers                                     []uint64
	LevelExp                                   uint64
	Killed, Picked                             map[uint64]uint64
	Native                                     *NativeRun `json:"native,omitempty"`
	LastProgress, OfferNonce                   uint64
	Regen, CoinRate, ExpRate                   float64
	PackRecord                                 []byte
	Blocks                                     []byte
	Boxes, SpecialItems                        map[uint64]uint64
	RandomState                                [16]uint64
	RandomIndex                                uint64
	Drops                                      []survivalDrop
}
type survivalDrop struct{ ID, Created uint64 }
type record struct {
	UID, Game, Stage, Mode, Best, Paid, Date uint64
	Family                                   string
	Clears                                   map[string]bool
	PendingBundle                            []byte
	BestScore                                float64
	CapturedArea, ClearTime, AchievedAt      uint64
	BestGrade, BestCombo                     uint64
	PlayInfo                                 string
}
type reply struct{ Request, Body []byte }
type snapshot struct {
	Version  int               `json:"version"`
	Records  map[string]record `json:"records"`
	Runs     map[string]Run    `json:"runs"`
	Replies  map[string]reply  `json:"replies"`
	Stories  map[string]bool   `json:"stories"`
	Upgrades map[string]uint64 `json:"upgrades"`
}

// FieldRuleLoader returns shared immutable field game rules.
type FieldRuleLoader func(uint64) (*gamedata.EventField, error)

type Service struct {
	battleCostumes   func(command.Context) map[uint64]bool
	fieldRules       FieldRuleLoader
	battleChallenges gamedata.EventBattleChallenges

	store                   stateio.Store
	registry                events.Resolver
	economy                 Rewards
	design                  *gamedata.EventPlayCatalog
	state                   snapshot
	now                     func() time.Time
	rooms                   RoomRuntime
	arcadeRanks             *ranking.Arcade
	arcadeProfile           func(command.Context) (ranking.ArcadeProfile, error)
	arcadeGuilds            func(command.Context, []uint64) (map[uint64][]byte, error)
	onProgress              func(command.Context, uint64, uint64, uint64) error
	onRhythmResult          func(command.Context, events.RhythmResult) error
	achievementDesign       *gamedata.AchievementCounterDesign
	achievementProgress     map[[2]uint64]uint64
	completedRhythmResults  []events.RhythmResult
	hubCalendars            *readonly.Seed
	fieldBindings           []FieldBinding
	actionMissionCharacters map[uint64]uint64
}

func (s *Service) AttachBattleCostumes(owned func(command.Context) map[uint64]bool) {
	s.battleCostumes = owned
}

func Open(ctx command.Context, store stateio.Store, design *gamedata.EventPlayCatalog, fieldRules FieldRuleLoader, registry events.Resolver, economy Rewards) (*Service, error) {
	if store == nil || design == nil || fieldRules == nil || registry == nil || economy == nil {
		return nil, fmt.Errorf("eventplay: incomplete configuration")
	}
	s := &Service{store: store, registry: registry, economy: economy, design: design, fieldRules: fieldRules, now: time.Now, state: snapshot{2, map[string]record{}, map[string]Run{}, map[string]reply{}, map[string]bool{}, map[string]uint64{}}}
	raw, e := store.Load(ctx.State, "eventplay")
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "version", "records", "runs", "replies", "stories", "upgrades"); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.state); e != nil {
			return nil, e
		}
		if s.state.Version != 2 || s.state.Records == nil || s.state.Runs == nil || s.state.Replies == nil || s.state.Stories == nil || s.state.Upgrades == nil {
			return nil, fmt.Errorf("eventplay: incompatible save")
		}
	}
	for _, r := range s.state.Records {
		if r.UID == 0 || r.Game == 0 || r.Best > math.MaxInt32 || r.Clears == nil || math.IsNaN(r.BestScore) || math.IsInf(r.BestScore, 0) || r.BestScore < 0 || r.CapturedArea > 100 || r.ClearTime > math.MaxInt32 || r.Best > 0 && r.AchievedAt == 0 {
			return nil, fmt.Errorf("eventplay: invalid saved record")
		}
		if _, e = s.design.Row("PackEventMiniGameTable", 8, r.Game); e != nil {
			return nil, e
		}
	}
	for key, run := range s.state.Runs {
		if run.Family == "Battle" {
			if !strings.HasPrefix(key, "battle:") || len(key) == len("battle:") || run.UID == 0 || run.Game == 0 || run.Stage == 0 || run.Mode != 17 || run.Started == 0 {
				return nil, fmt.Errorf("eventplay: invalid saved event battle")
			}
			c, err := s.registry.Resolve(run.UID)
			if err != nil {
				return nil, err
			}
			if c.Type != 17 || c.ID != run.Game {
				return nil, fmt.Errorf("eventplay: saved event battle schedule differs")
			}
			if _, err := s.design.Row("PackEventBattleGroupTable", 3, run.Game); err != nil {
				return nil, err
			}
			continue
		}
		if key != run.Family || run.UID == 0 || run.Game == 0 || run.Started == 0 || run.Started > math.MaxInt64 {
			return nil, fmt.Errorf("eventplay: invalid saved run")
		}
		if _, err := s.calendar(run.UID, run.Family); err != nil {
			return nil, err
		}
		if run.Family == "Survival" && (run.Killed == nil || run.Picked == nil || run.Boxes == nil || run.SpecialItems == nil || run.SkillLevels == nil || run.RandomIndex >= 16 || run.MaxHP == 0) {
			return nil, fmt.Errorf("eventplay: invalid saved survival progress")
		}
	}
	return s, nil
}

// AttachHubCalendars installs project-maintained hub layouts, including the
// separate play/end windows and slots referencing multiple domain identities.
func (s *Service) AttachHubCalendars(seed *readonly.Seed) {
	s.hubCalendars = seed
}

func (s *Service) grant(ctx command.Context, identity string, rewards []gamedata.BattleReward) ([]byte, error) {
	rs := make([]gamedata.Reward, 0, len(rewards))
	for _, r := range rewards {
		rs = append(rs, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
	}
	return s.economy.Apply(ctx, identity, nil, rs)
}

func family(path string) string {
	p := strings.TrimPrefix(path, "/MiniGame")
	for _, f := range []string{"Hopscotch", "Survival", "Defense", "Rhythm", "Sichuan", "Action", "PackMan", "Field", "Run"} {
		if strings.HasPrefix(p, f) {
			return f
		}
	}
	return ""
}
func recKey(uid, stage, mode uint64, f string) string {
	return fmt.Sprintf("%d:%s:%d:%d", uid, f, stage, mode)
}
