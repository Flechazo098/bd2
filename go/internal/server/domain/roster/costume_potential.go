package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"errors"
	"fmt"
	"math"
)

type CostumePotentialService struct {
	design       *gamedata.CostumePotentialDesign
	collection   *CollectionStore
	characters   *CharacterStore
	inventory    *assets.Inventory
	wallet       *assets.Wallet
	connectStore stateio.Store
}

func NewCostumePotentialService(design *gamedata.CostumePotentialDesign, collection *CollectionStore, characters *CharacterStore, inventory *assets.Inventory, wallet *assets.Wallet) (*CostumePotentialService, error) {
	if design == nil || collection == nil || characters == nil || inventory == nil || wallet == nil {
		return nil, errors.New("player: incomplete costume potential service")
	}
	return &CostumePotentialService{design: design, collection: collection, characters: characters, inventory: inventory, wallet: wallet}, nil
}

// Contributions follows CharStatInfo.GetCostumeNodeBuffStat: type 2 nodes
// contribute across every owned costume of the same UniqueCharId, while type
// 1 nodes contribute only from CharDBInfo.ConnectPotentialCostume. Values are
// already fractions for the percent options 2, 4 and 6; they are not equipment
// option percentages and must not be divided by 100 or rounded individually.
func (s *CostumePotentialService) Contributions(ctx command.Context, character Character) ([]gamedata.StatContribution, error) {
	if s == nil || s.design == nil || s.collection == nil {
		return nil, errors.New("player: missing costume potential contribution source")
	}
	unique, ok := s.design.CharacterUnique[character.ID]
	if !ok || unique == 0 {
		return nil, fmt.Errorf("player: unknown costume potential character %d", character.ID)
	}
	var result []gamedata.StatContribution
	connectedFound := character.ConnectPotentialCostume == 0
	for _, costume := range s.collection.Costumes() {
		if s.design.CostumeUnique[costume.ID] != unique {
			continue
		}
		if costume.ID == character.ConnectPotentialCostume {
			connectedFound = true
		}
		seen := map[uint64]bool{}
		for _, id := range costume.PotentialIDs {
			if seen[id] {
				return nil, errors.New("player: duplicate active costume potential stat node")
			}
			seen[id] = true
			node, found := s.design.Nodes[costume.ID][id]
			if !found {
				return nil, fmt.Errorf("player: unknown active potential node %d/%d", costume.ID, id)
			}
			if node.NodeType != 2 && (node.NodeType != 1 || costume.ID != character.ConnectPotentialCostume) {
				continue
			}
			if math.IsNaN(node.StatValue) || math.IsInf(node.StatValue, 0) || node.StatValue < 0 {
				return nil, errors.New("player: invalid costume potential stat value")
			}
			contribution := gamedata.StatContribution{Option: node.StatType}
			switch node.StatType {
			case 1, 2:
				contribution.Stat = gamedata.StatHealth
			case 3, 4:
				contribution.Stat = gamedata.StatAttack
			case 5, 6:
				contribution.Stat = gamedata.StatMagic
			case 7:
				contribution.Stat = gamedata.StatDefencePercent
			case 8:
				contribution.Stat = gamedata.StatMagicResistancePercent
			case 9:
				contribution.Stat = gamedata.StatCriticalChance
			case 10:
				contribution.Stat = gamedata.StatCriticalDamage
			case 11, 12, 13, 14, 15, 19:
				contribution.Stat = gamedata.StatElementDamage
			case 16, 17, 18, 20:
				contribution.Stat = gamedata.StatElementResistance
			default:
				return nil, fmt.Errorf("player: unsupported costume potential stat option %d", node.StatType)
			}
			if node.StatType == 2 || node.StatType == 4 || node.StatType == 6 {
				contribution.Percent = node.StatValue
			} else {
				contribution.Flat = node.StatValue
			}
			result = append(result, contribution)
		}
	}
	if !connectedFound {
		return nil, errors.New("player: connected potential costume is not owned by this character")
	}
	return result, nil
}
