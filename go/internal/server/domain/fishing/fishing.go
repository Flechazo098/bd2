package fishing

import (
	"bd2server/internal/common/util"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	eventplay "bd2server/internal/server/domain/events/play"
	"bd2server/internal/server/protocol/wire"
)

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type Item struct {
	Index, ID, Count uint64
	Time             int64
	Locked           bool
	Size             uint64
}
type Collection struct {
	Min, Max uint64
	Created  int64
}
type Buff struct {
	ID  uint64
	End int64
}
type Encounter struct {
	Fish, Size       uint64
	Cast             uint64
	HP, MaxHP        int64
	Started, Expires int64
}
type response struct{ Request, Body []byte }
type snapshot struct {
	Version                           int
	Level, Exp, Boat, Skin, Rod, Next uint64
	Maps, Skins                       []uint64
	Rods, Baits, Fish                 []Item
	Collection                        map[uint64]Collection
	Buffs                             []Buff
	Voyage                            *eventplay.NativeRun
	Map                               uint64
	Cast                              uint64
	Encounter                         *Encounter
	Replies                           map[string]response
	Sequences                         map[string]uint64
	MultiAP                           uint64
	APDay                             int64
	NextAuto                          int64
	VoyageStarted                     int64
	Purchases                         map[string]uint64
	FishSlots, RodSlots, ItemSlots    uint64
}
type Profile struct {
	Level, Exp, RodID, BoatLevel, BoatSkinID uint64
	Maps                                     []uint64
	MultiAP                                  uint64
	ResetTime                                int64
	FishSlots, RodSlots, ItemSlots           uint64
}
type Projection struct {
	Profile               Profile
	Buffs                 []Buff
	ItemID                uint64
	SessionID, InstanceID string
}
type RoomInfo struct {
	Code                      string
	MapID, BoatLevel, Members uint64
}
type RoomDirectory interface {
	FishingRooms(command.Context) ([]RoomInfo, error)
}
type PublicProfileLookup func(command.Context, uint64) (string, Profile, uint64, error)
type Service struct {
	design        *gamedata.EventPlayCatalog
	economy       Economy
	rooms         eventplay.RoomRuntime
	state         snapshot
	now           func() time.Time
	sample        func(uint64) (uint64, error)
	publicProfile PublicProfileLookup
}

func (s *Service) NativeRuns() []eventplay.NativeRun {
	if s.state.Voyage == nil {
		return nil
	}
	return []eventplay.NativeRun{*s.state.Voyage}
}

