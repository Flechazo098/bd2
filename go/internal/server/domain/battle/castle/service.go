package castle

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/battle"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"time"
)

type Participant struct{ CharacterIndex, CostumeIndex, CharacterID, CostumeID uint64 }
type Service struct {
	rankOwner          uint64
	rankSource         RankSource
	settled            func(uint64) bool
	design             *gamedata.CastleRewardDesign
	now                func() time.Time
	period             func(command.Context) (uint64, bool, error)
	evidence           func(command.Context) battle.SettlementEvidence
	validate           func(command.Context, []Participant) error
	grant              func(command.Context, string, []gamedata.Reward) ([]byte, error)
	currentPack        func(command.Context) (int, error)
	characters         *roster.CharacterStore
	collection         *roster.CollectionStore
	equipment          *assets.EquipmentInventory
	wallet             *assets.Wallet
	totalWarCompletion func(command.Context, string, uint64, []uint64) error
	totalWarSettled    func(command.Context, uint64) error
	totalWarTextCheck  func(command.Context, string, string) error
}

func NewService(d *gamedata.CastleRewardDesign) *Service                            { return &Service{design: d, now: time.Now} }
func (s *Service) AttachClock(f func() time.Time)                                   { s.now = f }
func (s *Service) AttachPeriod(f func(command.Context) (uint64, bool, error))       { s.period = f }
func (s *Service) AttachEvidence(f func(command.Context) battle.SettlementEvidence) { s.evidence = f }
func (s *Service) AttachTotalWarValidation(f func(command.Context, []Participant) error) {
	s.validate = f
}
func (s *Service) AttachRewards(f func(command.Context, string, []gamedata.Reward) ([]byte, error)) {
	s.grant = f
}
func (s *Service) AttachCurrentPack(f func(command.Context) (int, error)) { s.currentPack = f }

type tower struct {
	ID, Mode   uint64
	Challenges []uint64
}
type floor struct {
	ID, Highest, Point, Retry, Achieved uint64
	Rewarded                            bool
}
type run struct {
	Floor, Retry uint64
	Cleared      map[uint64]bool
	Point        uint64
}
type encounter struct {
	Mode, ID, Deck, Monster, Season uint64
	Receipt                         string
}
type totalWarRun struct {
	Identity string
	Costumes []uint64
	Request  []byte
}
type score struct{ ID, Value uint64 }
type state struct {
	Season                           uint64
	Towers                           map[string]tower
	Floors                           map[uint64]floor
	Run                              *run
	Encounter                        *encounter
	CastleClaimDay, TotalWarClaimDay string
	TotalWarScores                   []score
	TotalWarRun                      *totalWarRun
	TotalWarDeck                     *totalWarDeckState
}
type receipt struct {
	Digest [32]byte
	Body   []byte
}

