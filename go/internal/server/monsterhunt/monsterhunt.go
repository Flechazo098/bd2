// Package monsterhunt owns the configured Fiend Hunt seasons and local player progress.
package monsterhunt

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/readonly"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

const BattleMode uint64 = 8
const PracticeMode uint64 = 24

type season struct {
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
	mu                  sync.Mutex
	storage             stateio.Store
	root, version       string
	seed                *readonly.Seed
	seasons             []season
	state               snapshot
	inventory           *player.Inventory
	wallet              *player.Wallet
	characters          *player.CharacterStore
	equipment           *player.EquipmentInventory
	collection          *player.CollectionStore
	presets             *gamedata.PresetDesign
	baseSlots, maxSlots uint64
	now                 func() time.Time
	active              map[string]encounter
	load                func(uint64) (*gamedata.MonsterHunt, error)
	rewardGrant         func(string, []gamedata.Reward) ([]byte, error)
}

func Open(storage stateio.Store, root, version string, seed *readonly.Seed, inventory *player.Inventory, wallet *player.Wallet) (*Service, error) {
	if storage == nil || seed == nil || inventory == nil || wallet == nil {
		return nil, fmt.Errorf("monsterhunt: incomplete configuration")
	}
	s := &Service{storage: storage, root: root, version: version, seed: seed, inventory: inventory, wallet: wallet, now: time.Now, active: map[string]encounter{}}
	designs := map[uint64]*gamedata.MonsterHunt{}
	s.load = func(id uint64) (*gamedata.MonsterHunt, error) {
		if d, ok := designs[id]; ok {
			return d, nil
		}
		d, e := gamedata.LoadMonsterHunt(root, version, id)
		if e == nil {
			designs[id] = d
		}
		return d, e
	}
	for _, f := range seed.Responses["/MonsterHuntScheduleInfo"].Fields {
		if f.Number != 1 || f.Type != 2 {
			continue
		}
		var c season
		for _, v := range f.Fields {
			switch v.Number {
			case 1:
				for _, x := range v.Fields {
					switch x.Number {
					case 1:
						c.ID = x.Varint
					case 2:
						c.Start = x.Varint
					case 3:
						c.End = x.Varint
					}
				}
			case 2:
				c.Hunt = v.Varint
			case 4:
				c.Calculate = v.Varint
			case 6:
				c.Independent = v.Varint != 0
			case 7:
				c.RankGroup = v.Varint
			}
		}
		if c.ID == 0 || c.Hunt == 0 || c.Start > c.End {
			return nil, fmt.Errorf("monsterhunt: invalid configured season")
		}
		s.seasons = append(s.seasons, c)
	}
	if len(s.seasons) == 0 {
		return nil, fmt.Errorf("monsterhunt: configured schedule missing")
	}
	d, e := gamedata.LoadMonsterHuntPresetDesign(root, version)
	if e != nil {
		return nil, e
	}
	s.presets = d
	s.baseSlots = d.BaseCount
	s.maxSlots = d.Maximum
	s.state = snapshot{Version: versionconfig.State(), Slots: s.baseSlots, Users: map[string]user{}, Presets: map[string][]byte{}, Replies: map[string]reply{}}
	raw, e := storage.Load("monsterhunt")
	if e != nil {
		return nil, e
	}
	if raw != nil {
		if e = stateio.RequireExactJSONObject(raw, "version", "slots", "users", "decks", "settings", "presets", "replies"); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &s.state); e != nil {
			return nil, e
		}
		if s.state.Version != versionconfig.State() || s.state.Users == nil || s.state.Presets == nil || s.state.Replies == nil || s.state.Slots < s.baseSlots || s.state.Slots > s.maxSlots {
			return nil, fmt.Errorf("monsterhunt: incompatible state")
		}
	}
	for _, c := range s.seasons {
		if _, e = s.load(c.Hunt); e != nil {
			return nil, e
		}
	}
	if e = s.validateDecks(s.state.Decks); e != nil {
		return nil, e
	}
	if e = s.validateSettings(s.state.Settings); e != nil {
		return nil, e
	}
	for key, u := range s.state.Users {
		if key != strconv.FormatUint(u.Season, 10) || u.Season == 0 || u.Hunt == 0 || u.Level == 0 {
			return nil, fmt.Errorf("monsterhunt: invalid saved user")
		}
		d, e := s.load(u.Hunt)
		if e != nil {
			return nil, e
		}
		hp, e := d.HP(u.Level)
		if e != nil || u.StartHP > hp || u.HighestHP != hp || u.ClearLevel > d.MaxLevel {
			return nil, fmt.Errorf("monsterhunt: saved progress exceeds design")
		}
	}
	for key, p := range s.state.Presets {
		slot, _ := scalar(p, 4)
		if key != strconv.FormatUint(slot, 10) {
			return nil, fmt.Errorf("monsterhunt: invalid saved preset key")
		}
		if e = s.validatePreset(p); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Service) AttachRewards(grant func(string, []gamedata.Reward) ([]byte, error)) {
	s.rewardGrant = grant
}
func (s *Service) AttachCharacters(c *player.CharacterStore) { s.characters = c }
func (s *Service) AttachPresetRuntime(c *player.CharacterStore, e *player.EquipmentInventory, col *player.CollectionStore) error {
	if c == nil || e == nil || col == nil {
		return fmt.Errorf("monsterhunt: incomplete preset runtime")
	}
	s.characters = c
	s.equipment = e
	s.collection = col
	if err := s.validateDecks(s.state.Decks); err != nil {
		return err
	}
	if err := s.validateSettings(s.state.Settings); err != nil {
		return err
	}
	for _, p := range s.state.Presets {
		if err := s.validatePreset(p); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) PresetSlotCount() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.state.Slots }
