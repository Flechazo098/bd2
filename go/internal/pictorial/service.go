// Package pictorial resolves account-owned collections through the installed
// GameData. Captured protobufs are never runtime inputs.
package pictorial

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

type Owned interface {
	PictorialCharacters() []player.Character
	PictorialCostumes() []player.Costume
	PictorialItems() []player.Item
	PictorialEquipment() []player.Equipment
	PictorialDiscovered() []player.Pictorial
}

type Service struct {
	Design *gamedata.PictorialDesign
	Owned  Owned
	// AwakeContributions is injected by the character-awakening domain so all
	// server-side maximum-HP consumers use the same derived account state.
	AwakeContributions func(player.Character) ([]gamedata.StatContribution, error)
	baseHealth         sync.Map // [2]uint64 (design character ID, level) -> design-only base HP
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/PictorialBookInfo" && path != "/AllCharRefresh" {
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("pictorial: %s missing sequence", path)
	}
	entries, buffs, err := s.Snapshot()
	if err != nil {
		return 0, nil, true, err
	}
	var response []byte
	if path == "/PictorialBookInfo" {
		for _, entry := range entries {
			book := wire.AppendVarint(nil, 1, entry.GroupID)
			book = wire.AppendVarint(book, 2, entry.ID)
			response = wire.AppendBytes(response, 1, book)
		}
		return 111, response, true, nil
	}
	for _, buff := range buffs {
		response = wire.AppendBytes(response, 1, BuffWire(buff))
	}
	return 165, response, true, nil
}

type Entry struct {
	GroupID uint64
	ID      uint64
	BuffID  uint64
}

func BuffWire(buff gamedata.PictorialBuffStat) []byte {
	proto := wire.AppendVarint(nil, 1, buff.StatType)
	proto = wire.AppendDouble(proto, 2, buff.Value)
	if buff.Category != 0 {
		proto = wire.AppendVarint(proto, 3, buff.Category)
	}
	return proto
}

// Snapshot computes both network responses from the exact same owned-account
// view. Identical buff IDs on distinct completed pictorial entries contribute
// once per entry (e.g. three different collection quests all use buff 2002).
// Duplicate inventory instances of the SAME entry cannot contribute twice.
func (s *Service) Snapshot() ([]Entry, []gamedata.PictorialBuffStat, error) {
	if s == nil || s.Design == nil || s.Owned == nil {
		return nil, nil, errors.New("pictorial: missing design or account ownership")
	}
	d := s.Design
	unique, talents := map[uint64]bool{}, map[uint64]bool{}
	for _, character := range s.Owned.PictorialCharacters() {
		meta, found := d.CharMeta[character.ID]
		if !found {
			return nil, nil, fmt.Errorf("pictorial: unknown owned character design %d", character.ID)
		}
		if meta.UsePackTemporary {
			continue
		}
		unique[meta.UniqueID] = true
		if meta.TalentID != 0 {
			talents[meta.TalentID] = true
		}
	}
	costumes := map[uint64]uint64{}
	for _, costume := range s.Owned.PictorialCostumes() {
		if previous, found := costumes[costume.ID]; !found || costume.Level > previous {
			costumes[costume.ID] = costume.Level
		}
		// Level 0 is the first, owned-only threshold. Presence in the map
		// is sufficient even when zero is the stored level.
	}
	equipment := map[uint64]bool{}
	for _, owned := range s.Owned.PictorialEquipment() {
		equipment[owned.ID] = true
	}
	items := map[[2]uint64]bool{}
	itemOrder := map[uint64]int{}
	itemIndex := 0
	books := map[Entry]bool{}
	for _, book := range s.Owned.PictorialDiscovered() {
		books[Entry{GroupID: book.ID, ID: book.GroupID}] = true
	}
	for _, owned := range s.Owned.PictorialItems() {
		if owned.Count != 0 {
			items[[2]uint64{owned.Type, owned.ID}] = true
			if _, found := itemOrder[owned.ID]; !found && (owned.Type == 13 || owned.Type == 17) {
				itemOrder[owned.ID] = itemIndex
				itemIndex++
			}
			if owned.Pictorialbook != nil {
				books[Entry{GroupID: owned.Pictorialbook.ID, ID: owned.Pictorialbook.GroupID}] = true
			}
		}
	}
	var entries []Entry
	entryOrder := map[uint64]int{}
	for _, row := range d.Characters {
		if allPresent(row.UniqueIDs, unique) {
			entries = append(entries, Entry{gamedata.PictorialCharacter, row.ID, row.BuffID})
		}
	}
	for _, row := range d.Talents {
		if allPresent(row.TalentIDs, talents) || books[Entry{GroupID: gamedata.PictorialTalent, ID: row.ID}] {
			entries = append(entries, Entry{gamedata.PictorialTalent, row.ID, 0})
		}
	}
	for _, row := range d.Costumes {
		level, found := costumes[row.CostumeID]
		if !found {
			continue
		}
		buffID := uint64(0)
		for i, threshold := range row.Thresholds {
			if threshold <= level {
				buffID = row.BuffIDs[i]
			}
		}
		entries = append(entries, Entry{gamedata.PictorialCostume, row.ID, buffID})
	}
	for _, row := range d.Equipment {
		if equipment[row.EquipmentID] {
			entries = append(entries, Entry{gamedata.PictorialEquipment, row.ID, row.BuffID})
		}
	}
	for _, row := range d.Cooking {
		if items[[2]uint64{14, row.RecipeID}] || books[Entry{GroupID: gamedata.PictorialCooking, ID: row.ID}] {
			entries = append(entries, Entry{gamedata.PictorialCooking, row.ID, row.BuffID})
		}
	}
	for _, row := range d.Items {
		itemType := uint64(17)
		if row.Category == 1 {
			itemType = 13
		}
		if items[[2]uint64{itemType, row.ItemID}] || books[Entry{GroupID: gamedata.PictorialItem, ID: row.ID}] {
			entry := Entry{gamedata.PictorialItem, row.ID, row.BuffID}
			entries = append(entries, entry)
			order, found := itemOrder[row.ItemID]
			if !found {
				order = len(itemOrder) + int(row.ID)
			}
			entryOrder[row.ID] = order
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].GroupID != entries[j].GroupID {
			return entries[i].GroupID < entries[j].GroupID
		}
		if entries[i].GroupID == gamedata.PictorialItem {
			// Official PictorialBookInfo preserves discovery order for item
			// collections (starter 30021, then quest 2101 and 2102).
			first, second := entryOrder[entries[i].ID], entryOrder[entries[j].ID]
			if first != second {
				return first < second
			}
		}
		return entries[i].ID < entries[j].ID
	})
	type key struct{ category, stat uint64 }
	values := map[key]float64{}
	for _, entry := range entries {
		if entry.BuffID == 0 {
			continue
		}
		buff, found := d.Buffs[entry.BuffID]
		if !found {
			return nil, nil, fmt.Errorf("pictorial: buff %d missing for entry %d/%d", entry.BuffID, entry.GroupID, entry.ID)
		}
		// DB StatValue is defined to four decimal places. Accumulate fixed
		// ten-thousandths to prevent binary float drift across many rows.
		values[key{buff.Category, buff.StatType}] += buff.Value
	}
	buffs := make([]gamedata.PictorialBuffStat, 0, len(values))
	for k, value := range values {
		buffs = append(buffs, gamedata.PictorialBuffStat{Category: k.category, StatType: k.stat, Value: math.Round(value*100000000) / 100000000})
	}
	sort.Slice(buffs, func(i, j int) bool {
		if buffs[i].Category != buffs[j].Category {
			return buffs[i].Category < buffs[j].Category
		}
		return buffs[i].StatType < buffs[j].StatType
	})
	return entries, buffs, nil
}

