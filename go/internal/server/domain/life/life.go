package life

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"bd2server/internal/common/util"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

var ErrInvalid = errors.New("life: invalid operation")
var ErrFunds = errors.New("life: insufficient materials")

type Options struct {
	Now            func() time.Time
	Sample         func(uint64) (uint64, error)
	ValidateAvatar func(command.Context, []byte) error
}
type Service struct {
	design  *gamedata.LifeCatalog
	options Options
}
type item struct{ Index, ID, Type, Count uint64 }
type object struct {
	X, Y                      int32
	Rotate, Index, ID, Status uint64
	Start, End                int64
	Inner                     map[uint64]*object
	Refund                    []gamedata.Reward
}
type citizen struct {
	Index, Slot uint64
	Avatar      []byte
}
type helper struct {
	Index, ID, Slot uint64
	Name            string
	Avatar          []byte
	Work, WorkID    uint64
	Assigned        int64
}
type purchase struct {
	Count  uint64
	Period string
}
type snapshot struct {
	Version           int
	World, Coin, Next uint64
	Growth            [6]uint64
	Chunks            map[uint64]bool
	Objects           map[uint64]map[string]*object
	Counts            map[uint64]uint64
	ActiveCounts      map[uint64]uint64
	Layers            map[uint64]uint64
	Items             map[string]item
	Tools             map[uint64]uint64
	Citizens          map[uint64]citizen
	Helpers           map[uint64]helper
	Candidates        map[uint64][]helper
	Purchases         map[string]purchase
	Foods             map[uint64]int64
	Collections       map[uint64]bool
	ExpansionRefund   []gamedata.Reward
}
type receipt struct {
	Request, Body []byte
	Sequence      uint64
}