func (s *Service) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Encounters are keyed by session-specific receipts and survive repeated activation.
}
func (s *Service) current() season {
	// Future rows are public calendar data, not an instruction to switch the
	// player's current hunt before its window begins. Regular client schedules
	// have a single slot; independent hunts use their separate client list.
	now := uint64(s.now().UnixMilli())
	var regular []season
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

func selectSeason(rows []season, now uint64) season {
	var active, begun, future season
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

func (s *Service) scheduleInfo(req []byte) (int, []byte, bool, error) {
	current := s.current()
	response := s.seed.Responses["/MonsterHuntScheduleInfo"]
	fields := make([]readonly.Field, 0, len(response.Fields))
	for _, field := range response.Fields {
		if field.Number == 1 && field.Type == 2 {
			var id uint64
			independent := false
			for _, f := range field.Fields {
				if f.Number == 1 {
					for _, v := range f.Fields {
						if v.Number == 1 {
							id = v.Varint
						}
					}
				}
				if f.Number == 6 {
					independent = f.Varint != 0
				}
			}
			if !independent && id != current.ID {
				continue
			}
		}
		fields = append(fields, field)
	}
	// Keep the entire loaded calendar immutable; projection only affects this
	// response so subsequent requests can cross a season boundary without reload.
	projected := &readonly.Seed{Version: s.seed.Version, Responses: map[string]readonly.Response{
		"/MonsterHuntScheduleInfo": {PacketCode: response.PacketCode, Fields: fields},
	}}
	return projected.Handle("/MonsterHuntScheduleInfo", req)
}
func (s *Service) playing(c season) bool {
	n := uint64(s.now().UnixMilli())
	return n >= c.Start && n < c.End
}
func (s *Service) getUser(c season) (user, error) {
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
func (s *Service) encodeUser(u user, c season) []byte {
	var b []byte
	for n, v := range map[int]uint64{1: u.Season, 2: u.Hunt, 3: u.Level, 4: u.StartHP, 6: u.HighestHP, 7: u.HighestDate, 8: u.CurrentDamage, 9: u.DailyDamage, 11: u.DailyLevel, 12: u.DailyDate} {
		if n == 3 {
			v = u.ClearLevel
			if v == 0 {
				v = 1
			}
		}
		if v != 0 {
			b = wire.AppendVarint(b, n, v)
		}
	}
	if u.Played && !u.Claimed && uint64(s.now().UnixMilli()) > c.Calculate {
		b = wire.AppendVarint(b, 10, 1)
	}
	return b
}
func (s *Service) save(next snapshot) error {
	b, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if e = s.storage.Save("monsterhunt", b); e != nil {
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
func scalar(b []byte, n int) (uint64, error) {
	v, _, e := wire.Varint(b, n)
	if e != nil || v > math.MaxInt64 {
		return 0, fmt.Errorf("monsterhunt: invalid scalar %d", n)
	}
	return v, nil
}
func messages(b []byte, n int) ([][]byte, error) {
	var out [][]byte
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number == n {
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			out = append(out, append([]byte(nil), f.Value...))
		}
		return nil
	})
	return out, e
}
func packed(b []byte, n int) ([]uint64, error) {
	var out []uint64
	e := wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return wire.ErrMalformed
		}
		for p := f.Value; len(p) > 0; {
			v, k := binary.Uvarint(p)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
			p = p[k:]
		}
		return nil
	})
	return out, e
}
func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	return s.HandleSession(path, req, "local")
}