func (s *Service) load(ctx command.Context) (*state, uint64, bool, error) {
	if ctx.State == nil || s.design == nil || s.now == nil {
		return nil, 0, false, fmt.Errorf("castle: storage/design unavailable")
	}
	v := &state{Towers: map[string]tower{}, Floors: map[uint64]floor{}}
	raw, ok, e := ctx.State.LoadEntry("missions", "castle_state", "progress")
	if e != nil {
		return nil, 0, false, e
	}
	if ok && json.Unmarshal(raw, v) != nil {
		return nil, 0, false, fmt.Errorf("castle: invalid saved progress")
	}
	if v.Towers == nil || v.Floors == nil {
		return nil, 0, false, fmt.Errorf("castle: invalid progress maps")
	}
	var season uint64
	active := false
	if s.period != nil {
		season, active, e = s.period(ctx)
		if e != nil {
			return nil, 0, false, e
		}
	}
	if season != v.Season {
		if v.Season != 0 && len(v.Floors) != 0 {
			raw, err := json.Marshal(v.Floors)
			if err != nil {
				return nil, 0, false, err
			}
			if err = ctx.State.PutEntry("missions", "castle_seasons", fmt.Sprint(v.Season), raw); err != nil {
				return nil, 0, false, err
			}
		}
		v.Season = season
		v.Floors = map[uint64]floor{}
		v.Run = nil
		if v.Encounter != nil && v.Encounter.Mode == 7 {
			v.Encounter = nil
		}
	}
	return v, season, active, nil
}
func save(ctx command.Context, v *state) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return ctx.State.PutEntry("missions", "castle_state", "progress", raw)
}
func (s *Service) day() (string, error) { return s.design.Reset.Period(0, s.now()) }
func n(raw []byte, num int) uint64      { v, _, _ := wire.Varint(raw, num); return v }
func msgs(raw []byte, num int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(raw, func(f wire.Field) error {
		if f.Number == num {
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			out = append(out, bytes.Clone(f.Value))
		}
		return nil
	})
	return out, e
}
func sum(v []score) uint64 {
	var out uint64
	for _, s := range v {
		out += s.Value
	}
	return out
}
func castleTotal(v *state) uint64 {
	var out uint64
	for _, f := range v.Floors {
		out += f.Highest
	}
	return out
}
func threshold(rows []gamedata.ScoreReward, value uint64) (gamedata.ScoreReward, bool) {
	var out gamedata.ScoreReward
	for _, r := range rows {
		if float64(value) >= r.Score {
			out = r
		}
	}
	return out, out.ID != 0
}
func towerKey(mode, id uint64) string { return fmt.Sprintf("%d:%d", mode, id) }
func towerWire(t tower) []byte {
	out := wire.AppendVarint(nil, 1, t.ID)
	for _, c := range t.Challenges {
		out = wire.AppendVarint(out, 2, c)
	}
	return wire.AppendVarint(out, 3, t.Mode)
}
func floorWire(f floor) []byte {
	out := wire.AppendVarint(nil, 2, f.ID)
	out = wire.AppendVarint(out, 3, f.Retry)
	out = wire.AppendVarint(out, 4, f.Point)
	out = wire.AppendVarint(out, 5, f.Highest)
	if f.Rewarded {
		out = wire.AppendVarint(out, 6, 1)
	}
	return out
}
func totalWire(v *state) []byte { return wire.AppendVarint(nil, 2, castleTotal(v)) }
func scoresWire(v []score) []byte {
	var out []byte
	for _, s := range v {
		item := wire.AppendVarint(nil, 1, s.ID)
		item = wire.AppendVarint(item, 2, s.Value)
		out = wire.AppendBytes(out, 1, item)
	}
	return out
}

