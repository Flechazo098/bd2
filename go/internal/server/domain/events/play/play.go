// Package eventplay owns event stories and client-simulated minigame runs.
package eventplay

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
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
	Coin, Exp, Char, MapGroup                  uint64
	SkillLevels                                map[uint64]uint64
	MaxHP, Rerolls, Level, SkillCredits        uint64
	Offers                                     []uint64
	LevelExp                                   uint64
	Killed, Picked, Available                  map[uint64]uint64
}
type record struct {
	UID, Game, Stage, Mode, Best, Paid, Date uint64
	Family                                   string
	Clears                                   map[string]bool
	PendingBundle                            []byte
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
	fieldRules       FieldRuleLoader
	battleChallenges gamedata.EventBattleChallenges

	store         stateio.Store
	registry      events.Resolver
	economy       Rewards
	design        *gamedata.EventPlayCatalog
	state         snapshot
	now           func() time.Time
	rooms         RoomRuntime
	onProgress    func(command.Context, uint64, uint64, uint64) error
	hubCalendars  *readonly.Seed
	fieldBindings []FieldBinding
}

func Open(ctx command.Context, store stateio.Store, design *gamedata.EventPlayCatalog, fieldRules FieldRuleLoader, registry events.Resolver, economy Rewards) (*Service, error) {
	if store == nil || design == nil || fieldRules == nil || registry == nil || economy == nil {
		return nil, fmt.Errorf("eventplay: incomplete configuration")
	}
	s := &Service{store: store, registry: registry, economy: economy, design: design, fieldRules: fieldRules, now: time.Now, state: snapshot{1, map[string]record{}, map[string]Run{}, map[string]reply{}, map[string]bool{}, map[string]uint64{}}}
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
		if s.state.Version != 1 || s.state.Records == nil || s.state.Runs == nil || s.state.Replies == nil || s.state.Stories == nil || s.state.Upgrades == nil {
			return nil, fmt.Errorf("eventplay: incompatible save")
		}
	}
	for _, r := range s.state.Records {
		if r.UID == 0 || r.Game == 0 || r.Best > math.MaxInt32 || r.Clears == nil {
			return nil, fmt.Errorf("eventplay: invalid saved record")
		}
		if _, e = s.design.Row("PackEventMiniGameTable", 8, r.Game); e != nil {
			return nil, e
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
	for _, f := range []string{"Hopscotch", "Survival", "Defense", "Rhythm", "Sichuan", "Action", "Field", "Run"} {
		if strings.HasPrefix(p, f) {
			return f
		}
	}
	return ""
}
func recKey(uid, stage, mode uint64, f string) string {
	return fmt.Sprintf("%d:%s:%d:%d", uid, f, stage, mode)
}
