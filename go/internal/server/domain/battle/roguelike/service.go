package roguelike

import (
	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
)

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type Period struct {
	Season, Regular uint64
	Active          bool
}
type RankEntry struct {
	Owner, Score, Damage, Rank uint64
	Profile                    []byte
}
type RankPublication struct {
	Account         string
	Season, Version uint64
	Entry           RankEntry
}
type Ranking interface {
	Snapshot(command.Context, uint64, RankEntry) ([]RankEntry, RankEntry, error)
	Total(command.Context, uint64) (uint64, error)
}
type Options struct {
	Access         func(command.Context) error
	Economy        Economy
	Period         func(command.Context) (Period, error)
	Tickets        func(command.Context, string, uint64) error
	CostumeVisible func(uint64) bool
	CostumeOwned   func(uint64) bool
	SeasonSettled  func(uint64) bool
	Health         func(command.Context, gamedata.RogueCostume, float64) (uint64, error)
	Evidence       func(command.Context) battle.SettlementEvidence
	Ranking        Ranking
	Publish        func(command.Context, uint64, uint64, uint64, uint64) error
	OnBattle       func(command.Context) error
	Owner          uint64
}
type Service struct {
	store   stateio.Store
	design  *gamedata.RogueLikeDesign
	options Options
	sample  func(uint64) uint64
}
type room struct {
	Number, Group, ID uint64
	Clear             bool
}
type floor struct {
	Number uint64
	Rooms  []room
}
type character struct{ Index, ID, Unique, HP, MaxHP, Costume uint64 }
type costume struct{ Index, ID, Character, Level, Burst uint64 }
type relic struct{ Index, ID uint64 }
type offer struct {
	Type, ID, Price uint64
	Sold            bool
}
type run struct {
	ID, Level                                uint64
	Floor, Room, State                       uint64
	Gold, Reroll, ShopRoll                   uint64
	Remaining, ChoiceType, RelicChoice       uint64
	EventGroup, EventID, PendingRelic        uint64
	BattleLevel, EncounterMode               uint64
	Events, Elites, Bosses, Cleared, Crystal uint64
	Floors                                   []floor
	Choice                                   []uint64
	Characters                               []character
	Costumes                                 []costume
	Relics                                   []relic
	Deck                                     [][]byte
	Shop                                     []offer
	Encounter                                string
	ChangedParticipants                      []uint64
	DeferredEffects                          []uint64
}
type seasonRecord struct {
	Score, Damage    uint64
	Claimed, Cleared bool
}
type state struct {
	Version                                                                          string
	Season, Regular, NextIndex, NextRun, MaxTry, MaxReward, Obsidian, HighestCrystal uint64
	Growth                                                                           map[uint64]uint64
	Seasons                                                                          map[uint64]seasonRecord
	Run                                                                              *run
	LastScore                                                                        []byte
}
type receipt struct {
	Digest [32]byte
	Reply  []byte
}

func Open(store stateio.Store, d *gamedata.RogueLikeDesign, o Options) (*Service, error) {
	if store == nil || d == nil || o.Economy == nil || o.Period == nil || o.Tickets == nil || o.Access == nil {
		return nil, fmt.Errorf("roguelike: missing dependency")
	}
	return &Service{store: store, design: d, options: o, sample: func(n uint64) uint64 {
		if n == 0 {
			return 0
		}
		return rand.Uint64N(n)
	}}, nil
}
func (s *Service) load(ctx command.Context) (*state, Period, error) {
	p, e := s.options.Period(ctx)
	if e != nil {
		return nil, p, e
	}
	if p.Season == 0 {
		return nil, p, fmt.Errorf("roguelike: missing season")
	}
	v := &state{Version: versionconfig.State(), Season: p.Season, Regular: p.Regular, NextIndex: 1, NextRun: 1, Growth: map[uint64]uint64{}, Seasons: map[uint64]seasonRecord{}}
	raw, e := s.store.Load(ctx.State, "roguelike")
	if e != nil {
		return nil, p, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "Version", "Season", "Regular", "NextIndex", "NextRun", "MaxTry", "MaxReward", "Obsidian", "HighestCrystal", "Growth", "Seasons", "Run", "LastScore"); e != nil {
			return nil, p, e
		}
		if e = json.Unmarshal(raw, v); e != nil {
			return nil, p, e
		}
		if v.Version != versionconfig.State() || v.NextIndex == 0 || v.NextRun == 0 || v.Growth == nil || v.Seasons == nil {
			return nil, p, fmt.Errorf("roguelike: invalid state")
		}
	}
	if v.Season != p.Season {
		v.Season = p.Season
		v.Regular = p.Regular
		v.Run = nil
		v.Growth = map[uint64]uint64{}
		v.Obsidian = s.design.Default.V(35)
		v.MaxReward = 0
		v.MaxTry = 0
		v.HighestCrystal = 0
	}
	return v, p, nil
}
func (s *Service) save(ctx command.Context, v *state) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "roguelike", b)
}
func n(b []byte, f int) uint64 { v, _, _ := wire.Varint(b, f); return v }
func messages(b []byte, f int) (a [][]byte, e error) {
	e = wire.Walk(b, func(x wire.Field) error {
		if x.Number == f {
			if x.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, x.Number, x.Type)
			}
			a = append(a, slices.Clone(x.Value))
		}
		return nil
	})
	return
}
func (s *Service) index(v *state) uint64 { i := v.NextIndex; v.NextIndex++; return i }
func fail(message string) error          { return fmt.Errorf("roguelike: %s", message) }