func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/EvilCastleRankingInfo": 0, "/EvilCastleStageRankingInfo": 116, "/EvilCastleRewardInfo": 135, "/EvilCastleTowerInfo": 200, "/EvilCastleDailyRewardState": 441, "/EvilCastleDailyReward": 440, "/EvilCastleInfo": 112, "/EvilCastleEnter": 113, "/EvilCastlePing": 125, "/EvilCastleGiveUp": 547, "/EvilCastleStageClearReward": 115, "/TotalWarInfo": 236, "/TotalWarBattleStart": 243, "/TotalWarBattleEnd": 244, "/TotalWarRewardState": 252, "/TotalWarReward": 242}
	maps.Copy(codes, totalWarDeckCodes)
	code, handled := codes[path]
	if !handled {
		return 0, nil, false, nil
	}
	if _, totalWarDeckRequest := totalWarDeckCodes[path]; totalWarDeckRequest {
		if _, err := totalWarScalar(req, 1); err != nil {
			return code, nil, true, err
		}
	}
	if len(req) > 4<<20 || n(req, 1) == 0 || n(req, 1) > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("castle: invalid request sequence/size")
	}
	if e := wire.Walk(req, func(wire.Field) error { return nil }); e != nil {
		return code, nil, true, e
	}
	v, season, active, e := s.load(ctx)
	if e != nil {
		return code, nil, true, e
	}
	day, e := s.day()
	if e != nil {
		return code, nil, true, e
	}
	mutation := path != "/EvilCastleRankingInfo" && path != "/EvilCastleStageRankingInfo" && path != "/EvilCastleTowerInfo" && path != "/EvilCastleDailyRewardState" && path != "/TotalWarRewardState" && path != "/EvilCastleInfo" && path != "/TotalWarInfo" && path != "/EvilCastlePing" && path != "/TotalWarDeckInfo" && path != "/TotalWarDeckPresetInfo"
	receiptKey := fmt.Sprintf("command:%s:%s:%d", ctx.SessionID, path, n(req, 1))
	if mutation {
		if out, ok, e := loadReceipt(ctx, receiptKey, req); e != nil || ok {
			return code, out, true, e
		}
	}
	var out []byte
	switch path {
	case "/EvilCastleRankingInfo", "/EvilCastleStageRankingInfo":
		out, e = s.rankInfo(ctx, v, path, req)
		if e != nil {
			return code, nil, true, e
		}
	case "/EvilCastleRewardInfo":
		out, e = s.seasonRewards(ctx, v, req)
		if e != nil {
			return code, nil, true, e
		}
	case "/EvilCastleTowerInfo":
		keys := make([]string, 0, len(v.Towers))
		for k := range v.Towers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = wire.AppendBytes(out, 1, towerWire(v.Towers[k]))
		}
	case "/EvilCastleInfo":
		ids := make([]uint64, 0, len(v.Floors))
		for id := range v.Floors {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			out = wire.AppendBytes(out, 1, floorWire(v.Floors[id]))
		}
		out = wire.AppendBytes(out, 2, totalWire(v))
		out = wire.AppendVarint(out, 3, season)
	case "/EvilCastleDailyRewardState", "/TotalWarRewardState":
		var available bool
		if path == "/EvilCastleDailyRewardState" {
			_, ok := threshold(s.design.Daily, castleTotal(v))
			available = ok && active && v.CastleClaimDay != day
		} else {
			_, ok := threshold(s.design.TotalWarDaily, sum(v.TotalWarScores))
			available = ok && v.TotalWarClaimDay != day
		}
		if available {
			out = wire.AppendVarint(nil, 1, 1)
		}
	case "/EvilCastleDailyReward", "/TotalWarReward":
		rows, points, claimed := s.design.Daily, castleTotal(v), v.CastleClaimDay
		if path == "/TotalWarReward" {
			rows, points, claimed = s.design.TotalWarDaily, sum(v.TotalWarScores), v.TotalWarClaimDay
		}
		r, ok := threshold(rows, points)
		if !ok || claimed == day || (path == "/EvilCastleDailyReward" && !active) {
			return code, nil, true, fmt.Errorf("castle: daily reward unavailable")
		}
		if s.grant == nil {
			return code, nil, true, fmt.Errorf("castle: reward grant unavailable")
		}
		bundle, e := s.grant(ctx, "castle:daily:"+path+":"+day, []gamedata.Reward{r.Reward})
		if e != nil {
			return code, nil, true, e
		}
		out = wire.AppendBytes(nil, 1, bundle)
		if path == "/TotalWarReward" {
			v.TotalWarClaimDay = day
		} else {
			v.CastleClaimDay = day
		}
	case "/EvilCastleEnter":
		if n(req, 2) != s.design.PackID {
			return code, nil, true, fmt.Errorf("castle: castle season/pack unavailable")
		}
		if s.currentPack == nil {
			return code, nil, true, fmt.Errorf("castle: current pack unavailable")
		}
		pack, e := s.currentPack(ctx)
		if e != nil {
			return code, nil, true, e
		}
		if uint64(pack) != s.design.PackID {
			return code, nil, true, fmt.Errorf("castle: not in castle pack")
		}
		id := n(req, 3)
		if id == 0 {
			v.Run = nil
			v.Encounter = nil
			break
		}
		if !active {
			return code, nil, true, fmt.Errorf("castle: castle season unavailable")
		}
		f, ok := s.design.Floors[id]
		if !ok || (f.Previous != 0 && v.Floors[f.Previous].Highest == 0) {
			return code, nil, true, fmt.Errorf("castle: floor locked")
		}
		if n(req, 4) != 0 || v.Run == nil || v.Run.Floor != id {
			retry := uint64(0)
			if v.Run != nil && v.Run.Floor == id {
				retry = v.Run.Retry + 1
			}
			v.Run = &run{Floor: id, Retry: retry, Cleared: map[uint64]bool{}}
		}
		for monster, deck := range f.Monsters {
			if v.Run.Cleared[monster] {
				continue
			}
			item := wire.AppendVarint(nil, 1, monster)
			item = wire.AppendVarint(item, 2, deck)
			item = wire.AppendVarint(item, 6, 1)
			out = wire.AppendBytes(out, 1, item)
		}
	case "/EvilCastlePing":
		if v.Run == nil || v.Run.Floor != n(req, 2) {
			return code, nil, true, fmt.Errorf("castle: no active floor")
		}
		out = wire.AppendVarint(nil, 1, v.Run.Point)
	case "/EvilCastleGiveUp":
		v.Run = nil
		v.Encounter = nil
	case "/EvilCastleStageClearReward":
		if n(req, 2) != s.design.PackID {
			return code, nil, true, fmt.Errorf("castle: invalid reward pack")
		}
		out, e = s.floorRewards(ctx, v, n(req, 3))
		if e != nil {
			return code, nil, true, e
		}
	case "/TotalWarContentsItemRenew", "/TotalWarDeckInfo", "/TotalWarDeckPresetInfo", "/TotalWarDeckPresetSave", "/TotalWarDeckPresetSlotAdd", "/TotalWarDeckSave", "/TotalWarEquipSave", "/TotalWarPresetDelete", "/TotalWarPresetInfoChange":
		out, e = s.totalWarDeckHandle(ctx, v, path, req)
		if e != nil {
			return code, nil, true, e
		}
	case "/TotalWarInfo":
		out = scoresWire(v.TotalWarScores)
	case "/TotalWarBattleStart":
		out, e = s.totalWarStart(ctx, v, req)
		if e != nil {
			return code, nil, true, e
		}
	case "/TotalWarBattleEnd":
		out, e = s.totalWarEnd(ctx, v, req)
		if e != nil {
			return code, nil, true, e
		}
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return code, nil, true, e
	}
	changes := []stateio.EntryMutation{{Bucket: "castle_state", Key: "progress", Payload: raw}}
	if mutation {
		raw, e = json.Marshal(receipt{sha256.Sum256(req), out})
		if e != nil {
			return code, nil, true, e
		}
		changes = append(changes, stateio.EntryMutation{Bucket: "castle_receipts", Key: receiptKey, Payload: raw})
	}
	if e = ctx.State.SaveWithEntries("missions", nil, changes); e != nil {
		return code, nil, true, e
	}
	return code, out, true, nil
}