var codes = map[string]int{"/MonsterHuntScheduleInfo": 0, "/MonsterHuntUserInfo": 196, "/MonsterHuntRankInfo": 197, "/MonsterHuntDeckInfo": 263, "/MonsterHuntDeckSave": 264, "/MonsterHuntChangeTeam": 265, "/MonsterHuntQuickBattle": 266, "/MonsterHuntPresetSlotAdd": 400, "/MonsterHuntPresetInfo": 405, "/MonsterHuntPresetSave": 406, "/MonsterHuntPresetUse": 410, "/MonsterHuntPresetDelete": 0, "/MonsterHuntPresetInfoChange": 0, "/MonsterHuntSeasonReward": 0}

func (s *Service) HandleSession(path string, req []byte, session string) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, e := scalar(req, 1)
	if e != nil || seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, fmt.Errorf("monsterhunt: invalid sequence")
	}
	if e = validateRequest(req); e != nil {
		return code, nil, true, e
	}
	key := session + ":" + path + ":" + strconv.FormatUint(seq, 10)
	if r, found := s.state.Replies[key]; found {
		if !bytes.Equal(req, r.Request) {
			return code, nil, true, fmt.Errorf("monsterhunt: sequence reused with different request")
		}
		return code, r.Response, true, nil
	}
	if path == "/MonsterHuntScheduleInfo" {
		return s.scheduleInfo(req)
	}
	c := s.current()
	u, e := s.getUser(c)
	if e != nil {
		return code, nil, true, e
	}
	next := s.clone()
	var out []byte
	mutation := false
	var updateReceipt string
	var updateEncounter encounter
	switch path {
	case "/MonsterHuntUserInfo":
		out = wire.AppendBytes(nil, 1, s.encodeUser(u, c))
		if u.Played {
			out = wire.AppendVarint(out, 2, 1)
			out = wire.AppendDouble(out, 3, 100)
		}
	case "/MonsterHuntDeckInfo":
		for _, v := range s.state.Decks {
			out = wire.AppendBytes(out, 1, v)
		}
		for _, v := range s.state.Settings {
			out = wire.AppendBytes(out, 2, v)
		}
	case "/MonsterHuntDeckSave":
		next.Decks, e = messages(req, 2)
		if e == nil {
			e = s.validateDecks(next.Decks)
		}
		if e == nil {
			next.Settings, e = messages(req, 3)
		}
		if e == nil {
			e = s.validateSettings(next.Settings)
		}
		mutation = true
	case "/MonsterHuntChangeTeam":
		var d []byte
		d, _, e = wire.Bytes(req, 2)
		if e == nil {
			e = s.validateDecks([][]byte{d})
		}
		mode, _ := scalar(req, 4)
		if mode != BattleMode && mode != PracticeMode {
			e = fmt.Errorf("monsterhunt: invalid team mode")
		}
		if e == nil {
			found := false
			team, _ := scalar(d, 1)
			for receipt, a := range s.active {
				if !strings.HasPrefix(receipt, session+":") || a.Mode != mode {
					continue
				}
				found = true
				if team != a.Team+1 {
					e = fmt.Errorf("monsterhunt: team transition is out of order")
					break
				}
				design, err := s.load(a.Hunt)
				if err != nil {
					e = err
					break
				}
				if team > 1 && (int(team-2) >= len(design.TeamOpenLevels) || a.Level < design.TeamOpenLevels[team-2]) {
					e = fmt.Errorf("monsterhunt: team not unlocked")
					break
				}
				a.Team = team
				updateReceipt = receipt
				updateEncounter = a
				break
			}
			if !found {
				e = fmt.Errorf("monsterhunt: no active battle for team change")
			}
		}
		if e == nil {
			team, _ := scalar(d, 1)
			found := false
			for i, v := range next.Decks {
				t, _ := scalar(v, 1)
				if team == t {
					next.Decks[i] = d
					found = true
				}
			}
			if !found {
				next.Decks = append(next.Decks, d)
			}
		}
		mutation = true
	case "/MonsterHuntRankInfo":
		id, _ := scalar(req, 2)
		if id == 0 {
			id = c.ID
		}
		if v, ok := s.state.Users[strconv.FormatUint(id, 10)]; ok && v.Played {
			b := s.rankWire(v)
			out = wire.AppendBytes(out, 1, b)
			out = wire.AppendBytes(out, 2, b)
		}
	case "/MonsterHuntPresetInfo":
		keys := make([]int, 0, len(next.Presets))
		for k := range next.Presets {
			i, _ := strconv.Atoi(k)
			keys = append(keys, i)
		}
		sort.Ints(keys)
		for _, k := range keys {
			out = wire.AppendBytes(out, 1, next.Presets[strconv.Itoa(k)])
		}
	case "/MonsterHuntPresetSave":
		var p []byte
		p, _, e = wire.Bytes(req, 2)
		if e == nil {
			e = s.validatePreset(p)
		}
		if e == nil {
			slot, _ := scalar(p, 4)
			next.Presets[strconv.FormatUint(slot, 10)] = p
		}
		mutation = true
	case "/MonsterHuntPresetInfoChange":
		slot, _ := scalar(req, 5)
		p, exists := next.Presets[strconv.FormatUint(slot, 10)]
		if !exists {
			e = fmt.Errorf("monsterhunt: preset missing")
			break
		}
		name, _, _ := wire.Bytes(req, 2)
		if !utf8.Valid(name) || utf8.RuneCount(name) > 30 {
			e = fmt.Errorf("monsterhunt: invalid preset name")
			break
		}
		p, _, e = wire.ReplaceBytes(p, 1, name)
		for a, b := range map[int]int{3: 2, 4: 3} {
			v, _ := scalar(req, a)
			p, _, e = wire.ReplaceVarint(p, b, v)
		}
		if e == nil {
			e = s.validatePreset(p)
		}
		next.Presets[strconv.FormatUint(slot, 10)] = p
		mutation = true
	case "/MonsterHuntPresetDelete":
		var slots []uint64
		slots, e = packed(req, 2)
		if len(slots) == 0 {
			e = fmt.Errorf("monsterhunt: empty preset deletion")
		}
		for _, slot := range slots {
			if slot >= s.state.Slots {
				e = fmt.Errorf("monsterhunt: invalid preset slot")
			}
			delete(next.Presets, strconv.FormatUint(slot, 10))
		}
		mutation = true
	case "/MonsterHuntPresetSlotAdd":
		count, _ := scalar(req, 2)
		if count == 0 || count > s.maxSlots-next.Slots {
			e = fmt.Errorf("monsterhunt: invalid added slot count")
			break
		}
		if count > math.MaxUint64/s.presets.Price {
			e = fmt.Errorf("monsterhunt: preset price overflow")
			break
		}
		cost := count * s.presets.Price
		switch s.presets.PriceType {
		case 4:
			_, e = s.wallet.SpendGoldOnce("monsterhunt:"+key, cost)
		case 3:
			_, e = s.wallet.SpendFreeJewelryOnce("monsterhunt:"+key, cost)
		case 2:
			_, e = s.wallet.SpendJewelryOnce("monsterhunt:"+key, cost)
		case 12:
			_, e = s.wallet.SpendCatalystOnce("monsterhunt:"+key, cost)
		default:
			e = fmt.Errorf("monsterhunt: unsupported preset currency")
		}
		if e == nil {
			next.Slots += count
		}
		mutation = true
	case "/MonsterHuntPresetUse":
		slot, _ := scalar(req, 2)
		p, exists := next.Presets[strconv.FormatUint(slot, 10)]
		if !exists {
			e = fmt.Errorf("monsterhunt: preset missing")
			break
		}
		next.Decks, e = s.presetDecks(p)
		if e == nil {
			out, e = s.applyPreset(p)
		}
		if e == nil {
			for _, v := range next.Decks {
				out = wire.AppendBytes(out, 1, v)
			}
		}
		mutation = true
	case "/MonsterHuntQuickBattle":
		if !s.playing(c) || !u.Played || u.ClearLevel == 0 || u.CurrentDamage == 0 || u.DailyDamage >= u.CurrentDamage {
			e = fmt.Errorf("monsterhunt: no eligible saved battle to sweep")
			break
		}
		u.DailyDamage = u.CurrentDamage
		d, loadErr := s.load(u.Hunt)
		if loadErr != nil {
			e = loadErr
			break
		}
		bundle, grantErr := s.grant("monsterhunt:"+key, dailyDifference(d, u.DailyLevel, u.ClearLevel))
		if grantErr != nil {
			e = grantErr
			break
		}
		out = wire.AppendBytes(out, 3, bundle)
		u.DailyLevel = u.ClearLevel
		u.DailyDate = uint64(s.now().UnixMilli())
		next.Users[strconv.FormatUint(c.ID, 10)] = u
		out = wire.AppendBytes(out, 1, s.encodeUser(u, c))
		out = wire.AppendVarint(out, 4, 1)
		out = wire.AppendDouble(out, 5, 100)
		mutation = true
	case "/MonsterHuntSeasonReward":
		if !u.Played || u.Claimed || uint64(s.now().UnixMilli()) <= c.Calculate {
			e = fmt.Errorf("monsterhunt: season reward unavailable")
			break
		}
		u.Claimed = true
		d, loadErr := s.load(u.Hunt)
		if loadErr != nil {
			e = loadErr
			break
		}
		rewards := s.rankRewards(d, c.RankGroup)
		bundle, grantErr := s.grant("monsterhunt:"+key, rewards)
		if grantErr != nil {
			e = grantErr
			break
		}
		next.Users[strconv.FormatUint(c.ID, 10)] = u
		out = wire.AppendVarint(nil, 3, u.Hunt)
		out = wire.AppendBytes(out, 4, bundle)
		out = wire.AppendVarint(out, 1, 1)
		out = wire.AppendVarint(out, 2, uint64(s.score(u)))
		out = wire.AppendDouble(out, 5, 100)
		mutation = true
	}
	if e != nil {
		return code, nil, true, e
	}
	if mutation {
		next.Replies[key] = reply{append([]byte(nil), req...), out}
		if e = s.save(next); e != nil {
			return code, nil, true, e
		}
		if updateReceipt != "" {
			s.active[updateReceipt] = updateEncounter
		}
	}
	return code, out, true, nil
}

