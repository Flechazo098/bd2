// Package hunting owns ordinary HuntingGround progress and repeatable rewards.
package hunting

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
)

const BattleMode uint64 = 5

type packState struct {
	Current, Highest uint64
	Auto             bool
	Defeated         []uint64
}
type snapshot struct {
	Version  string               `json:"version"`
	Free     uint64               `json:"free"`
	Bonus    uint64               `json:"bonus"`
	Packs    map[string]packState `json:"packs"`
	Receipts map[string]bool      `json:"receipts"`
}
type Service struct {
	mu                  sync.Mutex
	storage             stateio.Store
	inventory           *player.Inventory
	wallet              *player.Wallet
	state               snapshot
	currentPack         func() (int, error)
	load                func(int) (*gamedata.HuntingPack, error)
	dispatchLoad        func(uint64, uint64) (*gamedata.DispatchDesign, error)
	dispatchEligibility func(*gamedata.DispatchDesign) error
}

func Open(store stateio.Store, root, version string, inventory *player.Inventory, wallet *player.Wallet, currentPack func() (int, error), free, bonus uint64) (*Service, error) {
	if store == nil || inventory == nil || wallet == nil || currentPack == nil {
		return nil, fmt.Errorf("hunting: invalid configuration")
	}
	s := &Service{storage: store, inventory: inventory, wallet: wallet, currentPack: currentPack, state: snapshot{versionconfig.State(), free, bonus, map[string]packState{}, map[string]bool{}}}
	s.dispatchLoad = func(group, id uint64) (*gamedata.DispatchDesign, error) {
		return gamedata.LoadDispatchDesign(root, version, group, id)
	}
	s.load = func(pack int) (*gamedata.HuntingPack, error) { return gamedata.LoadHuntingPack(root, version, pack) }
	b, err := store.Load("hunting")
	if err != nil {
		return nil, err
	}
	if b != nil {
		if err := stateio.RequireExactJSONObject(b, "version", "free", "bonus", "packs", "receipts"); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
		if s.state.Version != versionconfig.State() || s.state.Packs == nil || s.state.Receipts == nil {
			return nil, fmt.Errorf("hunting: incompatible state")
		}
	}
	if s.state.Free > math.MaxInt32 || s.state.Bonus > math.MaxInt32 {
		return nil, fmt.Errorf("hunting: AP exceeds protocol range")
	}
	for key, st := range s.state.Packs {
		pack, err := strconv.Atoi(key)
		if err != nil || pack <= 0 || strconv.Itoa(pack) != key {
			return nil, fmt.Errorf("hunting: invalid saved pack")
		}
		d, err := s.load(pack)
		if err != nil {
			return nil, err
		}
		if _, ok := ground(d, st.Current); !ok {
			return nil, fmt.Errorf("hunting: saved current ground missing from GameData")
		}
		if st.Highest != 0 {
			if _, ok := ground(d, st.Highest); !ok {
				return nil, fmt.Errorf("hunting: saved highest ground missing from GameData")
			}
		}
		g, _ := ground(d, st.Current)
		seen := map[uint64]bool{}
		for _, id := range st.Defeated {
			member := id == g.BossID
			for _, candidate := range g.Monsters {
				member = member || id == candidate
			}
			if !member || seen[id] {
				return nil, fmt.Errorf("hunting: invalid defeated monster")
			}
			seen[id] = true
		}
	}
	for key, claimed := range s.state.Receipts {
		if key == "" || !claimed {
			return nil, fmt.Errorf("hunting: invalid receipt")
		}
	}
	return s, nil
}
func (s *Service) HuntingAP() (uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Free, s.state.Bonus, nil
}
func (s *Service) Handle(path string, req []byte) (int, []byte, bool, error) {
	if path == "/HuntDispatch" || path == "/HuntDispatchInfo" || path == "/HuntDispatchStart" || path == "/HuntDispatchEnd" || path == "/HuntDispatchRewardPreview" {
		return s.handleDispatch(path, req, "local")
	}
	if path != "/HuntingGroundInfo" && path != "/HuntingGroundInfoList" && path != "/HuntingGroundEnter" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, found, err := wire.Varint(req, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("hunting: missing sequence")
	}
	if path == "/HuntingGroundInfoList" {
		ids, err := packed(req, 2)
		if err != nil {
			return 387, nil, true, err
		}
		var response []byte
		seen := map[uint64]bool{}
		for _, id := range ids {
			if id == 0 || id > math.MaxInt32 || seen[id] {
				return 387, nil, true, fmt.Errorf("hunting: invalid pack list")
			}
			seen[id] = true
			d, err := s.load(int(id))
			if err != nil {
				return 387, nil, true, err
			}
			if len(d.Grounds) == 0 {
				continue
			}
			info, err := s.info(int(id), d)
			if err != nil {
				return 387, nil, true, err
			}
			response = wire.AppendBytes(response, 1, info)
		}
		return 387, response, true, nil
	}
	pack, found, err := wire.Varint(req, 2)
	if err != nil || !found || pack == 0 || pack > math.MaxInt32 {
		return 134, nil, true, fmt.Errorf("hunting: invalid pack")
	}
	d, err := s.load(int(pack))
	if err != nil {
		return 134, nil, true, err
	}
	if len(d.Grounds) == 0 {
		return 134, nil, true, fmt.Errorf("hunting: pack has no hunting ground")
	}
	if path == "/HuntingGroundInfo" {
		b, err := s.info(int(pack), d)
		return 134, wire.AppendBytes(nil, 1, b), true, err
	}
	current, err := s.currentPack()
	if err != nil || current != int(pack) {
		return 110, nil, true, fmt.Errorf("hunting: enter pack is not current")
	}
	id, _, err := wire.Varint(req, 3)
	if err != nil {
		return 110, nil, true, err
	}
	auto, _, err := wire.Varint(req, 4)
	if err != nil || auto > 1 {
		return 110, nil, true, fmt.Errorf("hunting: invalid auto flag")
	}
	st := s.state.Packs[strconv.Itoa(int(pack))]
	g, ok := ground(d, id)
	if !ok {
		return 110, nil, true, fmt.Errorf("hunting: unknown ground")
	}
	first := d.Grounds[0].ID
	if id != first && id > st.Highest {
		previous := uint64(0)
		for _, v := range d.Grounds {
			if v.ID < id {
				previous = v.ID
			}
		}
		if previous == 0 || previous > st.Highest {
			return 110, nil, true, fmt.Errorf("hunting: ground difficulty locked")
		}
	}
	st.Current, st.Auto = id, auto != 0
	st.Defeated = nil
	next := s.clone()
	next.Packs[strconv.Itoa(int(pack))] = st
	if err := s.persist(next); err != nil {
		return 110, nil, true, err
	}
	var out []byte
	for _, m := range monstersWithState(d, g, st.Defeated) {
		out = wire.AppendBytes(out, 1, m)
	}
	return 110, out, true, nil
}
func (s *Service) info(pack int, d *gamedata.HuntingPack) ([]byte, error) {
	st := s.state.Packs[strconv.Itoa(pack)]
	if st.Current == 0 {
		st.Current = d.Grounds[0].ID
	}
	g, ok := ground(d, st.Current)
	if !ok {
		return nil, fmt.Errorf("hunting: saved ground missing from GameData")
	}
	b := wire.AppendVarint(nil, 2, st.Current)
	b = wire.AppendVarint(b, 3, st.Highest)
	b = wire.AppendVarint(b, 5, uint64(pack))
	if st.Auto {
		b = wire.AppendVarint(b, 1, 1)
	}
	for _, m := range monstersWithState(d, g, st.Defeated) {
		b = wire.AppendBytes(b, 4, m)
	}
	return b, nil
}
func ground(d *gamedata.HuntingPack, id uint64) (gamedata.HuntingGround, bool) {
	for _, g := range d.Grounds {
		if g.ID == id {
			return g, true
		}
	}
	return gamedata.HuntingGround{}, false
}
func monsterWire(m gamedata.HuntingMonster, active bool) []byte {
	b := wire.AppendVarint(nil, 1, m.ID)
	b = wire.AppendVarint(b, 2, m.Decks[0])
	if active {
		b = wire.AppendVarint(b, 6, 1)
	}
	return b
}
func monsters(d *gamedata.HuntingPack, g gamedata.HuntingGround) [][]byte {
	return monstersWithState(d, g, nil)
}
func monstersWithState(d *gamedata.HuntingPack, g gamedata.HuntingGround, defeated []uint64) [][]byte {
	ids := append(append([]uint64(nil), g.Monsters...), g.BossID)
	out := make([][]byte, 0, len(ids))
	for _, id := range ids {
		active := true
		for _, dead := range defeated {
			if dead == id {
				active = false
			}
		}
		out = append(out, monsterWire(d.Monsters[id], active))
	}
	return out
}
func (s *Service) ValidateBattle(pack int, mode, monster, deck uint64) error {
	if mode != BattleMode {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, _, err := s.validate(pack, monster, deck)
	return err
}
func (s *Service) validate(pack int, monster, deck uint64) (*gamedata.HuntingPack, gamedata.HuntingGround, gamedata.HuntingMonster, error) {
	d, err := s.load(pack)
	if err != nil {
		return nil, gamedata.HuntingGround{}, gamedata.HuntingMonster{}, err
	}
	st := s.state.Packs[strconv.Itoa(pack)]
	g, ok := ground(d, st.Current)
	if !ok {
		return nil, g, gamedata.HuntingMonster{}, fmt.Errorf("hunting: battle before ground enter")
	}
	member := monster == g.BossID
	for _, id := range g.Monsters {
		member = member || id == monster
	}
	m := d.Monsters[monster]
	for _, dead := range st.Defeated {
		if dead == monster {
			return nil, g, m, fmt.Errorf("hunting: defeated monster requires reentry")
		}
	}
	known := false
	for _, id := range m.Decks {
		known = known || id == deck
	}
	if !member || !known {
		return nil, g, m, fmt.Errorf("hunting: monster/deck does not belong to selected ground")
	}
	cost := d.NormalAP
	if m.Type == 1 {
		cost = d.BossAP
	}
	if s.state.Free < cost && s.state.Bonus < cost-s.state.Free {
		return nil, g, m, fmt.Errorf("hunting: insufficient hunting AP")
	}
	return d, g, m, nil
}
func (s *Service) CompleteBattle(pack int, mode, monster, deck uint64, receipt string) ([]byte, [][]byte, error) {
	if mode != BattleMode {
		return nil, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt == "" {
		return nil, nil, fmt.Errorf("hunting: missing battle receipt")
	}
	if s.state.Receipts[receipt] {
		return nil, nil, nil
	}
	d, g, m, err := s.validate(pack, monster, deck)
	if err != nil {
		return nil, nil, err
	}
	rewards := m.Rewards[deck]
	identity := "hunting:" + receipt
	currency := make([]gamedata.Reward, 0)
	stack := make([]gamedata.BattleReward, 0)
	for _, r := range rewards {
		if r.Type == 2 || r.Type == 3 || r.Type == 4 || r.Type == 12 || r.Type == 20 {
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count})
		} else {
			stack = append(stack, r)
		}
	}
	if _, err := s.wallet.GrantQuestOnce(identity+":currency", currency); err != nil {
		return nil, nil, err
	}
	items, err := s.inventory.GrantOnce(identity+":items", stack)
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		items = s.inventory.GrantedItems(identity + ":items")
	}
	next := s.clone()
	cost := d.NormalAP
	if m.Type == 1 {
		cost = d.BossAP
	}
	if next.Free >= cost {
		next.Free -= cost
	} else {
		next.Bonus -= cost - next.Free
		next.Free = 0
	}
	if monster == g.BossID {
		st := next.Packs[strconv.Itoa(pack)]
		if st.Highest < g.ID {
			st.Highest = g.ID
		}
		next.Packs[strconv.Itoa(pack)] = st
	}
	next.Receipts[receipt] = true
	st := next.Packs[strconv.Itoa(pack)]
	st.Defeated = append(append([]uint64(nil), st.Defeated...), monster)
	next.Packs[strconv.Itoa(pack)] = st
	if err := s.persist(next); err != nil {
		return nil, nil, err
	}
	var bundle []byte
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, view)
	}
	for _, r := range currency {
		b := wire.AppendVarint(nil, 3, r.Type)
		b = wire.AppendVarint(b, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, b)
	}
	return bundle, [][]byte{monsterWire(m, false)}, nil
}
func (s *Service) clone() snapshot {
	next := s.state
	next.Packs = map[string]packState{}
	for k, v := range s.state.Packs {
		next.Packs[k] = v
	}
	next.Receipts = map[string]bool{}
	for k, v := range s.state.Receipts {
		next.Receipts[k] = v
	}
	return next
}
func (s *Service) persist(next snapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.Save("hunting", b); err != nil {
		return err
	}
	s.state = next
	return nil
}
func packed(req []byte, n int) ([]uint64, error) {
	var out []uint64
	err := wire.Walk(req, func(f wire.Field) error {
		if f.Number != n {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			out = append(out, v)
			return nil
		}
		if f.Type != 2 {
			return fmt.Errorf("hunting: invalid repeated integer")
		}
		for raw := f.Value; len(raw) > 0; {
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return fmt.Errorf("hunting: invalid packed integer")
			}
			out = append(out, v)
			raw = raw[n:]
		}
		return nil
	})
	return out, err
}