func (s *Service) floorRewards(ctx command.Context, v *state, id uint64) ([]byte, error) {
	if s.grant == nil {
		return nil, fmt.Errorf("castle: reward grant unavailable")
	}
	var out []byte
	granted := false
	ids := []uint64{id}
	if id == 0 {
		ids = nil
		for floor := range v.Floors {
			ids = append(ids, floor)
		}
		slices.Sort(ids)
	}
	for _, id := range ids {
		f, ok := v.Floors[id]
		d, defined := s.design.Floors[id]
		if !ok || !defined || f.Highest == 0 || f.Rewarded {
			if len(ids) == 1 {
				return nil, fmt.Errorf("castle: floor reward unavailable")
			}
			continue
		}
		bundle, e := s.grant(ctx, fmt.Sprintf("castle:floor:%d:%d", v.Season, id), d.Rewards)
		if e != nil {
			return nil, e
		}
		e = wire.Walk(bundle, func(field wire.Field) error {
			to := map[int]int{1: 1, 2: 2, 3: 4, 4: 3}[field.Number]
			if to != 0 {
				out = wire.AppendBytes(out, to, field.Value)
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		f.Rewarded = true
		v.Floors[id] = f
		granted = true
	}
	if !granted {
		return nil, fmt.Errorf("castle: no floor rewards available")
	}
	return out, nil
}

func loadReceipt(ctx command.Context, key string, req []byte) ([]byte, bool, error) {
	raw, ok, e := ctx.State.LoadEntry("missions", "castle_receipts", key)
	if e != nil || !ok {
		return nil, false, e
	}
	var r receipt
	if json.Unmarshal(raw, &r) != nil || r.Digest != sha256.Sum256(req) {
		return nil, false, fmt.Errorf("castle: changed settlement retry")
	}
	return r.Body, true, nil
}
func putReceipt(ctx command.Context, key string, req, body []byte) error {
	raw, e := json.Marshal(receipt{sha256.Sum256(req), body})
	if e != nil {
		return e
	}
	return ctx.State.PutEntry("missions", "castle_receipts", key, raw)
}
