package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type npcReputationRuntime struct {
	store stateio.Store
	load  func(int) (gamedata.NPCReputationDesign, error)
	inns  func(int) ([]gamedata.InnRule, error)
	now   func() time.Time
}
type npcReputationSnapshot struct {
	Starts map[string]int64
	Claims map[string]bool
}

func (s *Service) ConfigureNPCRuntime(root, version string, store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("world: missing NPC reputation store")
	}
	s.npcReputation = &npcReputationRuntime{store: store, load: func(pack int) (gamedata.NPCReputationDesign, error) {
		return gamedata.LoadNPCReputation(root, version, pack)
	}, inns: func(pack int) ([]gamedata.InnRule, error) { return gamedata.LoadInns(root, version, pack) }, now: time.Now}
	return nil
}
func (r *npcReputationRuntime) snapshot() (npcReputationSnapshot, error) {
	v := npcReputationSnapshot{Starts: map[string]int64{}, Claims: map[string]bool{}}
	b, err := r.store.Load("npcreputation")
	if err != nil {
		return v, err
	}
	if b != nil {
		if err = stateio.RequireExactJSONObject(b, "Starts", "Claims"); err != nil {
			return v, err
		}
		if err = json.Unmarshal(b, &v); err != nil || v.Starts == nil || v.Claims == nil {
			return v, fmt.Errorf("world: invalid NPC reputation state")
		}
	}
	for _, start := range v.Starts {
		if start <= 0 {
			return v, fmt.Errorf("world: invalid NPC reputation timestamp")
		}
	}
	return v, nil
}
func reputationKey(pack int, group uint64) string { return fmt.Sprintf("%d/%d", pack, group) }
func (s *Service) reputationState(pack int, rule gamedata.NPCReputationRule) (uint64, uint64, error) {
	if s.npcReputation == nil {
		return 1, 0, nil
	}
	v, err := s.npcReputation.snapshot()
	if err != nil {
		return 0, 0, err
	}
	start, ok := v.Starts[reputationKey(pack, rule.ID)]
	if !ok {
		return 1, 0, nil
	}
	elapsed := s.npcReputation.now().Unix() - start
	if elapsed < 0 {
		elapsed = 0
	}
	if uint64(elapsed) >= rule.DownHours*3600 {
		return 1, 0, nil
	}
	return 2, uint64(elapsed), nil
}
func (s *Service) CompleteNPCReputation(identity string, pack int, group uint64) ([]byte, error) {
	if s.npcReputation == nil || identity == "" || group == 0 || !s.packUnlocked(pack) {
		return nil, ErrInvalidRequest
	}
	d, err := s.npcReputation.load(pack)
	if err != nil {
		return nil, err
	}
	rule, ok := d.Groups[group]
	if !ok {
		return nil, fmt.Errorf("world: unknown reputation group")
	}
	v, err := s.npcReputation.snapshot()
	if err != nil {
		return nil, err
	}
	claim := fmt.Sprintf("%s:%d:%d", identity, pack, group)
	if !v.Claims[claim] {
		v.Claims[claim] = true
		v.Starts[reputationKey(pack, group)] = s.npcReputation.now().Unix()
		b, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		if e = s.npcReputation.store.Save("npcreputation", b); e != nil {
			return nil, e
		}
	}
	state, elapsed, err := s.reputationState(pack, rule)
	if err != nil {
		return nil, err
	}
	return reputationWire(group, state, elapsed), nil
}
func reputationWire(group, state, elapsed uint64) []byte {
	b := wire.AppendVarint(nil, 1, group)
	b = wire.AppendVarint(b, 2, state)
	if elapsed > 0 {
		b = wire.AppendVarint(b, 3, elapsed)
	}
	return b
}

func (s *Service) npcReputationRows(pack int) ([][]byte, error) {
	if s.npcReputation == nil {
		return nil, nil
	}
	d, err := s.npcReputation.load(pack)
	if err != nil {
		return nil, err
	}
	var groups []uint64
	for id := range d.Groups {
		groups = append(groups, id)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	var rows [][]byte
	for _, id := range groups {
		state, elapsed, e := s.reputationState(pack, d.Groups[id])
		if e != nil {
			return nil, e
		}
		rows = append(rows, reputationWire(id, state, elapsed))
	}
	return rows, nil
}
func (s *Service) NPCShopReputation(pack uint64) (uint64, uint64, error) {
	current, err := s.CurrentPackID()
	if err != nil || pack == 0 || !s.packUnlocked(int(pack)) || s.npcReputation == nil {
		return 0, 0, ErrInvalidRequest
	}
	d, err := s.npcReputation.load(int(pack))
	if err != nil {
		return 0, 0, err
	}
	var group uint64
	position, ok := s.state.Position()
	if ok && position.PackID == current {
		currentDesign := d
		if pack != uint64(current) {
			currentDesign, err = s.npcReputation.load(current)
			if err != nil {
				return 0, 0, err
			}
		}
		group = currentDesign.MapGroups[position.Position.MapID]
	}
	if group == 0 && len(d.Groups) == 1 {
		for id := range d.Groups {
			group = id
		}
	}
	rule, ok := d.Groups[group]
	if !ok {
		return 1, 0, nil
	}
	state, _, err := s.reputationState(int(pack), rule)
	if err != nil {
		return 0, 0, err
	}
	if state == 2 {
		return state, rule.GoodPrice, nil
	}
	return state, 0, nil
}

// InnContext validates the motel's real map, rather than trusting an NPC ID
// supplied from another pack or the previously visited scene.
func (s *Service) InnContext(pack, npc uint64) (gamedata.InnRule, uint64, error) {
	var empty gamedata.InnRule
	current, err := s.CurrentPackID()
	if err != nil || s.npcReputation == nil || (pack != 0 && pack != uint64(current)) || !s.packUnlocked(current) {
		return empty, 0, ErrInvalidRequest
	}
	position, ok := s.state.Position()
	if !ok || position.PackID != current {
		return empty, 0, fmt.Errorf("world: inn requires current field position")
	}
	rules, err := s.npcReputation.inns(current)
	if err != nil {
		return empty, 0, err
	}
	d, err := s.npcReputation.load(current)
	if err != nil {
		return empty, 0, err
	}
	for _, r := range rules {
		if r.MapID != uint64(position.Position.MapID) || (npc != 0 && npc != r.NPCID) {
			continue
		}
		state, _, e := s.reputationState(current, d.Groups[r.MapGroup])
		return r, state, e
	}
	return empty, 0, fmt.Errorf("world: motel NPC outside current map")
}