func allPresent(ids []uint64, available map[uint64]bool) bool {
	for _, id := range ids {
		if !available[id] {
			return false
		}
	}
	return len(ids) != 0
}

func (s *Service) Contributions() ([]gamedata.StatContribution, error) {
	_, buffs, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	var result []gamedata.StatContribution
	for _, buff := range buffs {
		if buff.Category != 0 {
			continue
		}
		contribution := gamedata.StatContribution{Percent: buff.Value}
		switch buff.StatType {
		case 1, 2:
			contribution.Stat = gamedata.StatHealth
		case 3, 4:
			contribution.Stat = gamedata.StatAttack
		case 5, 6:
			contribution.Stat = gamedata.StatMagic
		default:
			continue
		}
		if buff.StatType%2 == 1 {
			contribution.Flat, contribution.Percent = contribution.Percent, 0
		}
		result = append(result, contribution)
	}
	return result, nil
}

func (s *Service) MaxHealth(character player.Character) (uint64, error) {
	if s == nil || s.Design == nil {
		return 0, errors.New("pictorial: missing character stat design")
	}
	cacheKey := [2]uint64{character.ID, character.Level}
	value, found := s.baseHealth.Load(cacheKey)
	if !found {
		base, err := gamedata.CharacterBaseStats(s.Design.Root, s.Design.Version, int(character.ID), int(character.Level))
		if err != nil {
			return 0, err
		}
		value, _ = s.baseHealth.LoadOrStore(cacheKey, base.Health)
	}
	contributions, err := s.Contributions()
	if err != nil {
		return 0, err
	}
	if s.AwakeContributions != nil {
		awake, err := s.AwakeContributions(character)
		if err != nil {
			return 0, err
		}
		contributions = append(contributions, awake...)
	}
	maxHP := gamedata.AggregateStats(gamedata.BaseStats{Health: value.(float64)}, contributions).Health
	if maxHP < 1 || maxHP > float64(^uint64(0)) {
		return 0, fmt.Errorf("pictorial: invalid maximum health for character %d: %v", character.ID, maxHP)
	}
	return uint64(maxHP), nil
}
