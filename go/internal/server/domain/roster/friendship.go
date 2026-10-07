package roster

import (
	"bd2server/internal/server/design/gamedata"

	assets "bd2server/internal/server/domain/inventory"
	"errors"
	"math"

	"time"
)

// FriendshipService applies inventory gifts and counseling inside the account
// request transaction. Static IDs, thresholds and rewards come from GameData.
type FriendshipService struct {
	design     *gamedata.FriendshipDesign
	awake      *gamedata.CharAwakeDesign
	potential  *gamedata.CostumePotentialDesign
	collection *CollectionStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet

	now func() time.Time
}

func NewFriendshipService(design *gamedata.FriendshipDesign, awake *gamedata.CharAwakeDesign, potential *gamedata.CostumePotentialDesign, collection *CollectionStore, inventory *assets.Inventory, wallet *assets.Wallet) (*FriendshipService, error) {
	if design == nil || awake == nil || potential == nil || collection == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete friendship service")
	}
	s := &FriendshipService{design: design, awake: awake, potential: potential, collection: collection, inventory: inventory, wallet: wallet, now: time.Now}
	for _, entry := range collection.FriendshipEntries() {
		if entry.Daily != nil && entry.Daily.Used > design.Default.MaxCounselingAP {
			return nil, errors.New("player: saved daily friendship AP exceeds maximum")
		}
		if entry.State == nil {
			continue
		}
		state := entry.State
		if _, owned := s.owned(state.CostumeID); !owned {
			return nil, errors.New("player: saved friendship costume is not owned")
		}
		level, ok := design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok || (level.NextEXP != 0 && state.EXP >= level.NextEXP) || (state.Level >= design.Default.MaxLevels[2] && state.EXP != 0) {
			return nil, errors.New("player: saved friendship level is not in GameData")
		}
		if state.CounselingCount > design.Default.MaxCounselingAPByCostume && state.Level < design.Default.MaxLevels[2] {
			return nil, errors.New("player: invalid saved costume counseling count")
		}
		for _, id := range state.Sessions {
			if _, ok := design.Sessions[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: id}]; !ok {
				return nil, errors.New("player: saved counseling session is not in GameData")
			}
		}
	}
	return s, nil
}

func (s *FriendshipService) FriendshipAP() (uint64, error) {

	daily := s.daily(s.collection.FriendshipEntries(), s.now().UTC().Format("2006-01-02"))
	if daily.Used >= s.design.Default.MaxCounselingAP {
		return 0, nil
	}
	return s.design.Default.MaxCounselingAP - daily.Used, nil
}

func (s *FriendshipService) daily(entries map[string]FriendshipEntry, day string) FriendshipDaily {
	if entry := entries["daily"]; entry.Daily != nil && entry.Daily.Day == day {
		return *entry.Daily
	}
	return FriendshipDaily{Day: day}
}

func (s *FriendshipService) owned(id uint64) (Costume, bool) {
	costumeID, ok := s.design.Costumes[id]
	if !ok {
		return Costume{}, false
	}
	return s.collection.CostumeByID(costumeID)
}

func (s *FriendshipService) maximum(costume Costume) uint64 {
	max := s.design.Default.MaxLevels[0]
	unique := s.potential.CostumeUnique[costume.ID]
	awake, exists := s.collection.CharAwakeState(unique)
	rule, hasRule := s.awake.Characters[unique]
	if !exists || !hasRule || !awake.IsAwake {
		return max
	}
	for i, level := range awake.ImprintLevels {
		if len(rule.ImprintGrowth[i]) == 0 || level != uint64(len(rule.ImprintGrowth[i])) {
			return max
		}
	}
	max = s.design.Default.MaxLevels[1]
	nodes := s.potential.Nodes[costume.ID]
	if len(nodes) == 0 || len(costume.PotentialIDs) != len(nodes) {
		return max
	}
	for _, id := range costume.PotentialIDs {
		if _, ok := nodes[id]; !ok {
			return max
		}
	}
	return s.design.Default.MaxLevels[2]
}

func (s *FriendshipService) advance(state FriendshipState, max, exp uint64) (FriendshipState, []gamedata.Reward, error) {
	if state.EXP > math.MaxInt32-exp {
		return state, nil, errors.New("player: friendship experience overflow")
	}
	state.EXP += exp
	var rewards []gamedata.Reward
	for state.Level < max {
		rule, ok := s.design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok || rule.NextEXP == 0 {
			return state, nil, errors.New("player: missing friendship level threshold")
		}
		if state.EXP < rule.NextEXP {
			break
		}
		state.EXP -= rule.NextEXP
		state.Level++
		next, ok := s.design.Levels[gamedata.FriendshipKey{GroupID: state.CostumeID, ID: state.Level}]
		if !ok {
			return state, nil, errors.New("player: missing friendship level reward")
		}
		rewards = append(rewards, next.Rewards...)
	}
	if state.Level >= max {
		state.EXP = 0
	}
	return state, rewards, nil
}