func (s *Service) validateDecks(ds [][]byte) error {
	seen := map[uint64]bool{}
	for _, d := range ds {
		t, e := scalar(d, 1)
		if e != nil || t == 0 || t > 3 || seen[t] {
			return fmt.Errorf("monsterhunt: invalid or duplicate team")
		}
		seen[t] = true
		chars, e := messages(d, 2)
		if e != nil || len(chars) > 5 {
			return fmt.Errorf("monsterhunt: invalid team size")
		}
		positions := map[uint64]bool{}
		indices := map[uint64]bool{}
		for _, b := range chars {
			i, e := scalar(b, 1)
			p, _ := scalar(b, 2)
			seq, _ := scalar(b, 3)
			if e != nil || i == 0 || p > 11 || seq == 0 || seq > 5 || positions[p] || indices[i] {
				return fmt.Errorf("monsterhunt: invalid deck character")
			}
			if s.characters != nil {
				if _, found := s.characters.Find(i); !found {
					return fmt.Errorf("monsterhunt: character not owned")
				}
			}
			positions[p] = true
			indices[i] = true
		}
	}
	return nil
}
func (s *Service) validatePreset(p []byte) error {
	slot, e := scalar(p, 4)
	if e != nil || slot >= s.state.Slots {
		return fmt.Errorf("monsterhunt: invalid preset slot")
	}
	name, _, e := wire.Bytes(p, 1)
	if e != nil || !utf8.Valid(name) || utf8.RuneCount(name) > 30 {
		return fmt.Errorf("monsterhunt: invalid preset name")
	}
	icon, _ := scalar(p, 2)
	color, _ := scalar(p, 3)
	if color > 5 || icon != 0 && !s.presets.Icons[icon] {
		return fmt.Errorf("monsterhunt: invalid preset icon")
	}
	d, e := s.presetDecks(p)
	if e != nil {
		return e
	}
	if _, _, e = s.presetBindings(p); e != nil {
		return e
	}
	return s.validateDecks(d)
}
func (s *Service) presetDecks(p []byte) ([][]byte, error) {
	entries, e := messages(p, 5)
	if e != nil {
		return nil, e
	}
	teams := map[uint64][]byte{}
	for _, entry := range entries {
		base, ok, e := wire.Bytes(entry, 1)
		if e != nil || !ok {
			return nil, fmt.Errorf("monsterhunt: missing preset deck")
		}
		t, e := scalar(entry, 4)
		if e != nil || t == 0 || t > 3 {
			return nil, fmt.Errorf("monsterhunt: invalid preset team")
		}
		teams[t] = wire.AppendBytes(teams[t], 2, base)
	}
	var out [][]byte
	for t := uint64(1); t <= 3; t++ {
		if b, ok := teams[t]; ok {
			out = append(out, append(wire.AppendVarint(nil, 1, t), b...))
		}
	}
	return out, nil
}

