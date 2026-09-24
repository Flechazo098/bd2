package player

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"bd2server/internal/gamedata"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

type characterSnapshot struct {
	Version        string   `json:"version"`
	CharacterOrder []uint64 `json:"character_order"`
}

// CharacterStore owns mutable character growth separately from immutable
// character design data. The seed supplies the initially owned instances.
type CharacterStore struct {
	mu              sync.Mutex
	store           stateio.AtomicEntryStore
	characters      []Character
	persisted       map[uint64]bool
	persistedOrder  []uint64
	persistedCore   bool
	inventory       *Inventory
	gameDataRoot    string
	gameDataVersion string
	grow            func(Character, []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error)
	collection      *CollectionStore
	wallet          *Wallet
	maxHealth       func(Character) (uint64, error)
	promoteGrowth   func(Character, []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error)
	talentGrowth    *gamedata.TalentGrowthDesign
	sessionID       string
	talentReplies   map[string]talentUpgradeReply
}

type talentUpgradeReply struct {
	code int
	body []byte
}

func (s *CharacterStore) AttachWallet(wallet *Wallet) error {
	if wallet == nil {
		return errors.New("player: nil growth wallet")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallet = wallet
	return nil
}

func (s *CharacterStore) AttachTalentGrowth(design *gamedata.TalentGrowthDesign) error {
	if design == nil {
		return errors.New("player: nil talent growth design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.talentGrowth = design
	return nil
}

// BeginSession scopes protobuf sequence replay. Network retries reuse the
// exact TalentSkillUpgrade request and must receive success without a second
// level increase or charge.
func (s *CharacterStore) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
	s.talentReplies = make(map[string]talentUpgradeReply)
}

// AttachMaxHealth makes growth and post-battle revival consume the same
// GameData/collection calculator as AllCharRefresh and BattleEnter.
func (s *CharacterStore) AttachMaxHealth(maxHealth func(Character) (uint64, error)) error {
	if maxHealth == nil {
		return errors.New("player: nil maximum health calculator")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxHealth = maxHealth
	return nil
}

func (s *CharacterStore) AttachCollection(collection *CollectionStore) error {
	if collection == nil {
		return errors.New("player: nil collection store")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collection = collection
	return nil
}

func OpenCharacterStore(store stateio.Store, seed []Character, inventory *Inventory, gameDataRoot, gameDataVersion string) (*CharacterStore, error) {
	if store == nil || inventory == nil {
		return nil, errors.New("player: invalid character store configuration")
	}
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok {
		return nil, errors.New("player: character store requires atomic entries")
	}
	s := &CharacterStore{store: entries, inventory: inventory, characters: append([]Character(nil), seed...), persisted: make(map[uint64]bool), gameDataRoot: gameDataRoot, gameDataVersion: gameDataVersion, talentReplies: make(map[string]talentUpgradeReply)}
	s.grow = func(character Character, materials []gamedata.GrowthMaterial) (uint64, uint64, []gamedata.GrowthMaterial, error) {
		return gamedata.CharacterGrowth(s.gameDataRoot, s.gameDataVersion, int(character.ID), character.Level, character.Exp, materials)
	}
	s.promoteGrowth = func(character Character, submitted []gamedata.PromotionCost) (gamedata.PromotionGrowthResult, error) {
		return gamedata.CharacterGrowthPromotions(s.gameDataRoot, s.gameDataVersion, int(character.ID), character.Level, character.Exp, submitted)
	}
	data, err := entries.Load("characters")
	if err != nil {
		return nil, err
	}
	if data == nil {
		orphaned, err := entries.ListEntries("characters", "characters")
		if err != nil {
			return nil, fmt.Errorf("player: list character entries: %w", err)
		}
		if len(orphaned) != 0 {
			return nil, errors.New("player: character entries exist without core")
		}
		return s, validateCharacters(s.characters)
	}
	saved, loaded, err := loadCharacterEntries(entries, data)
	if err != nil {
		return nil, err
	}
	s.characters = loaded
	s.persistedOrder = append([]uint64{}, saved.CharacterOrder...)
	s.persistedCore = true
	seen := make(map[uint64]bool, len(s.characters))
	for _, character := range s.characters {
		seen[character.InvenIndex] = true
		s.persisted[character.InvenIndex] = true
	}
	for _, character := range seed {
		if !seen[character.InvenIndex] {
			return nil, fmt.Errorf("player: current character state omits seeded inventory index %d", character.InvenIndex)
		}
	}
	if err := validateCharacters(s.characters); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *CharacterStore) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persist(append([]Character(nil), s.characters...))
}

func validateCharacters(characters []Character) error {
	seen := make(map[uint64]bool, len(characters))
	for _, character := range characters {
		if character.InvenIndex == 0 || character.ID == 0 || character.Level == 0 || seen[character.InvenIndex] {
			return errors.New("player: invalid saved character")
		}
		seen[character.InvenIndex] = true
	}
	return nil
}

func (s *CharacterStore) All() []Character {
	characters := s.RawAll()
	s.mu.Lock()
	maxHealth := s.maxHealth
	s.mu.Unlock()
	if maxHealth != nil {
		for i := range characters {
			if hp, err := maxHealth(characters[i]); err == nil {
				characters[i].HP = hp
			}
		}
	}
	return characters
}

// RawAll is for account-ownership queries used by the stat calculator itself.
// Calling All() from that calculator would recursively calculate its inputs.
func (s *CharacterStore) RawAll() []Character {
	s.mu.Lock()
	characters := append([]Character(nil), s.characters...)
	collection := s.collection
	s.mu.Unlock()
	if collection != nil {
		characters = append(characters, collection.Characters()...)
	}
	return characters
}

func (s *CharacterStore) Find(inventoryIndex uint64) (Character, bool) {
	s.mu.Lock()
	for _, character := range s.characters {
		if character.InvenIndex == inventoryIndex {
			maxHealth := s.maxHealth
			s.mu.Unlock()
			if maxHealth != nil {
				if hp, err := maxHealth(character); err == nil {
					character.HP = hp
				}
			}
			return character, true
		}
	}
	collection := s.collection
	s.mu.Unlock()
	if collection != nil {
		character, found := collection.FindCharacter(inventoryIndex)
		if found && s.maxHealth != nil {
			if hp, err := s.maxHealth(character); err == nil {
				character.HP = hp
			}
		}
		return character, found
	}
	return Character{}, false
}

func (s *CharacterStore) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path == "/CharImmortal" {
		return s.charImmortal(request)
	}
	if path == "/TalentSkillUpgrade" {
		return s.talentSkillUpgrade(request)
	}
	if path != "/CharGrowth" {
		return 0, nil, false, nil
	}
	index, found, err := wire.Varint(request, 2)
	if err != nil || !found || index == 0 {
		return 0, nil, true, errors.New("player: CharGrowth missing character")
	}
	var materials []Item
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 3 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("player: invalid growth material")
		}
		var item Item
		if err := decodeVarints(field.Value, map[int]*uint64{1: &item.InvenIndex, 2: &item.ID, 3: &item.Type, 4: &item.Count, 5: &item.KeepFlag, 6: &item.TimeValue, 9: &item.SortID, 10: &item.UseCount}); err != nil {
			return err
		}
		if item.Type == 0 || item.Count == 0 || (item.Type != 4 && (item.InvenIndex == 0 || item.ID == 0)) {
			return errors.New("player: incomplete growth material")
		}
		materials = append(materials, item)
		return nil
	}); err != nil {
		return 0, nil, true, err
	}
	if len(materials) == 0 {
		return 0, nil, true, errors.New("player: CharGrowth has no materials")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	position := -1
	for i, current := range s.characters {
		if current.InvenIndex == index {
			position = i
			break
		}
	}
	var current Character
	fromCollection := false
	if position >= 0 {
		current = s.characters[position]
	} else if s.collection != nil {
		current, fromCollection = s.collection.FindCharacter(index)
	}
	if position < 0 && !fromCollection {
		return 0, nil, true, fmt.Errorf("player: unknown character inventory index %d", index)
	}
	isPromotion := false
	for _, material := range materials {
		if material.Type == 4 {
			isPromotion = true
			break
		}
	}
	if isPromotion {
		return s.promoteCharacter(current, position, fromCollection, materials)
	}
	growthMaterials := make([]gamedata.GrowthMaterial, len(materials))
	for i, material := range materials {
		if material.Type != 8 {
			return 0, nil, true, fmt.Errorf("player: unsupported growth material type %d", material.Type)
		}
		growthMaterials[i] = gamedata.GrowthMaterial{ID: material.ID, Count: material.Count}
	}
	newLevel, newExp, refunds, err := s.grow(current, growthMaterials)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: calculate character growth: %w", err)
	}
	current.Level = newLevel
	current.Exp = newExp
	if s.maxHealth != nil {
		maxHealth := s.maxHealth
		s.mu.Unlock()
		hp, healthErr := maxHealth(current)
		s.mu.Lock()
		current.HP, err = hp, healthErr
		if err != nil {
			return 0, nil, true, fmt.Errorf("player: calculate grown character maximum health: %w", err)
		}
	}
	returned, err := s.inventory.ConsumeAndRefund(materials, refunds)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: consume growth material: %w", err)
	}
	if fromCollection {
		if err := s.collection.UpdateCharacter(current.ID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist collection character growth: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(next); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist character growth: %w", err)
		}
		s.characters = next
	}
	response := wire.AppendBytes(nil, 1, CharacterWire(current))
	var bundle []byte
	for _, item := range returned {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	response = wire.AppendBytes(response, 2, bundle)
	return 433, response, true, nil
}

