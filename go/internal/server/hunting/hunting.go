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
	"maps"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"
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
	eligibility         func(int, uint64) error
	grant               func(string, []gamedata.Reward) ([]byte, error)
	apDesign            *gamedata.HuntingAPDesign
	now                 func() time.Time
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
	if e := s.refreshAP(); e != nil {
		return 0, 0, e
	}
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
				response = wire.AppendBytes(response, 1, wire.AppendVarint(nil, 5, id))
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
	if path == "/HuntingGroundInfo" {
		// A catalog entry is not an account's active hunting run. The detail
		// endpoint preserves an explicitly present empty message until entry.
		if s.state.Packs[strconv.Itoa(int(pack))].Current == 0 {
			return 134, wire.AppendBytes(nil, 1, nil), true, nil
		}
		b, err := s.info(int(pack), d)
		return 134, wire.AppendBytes(nil, 1, b), true, err
	}
	if len(d.Grounds) == 0 {
		return 110, nil, true, fmt.Errorf("hunting: pack has no hunting ground")
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
	if s.eligibility != nil {
		if err := s.eligibility(int(pack), g.Difficulty); err != nil {
			return 110, nil, true, err
		}
	} else if id != d.Grounds[0].ID {
		return 110, nil, true, fmt.Errorf("hunting: main quest difficulty eligibility unavailable")
	}
	if st.Current != id {
		st.Defeated = nil
	}
	st.Current, st.Auto = id, auto != 0
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
	initial := st.Current == 0
	if st.Current == 0 {
		st.Current = d.Grounds[0].ID
	}
	g, ok := ground(d, st.Current)
	if !ok {
		return nil, fmt.Errorf("hunting: saved ground missing from GameData")
	}
	b := wire.AppendVarint(nil, 2, st.Current)
	if st.Highest != 0 {
		b = wire.AppendVarint(b, 3, st.Highest)
	}
	b = wire.AppendVarint(b, 5, uint64(pack))
	if st.Auto {
		b = wire.AppendVarint(b, 1, 1)
	}
	var monsterInfos [][]byte
	if initial {
		// The initial catalog lists ordinary monsters only. A boss is not an
		// active account encounter simply because its design row exists.
		for _, id := range g.Monsters {
			monsterInfos = append(monsterInfos, monsterWire(d.Monsters[id], true))
		}
	} else {
		monsterInfos = monstersWithState(d, g, st.Defeated)
	}
	for _, m := range monsterInfos {
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
func monstersWithState(d *gamedata.HuntingPack, g gamedata.HuntingGround, defeated []uint64) [][]byte {
	dead := map[uint64]bool{}
	for _, id := range defeated {
		dead[id] = true
	}
	boss := len(g.Monsters) == 0
	all := true
	for _, id := range g.Monsters {
		all = all && dead[id]
	}
	boss = boss || all
	out := make([][]byte, 0, len(g.Monsters)+1)
	for _, id := range g.Monsters {
		out = append(out, monsterWire(d.Monsters[id], !dead[id]))
	}
	if boss {
		out = append(out, monsterWire(d.Monsters[g.BossID], true))
	}
	return out
}
func (s *Service) ValidateBattle(pack int, mode, monster, deck uint64) error {
	if mode != BattleMode {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.refreshAP(); e != nil {
		return e
	}
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
	if monster == g.BossID {
		for _, id := range g.Monsters {
			done := false
			for _, dead := range st.Defeated {
				done = done || dead == id
			}
			if !done {
				return nil, g, m, fmt.Errorf("hunting: boss locked until ordinary monsters are defeated")
			}
		}
	}
	if slices.Contains(st.Defeated, monster) {
		return nil, g, m, fmt.Errorf("hunting: defeated monster requires reentry")
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
	fingerprint := fmt.Sprintf("%d:%d:%d:%d", pack, mode, monster, deck)
	ledger, err := s.loadBattleReceipts()
	if err != nil {
		return nil, nil, err
	}
	if saved, ok := ledger[receipt]; ok {
		if saved.Fingerprint != fingerprint {
			return nil, nil, fmt.Errorf("hunting: battle receipt conflict")
		}
		return saved.Bundle, saved.Monsters, nil
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
			currency = append(currency, gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count}) //nolint:staticcheck // S1016
		} else {
			stack = append(stack, r)
		}
	}
	var grantedBundle []byte
	var items []player.Item
	if s.grant != nil {
		rs := make([]gamedata.Reward, len(rewards))
		for i, r := range rewards {
			rs[i] = gamedata.Reward{Type: r.Type, ID: r.ID, Count: r.Count} //nolint:staticcheck // S1016
		}
		s.mu.Unlock()
		grantedBundle, err = s.grant(identity, rs)
		s.mu.Lock()
		if err != nil {
			return nil, nil, err
		}
	} else {
		if _, err := s.wallet.GrantQuestOnce(identity+":currency", currency); err != nil {
			return nil, nil, err
		}
		items, err = s.inventory.GrantOnce(identity+":items", stack)
		if err != nil {
			return nil, nil, err
		}
		if len(items) == 0 {
			items = s.inventory.GrantedItems(identity + ":items")
		}
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
	st := next.Packs[strconv.Itoa(pack)]
	if monster == g.BossID {
		if st.Highest < g.ID {
			st.Highest = g.ID
		}
		st.Defeated = nil
	} else {
		st.Defeated = append(append([]uint64(nil), st.Defeated...), monster)
	}
	next.Packs[strconv.Itoa(pack)] = st
	next.Receipts[receipt] = true
	var bundle []byte
	if s.grant != nil {
		bundle = grantedBundle
	}
	for _, item := range items {
		bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		bundle = wire.AppendBytes(bundle, 6, view)
	}
	for _, r := range currency {
		if s.grant != nil {
			break
		}
		b := wire.AppendVarint(nil, 3, r.Type)
		b = wire.AppendVarint(b, 4, r.Count)
		bundle = wire.AppendBytes(bundle, 1, b)
	}
	updates := [][]byte{monsterWire(m, false)}
	if monster == g.BossID {
		for _, id := range g.Monsters {
			updates = append(updates, monsterWire(d.Monsters[id], true))
		}
	} else if len(st.Defeated) == len(g.Monsters) {
		updates = append(updates, monsterWire(d.Monsters[g.BossID], true))
	}
	if err := s.persist(next); err != nil {
		return nil, nil, err
	}
	latest, err := s.loadBattleReceipts()
	if err != nil {
		return nil, nil, err
	}
	maps.Copy(ledger, latest)
	ledger[receipt] = battleReceipt{Fingerprint: fingerprint, Bundle: bundle, Monsters: updates}
	if err := s.saveBattleReceipts(ledger); err != nil {
		return nil, nil, err
	}
	return bundle, updates, nil
}
func (s *Service) clone() snapshot {
	next := s.state
	next.Packs = map[string]packState{}
	maps.Copy(next.Packs, s.state.Packs)
	next.Receipts = map[string]bool{}
	maps.Copy(next.Receipts, s.state.Receipts)
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
