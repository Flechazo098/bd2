package ib

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type Economy interface {
	Apply(command.Context, string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
}
type Period struct {
	Season, Regular uint64
	Open            bool
}
type Options struct {
	Period       func(command.Context) (Period, error)
	CostumeLevel func(command.Context, uint64) (uint64, error)
}
type item struct{ Index, Type, ID, Level uint64 }
type placement struct{ Index, Position, Rotation uint64 }
type shopItem struct {
	Slot, Type, ID, Price, Original uint64
	Discount, Reserved, Sold        bool
}
type play struct {
	Dungeon, Stage, Life, Coin, Season, Refreshes uint64
	Entered                                       bool
}
type receipt struct {
	Digest [32]byte
	Reply  []byte
}
type snapshot struct {
	Version                string
	Season, Run, NextIndex uint64
	Play                   play
	Cleared                map[uint64]bool
	Items                  map[uint64]item
	Deck                   []placement
	Shop                   []shopItem
	Process                []byte
	Receipts               map[string]receipt
}
type Service struct {
	store   stateio.Store
	design  *gamedata.IBDesign
	economy Economy
	options Options
	state   snapshot
}

func Open(ctx command.Context, store stateio.Store, d *gamedata.IBDesign, e Economy, o Options) (*Service, error) {
	if store == nil || d == nil || e == nil || o.Period == nil {
		return nil, errors.New("ib: missing dependency")
	}
	s := &Service{store: store, design: d, economy: e, options: o, state: snapshot{Version: versionconfig.State(), NextIndex: 1, Cleared: map[uint64]bool{}, Items: map[uint64]item{}, Receipts: map[string]receipt{}}}
	b, err := store.Load(ctx.State, "ib")
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Version", "Season", "Run", "NextIndex", "Play", "Cleared", "Items", "Deck", "Shop", "Process", "Receipts"); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if err = s.validate(); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Service) validate() error {
	if s.state.Version != versionconfig.State() || s.state.Cleared == nil || s.state.Items == nil || s.state.Receipts == nil || s.state.NextIndex == 0 || s.state.NextIndex > math.MaxInt32 {
		return errors.New("ib: invalid state")
	}
	for id, yes := range s.state.Cleared {
		if _, ok := s.design.Dungeons[id]; !ok || !yes {
			return errors.New("ib: invalid clear record")
		}
	}
	for index, i := range s.state.Items {
		d, ok := s.design.Items[i.ID]
		if !ok || index != i.Index || index == 0 || index >= s.state.NextIndex || i.Type != d.Type {
			return errors.New("ib: invalid item")
		}
		if i.Level > 0 {
			if _, ok = s.design.UpgradeCosts[[3]uint64{i.Type, d.TypeID, i.Level + 1}]; !ok {
				return errors.New("ib: invalid item level")
			}
		}
	}
	if s.state.Play.Dungeon == 0 {
		if len(s.state.Items) > 0 || len(s.state.Deck) > 0 || len(s.state.Shop) > 0 || len(s.state.Process) > 0 || s.state.Play != (play{}) {
			return errors.New("ib: orphaned run")
		}
		return nil
	}
	d, _, err := s.stage()
	if err != nil {
		return err
	}
	if s.state.Play.Life == 0 || s.state.Play.Life > d.Life || s.state.Play.Season != s.state.Season || s.state.Play.Coin > math.MaxInt32 || s.state.Play.Entered != (len(s.state.Process) > 0) {
		return errors.New("ib: invalid run")
	}
	if err := s.validateDeck(s.state.Deck); err != nil {
		return err
	}
	if len(s.state.Shop) != 4 {
		return errors.New("ib: invalid shop")
	}
	for slot, i := range s.state.Shop {
		d, ok := s.design.Items[i.ID]
		if !ok || i.Type != d.Type || i.Slot != uint64(slot) || i.Original != d.Price || i.Price > i.Original || i.Reserved && i.Sold {
			return errors.New("ib: invalid shop item")
		}
	}
	return nil
}
func (s *Service) save(ctx command.Context) error {
	b, e := json.Marshal(s.state)
	if e != nil {
		return e
	}
	return s.store.Save(ctx.State, "ib", b)
}
func value(b []byte, n int) uint64 { v, _, _ := wire.Varint(b, n); return v }
func scalars(values ...uint64) []byte {
	var b []byte
	for n, v := range values {
		if v != 0 {
			b = wire.AppendVarint(b, n+1, v)
		}
	}
	return b
}
func itemWire(i item) []byte      { return scalars(i.Index, i.Type, i.ID, i.Level) }
func deckWire(p placement) []byte { return scalars(p.Index, p.Position, p.Rotation) }
func shopWire(i shopItem) []byte {
	b := scalars(i.Slot, i.Type, i.ID, i.Price, i.Original)
	for n, v := range []bool{i.Discount, i.Reserved, i.Sold} {
		if v {
			b = wire.AppendVarint(b, n+6, 1)
		}
	}
	return b
}
func (s *Service) playWire() []byte {
	p := s.state.Play
	b := scalars(p.Dungeon, p.Stage, p.Life, p.Coin, p.Season, p.Refreshes)
	if p.Entered {
		b = wire.AppendVarint(b, 7, 1)
	}
	return b
}
func (s *Service) itemsWire(b []byte, field int) []byte {
	ids := make([]uint64, 0, len(s.state.Items))
	for id := range s.state.Items {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		b = wire.AppendBytes(b, field, itemWire(s.state.Items[id]))
	}
	return b
}
func (s *Service) decksWire(b []byte, field int) []byte {
	for _, p := range s.state.Deck {
		b = wire.AppendBytes(b, field, deckWire(p))
	}
	return b
}
func (s *Service) shopWire() []byte {
	var b []byte
	for _, i := range s.state.Shop {
		b = wire.AppendBytes(b, 1, shopWire(i))
	}
	return b
}
func randomBelow(n uint64) (uint64, error) {
	if n == 0 {
		return 0, errors.New("ib: empty random range")
	}
	v, e := rand.Int(rand.Reader, new(big.Int).SetUint64(n))
	if e != nil {
		return 0, e
	}
	return v.Uint64(), nil
}

var codes = map[string]int{"/IbMainInfo": 558, "/IbItemInfo": 559, "/IbDeckInfo": 560, "/IbDeckSave": 561, "/IbShopRefresh": 562, "/IbShopBuy": 563, "/IbItemSell": 564, "/IbDungeonEnter": 565, "/IbStageStart": 566, "/IbStageEnd": 567, "/IbDungeonGiveUp": 568, "/IbItemUpgrade": 577, "/IbShopItemReserve": 580, "/IbShopInfo": 621}

func (s *Service) Handle(ctx command.Context, path string, b []byte) (int, []byte, bool, error) {
	code, ok := codes[path]
	if !ok {
		return 0, nil, false, nil
	}
	if err := wire.Walk(b, func(wire.Field) error { return nil }); err != nil {
		return code, nil, true, err
	}
	seq := value(b, 1)
	if seq == 0 || seq > math.MaxInt32 {
		return code, nil, true, errors.New("ib: missing sequence")
	}
	query := path == "/IbMainInfo" || path == "/IbItemInfo" || path == "/IbDeckInfo" || path == "/IbShopInfo"
	key := fmt.Sprintf("%s:%s:%d", ctx.SessionID, path, seq)
	digest := sha256.Sum256(append([]byte(path), b...))
	if !query {
		if r, ok := s.state.Receipts[key]; ok {
			if r.Digest != digest {
				return code, nil, true, errors.New("ib: changed replay")
			}
			return code, slices.Clone(r.Reply), true, nil
		}
	}
	before, _ := json.Marshal(s.state)
	period, err := s.options.Period(ctx)
	if err == nil && period.Season != 0 {
		if _, ok := s.design.Seasons[period.Season]; !ok {
			err = errors.New("ib: undefined season")
		}
	}
	reset := s.state.Season != 0 && s.state.Season != period.Season
	if err == nil && s.state.Season != period.Season {
		s.clearRun()
		s.state.Cleared = map[uint64]bool{}
		s.state.Season = period.Season
	}
	var out []byte
	if err == nil {
		out, err = s.handle(ctx, path, b, period, reset)
	}
	if err == nil {
		if !query {
			s.state.Receipts[key] = receipt{digest, slices.Clone(out)}
		}
		err = s.save(ctx)
	}
	if err != nil {
		var restored snapshot
		_ = json.Unmarshal(before, &restored)
		s.state = restored
		return code, nil, true, err
	}
	return code, out, true, nil
}
func (s *Service) clearRun() {
	s.state.Play = play{}
	s.state.Items = map[uint64]item{}
	s.state.Deck = nil
	s.state.Shop = nil
	s.state.Process = nil
}
func (s *Service) stage() (gamedata.IBDungeon, gamedata.IBStage, error) {
	d, ok := s.design.Dungeons[s.state.Play.Dungeon]
	if ok {
		for _, stage := range d.Stages {
			if stage.ID == s.state.Play.Stage {
				return d, stage, nil
			}
		}
	}
	return d, gamedata.IBStage{}, errors.New("ib: no active stage")
}
func (s *Service) preparing() error {
	if s.state.Play.Dungeon == 0 {
		return errors.New("ib: no active dungeon")
	}
	if s.state.Play.Entered {
		return errors.New("ib: battle in progress")
	}
	return nil
}
func (s *Service) spend(cost uint64) error {
	if cost > s.state.Play.Coin {
		return errors.New("ib: insufficient soul tokens")
	}
	s.state.Play.Coin -= cost
	return nil
}
func (s *Service) credit(n uint64) error {
	if n > math.MaxInt32-s.state.Play.Coin {
		return errors.New("ib: token overflow")
	}
	s.state.Play.Coin += n
	return nil
}
func (s *Service) addItem(id uint64) (item, error) {
	d, ok := s.design.Items[id]
	if !ok || s.state.NextIndex >= math.MaxInt32 {
		return item{}, errors.New("ib: cannot create item")
	}
	i := item{Index: s.state.NextIndex, Type: d.Type, ID: id}
	s.state.NextIndex++
	s.state.Items[i.Index] = i
	return i, nil
}

func (s *Service) handle(ctx command.Context, path string, b []byte, period Period, reset bool) ([]byte, error) {
	a, c := value(b, 2), value(b, 3)
	switch path {
	case "/IbMainInfo":
		out := wire.AppendBytes(nil, 1, s.playWire())
		if reset {
			out = wire.AppendVarint(out, 2, 1)
		}
		ids := make([]uint64, 0, len(s.state.Cleared))
		for id := range s.state.Cleared {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			out = wire.AppendVarint(out, 3, id)
		}
		return wire.AppendVarint(out, 4, period.Regular), nil
	case "/IbItemInfo":
		return s.itemsWire(nil, 1), nil
	case "/IbDeckInfo":
		return s.decksWire(nil, 1), nil
	}
	if !period.Open || period.Season == 0 {
		return nil, errors.New("ib: season closed")
	}
	switch path {
	case "/IbDungeonEnter":
		if s.state.Play.Dungeon != 0 {
			return nil, errors.New("ib: active dungeon must be abandoned first")
		}
		d, ok := s.design.Dungeons[a]
		if !ok || !slices.Contains(s.design.Seasons[period.Season], a) || d.Unlock != 0 && !s.state.Cleared[d.Unlock] {
			return nil, errors.New("ib: dungeon locked")
		}
		s.state.Run++
		s.state.Play = play{Dungeon: a, Stage: d.Stages[0].ID, Life: d.Life, Coin: d.Stages[0].Credit, Season: period.Season}
		for _, p := range s.design.Decks[s.design.StartDeck] {
			i, e := s.addItem(p.ItemID)
			if e != nil {
				return nil, e
			}
			s.state.Deck = append(s.state.Deck, placement{i.Index, p.Position, p.Rotation})
		}
		if err := s.refreshShop(false); err != nil {
			return nil, err
		}
		out := wire.AppendBytes(nil, 1, s.playWire())
		out = s.itemsWire(out, 2)
		return s.decksWire(out, 3), nil
	case "/IbDungeonGiveUp":
		s.clearRun()
		return nil, nil
	case "/IbStageStart":
		_, stage, err := s.stage()
		if err != nil {
			return nil, err
		}
		if a != s.state.Play.Dungeon || c != stage.ID {
			return nil, errors.New("ib: wrong stage")
		}
		if !s.state.Play.Entered {
			if err = s.validateDeck(s.state.Deck); err != nil {
				return nil, err
			}
			if len(s.state.Deck) == 0 {
				return nil, errors.New("ib: deploy items before battle")
			}
			seed, err := randomBelow(math.MaxInt32)
			if err != nil {
				return nil, err
			}
			process := scalars(30, seed+1, a, c)
			process = s.decksWire(process, 5)
			process = s.itemsWire(process, 6)
			for n, p := range s.design.Decks[stage.EnemyDeck] {
				index := uint64(n + 1)
				d := s.design.Items[p.ItemID]
				process = wire.AppendBytes(process, 7, deckWire(placement{index, p.Position, p.Rotation}))
				process = wire.AppendBytes(process, 8, itemWire(item{Index: index, Type: d.Type, ID: d.ID}))
			}
			s.state.Process = process
			s.state.Play.Entered = true
		}
		return wire.AppendBytes(nil, 1, []byte(base64.StdEncoding.EncodeToString(s.state.Process))), nil
	case "/IbStageEnd":
		return s.end(ctx, a)
	}
	if err := s.preparing(); err != nil {
		return nil, err
	}
	switch path {
	case "/IbShopInfo":
		return s.shopWire(), nil
	case "/IbDeckSave":
		var deck []placement
		err := wire.Walk(b, func(f wire.Field) error {
			if f.Number != 2 {
				return nil
			}
			if f.Type != 2 {
				return fmt.Errorf("%w: field %d has wire type %d, want a message", wire.ErrMalformed, f.Number, f.Type)
			}
			if e := wire.Walk(f.Value, func(wire.Field) error { return nil }); e != nil {
				return e
			}
			deck = append(deck, placement{value(f.Value, 1), value(f.Value, 2), value(f.Value, 3)})
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err = s.validateDeck(deck); err != nil {
			return nil, err
		}
		s.state.Deck = deck
		return nil, nil
	case "/IbShopItemReserve":
		if a >= uint64(len(s.state.Shop)) || s.state.Shop[a].Sold || c > 1 {
			return nil, errors.New("ib: invalid reservation")
		}
		s.state.Shop[a].Reserved = c == 1
		return nil, nil
	case "/IbShopRefresh":
		cost := uint64(1)
		if s.design.RefreshPriceUp > 0 {
			cost += s.state.Play.Refreshes / s.design.RefreshPriceUp
		}
		if err := s.spend(cost); err != nil {
			return nil, err
		}
		s.state.Play.Refreshes++
		if err := s.refreshShop(true); err != nil {
			return nil, err
		}
		out := s.shopWire()
		out = wire.AppendVarint(out, 2, cost)
		out = wire.AppendVarint(out, 3, s.state.Play.Coin)
		return wire.AppendVarint(out, 4, s.state.Play.Refreshes), nil
	case "/IbShopBuy":
		if a >= uint64(len(s.state.Shop)) || s.state.Shop[a].Sold {
			return nil, errors.New("ib: unavailable shop slot")
		}
		entry := &s.state.Shop[a]
		if err := s.spend(entry.Price); err != nil {
			return nil, err
		}
		i, err := s.addItem(entry.ID)
		if err != nil {
			return nil, err
		}
		entry.Sold = true
		entry.Reserved = false
		out := wire.AppendBytes(nil, 1, itemWire(i))
		out = wire.AppendVarint(out, 2, entry.Price)
		return wire.AppendVarint(out, 3, s.state.Play.Coin), nil
	case "/IbItemSell":
		i, ok := s.state.Items[a]
		if !ok {
			return nil, errors.New("ib: item not owned")
		}
		for _, p := range s.state.Deck {
			if p.Index == a {
				return nil, errors.New("ib: remove item from deck before sale")
			}
		}
		price := uint64(math.RoundToEven(float64(s.design.Items[i.ID].Price) / 2))
		if err := s.credit(price); err != nil {
			return nil, err
		}
		delete(s.state.Items, a)
		return scalars(a, price, s.state.Play.Coin), nil
	case "/IbItemUpgrade":
		i, ok := s.state.Items[a]
		if !ok || c == 0 || c > math.MaxInt32 {
			return nil, errors.New("ib: invalid upgrade")
		}
		d := s.design.Items[i.ID]
		if i.Type == 1 && d.CostumeID != 0 {
			if s.options.CostumeLevel == nil {
				return nil, errors.New("ib: costume validation unavailable")
			}
			level, err := s.options.CostumeLevel(ctx, d.CostumeID)
			if err != nil {
				return nil, err
			}
			if i.Level+c > level {
				return nil, errors.New("ib: upgrade exceeds owned costume level")
			}
		}
		var total uint64
		for n := range c {
			cost, ok := s.design.UpgradeCosts[[3]uint64{i.Type, d.TypeID, i.Level + n + 2}]
			if !ok || cost > math.MaxInt32-total {
				return nil, errors.New("ib: level unavailable")
			}
			total += cost
		}
		if err := s.spend(total); err != nil {
			return nil, err
		}
		i.Level += c
		s.state.Items[a] = i
		out := wire.AppendBytes(nil, 1, itemWire(i))
		out = wire.AppendVarint(out, 2, total)
		return wire.AppendVarint(out, 3, s.state.Play.Coin), nil
	}
	return nil, errors.New("ib: unsupported operation")
}

func (s *Service) end(ctx command.Context, result uint64) ([]byte, error) {
	d, _, err := s.stage()
	if err != nil {
		return nil, err
	}
	if !s.state.Play.Entered || result < 1 || result > 3 {
		return nil, errors.New("ib: no battle to settle")
	}
	s.state.Play.Entered = false
	s.state.Process = nil
	var gain, next, cleared, first uint64
	var reward []byte
	if result == 1 {
		for n, stage := range d.Stages {
			if stage.ID != s.state.Play.Stage {
				continue
			}
			if n+1 < len(d.Stages) {
				next = d.Stages[n+1].ID
				gain = d.Stages[n+1].Credit
			} else {
				cleared = 1
			}
			break
		}
		if cleared == 1 && !s.state.Cleared[d.ID] {
			first = 1
			reward, err = s.economy.Apply(ctx, fmt.Sprintf("ib:%d:%d:%d", s.state.Season, d.ID, s.state.Run), nil, []gamedata.Reward{d.Reward})
			if err != nil {
				return nil, err
			}
			s.state.Cleared[d.ID] = true
		}
	} else {
		s.state.Play.Life--
		if s.state.Play.Life > 0 {
			gain = s.design.RetryCredit
		}
	}
	life := s.state.Play.Life
	if cleared == 1 || life == 0 {
		s.clearRun()
	} else {
		if next != 0 {
			s.state.Play.Stage = next
		}
		if err = s.credit(gain); err != nil {
			return nil, err
		}
		s.state.Play.Refreshes = 0
		if err = s.refreshShop(true); err != nil {
			return nil, err
		}
	}
	out := scalars(0, cleared, first, life, next, gain, s.state.Play.Coin)
	if len(reward) > 0 {
		out = wire.AppendBytes(out, 1, reward)
	}
	return out, nil
}

func (s *Service) refreshShop(keepReserved bool) error {
	_, stage, err := s.stage()
	if err != nil {
		return err
	}
	for slot := range uint64(4) {
		if keepReserved && int(slot) < len(s.state.Shop) && s.state.Shop[slot].Reserved && !s.state.Shop[slot].Sold {
			continue
		}
		id := stage.Slots[slot]
		if id == 0 {
			var pools [7][]uint64
			for id, i := range s.design.Items {
				if i.SellType == 1 && i.Grade < 7 {
					pools[i.Grade] = append(pools[i.Grade], id)
				}
			}
			var sum uint64
			for grade, ids := range pools {
				if len(ids) > 0 {
					sum += stage.Ratios[grade]
				}
			}
			draw, e := randomBelow(sum)
			if e != nil {
				return e
			}
			var chosen []uint64
			for grade, ids := range pools {
				if len(ids) == 0 {
					continue
				}
				weight := stage.Ratios[grade]
				if draw < weight {
					chosen = ids
					break
				}
				draw -= weight
			}
			slices.Sort(chosen)
			index, e := randomBelow(uint64(len(chosen)))
			if e != nil {
				return e
			}
			id = chosen[index]
		}
		i, ok := s.design.Items[id]
		if !ok {
			return errors.New("ib: shop item unavailable")
		}
		entry := shopItem{Slot: slot, Type: i.Type, ID: id, Price: i.Price, Original: i.Price}
		if len(s.state.Shop) <= int(slot) {
			s.state.Shop = append(s.state.Shop, entry)
		} else {
			s.state.Shop[slot] = entry
		}
	}
	return nil
}

func rotate(p gamedata.IBPoint, r uint64) gamedata.IBPoint {
	switch r {
	case 1:
		return gamedata.IBPoint{X: -p.Y, Y: p.X}
	case 2:
		return gamedata.IBPoint{X: -p.X, Y: -p.Y}
	case 3:
		return gamedata.IBPoint{X: p.Y, Y: -p.X}
	}
	return p
}
func cells(shape []gamedata.IBPoint, p placement) ([]gamedata.IBPoint, error) {
	if p.Position >= 49 || p.Rotation > 3 {
		return nil, errors.New("ib: invalid placement")
	}
	if len(shape) == 0 {
		shape = []gamedata.IBPoint{{}}
	}
	var out []gamedata.IBPoint
	for _, offset := range shape {
		point := rotate(offset, p.Rotation)
		point.X += int(p.Position % 7)
		point.Y += int(p.Position / 7)
		if point.X < 0 || point.X >= 7 || point.Y < 0 || point.Y >= 7 {
			return nil, errors.New("ib: item outside board")
		}
		out = append(out, point)
	}
	return out, nil
}
func (s *Service) validateDeck(deck []placement) error {
	if len(deck) > len(s.state.Items) {
		return errors.New("ib: too many deployed items")
	}
	seen := map[uint64]bool{}
	floors := map[gamedata.IBPoint]bool{}
	sockets := map[gamedata.IBPoint]bool{}
	for _, p := range deck {
		if _, ok := s.state.Items[p.Index]; !ok || seen[p.Index] {
			return errors.New("ib: invalid item reference")
		}
		seen[p.Index] = true
	}
	for typ := range uint64(3) {
		occupied := map[gamedata.IBPoint]bool{}
		for _, p := range deck {
			i := s.state.Items[p.Index]
			if i.Type != typ {
				continue
			}
			d := s.design.Items[i.ID]
			points, err := cells(d.Shape, p)
			if err != nil {
				return err
			}
			if typ == 2 {
				point := gamedata.IBPoint{X: int(p.Position % 7), Y: int(p.Position / 7)}
				if !sockets[point] || occupied[point] {
					return errors.New("ib: equipment requires free unit socket")
				}
				occupied[point] = true
				continue
			}
			for _, point := range points {
				if occupied[point] || typ == 1 && !floors[point] {
					return errors.New("ib: overlapping item or missing floor")
				}
				occupied[point] = true
				if typ == 0 {
					floors[point] = true
				}
			}
			if typ == 1 {
				for _, offset := range s.design.Sockets[[2]uint64{d.TypeID, i.Level + 1}] {
					if offset == (gamedata.IBPoint{}) || !slices.Contains(d.Shape, offset) {
						continue
					}
					point := rotate(offset, p.Rotation)
					point.X += int(p.Position % 7)
					point.Y += int(p.Position / 7)
					sockets[point] = true
				}
			}
		}
	}
	return nil
}