func (s *Service) EnterBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, e := scalar(req, 5)
	if e != nil || mode != BattleMode && mode != PracticeMode {
		return nil, fmt.Errorf("monsterhunt: invalid battle mode")
	}
	id, _ := scalar(req, 6)
	c := s.current()
	if id != c.Hunt {
		return nil, fmt.Errorf("monsterhunt: hunt not configured")
	}
	if mode == BattleMode && !s.playing(c) {
		return nil, fmt.Errorf("monsterhunt: season is closed")
	}
	d, e := s.load(id)
	if e != nil {
		return nil, e
	}
	deck, _ := scalar(req, 4)
	if deck != d.DeckID {
		return nil, fmt.Errorf("monsterhunt: invalid battle deck")
	}
	level, _ := scalar(req, 10)
	u, e := s.getUser(c)
	if e != nil {
		return nil, e
	}
	if level == 0 {
		level = u.Level
	}
	if level > d.MaxLevel || mode == BattleMode && level > d.ChallengeableLevel && level > u.ClearLevel+1 {
		return nil, fmt.Errorf("monsterhunt: invalid challenge level")
	}
	if mode == BattleMode && u.ClearLevel >= d.MaxLevel {
		return nil, fmt.Errorf("monsterhunt: maximum level already cleared")
	}
	if receipt == "" {
		return nil, fmt.Errorf("monsterhunt: missing battle receipt")
	}
	hp, e := d.HP(level)
	if e != nil {
		return nil, e
	}
	if mode == BattleMode && level == u.Level {
		hp = u.StartHP
	}
	s.active[receipt] = encounter{c.ID, id, level, hp, mode, u.Level, u.StartHP, 1}
	if mode == PracticeMode {
		u.Level = level
		u.StartHP = hp
	}
	return wire.AppendBytes(nil, 5, s.encodeUser(u, c)), nil
}
func (s *Service) CompleteBattle(req []byte, receipt string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "battle:" + receipt
	if r, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return nil, fmt.Errorf("monsterhunt: changed battle retry")
		}
		return r.Response, nil
	}
	a, ok := s.active[receipt]
	if !ok {
		return nil, fmt.Errorf("monsterhunt: battle was not entered")
	}
	remaining, e := scalar(req, 7)
	if e != nil || remaining > a.HP {
		return nil, fmt.Errorf("monsterhunt: invalid remaining body HP")
	}
	if a.Mode == PracticeMode {
		delete(s.active, receipt)
		return nil, nil
	}
	c := s.current()
	u, e := s.getUser(c)
	if e != nil {
		return nil, e
	}
	if a.Season != c.ID || a.Hunt != u.Hunt || a.BeforeLevel != u.Level || a.BeforeHP != u.StartHP {
		return nil, fmt.Errorf("monsterhunt: stale battle progress")
	}
	u.Level = a.Level
	d, e := s.load(u.Hunt)
	if e != nil {
		return nil, e
	}
	u.HighestHP, e = d.HP(a.Level)
	if e != nil {
		return nil, e
	}
	damage := a.HP - remaining
	if damage > u.CurrentDamage {
		u.CurrentDamage = damage
	}
	u.StartHP = remaining
	u.Played = true
	u.HighestDate = uint64(s.now().UnixMilli())
	previousClear := u.ClearLevel
	var clear uint64
	if remaining == 0 {
		clear = u.Level
		if clear > u.ClearLevel {
			u.ClearLevel = clear
		}
		d, e := s.load(u.Hunt)
		if e != nil {
			return nil, e
		}
		if u.ClearLevel < d.MaxLevel {
			u.Level = u.ClearLevel + 1
			u.StartHP, e = d.HP(u.Level)
			if e != nil {
				return nil, e
			}
			u.HighestHP = u.StartHP
		}
	}
	out := wire.AppendBytes(nil, 13, s.encodeUser(u, c))
	d, e = s.load(u.Hunt)
	if e != nil {
		return nil, e
	}
	if clear != 0 {
		out = wire.AppendVarint(out, 27, clear)
		var bundle []byte
		for level := previousClear + 1; level <= clear; level++ {
			b, e := s.grant(fmt.Sprintf("monsterhunt:season:%d:clear:%d", c.ID, level), d.Rewards[level].Clear)
			if e != nil {
				return nil, e
			}
			bundle = append(bundle, b...)
		}
		if len(bundle) > 0 {
			out = wire.AppendBytes(out, 7, bundle)
		}
	}
	day := uint64(s.now().UnixMilli()) / 86400000
	if damage > 0 && (u.DailyDate/86400000 != day || a.Level > u.DailyLevel) {
		bundle, e := s.grant("monsterhunt:"+key+":daily", dailyDifference(d, u.DailyLevel, a.Level))
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 8, bundle)
		u.DailyLevel = a.Level
		u.DailyDate = uint64(s.now().UnixMilli())
		u.DailyDamage = damage
	}
	out, _, _ = wire.ReplaceBytes(out, 13, s.encodeUser(u, c))
	out = wire.AppendVarint(out, 14, 1)
	out = wire.AppendDouble(out, 26, 100)
	next := s.clone()
	next.Users[strconv.FormatUint(c.ID, 10)] = u
	next.Replies[key] = reply{append([]byte(nil), req...), out}
	if e = s.save(next); e != nil {
		return nil, e
	}
	delete(s.active, receipt)
	return out, nil
}
