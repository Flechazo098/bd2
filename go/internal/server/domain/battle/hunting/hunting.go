// Package hunting owns ordinary HuntingGround progress and repeatable rewards.
package hunting

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

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

// Rules supplies shared immutable hunting and dispatch designs.
type Rules interface {
	HuntingPack(int) (*gamedata.HuntingPack, error)
	DispatchDesign(uint64, uint64) (*gamedata.DispatchDesign, error)
}

type Service struct {
	storage             stateio.Store
	inventory           *assets.Inventory
	wallet              *assets.Wallet
	state               snapshot
	currentPack         func(command.Context) (int, error)
	load                func(int) (*gamedata.HuntingPack, error)
	dispatchLoad        func(uint64, uint64) (*gamedata.DispatchDesign, error)
	dispatchEligibility func(*gamedata.DispatchDesign) error
	eligibility         func(ctx command.Context, _ int, _ uint64) error
	grant               func(command.Context, string, []gamedata.Reward) ([]byte, error)
	apDesign            *gamedata.HuntingAPDesign
	now                 func() time.Time
}

func Open(ctx command.Context, store stateio.Store, rules Rules, inventory *assets.Inventory, wallet *assets.Wallet, currentPack func(command.Context) (int, error), free, bonus uint64) (*Service, error) {
	if store == nil || rules == nil || inventory == nil || wallet == nil || currentPack == nil {
		return nil, fmt.Errorf("hunting: invalid configuration")
	}
	s := &Service{storage: store, inventory: inventory, wallet: wallet, currentPack: currentPack, state: snapshot{versionconfig.State(), free, bonus, map[string]packState{}, map[string]bool{}}}
	s.dispatchLoad = rules.DispatchDesign
	s.load = rules.HuntingPack
	b, err := store.Load(ctx.State, "hunting")
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
func (s *Service) HuntingAP(ctx command.Context) (uint64, uint64, error) {

	if e := s.refreshAP(ctx); e != nil {
		return 0, 0, e
	}
	return s.state.Free, s.state.Bonus, nil
}

func ground(d *gamedata.HuntingPack, id uint64) (gamedata.HuntingGround, bool) {
	for _, g := range d.Grounds {
		if g.ID == id {
			return g, true
		}
	}
	return gamedata.HuntingGround{}, false
}

func (s *Service) ValidateBattle(ctx command.Context, pack int, mode, monster, deck uint64) error {
	if mode != BattleMode {
		return nil
	}

	if e := s.refreshAP(ctx); e != nil {
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

func (s *Service) clone() snapshot {
	next := s.state
	next.Packs = map[string]packState{}
	maps.Copy(next.Packs, s.state.Packs)
	next.Receipts = map[string]bool{}
	maps.Copy(next.Receipts, s.state.Receipts)
	return next
}
func (s *Service) persist(ctx command.Context, next snapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.Save(ctx.State, "hunting", b); err != nil {
		return err
	}
	s.state = next
	return nil
}