func (s *CharacterStore) promoteCharacter(current Character, position int, fromCollection bool, materials []Item) (int, []byte, bool, error) {
	var items []Item
	requested := make(map[[2]uint64]uint64)
	var gold uint64
	for _, material := range materials {
		if material.Type == 4 {
			if material.InvenIndex != 0 || material.ID != 0 || gold != 0 {
				return 0, nil, true, errors.New("player: invalid promotion currency")
			}
			gold = material.Count
		} else if material.Type == 8 {
			items = append(items, material)
			key := [2]uint64{8, material.ID}
			if material.Count > ^uint64(0)-requested[key] {
				return 0, nil, true, errors.New("player: promotion material overflow")
			}
			requested[key] += material.Count
		} else {
			return 0, nil, true, fmt.Errorf("player: unsupported promotion item type %d", material.Type)
		}
	}
	if gold == 0 {
		return 0, nil, true, errors.New("player: promotion has no gold cost")
	}
	submitted := make([]gamedata.PromotionCost, 0, len(requested)+1)
	for key, count := range requested {
		submitted = append(submitted, gamedata.PromotionCost{Type: key[0], ID: key[1], Count: count})
	}
	submitted = append(submitted, gamedata.PromotionCost{Type: 4, Count: gold})
	result, err := s.promoteGrowth(current, submitted)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: calculate combined character promotion: %w; request=%+v", err, materials)
	}
	if gold != 0 && (s.wallet == nil || !s.wallet.CanSpendGold(gold)) {
		return 0, nil, true, errors.New("player: insufficient gold for promotion")
	}
	if len(items) == 0 {
		return 0, nil, true, errors.New("player: promotion has no item material")
	}
	previousID := current.ID
	current.ID = result.CharacterID
	current.Level = result.Level
	current.Exp = result.Exp
	if fromCollection {
		if err := s.collection.CanUpdateCharacter(previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: validate promoted collection character: %w", err)
		}
	}
	if err := s.inventory.CanConsume(items); err != nil {
		return 0, nil, true, fmt.Errorf("player: validate promotion items: %w", err)
	}
	if s.maxHealth != nil {
		maxHealth := s.maxHealth
		s.mu.Unlock()
		hp, healthErr := maxHealth(current)
		s.mu.Lock()
		if healthErr != nil {
			return 0, nil, true, fmt.Errorf("player: calculate promoted character health: %w", healthErr)
		}
		current.HP = hp
	}
	if gold != 0 {
		identity := "char-promote:" + strconv.FormatUint(current.InvenIndex, 10) + ":" + strconv.FormatUint(current.ID, 10)
		if _, err := s.wallet.SpendGoldOnce(identity, gold); err != nil {
			return 0, nil, true, fmt.Errorf("player: consume promotion gold: %w", err)
		}
	}
	returned, err := s.inventory.ConsumeAndRefund(items, result.Refunds)
	if err != nil {
		return 0, nil, true, fmt.Errorf("player: consume promotion items: %w", err)
	}
	if fromCollection {
		if err := s.collection.UpdateCharacter(previousID, current); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist promoted collection character: %w", err)
		}
	} else {
		next := append([]Character(nil), s.characters...)
		next[position] = current
		if err := s.persist(next); err != nil {
			return 0, nil, true, fmt.Errorf("player: persist promoted character: %w", err)
		}
		s.characters = next
	}
	response := wire.AppendBytes(nil, 1, CharacterWire(current))
	var bundle []byte
	for _, item := range returned {
		bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
	}
	if len(bundle) != 0 {
		response = wire.AppendBytes(response, 2, bundle)
	}
	return 433, response, true, nil
}

