package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
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

func (s *Service) ConfigureNPCRuntime(source *gamedata.Source, store stateio.Store) error {
	if store == nil {
		return fmt.Errorf("world: missing NPC reputation store")
	}
	s.npcReputation = &npcReputationRuntime{store: store, load: func(pack int) (gamedata.NPCReputationDesign, error) {
		return source.NPCReputation(pack)
	}, inns: func(pack int) ([]gamedata.InnRule, error) { return source.Inns(pack) }, now: time.Now}
	return nil
}
func (r *npcReputationRuntime) snapshot(ctx command.Context) (npcReputationSnapshot, error) {
	v := npcReputationSnapshot{Starts: map[string]int64{}, Claims: map[string]bool{}}
	b, err := r.store.Load(ctx.State, "npcreputation")
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
func (s *Service) reputationState(ctx command.Context, pack int, rule gamedata.NPCReputationRule) (uint64, uint64, error) {
	if s.npcReputation == nil {
		return 1, 0, nil
	}
	v, err := s.npcReputation.snapshot(ctx)
	if err != nil {
		return 0, 0, err
	}
	start, ok := v.Starts[reputationKey(pack, rule.ID)]
	if !ok {
		return 1, 0, nil
	}
	elapsed := max(s.npcReputation.now().Unix()-start, 0)
	if uint64(elapsed) >= rule.DownHours*3600 {
		return 1, 0, nil
	}
	return 2, uint64(elapsed), nil
}

func (s *Service) NPCShopReputation(ctx command.Context, pack uint64) (uint64, uint64, error) {
	current, err := s.CurrentPackID(ctx)
	if err != nil || pack == 0 || !s.packUnlocked(ctx, int(pack)) || s.npcReputation == nil {
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
	state, _, err := s.reputationState(ctx, int(pack), rule)
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
func (s *Service) InnContext(ctx command.Context, pack, npc uint64) (gamedata.InnRule, uint64, error) {
	var empty gamedata.InnRule
	current, err := s.CurrentPackID(ctx)
	if err != nil || s.npcReputation == nil || (pack != 0 && pack != uint64(current)) || !s.packUnlocked(ctx, current) {
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
		state, _, e := s.reputationState(ctx, current, d.Groups[r.MapGroup])
		return r, state, e
	}
	return empty, 0, fmt.Errorf("world: motel NPC outside current map")
}
