package player

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/wire"
)

type CostumePotentialService struct {
	design     *gamedata.CostumePotentialDesign
	collection *CollectionStore
	characters *CharacterStore
	inventory  *Inventory
	wallet     *Wallet
}

func NewCostumePotentialService(design *gamedata.CostumePotentialDesign, collection *CollectionStore, characters *CharacterStore, inventory *Inventory, wallet *Wallet) (*CostumePotentialService, error) {
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
func (s *CostumePotentialService) Contributions(character Character) ([]gamedata.StatContribution, error) {
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

func (s *CostumePotentialService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/CostumeNodeActivation" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing sequence")
	}
	characterIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || characterIndex == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing character")
	}
	costumeIndex, found, err := wire.Varint(request, 3)
	if err != nil || !found || costumeIndex == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation missing costume")
	}
	var nodes []uint64
	var materials []Item
	err = wire.Walk(request, func(field wire.Field) error {
		switch field.Number {
		case 4:
			if field.Type != 0 && field.Type != 2 {
				return errors.New("player: invalid potential node field")
			}
			for data := field.Value; len(data) != 0; {
				id, count := binary.Uvarint(data)
				if count <= 0 || id == 0 {
					return errors.New("player: invalid potential node ID")
				}
				nodes = append(nodes, id)
				data = data[count:]
			}
		case 5:
			if field.Type != 2 {
				return errors.New("player: invalid potential material field")
			}
			var item Item
			if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
				return err
			}
			if item.Type == 0 || item.Count == 0 || (item.Type != 4 && (item.InvenIndex == 0 || item.ID == 0)) {
				return errors.New("player: incomplete potential material")
			}
			materials = append(materials, item)
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(nodes) == 0 || len(materials) == 0 {
		return 0, nil, true, errors.New("player: CostumeNodeActivation has no nodes or materials")
	}
	character, found := s.characters.Find(characterIndex)
	if !found {
		return 0, nil, true, fmt.Errorf("player: unknown potential character %d", characterIndex)
	}
	costume, found := s.collection.CostumeByIndex(costumeIndex)
	if !found || costume.UseChar != characterIndex {
		return 0, nil, true, fmt.Errorf("player: potential costume %d is not owned by character %d", costumeIndex, characterIndex)
	}
	if err := s.collection.ValidateCostumePotentialActivation(costumeIndex, nodes); err != nil {
		return 0, nil, true, err
	}
	costs, err := s.design.Validate(costume.ID, character.ID, 0, costume.PotentialIDs, nodes)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: validate costume potential GameData: %w", err)
	}
	want := make(map[[2]uint64]uint64)
	for _, cost := range costs {
		key := [2]uint64{cost.Type, cost.ID}
		if cost.Count > ^uint64(0)-want[key] {
			return 0, nil, true, errors.New("player: costume potential cost overflow")
		}
		want[key] += cost.Count
	}
	got := make(map[[2]uint64]uint64)
	var items []Item
	var gold uint64
	for _, material := range materials {
		key := [2]uint64{material.Type, material.ID}
		if material.Type == 4 {
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return 0, nil, true, errors.New("player: invalid costume potential currency")
			}
			gold = material.Count
		} else {
			if material.Type != 8 {
				return 0, nil, true, fmt.Errorf("player: unsupported costume potential material type %d", material.Type)
			}
			items = append(items, material)
		}
		if material.Count > ^uint64(0)-got[key] {
			return 0, nil, true, errors.New("player: submitted costume potential material overflow")
		}
		got[key] += material.Count
	}
	if len(got) != len(want) {
		return 0, nil, true, fmt.Errorf("player: costume potential material kinds mismatch: request=%v GameData=%v", got, want)
	}
	for key, count := range want {
		if got[key] != count {
			return 0, nil, true, fmt.Errorf("player: costume potential material %d/%d=%d want %d", key[0], key[1], got[key], count)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.CanConsume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate costume potential items: %w", err)
		}
	}
	if gold != 0 && !s.wallet.CanSpendGold(gold) {
		return 0, nil, true, errors.New("player: insufficient gold for costume potential")
	}
	sortedNodes := append([]uint64(nil), nodes...)
	sort.Slice(sortedNodes, func(i, j int) bool { return sortedNodes[i] < sortedNodes[j] })
	parts := make([]string, len(sortedNodes))
	for i, id := range sortedNodes {
		parts[i] = strconv.FormatUint(id, 10)
	}
	identity := "costume-potential:" + strconv.FormatUint(costumeIndex, 10) + ":" + strings.Join(parts, ",")
	if gold != 0 {
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: spend costume potential gold: %w", err)
		}
	}
	if len(items) != 0 {
		if err := s.inventory.Consume(items); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume costume potential items: %w", err)
		}
	}
	if err := s.collection.ActivateCostumePotential(costumeIndex, nodes); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist costume potential: %w", err)
	}
	return 261, nil, true, nil
}