var routes = map[string]int{
	"Info": 279, "Enter": 280, "RoomEnter": 282, "SelectReward": 283, "EventSelect": 318, "EventChoice": 319, "GiveUp": 281, "GiveUpInfo": 324, "ShopBuy": 320, "ShopReRoll": 321, "ShopExit": 322, "CostumeUpgrade": 342, "CostumeBurstUpgrade": 652, "Growth": 323, "AllUserScoreInfo": 325, "RankInfo": 326, "SeasonReward": 327, "QuickBattle": 340, "RelicMix": 347, "RewardReRoll": 284, "DeckSave": 285, "BattleCharChange": 317, "BattleEndPreview": 466,
}

func (s *Service) Handle(ctx command.Context, path string, b []byte) (int, []byte, bool, error) {
	op := strings.TrimPrefix(path, "/EvilCastleRogueLike")
	code, ok := routes[op]
	if !ok || op == path {
		return 0, nil, false, nil
	}
	seq := n(b, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fail("invalid sequence")
	}
	if e := wire.Walk(b, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	query := op == "Info" || op == "GiveUpInfo" || op == "RankInfo" || op == "AllUserScoreInfo" || op == "BattleEndPreview"
	key := fmt.Sprintf("%s:%s:%d", ctx.SessionID, op, seq)
	digest := sha256.Sum256(b)
	if !query {
		raw, found, e := ctx.State.LoadEntry("battle", "rogue_replies", key)
		if e != nil {
			return code, nil, true, e
		}
		if found {
			var r receipt
			if json.Unmarshal(raw, &r) != nil || r.Digest != digest {
				return code, nil, true, fail("changed retry")
			}
			return code, r.Reply, true, nil
		}
	}
	v, p, e := s.load(ctx)
	if e != nil {
		return code, nil, true, e
	}
	out, e := s.handle(ctx, v, p, op, b, "roguelike:"+key)
	if e != nil {
		return code, nil, true, e
	}
	if e = s.save(ctx, v); e != nil {
		return code, nil, true, e
	}
	if !query {
		raw, e := json.Marshal(receipt{digest, out})
		if e != nil {
			return code, nil, true, e
		}
		if e = ctx.State.PutEntry("battle", "rogue_replies", key, raw); e != nil {
			return code, nil, true, e
		}
	}
	return code, out, true, nil
}
func (s *Service) handle(ctx command.Context, v *state, p Period, op string, b []byte, key string) ([]byte, error) {
	switch op {
	case "Info":
		return s.info(ctx, v, p)
	case "AllUserScoreInfo":
		total, e := s.total(ctx, v)
		return wire.AppendVarint(nil, 1, total), e
	case "RankInfo":
		return s.rank(ctx, v, n(b, 2))
	case "SeasonReward":
		return s.seasonReward(ctx, v, key)
	case "Growth":
		return s.growth(v, b)
	case "QuickBattle":
		if e := s.options.Access(ctx); e != nil {
			return nil, e
		}
		return s.quick(ctx, v, p, b, key)
	case "Enter":
		if e := s.options.Access(ctx); e != nil {
			return nil, e
		}
		return s.enter(ctx, v, p, n(b, 2), key)
	case "GiveUpInfo":
		if v.Run == nil {
			return nil, fail("no active run")
		}
		_, obs := s.score(v, true)
		return wire.AppendVarint(nil, 1, obs), nil
	case "GiveUp":
		if v.Run == nil {
			return nil, fail("no active run")
		}
		score, _, e := s.finish(ctx, v, false, true, key)
		return wire.AppendBytes(nil, 1, score), e
	}
	r := v.Run
	if r == nil {
		return nil, fail("no active run")
	}
	if !p.Active {
		return nil, fail("season is closed")
	}
	switch op {
	case "RoomEnter":
		return s.roomEnter(v, n(b, 2))
	case "SelectReward":
		return s.selectReward(ctx, v, n(b, 2), n(b, 4))
	case "RewardReRoll":
		if r.State != 1 && r.State != 2 {
			return nil, fail("choice cannot reroll")
		}
		if r.Reroll == 0 {
			return nil, fail("no rerolls")
		}
		r.Reroll--
		if e := s.newChoice(r, r.ChoiceType, r.RelicChoice, false); e != nil {
			return nil, e
		}
		return wire.AppendBytes(wire.AppendVarint(nil, 1, r.Reroll), 2, choiceWire(r)), nil
	case "ShopBuy":
		return s.shopBuy(ctx, v, n(b, 2), n(b, 3))
	case "ShopReRoll":
		return s.shopReroll(v)
	case "ShopExit":
		if r.State != 4 {
			return nil, fail("not in shop")
		}
		f, room := s.clearRoom(r)
		return clearedResponse(r, f, room, 1, 2, 3), nil
	case "CostumeUpgrade", "CostumeBurstUpgrade":
		return s.upgrade(r, n(b, 2), n(b, 3), op == "CostumeBurstUpgrade")
	case "DeckSave":
		return s.deckSave(r, b)
	case "BattleCharChange":
		return s.change(ctx, r, n(b, 2), n(b, 3))
	case "RelicMix":
		return s.mix(v, b)
	case "EventSelect":
		return s.eventSelect(v, n(b, 2))
	case "EventChoice":
		return s.eventChoice(v, n(b, 2), n(b, 3))
	case "BattleEndPreview":
		return s.preview(ctx, v, b)
	}
	return nil, fail("unknown operation")
}