func Open(ctx command.Context, design *gamedata.LifeCatalog, options Options) (*Service, error) {
	if ctx.State == nil || design == nil || design.Row("LifeDefaultTable", 0) == nil {
		return nil, fmt.Errorf("life: incomplete dependencies")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Sample == nil {
		options.Sample = util.RandomUint64N
	}
	s := &Service{design: design, options: options}
	_, err := s.load(ctx)
	return s, err
}
func (s *Service) load(ctx command.Context) (*snapshot, error) {
	if ctx.State == nil {
		return nil, ErrInvalid
	}
	raw, found, err := ctx.State.LoadEntry("missions", "life_state", "core")
	if err != nil {
		return nil, err
	}
	if found {
		var v snapshot
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if v.Version != 1 || v.Next == 0 || v.Chunks == nil || v.Objects == nil || v.Items == nil || v.Citizens == nil || v.Helpers == nil || v.Candidates == nil || v.Tools == nil || v.Counts == nil || v.ActiveCounts == nil || v.Layers == nil || v.Purchases == nil || v.Foods == nil || v.Collections == nil {
			return nil, fmt.Errorf("life: invalid saved state")
		}
		if v.World != 0 && s.design.Row("LifeWorldTable", v.World) == nil {
			return nil, fmt.Errorf("life: saved world missing in design")
		}
		return &v, nil
	}
	v := &snapshot{Version: 1, Next: 1, Growth: [6]uint64{1, 0, 1, 0, 1, 0}, Chunks: map[uint64]bool{}, Objects: map[uint64]map[string]*object{}, Counts: map[uint64]uint64{}, ActiveCounts: map[uint64]uint64{}, Layers: map[uint64]uint64{}, Items: map[string]item{}, Tools: map[uint64]uint64{}, Citizens: map[uint64]citizen{}, Helpers: map[uint64]helper{}, Candidates: map[uint64][]helper{}, Purchases: map[string]purchase{}, Foods: map[uint64]int64{}, Collections: map[uint64]bool{}}
	for group, ids := range s.design.ToolGroups {
		if len(ids) > 0 {
			v.Tools[group] = ids[0]
		}
	}
	return v, nil
}
func save(ctx command.Context, v *snapshot, changes ...stateio.EntryMutation) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	changes = append(changes, stateio.EntryMutation{Bucket: "life_state", Key: "core", Payload: raw})
	return ctx.State.SaveWithEntries("missions", nil, changes)
}
func num(raw []byte, n int) uint64 { v, _, _ := wire.Varint(raw, n); return v }
func msg(raw []byte, n int) []byte { v, _, _ := wire.Bytes(raw, n); return v }
func messages(raw []byte, n int) (out [][]byte) {
	_ = wire.Walk(raw, func(f wire.Field) error {
		if f.Number == n && f.Type == 2 {
			out = append(out, f.Value)
		}
		return nil
	})
	return
}
func ints(raw []byte, n int) (out []uint64) {
	_ = wire.Walk(raw, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
		}
		if f.Type == 2 {
			for b := f.Value; len(b) > 0; {
				v, k := binary.Uvarint(b)
				if k <= 0 {
					break
				}
				out = append(out, v)
				b = b[k:]
			}
		}
		return nil
	})
	return
}
func row(fields ...uint64) []byte {
	var b []byte
	for i := 0; i < len(fields); i += 2 {
		b = wire.AppendVarint(b, int(fields[i]), fields[i+1])
	}
	return b
}
func itemKey(typ, id uint64) string {
	return strconv.FormatUint(typ, 10) + ":" + strconv.FormatUint(id, 10)
}
func objectKey(o *object) string { return fmt.Sprintf("%d:%d:%d", o.ID, o.X, o.Y) }
func sorted[V any](m map[uint64]V) []uint64 {
	ids := make([]uint64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
func objectWire(o *object) []byte {
	b := row(1, uint64(int64(o.X)), 2, uint64(int64(o.Y)), 3, o.Rotate, 4, o.Index, 6, o.ID, 7, o.Status, 8, uint64(o.Start), 9, uint64(o.End))
	for _, id := range sorted(o.Inner) {
		b = wire.AppendBytes(b, 10, objectWire(o.Inner[id]))
	}
	return b
}
func placeWire(chunk uint64, objects ...*object) []byte {
	b := row(1, chunk)
	for _, o := range objects {
		b = wire.AppendBytes(b, 2, objectWire(o))
	}
	return b
}
func parseObject(b []byte) *object {
	return &object{X: int32(num(b, 1)), Y: int32(num(b, 2)), Rotate: num(b, 3), Index: num(b, 4), ID: num(b, 6), Inner: map[uint64]*object{}}
}
func itemWire(it item) []byte { return row(1, it.Index, 2, it.ID, 3, it.Type, 4, it.Count) }
func growthWire(v *snapshot) []byte {
	var b []byte
	for i, x := range v.Growth {
		b = wire.AppendVarint(b, i+1, x)
	}
	return b
}
func userWire(v *snapshot) []byte {
	b := row(1, v.Coin, 2, v.World)
	b = wire.AppendBytes(b, 3, growthWire(v))
	for _, id := range sorted(v.Chunks) {
		b = wire.AppendVarint(b, 4, id)
	}
	return b
}
func citizenWire(c citizen) []byte { return wire.AppendBytes(row(1, c.Index, 2, c.Slot), 3, c.Avatar) }
func helperWire(h helper) []byte {
	b := row(1, h.Index, 2, h.ID, 3, h.Slot, 6, h.Work, 7, h.WorkID, 8, uint64(h.Assigned))
	b = wire.AppendString(b, 4, h.Name)
	return wire.AppendBytes(b, 5, h.Avatar)
}
func (s *Service) info(v *snapshot, full bool) []byte {
	b := wire.AppendBytes(nil, 2, userWire(v))
	cf, hf := 3, 4
	if full {
		cf, hf = 7, 9
		for _, chunk := range sorted(v.Objects) {
			keys := make([]string, 0, len(v.Objects[chunk]))
			for key := range v.Objects[chunk] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			p := row(1, chunk)
			for _, key := range keys {
				p = wire.AppendBytes(p, 2, objectWire(v.Objects[chunk][key]))
			}
			b = wire.AppendBytes(b, 1, p)
		}
		keys := make([]string, 0, len(v.Items))
		for key := range v.Items {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			it := v.Items[key]
			if it.Count == 0 {
				continue
			}
			f := 3
			if it.Type == 65 {
				f = 4
			}
			b = wire.AppendBytes(b, f, itemWire(it))
		}
		for _, group := range sorted(v.Tools) {
			b = wire.AppendBytes(b, 5, row(1, group, 2, v.Tools[group]))
		}
		for _, id := range sorted(v.Foods) {
			if v.Foods[id] > s.options.Now().Unix() {
				b = wire.AppendBytes(b, 6, row(1, id, 2, uint64(v.Foods[id])))
			}
		}
		for _, id := range sorted(v.Collections) {
			b = wire.AppendVarint(b, 8, id)
		}
		for _, slot := range sorted(v.Candidates) {
			g := row(1, slot)
			for _, h := range v.Candidates[slot] {
				g = wire.AppendBytes(g, 2, helperWire(h))
			}
			b = wire.AppendBytes(b, 10, g)
		}
	}
	for _, slot := range sorted(v.Citizens) {
		b = wire.AppendBytes(b, cf, citizenWire(v.Citizens[slot]))
	}
	for _, id := range sorted(v.Helpers) {
		b = wire.AppendBytes(b, hf, helperWire(v.Helpers[id]))
	}
	return b
}

var routes = map[string]int{"/LifeWorldChoice": 593, "/LifeUserInfo": 594, "/LifeInfo": 595, "/LifeWorldObjectPositionSave": 596, "/LifeWorldObjectGathering": 597, "/LifeShopBuy": 598, "/LifeSeeding": 599, "/LifeToolUpgrade": 600, "/LifeEatFood": 601, "/LifeShopSell": 602, "/LifeShopBuyInfo": 603, "/LifeWorldObjectPlaceSave": 604, "/LifeWorldObjectBuildCompleted": 605, "/LifeCooking": 606, "/LifeCrafting": 607, "/LifeWorldObjectUnplaceSave": 608, "/LifeWorldChunkExpand": 609, "/LifeCitizenRecruit": 610, "/LifeCitizenAvatarSave": 611, "/LifeHelperGacha": 615, "/LifeHelperRecruit": 616, "/LifeHelperFire": 617, "/LifeHelperReName": 618, "/LifeHelperAssign": 619, "/LifeHelperReward": 620, "/LifeHelperReconnect": 628, "/LifeHelperRewardInfo": 629, "/LifeHelperGachaDelete": 630, "/LifeWorldObjectPositionDeltaSave": 634}

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	code, handled := routes[path]
	if !handled {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if err := wire.Walk(request, func(wire.Field) error { return nil }); err != nil {
		return fail(err)
	}
	seq := num(request, 1)
	if seq == 0 || seq > math.MaxInt32 || ctx.SessionID == "" {
		return fail(ErrInvalid)
	}
	v, err := s.load(ctx)
	if err != nil {
		return fail(err)
	}
	if path == "/LifeUserInfo" || path == "/LifeInfo" {
		if err := s.refresh(v); err != nil {
			return fail(err)
		}
		if err := save(ctx, v); err != nil {
			return fail(err)
		}
		return code, s.info(v, path == "/LifeInfo"), true, nil
	}
	if path == "/LifeShopBuyInfo" {
		return code, s.shopInfo(v), true, nil
	}
	if path == "/LifeHelperRewardInfo" {
		b, err := s.helperPreview(v)
		return code, b, true, err
	}
	key := ctx.SessionID + ":" + path
	old, found, err := ctx.State.LoadEntry("missions", "life_receipts", key)
	if err != nil {
		return fail(err)
	}
	if found {
		var r receipt
		if err := json.Unmarshal(old, &r); err != nil {
			return fail(err)
		}
		if seq == r.Sequence {
			if !bytes.Equal(r.Request, request) {
				return fail(ErrInvalid)
			}
			return code, r.Body, true, nil
		}
		if seq < r.Sequence {
			return fail(ErrInvalid)
		}
	}
	if err := s.refresh(v); err != nil {
		return fail(err)
	}
	body, err := s.apply(ctx, v, path, request)
	if err != nil {
		return fail(err)
	}
	raw, err := json.Marshal(receipt{Request: request, Body: body, Sequence: seq})
	if err != nil {
		return fail(err)
	}
	if err := save(ctx, v, stateio.EntryMutation{Bucket: "life_receipts", Key: key, Payload: raw}); err != nil {
		return fail(err)
	}
	return code, body, true, nil
}

func (s *Service) Apply(ctx command.Context, key string, costs, gains []gamedata.Reward) ([]byte, error) {
	if key == "" {
		return nil, ErrInvalid
	}
	request, err := json.Marshal(struct{ Costs, Gains []gamedata.Reward }{costs, gains})
	if err != nil {
		return nil, err
	}
	raw, found, err := ctx.State.LoadEntry("missions", "life_economy", key)
	if err != nil {
		return nil, err
	}
	if found {
		var r receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if !bytes.Equal(r.Request, request) {
			return nil, ErrInvalid
		}
		return r.Body, nil
	}
	v, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.spend(v, costs); err != nil {
		return nil, err
	}
	body, err := s.grant(v, gains)
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(receipt{Request: request, Body: body})
	if err != nil {
		return nil, err
	}
	return body, save(ctx, v, stateio.EntryMutation{Bucket: "life_economy", Key: key, Payload: raw})
}
func (s *Service) CanApply(ctx command.Context, costs, gains []gamedata.Reward) error {
	v, err := s.load(ctx)
	if err != nil {
		return err
	}
	if err := s.spend(v, costs); err != nil {
		return err
	}
	_, err = s.grant(v, gains)
	return err
}
func (s *Service) Items(ctx command.Context) ([][]byte, error) {
	v, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	keys := make([]string, 0, len(v.Items))
	for key := range v.Items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if it := v.Items[key]; it.Count > 0 {
			out = append(out, itemWire(it))
		}
	}
	return out, nil
}
func (s *Service) Coin(ctx command.Context) (uint64, error) { return s.Balance(ctx, 64, 0) }
func (s *Service) Balance(ctx command.Context, typ, id uint64) (uint64, error) {
	v, err := s.load(ctx)
	if err != nil {
		return 0, err
	}
	if typ == 64 {
		return v.Coin, nil
	}
	return v.Items[itemKey(typ, id)].Count, nil
}
func (s *Service) spend(v *snapshot, costs []gamedata.Reward) error {
	for _, r := range costs {
		if r.Count == 0 || r.Count > math.MaxInt32 {
			return ErrInvalid
		}
		switch r.Type {
		case 64:
			if v.Coin < r.Count {
				return ErrFunds
			}
			v.Coin -= r.Count
		case 65, 66:
			key := itemKey(r.Type, r.ID)
			it := v.Items[key]
			if it.Count < r.Count {
				return ErrFunds
			}
			it.Count -= r.Count
			v.Items[key] = it
		default:
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) grant(v *snapshot, gains []gamedata.Reward) ([]byte, error) {
	var b []byte
	for _, r := range gains {
		if r.Count == 0 || r.Count > math.MaxInt32 {
			return nil, ErrInvalid
		}
		it := item{Type: r.Type, ID: r.ID}
		switch r.Type {
		case 64:
			if v.Coin > math.MaxInt32-r.Count {
				return nil, ErrInvalid
			}
			v.Coin += r.Count
			it.Count = v.Coin
		case 65, 66:
			if r.Type == 66 && s.design.Row("LifeItemTable", r.ID) == nil || r.Type == 65 && s.design.Row("LifeObjectGroupTable", r.ID) == nil {
				return nil, ErrInvalid
			}
			key := itemKey(r.Type, r.ID)
			it = v.Items[key]
			if it.Index == 0 {
				it = item{Index: v.Next, Type: r.Type, ID: r.ID}
				v.Next++
			}
			if it.Count > math.MaxInt32-r.Count {
				return nil, ErrInvalid
			}
			it.Count += r.Count
			if r.Type == 66 {
				limit := num(s.design.Row("LifeItemTable", r.ID), 8)
				if limit > 0 && it.Count > limit {
					return nil, fmt.Errorf("life: item stack full")
				}
			}
			v.Items[key] = it
		default:
			return nil, ErrInvalid
		}
		for _, id := range s.design.CollectionsByItem[[2]uint64{r.Type, r.ID}] {
			v.Collections[id] = true
		}
		b = wire.AppendBytes(b, 1, itemWire(it))
		b = wire.AppendBytes(b, 6, itemWire(item{ID: r.ID, Type: r.Type, Count: r.Count}))
	}
	return b, nil
}