func Open(ctx command.Context, design *gamedata.EventPlayCatalog, economy Economy, rooms eventplay.RoomRuntime) (*Service, error) {
	if ctx.State == nil || design == nil || economy == nil {
		return nil, fmt.Errorf("fishing: incomplete dependencies")
	}
	s := &Service{design: design, economy: economy, rooms: rooms, now: time.Now, sample: sample}
	raw, found, err := ctx.State.LoadEntry("fishing", "state", "core")
	if err != nil {
		return nil, err
	}
	if found {
		if err = json.Unmarshal(raw, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != 1 || s.state.Collection == nil || s.state.Replies == nil || s.state.Level == 0 {
			return nil, fmt.Errorf("fishing: invalid saved state")
		}
		return s, nil
	}
	d, err := design.Row("FishingDefaultTable", 25, 0)
	if err != nil {
		return nil, err
	}
	s.state = snapshot{Version: 1, Next: 1, Collection: map[uint64]Collection{}, Replies: map[string]response{}, Sequences: map[string]uint64{}, Purchases: map[string]uint64{}}
	for _, row := range design.Tables["FishingCharTable"] {
		id := num(row, 1)
		if id > 0 && (s.state.Level == 0 || id < s.state.Level) {
			s.state.Level = id
		}
	}
	for _, row := range design.Tables["FishingBoatTable"] {
		id := num(row, 8)
		if id > 0 && (s.state.Boat == 0 || id < s.state.Boat) {
			s.state.Boat = id
		}
	}
	b, _ := design.Row("FishingBoatTable", 8, s.state.Boat)
	skins, _ := ints(b, 5)
	if len(skins) == 0 {
		return nil, fmt.Errorf("fishing: starter boat missing skin")
	}
	s.state.Skin = skins[0]
	s.state.Skins = []uint64{skins[0]}
	s.state.Maps = []uint64{num(d, 15)}
	s.state.MultiAP = num(d, 1)
	s.state.APDay = s.now().Unix() / 86400
	defaults, e := design.Row("GameDefaultTable", 57, 0)
	if e != nil {
		return nil, e
	}
	s.state.FishSlots, s.state.RodSlots, s.state.ItemSlots = num(defaults, 32), num(defaults, 35), num(defaults, 33)
	types, e := ints(d, 14)
	if e != nil {
		return nil, e
	}
	ids, e := ints(d, 13)
	if e != nil {
		return nil, e
	}
	counts, e := ints(d, 12)
	if e != nil {
		return nil, e
	}
	if len(types) != len(ids) || len(ids) != len(counts) {
		return nil, fmt.Errorf("fishing: starter definition mismatch")
	}
	for i, t := range types {
		it := Item{Index: s.state.Next, ID: ids[i], Count: counts[i], Time: s.now().Unix()}
		s.state.Next++
		switch t {
		case 57:
			s.state.Rods = append(s.state.Rods, it)
			s.state.Rod = it.Index
		case 58:
			s.state.Baits = append(s.state.Baits, it)
		default:
			return nil, fmt.Errorf("fishing: unsupported starter type %d", t)
		}
	}
	if s.state.Level == 0 || s.state.Rod == 0 || s.state.Maps[0] == 0 {
		return nil, fmt.Errorf("fishing: incomplete starter design")
	}
	return s, nil
}
func sample(max uint64) (uint64, error) {
	if max == 0 {
		return 0, fmt.Errorf("fishing: empty pool")
	}
	return util.RandomUint64N(max)
}
func num(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }
func dbl(b []byte, n int) float64 {
	var v float64
	_ = wire.Walk(b, func(f wire.Field) error {
		if f.Number == n && f.Type == 1 {
			v = math.Float64frombits(binary.LittleEndian.Uint64(f.Value))
		}
		return nil
	})
	return v
}
func ints(b []byte, n int) (out []uint64, err error) {
	err = wire.Walk(b, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		switch f.Type {
		case 0:
			v, k := binary.Uvarint(f.Value)
			if k <= 0 {
				return wire.ErrMalformed
			}
			out = append(out, v)
		case 2:
			for p := f.Value; len(p) > 0; {
				v, k := binary.Uvarint(p)
				if k <= 0 {
					return wire.ErrMalformed
				}
				out = append(out, v)
				p = p[k:]
			}
		default:
			return wire.ErrMalformed
		}
		return nil
	})
	return
}
func (s *Service) Profile() Profile {
	p := Profile{Level: s.state.Level, Exp: s.state.Exp, BoatLevel: s.state.Boat, BoatSkinID: s.state.Skin, Maps: append([]uint64(nil), s.state.Maps...)}
	for _, r := range s.state.Rods {
		if r.Index == s.state.Rod {
			p.RodID = r.ID
		}
	}
	p.MultiAP = s.state.MultiAP
	if s.state.APDay != s.now().Unix()/86400 {
		d, _ := s.design.Row("FishingDefaultTable", 25, 0)
		p.MultiAP = num(d, 1)
	}
	p.ResetTime = (s.now().Unix()/86400 + 1) * 86400
	p.FishSlots, p.RodSlots, p.ItemSlots = s.state.FishSlots, s.state.RodSlots, s.state.ItemSlots
	return p
}
func (s *Service) AttachRooms(rooms eventplay.RoomRuntime)         { s.rooms = rooms }
func (s *Service) CollectionCount() uint64                         { return uint64(len(s.state.Collection)) }
func (s *Service) AttachPublicProfiles(lookup PublicProfileLookup) { s.publicProfile = lookup }
func (s *Service) Handle(ctx command.Context, path string, req []byte) (int, []byte, bool, error) {
	code, ok := routes[path]
	if !ok {
		return 0, nil, false, nil
	}
	fail := func(e error) (int, []byte, bool, error) { return code, nil, true, e }
	if err := wire.Walk(req, func(wire.Field) error { return nil }); err != nil {
		return fail(err)
	}
	key := ctx.SessionID + ":" + path + ":" + strconv.FormatUint(num(req, 1), 10)
	if num(req, 1) == 0 || num(req, 1) > math.MaxInt32 {
		return fail(fmt.Errorf("fishing: invalid request sequence"))
	}
	if r, ok := s.state.Replies[key]; ok {
		if !bytes.Equal(r.Request, req) {
			return fail(fmt.Errorf("fishing: request sequence reused"))
		}
		return code, append([]byte(nil), r.Body...), true, nil
	}
	sequenceKey := ctx.SessionID + ":" + path
	if high, ok := s.state.Sequences[sequenceKey]; ok && num(req, 1) <= high {
		return fail(fmt.Errorf("fishing: request sequence is stale"))
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	if err := json.Unmarshal(raw, &next); err != nil {
		return fail(err)
	}
	if next.APDay != s.now().Unix()/86400 {
		d, _ := s.design.Row("FishingDefaultTable", 25, 0)
		next.MultiAP = num(d, 1)
		next.APDay = s.now().Unix() / 86400
	}
	body, err := s.apply(ctx, path, req, &next, key)
	if err != nil {
		return fail(err)
	}
	next.Replies[key] = response{append([]byte(nil), req...), append([]byte(nil), body...)}
	if next.Sequences == nil {
		next.Sequences = map[string]uint64{}
	}
	next.Sequences[sequenceKey] = num(req, 1)
	for len(next.Replies) > 512 {
		for old := range next.Replies {
			if old != key {
				delete(next.Replies, old)
				break
			}
		}
	}
	raw, err = json.Marshal(next)
	if err != nil {
		return fail(err)
	}
	if err = ctx.State.PutEntry("fishing", "state", "core", raw); err != nil {
		return fail(err)
	}
	s.state = next
	return code, body, true, nil
}

var routes = map[string]int{"/FishingFishLock": 631, "/FishingFishInvenSlotAdd": 461, "/FishingShopBuy": 490, "/FishingShopBuyInfo": 491, "/FishingShopSell": 492, "/FishingInfo": 456, "/FishingTrapInfo": 457, "/FishingTrapCumulativeReward": 458, "/FishingItemInfo": 459, "/FishingRodSet": 460, "/FishingBoatUpgrade": 462, "/FishingBoatSkinSet": 463, "/FishingMapBuy": 464, "/FishingCollectionInfo": 465, "/FishingBoatSkinBuy": 499, "/FishingVoyageStart": 500, "/FishingCasting": 501, "/FishingBiteStart": 502, "/FishingBiteFishStaminaUpdate": 503, "/FishingBiteEnd": 504, "/FishingBaitUse": 505, "/FishingFishAuto": 507, "/FishingVoyageEnd": 508, "/FishingMultiRoomInfo": 517, "/FishingUserInfo": 519}

func fishWire(i Item) []byte {
	b := wire.AppendVarint(nil, 1, i.Index)
	b = wire.AppendVarint(b, 2, i.ID)
	b = wire.AppendVarint(b, 3, i.Size)
	b = wire.AppendVarint(b, 4, uint64(i.Time))
	if i.Locked {
		b = wire.AppendVarint(b, 5, 1)
	}
	return b
}
func collectionWire(id uint64, c Collection) []byte {
	b := wire.AppendVarint(nil, 1, id)
	b = wire.AppendVarint(b, 2, c.Max)
	b = wire.AppendVarint(b, 3, c.Min)
	return wire.AppendVarint(b, 4, uint64(c.Created))
}
func (s *Service) emit(ctx command.Context, n *snapshot, item uint64, key string) error {
	p := Profile{Level: n.Level, Exp: n.Exp, BoatLevel: n.Boat, BoatSkinID: n.Skin, Maps: append([]uint64(nil), n.Maps...)}
	p.MultiAP, p.ResetTime = n.MultiAP, (n.APDay+1)*86400
	p.FishSlots, p.RodSlots, p.ItemSlots = n.FishSlots, n.RodSlots, n.ItemSlots
	for _, r := range n.Rods {
		if r.Index == n.Rod {
			p.RodID = r.ID
		}
	}
	projection := Projection{Profile: p, Buffs: append([]Buff(nil), n.Buffs...), ItemID: item, SessionID: ctx.SessionID}
	if n.Voyage != nil {
		projection.InstanceID = n.Voyage.InstanceID
	}
	b, e := json.Marshal(projection)
	if e != nil {
		return e
	}
	return ctx.Publish(command.Event{ID: "fishing:" + ctx.AccountID + ":" + key, Topic: "fishing.projection", Payload: b})
}

func (s *Service) apply(ctx command.Context, path string, req []byte, n *snapshot, key string) ([]byte, error) {
	var out []byte
	now := s.now().Unix()
	switch path {
	case "/FishingShopBuy", "/FishingShopBuyInfo", "/FishingShopSell":
		return s.shop(ctx, path, req, n, key)
	case "/FishingInfo":
		b := wire.AppendVarint(nil, 1, n.Exp)
		b = wire.AppendVarint(b, 2, n.Level)
		b = wire.AppendVarint(b, 3, n.Boat)
		b = wire.AppendVarint(b, 4, n.Skin)
		b = wire.AppendVarint(b, 5, n.Rod)
		b = wire.AppendVarint(b, 6, uint64((n.APDay+1)*86400))
		out = wire.AppendBytes(out, 1, b)
		for _, id := range n.Maps {
			out = wire.AppendVarint(out, 2, id)
		}
		for _, id := range n.Skins {
			out = wire.AppendVarint(out, 3, id)
		}
	case "/FishingFishLock":
		seen := map[uint64]bool{}
		err := wire.Walk(req, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 2 {
				return wire.ErrMalformed
			}
			index, locked := num(f.Value, 1), num(f.Value, 2)
			if index == 0 || seen[index] || locked > 1 {
				return fmt.Errorf("fishing: invalid lock request")
			}
			seen[index] = true
			for i := range n.Fish {
				if n.Fish[i].Index == index {
					n.Fish[i].Locked = locked == 1
					return nil
				}
			}
			return fmt.Errorf("fishing: fish unavailable")
		})
		if err != nil {
			return nil, err
		}
		if len(seen) == 0 {
			return nil, fmt.Errorf("fishing: empty lock request")
		}
	case "/FishingItemInfo":
		for _, i := range n.Fish {
			out = wire.AppendBytes(out, 1, fishWire(i))
		}
		for _, i := range n.Rods {
			b := wire.AppendVarint(nil, 1, i.Index)
			b = wire.AppendVarint(b, 2, i.ID)
			b = wire.AppendVarint(b, 3, uint64(i.Time))
			out = wire.AppendBytes(out, 2, b)
		}
		for _, i := range n.Baits {
			b := wire.AppendVarint(nil, 1, i.Index)
			b = wire.AppendVarint(b, 2, i.ID)
			b = wire.AppendVarint(b, 3, 58)
			b = wire.AppendVarint(b, 4, i.Count)
			b = wire.AppendVarint(b, 5, uint64(i.Time))
			out = wire.AppendBytes(out, 3, b)
		}
	case "/FishingCollectionInfo":
		ids := make([]uint64, 0, len(n.Collection))
		for id := range n.Collection {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			out = wire.AppendBytes(out, 1, collectionWire(id, n.Collection[id]))
		}
	case "/FishingTrapInfo":
		return s.trap(ctx, path, req, n, key)
	case "/FishingTrapCumulativeReward":
		return s.trap(ctx, path, req, n, key)
	case "/FishingUserInfo":
		profile := s.Profile()
		name := ""
		count := uint64(len(n.Collection))
		if num(req, 2) != 0 {
			if s.publicProfile == nil {
				return nil, fmt.Errorf("fishing: public directory unavailable")
			}
			var e error
			name, profile, count, e = s.publicProfile(ctx, num(req, 2))
			if e != nil {
				return nil, e
			}
		}
		out = wire.AppendString(nil, 1, name)
		out = wire.AppendVarint(out, 2, profile.Exp)
		out = wire.AppendVarint(out, 3, profile.Level)
		out = wire.AppendVarint(out, 4, profile.RodID)
		out = wire.AppendVarint(out, 5, count)
	case "/FishingMultiRoomInfo":
		if directory, ok := s.rooms.(RoomDirectory); ok {
			rooms, e := directory.FishingRooms(ctx)
			if e != nil {
				return nil, e
			}
			for _, r := range rooms {
				b := wire.AppendString(nil, 1, r.Code)
				b = wire.AppendVarint(b, 2, r.MapID)
				b = wire.AppendVarint(b, 3, r.BoatLevel)
				b = wire.AppendVarint(b, 4, r.Members)
				out = wire.AppendBytes(out, 1, b)
			}
		}
	case "/FishingVoyageStart":
		mapID := num(req, 2)
		if !slices.Contains(n.Maps, mapID) {
			if s.rooms == nil {
				return nil, fmt.Errorf("fishing: map is not owned")
			}
			admission, err := s.rooms.StartRun(ctx, "Fishing")
			if err != nil {
				return nil, err
			}
			if admission.MapID != mapID {
				return nil, fmt.Errorf("fishing: map differs from joined room")
			}
		}
		if n.Voyage != nil && s.rooms != nil {
			current, err := s.rooms.StartRun(ctx, "Fishing")
			if err != nil && !errors.Is(err, eventplay.ErrNoRoom) {
				return nil, err
			}
			if err == nil && current.InstanceID == n.Voyage.InstanceID {
				if current.MapID != mapID {
					return nil, fmt.Errorf("fishing: active room map differs")
				}
				if n.Voyage.Host {
					return wire.AppendVarint(nil, 1, 1), nil
				}
				return nil, nil
			}
			if _, err = s.rooms.Result(ctx, *n.Voyage); err != nil && !errors.Is(err, eventplay.ErrRoomAbandoned) {
				return nil, err
			}
			b, _ := json.Marshal(n.Voyage)
			if err = ctx.Publish(command.Event{ID: "room-claim:" + n.Voyage.InstanceID + ":" + ctx.AccountID, Topic: "room.run.claimed", Payload: b}); err != nil {
				return nil, err
			}
		}
		n.Map = mapID
		n.VoyageStarted = now
		n.Cast = 0
		n.Encounter = nil
		n.Voyage = nil
		if s.rooms != nil {
			admission, e := s.rooms.StartRun(ctx, "Fishing")
			if e != nil && !errors.Is(e, eventplay.ErrNoRoom) {
				return nil, e
			}
			if e == nil {
				if admission.MapID != mapID {
					return nil, fmt.Errorf("fishing: room map changed")
				}
				n.Voyage = &admission
				b, _ := json.Marshal(admission)
				if e = ctx.Publish(command.Event{ID: "room-start:" + admission.InstanceID + ":" + ctx.AccountID, Topic: "room.run.accepted", Payload: b}); e != nil {
					return nil, e
				}
				if admission.Host {
					if n.MultiAP == 0 {
						return nil, fmt.Errorf("fishing: multiplayer tickets exhausted")
					}
					n.MultiAP--
					out = wire.AppendVarint(nil, 1, 1)
				}
			}
		}
		d, _ := s.design.Row("FishingDefaultTable", 25, 0)
		n.NextAuto = now + int64(math.Ceil(math.Max(1, dbl(d, 5)-s.buffs(n)[7])))
	case "/FishingVoyageEnd":
		if n.Voyage != nil {
			b, _ := json.Marshal(n.Voyage)
			if e := ctx.Publish(command.Event{ID: "room-claim:" + n.Voyage.InstanceID + ":" + ctx.AccountID, Topic: "room.run.claimed", Payload: b}); e != nil {
				return nil, e
			}
		}
		n.Map = 0
		n.Cast = 0
		n.Encounter = nil
		n.Voyage = nil
	case "/FishingCasting":
		if n.Map == 0 {
			return nil, fmt.Errorf("fishing: voyage required")
		}
		if _, e := s.design.Row("FishingCastingTable", 3, num(req, 2)); e != nil {
			return nil, e
		}
		n.Cast = num(req, 2)
		n.Encounter = nil
	case "/FishingBiteStart":
		if n.Map == 0 || n.Cast == 0 {
			return nil, fmt.Errorf("fishing: cast required")
		}
		enc, e := s.encounter(n, false)
		if e != nil {
			return nil, e
		}
		n.Encounter = enc
		n.Cast = 0
		out = wire.AppendBytes(nil, 1, wire.AppendVarint(wire.AppendVarint(nil, 1, enc.Fish), 2, enc.Size))
	case "/FishingBiteFishStaminaUpdate":
		if n.Encounter == nil || now > n.Encounter.Expires {
			return nil, fmt.Errorf("fishing: encounter expired")
		}
		delta := int64(int32(num(req, 2)))
		max := s.maxDamage(n)
		if delta < -max || delta > n.Encounter.MaxHP {
			return nil, fmt.Errorf("fishing: stamina delta outside design")
		}
		n.Encounter.HP += delta
		if n.Encounter.HP < 0 {
			n.Encounter.HP = 0
		}
		if n.Encounter.HP > n.Encounter.MaxHP {
			n.Encounter.HP = n.Encounter.MaxHP
		}
		out = wire.AppendVarint(nil, 1, uint64(n.Encounter.HP))
	case "/FishingBiteEnd", "/FishingFishAuto":
		if uint64(len(n.Fish)) >= n.FishSlots {
			return nil, fmt.Errorf("fishing: fish inventory is full")
		}
		if path == "/FishingFishAuto" {
			if n.Map == 0 || now < n.NextAuto {
				return nil, fmt.Errorf("fishing: automatic catch not ready")
			}
			enc, e := s.encounter(n, true)
			if e != nil {
				return nil, e
			}
			enc.HP = 0
			n.Encounter = enc
			d, _ := s.design.Row("FishingDefaultTable", 25, 0)
			n.NextAuto = now + int64(math.Ceil(math.Max(1, dbl(d, 5)-s.buffs(n)[7])))
		}
		if n.Encounter == nil || n.Encounter.HP != 0 || now > n.Encounter.Expires {
			return nil, fmt.Errorf("fishing: catch is not complete")
		}
		e := n.Encounter
		it := Item{Index: n.Next, ID: e.Fish, Count: 1, Size: e.Size, Time: now}
		n.Next++
		n.Fish = append(n.Fish, it)
		c := n.Collection[it.ID]
		if c.Created == 0 {
			c = Collection{Min: it.Size, Max: it.Size, Created: now}
		}
		if it.Size < c.Min {
			c.Min = it.Size
		}
		if it.Size > c.Max {
			c.Max = it.Size
		}
		n.Collection[it.ID] = c
		fish, err := s.design.Row("FishingFishTable", 12, it.ID)
		if err != nil {
			return nil, err
		}
		n.Exp += num(fish, 4)
		for {
			level, err := s.design.Row("FishingCharTable", 1, n.Level)
			if err != nil {
				return nil, err
			}
			need := num(level, 2)
			if need == 0 || n.Exp < need {
				break
			}
			if _, err = s.design.Row("FishingCharTable", 1, n.Level+1); err != nil {
				break
			}
			n.Exp -= need
			n.Level++
		}
		out = wire.AppendBytes(nil, 1, wire.AppendBytes(nil, 11, fishWire(it)))
		out = wire.AppendBytes(out, 2, collectionWire(it.ID, c))
		out = wire.AppendVarint(out, 3, n.Level)
		out = wire.AppendVarint(out, 4, n.Exp)
		if path == "/FishingFishAuto" {
			out = wire.AppendVarint(out, 5, 1)
		}
		n.Encounter = nil
	case "/FishingRodSet":
		index := num(req, 2)
		found := false
		for _, r := range n.Rods {
			found = found || r.Index == index
		}
		if !found {
			return nil, fmt.Errorf("fishing: rod not owned")
		}
		n.Rod = index
		out = wire.AppendVarint(nil, 1, index)
		if err := s.emit(ctx, n, 0, key); err != nil {
			return nil, err
		}
	case "/FishingBaitUse":
		b, _, _ := wire.Bytes(req, 2)
		index, id, count := num(b, 1), num(b, 2), num(b, 4)
		if num(b, 3) != 58 || count != 1 {
			return nil, fmt.Errorf("fishing: invalid bait selection")
		}
		found := false
		for i := range n.Baits {
			it := &n.Baits[i]
			if it.Index == index && it.ID == id && it.Count >= count {
				it.Count -= count
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("fishing: bait unavailable")
		}
		n.Baits = slices.DeleteFunc(n.Baits, func(item Item) bool { return item.Count == 0 })
		row, err := s.design.Row("FishingItemTable", 4, id)
		if err != nil {
			return nil, err
		}
		buffs, err := ints(row, 1)
		if err != nil {
			return nil, err
		}
		for _, bid := range buffs {
			r, err := s.design.Row("FishingBuffTable", 4, bid)
			if err != nil {
				return nil, err
			}
			n.Buffs = append(n.Buffs, Buff{bid, now + int64(num(r, 1))})
		}
		if err = s.emit(ctx, n, id, key); err != nil {
			return nil, err
		}
	case "/FishingBoatUpgrade", "/FishingBoatSkinBuy", "/FishingMapBuy", "/FishingBoatSkinSet":
		return s.purchase(ctx, path, req, n, key)
	case "/FishingFishInvenSlotAdd":
		add := num(req, 2)
		d, e := s.design.Row("GameDefaultTable", 57, 0)
		if e != nil {
			return nil, e
		}
		max := num(d, 75)
		base := num(d, 32)
		if n.FishSlots < base || add == 0 || n.FishSlots > max || add > max-n.FishSlots {
			return nil, fmt.Errorf("fishing: invalid slot expansion")
		}
		var costs []gamedata.Reward
		for at := n.FishSlots - base + 1; at <= n.FishSlots-base+add; at++ {
			var found []byte
			for _, r := range s.design.Rows("FishingInventoryBuyTable", 4, 56) {
				if num(r, 5) == at {
					found = r
					break
				}
			}
			if found == nil {
				return nil, fmt.Errorf("fishing: slot price missing")
			}
			costs = append(costs, gamedata.Reward{Type: num(found, 3), ID: num(found, 2), Count: num(found, 1)})
		}
		if _, e = s.economy.Apply(ctx, "fishing:"+key, costs, nil); e != nil {
			return nil, e
		}
		n.FishSlots += add
		return wire.AppendVarint(nil, 1, n.FishSlots), nil
	}
	return out, nil
}