// charImmortal completes the automatic post-battle revival for characters
// whose TalentSkillTable.ClassType is 14. The story character 6010 has
// ValueList[0]=10000 at every talent level (100%). The authoritative maximum
// HP is recomputed by Find from the same calculator as character growth.
func (s *CharacterStore) charImmortal(request []byte) (int, []byte, bool, error) {
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, errors.New("player: CharImmortal missing sequence")
	}
	var indices []uint64
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 0 && field.Type != 2 {
			return errors.New("player: CharImmortal invalid inventory index field")
		}
		for data := field.Value; len(data) != 0; {
			index, count := binary.Uvarint(data)
			if count <= 0 || index == 0 {
				return errors.New("player: CharImmortal invalid inventory index")
			}
			indices = append(indices, index)
			data = data[count:]
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if len(indices) == 0 || len(indices) > 5 {
		return 0, nil, true, fmt.Errorf("player: CharImmortal invalid character count %d", len(indices))
	}
	seen := make(map[uint64]bool, len(indices))
	var response []byte
	for _, index := range indices {
		if seen[index] {
			return 0, nil, true, fmt.Errorf("player: CharImmortal duplicate character %d", index)
		}
		seen[index] = true
		character, found := s.Find(index)
		if !found {
			return 0, nil, true, fmt.Errorf("player: CharImmortal unknown character %d", index)
		}
		response = wire.AppendBytes(response, 1, CharacterWire(character))
	}
	return 96, response, true, nil
}

func CharacterWire(c Character) []byte {
	fields := []struct {
		n int
		v uint64
	}{{1, c.InvenIndex}, {2, c.ID}, {3, c.HP}, {4, c.Level}, {5, c.CostumeID}, {6, c.Exp}, {7, c.UseCostume}, {8, c.TalentLevel}, {9, c.TalentExp}, {10, c.SolidarityReward}, {11, c.ExpiryTime}, {13, c.ConnectPotentialCostume}}
	var out []byte
	for _, field := range fields {
		if field.v != 0 {
			out = wire.AppendVarint(out, field.n, field.v)
		}
	}
	for _, p := range c.Pictorialbook {
		book := wire.AppendVarint(wire.AppendVarint(nil, 1, p.ID), 2, p.GroupID)
		out = wire.AppendBytes(out, 12, book)
	}
	return out
}
