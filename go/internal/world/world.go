// Package world implements the local map/quest state that is not part of a
// player's inventory.  It deliberately stores semantic seed values, never a
// captured response payload.
package world

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	"bd2server/internal/deck"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/progress"
	"bd2server/internal/wire"
)

var ErrInvalidRequest = errors.New("world: invalid request")

type Seed struct {
	Version             string             `json:"version"`
	PackID              int                `json:"pack_id"`
	StartQuestID        int                `json:"start_quest_id"`
	BattleUnlockQuestID int                `json:"battle_unlock_quest_id"`
	RewardCharacter     player.Character   `json:"reward_character"`
	RewardCostume       player.Costume     `json:"reward_costume"`
	StoryCharacters     []player.Character `json:"story_characters"`
}

// Load reads the small, versioned world seed.  Quest IDs are then verified
// against the authoritative shared QuestTable<pack> GameData database.
func Load(seedPath, gameDataRoot, gameDataVersion, characterStatePath string, state *progress.Store, starter *player.Starter, equipment *player.EquipmentInventory, inventory *player.Inventory, wallet *player.Wallet) (*Service, error) {
	if state == nil || starter == nil || equipment == nil || inventory == nil || wallet == nil {
		return nil, errors.New("world: nil player state")
	}
	b, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, fmt.Errorf("world: read seed: %w", err)
	}
	var seed Seed
	if err := json.Unmarshal(b, &seed); err != nil {
		return nil, fmt.Errorf("world: decode seed: %w", err)
	}
	if seed.Version != "2.34.13" || seed.PackID <= 0 || seed.StartQuestID <= 0 || seed.BattleUnlockQuestID <= 0 || seed.RewardCharacter.ID == 0 || seed.RewardCostume.ID == 0 || len(seed.StoryCharacters) == 0 {
		return nil, errors.New("world: invalid seed")
	}
	ownedCharacters := append([]player.Character(nil), starter.Characters...)
	ownedCharacters = append(ownedCharacters, seed.RewardCharacter)
	ownedCharacters = append(ownedCharacters, seed.StoryCharacters...)
	characters, err := player.OpenCharacterStore(characterStatePath, ownedCharacters, inventory, gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("world: open character state: %w", err)
	}
	packs, transitions, err := gamedata.LoadQuestPackChain(gameDataRoot, gameDataVersion, seed.PackID)
	if err != nil {
		return nil, err
	}
	quests := packs[seed.PackID]
	if _, ok := quests[seed.StartQuestID]; !ok {
		return nil, fmt.Errorf("world: start quest %d is absent from QuestTable%d", seed.StartQuestID, seed.PackID)
	}
	transition := transitions[seed.PackID]
	activePack := seed.PackID
	if saved, found := state.Position(); found {
		if _, known := packs[saved.PackID]; known {
			activePack = saved.PackID
		}
	}
	service := &Service{seed: seed, state: state, starter: starter, equipment: equipment, inventory: inventory, wallet: wallet, characters: characters, quests: quests, transition: transition, packs: packs, transitions: transitions, activePack: activePack}
	if err := service.MigrateClearedRewards(); err != nil {
		return nil, fmt.Errorf("world: migrate cleared quest rewards: %w", err)
	}
	return service, nil
}

func (s *Service) CharacterService() *player.CharacterStore { return s.characters }

func (s *Service) EarnedQuestCostume() (player.Costume, bool) {
	return s.seed.RewardCostume, s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID)
}

// CurrentPackID returns the story pack selected by the latest successful
// PackInGameInfo request. BattleEnter does not carry a pack field, so battle
// sessions lock this value when they begin. On restart, Load seeds it from the
// persisted position and finally falls back to the versioned starter pack.
func (s *Service) CurrentPackID() (int, error) {
	s.activePackMu.RLock()
	packID := s.activePack
	s.activePackMu.RUnlock()
	if packID == 0 {
		packID = s.seed.PackID
	}
	if _, known := s.questsFor(packID); !known || !s.packUnlocked(packID) {
		return 0, fmt.Errorf("world: current pack %d is unavailable", packID)
	}
	return packID, nil
}

func (s *Service) setCurrentPack(packID int) {
	s.activePackMu.Lock()
	s.activePack = packID
	s.activePackMu.Unlock()
}

type Service struct {
	seed         Seed
	state        *progress.Store
	starter      *player.Starter
	equipment    *player.EquipmentInventory
	inventory    *player.Inventory
	wallet       *player.Wallet
	characters   *player.CharacterStore
	collection   *player.CollectionStore
	decks        *deck.Store
	quests       map[int]gamedata.QuestDesign
	transition   gamedata.PackTransition
	packs        map[int]map[int]gamedata.QuestDesign
	transitions  map[int]gamedata.PackTransition
	activePackMu sync.RWMutex
	activePack   int
}

func (s *Service) AttachCollection(collection *player.CollectionStore) error {
	if collection == nil {
		return errors.New("world: nil collection store")
	}
	s.collection = collection
	return s.characters.AttachCollection(collection)
}

func (s *Service) AttachDecks(decks *deck.Store) error {
	if decks == nil {
		return errors.New("world: nil deck store")
	}
	s.decks = decks
	return nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	switch path {
	case "/PackInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 0, nil, true, errors.New("world: PackInfo missing sequence")
		}
		return 4, s.accountPackInfo(), true, nil
	case "/CharInfo":
		if !s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID) {
			return 0, nil, false, nil
		}
		var response []byte
		characters := s.characters.All()
		slog.Info("team trace: deliver owned characters", "characters", characters)
		for _, character := range characters {
			response = wire.AppendBytes(response, 1, encodeCharacter(character))
		}
		response = wire.AppendVarint(response, 2, s.starter.FieldCharControlDeckType)
		return 9, response, true, nil
	case "/CostumeInfo":
		if !s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID) {
			return 0, nil, false, nil
		}
		var response []byte
		costumes := s.starter.Costumes
		if s.collection != nil {
			costumes = s.collection.Costumes()
		}
		slog.Info("team trace: deliver owned costumes", "costumes", costumes, "quest26", s.seed.RewardCostume)
		for _, costume := range costumes {
			response = wire.AppendBytes(response, 1, encodeCostume(costume))
		}
		if s.collection == nil {
			response = wire.AppendBytes(response, 1, encodeCostume(s.seed.RewardCostume))
		}
		return 40, response, true, nil
	case "/PackInGameInfo":
		pack, err := requestPack(request)
		if err != nil {
			return 0, nil, true, err
		}
		if !s.packUnlocked(pack) {
			return 0, nil, true, fmt.Errorf("%w: unsupported pack %d", ErrInvalidRequest, pack)
		}
		s.setCurrentPack(pack)
		slog.Info("team trace: deliver pack progress", "pack", pack, "clearedQuests", s.state.ClearedQuests(pack), "storyCharacters", s.storyCharacters(pack))
		return 5, s.packInfoFor(pack), true, nil
	case "/QuestClear":
		quest, pack, err := requestQuest(request)
		if err != nil {
			return 0, nil, true, err
		}
		quests, unlocked := s.questsFor(pack)
		design, exists := quests[quest]
		if !unlocked || !s.packUnlocked(pack) || !exists {
			return 0, nil, true, fmt.Errorf("%w: quest %d pack %d", ErrInvalidRequest, quest, pack)
		}
		if !s.canClear(pack, quest) {
			return 0, nil, true, fmt.Errorf("%w: quest %d is not active", ErrInvalidRequest, quest)
		}
		items, questEquipment, err := s.grantQuestRewards(pack, quest, design.Rewards[0])
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.state.ClearQuest(quest, pack); err != nil {
			return 0, nil, true, fmt.Errorf("world: clear quest: %w", err)
		}
		if s.collection != nil && quest == s.seed.BattleUnlockQuestID && pack == s.seed.PackID {
			if err := s.collection.AttachRewardCostume(s.seed.RewardCostume); err != nil {
				return 0, nil, true, fmt.Errorf("world: attach cleared quest costume: %w", err)
			}
		}
		slog.Info("team trace: quest cleared", "pack", pack, "quest", quest, "changesBattleDeck", pack == s.seed.PackID && quest == s.seed.BattleUnlockQuestID)
		return 18, s.clearResponse(pack, quest, design.Rewards[0], items, questEquipment), true, nil
	default:
		return 0, nil, false, nil
	}
}

func requestPack(request []byte) (int, error) {
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, ErrInvalidRequest
	}
	pack, found, err := wire.Varint(request, 2)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, ErrInvalidRequest
	}
	return int(pack), nil
}

func requestQuest(request []byte) (int, int, error) {
	quest, err := requestPack(request) // fields 1 and 2 have the same validation.
	if err != nil {
		return 0, 0, err
	}
	pack, found, err := wire.Varint(request, 3)
	if err != nil || !found || pack == 0 || pack > uint64(^uint32(0)>>1) {
		return 0, 0, ErrInvalidRequest
	}
	return quest, int(pack), nil
}

func (s *Service) questsFor(packID int) (map[int]gamedata.QuestDesign, bool) {
	if s.packs != nil {
		quests, found := s.packs[packID]
		return quests, found
	}
	if packID == s.seed.PackID && s.quests != nil {
		return s.quests, true
	}
	return nil, false
}

func (s *Service) transitionFor(packID int) gamedata.PackTransition {
	if s.transitions != nil {
		return s.transitions[packID]
	}
	if packID == s.seed.PackID {
		return s.transition
	}
	return gamedata.PackTransition{PackID: packID}
}

// packUnlocked follows the static story chain and requires every preceding
// pack to be complete. This accepts the configured next story pack only after
// its predecessor is complete, without exposing arbitrary GameData tables.
func (s *Service) packUnlocked(packID int) bool {
	current := s.seed.PackID
	for steps := 0; steps < 64 && current != 0; steps++ {
		if current == packID {
			_, found := s.questsFor(current)
			return found
		}
		if !s.packCompleteFor(current) {
			return false
		}
		current = s.transitionFor(current).NextPackID
	}
	return false
}

func (s *Service) storyCharacters(packID int) []player.Character {
	if packID != s.seed.PackID {
		return nil
	}
	return s.seed.StoryCharacters
}

func (s *Service) canClear(packID, quest int) bool {
	if s.state.QuestCleared(quest, packID) {
		return true
	}
	quests, found := s.questsFor(packID)
	if !found {
		return false
	}
	startQuestID := 0
	for id := range quests {
		if startQuestID == 0 || id < startQuestID {
			startQuestID = id
		}
	}
	if packID == s.seed.PackID {
		startQuestID = s.seed.StartQuestID
	}
	if quest == startQuestID {
		return true
	}
	for id := range quests {
		if id < quest && !s.state.QuestCleared(id, packID) {
			return false
		}
	}
	return true
}

// MigrateClearedRewards repairs legacy saves created while QuestClear only
// rendered rewards in its response. Every backing store is idempotent, so it
// is safe to run at each startup and after a partially completed write.
func (s *Service) MigrateClearedRewards() error {
	packIDs := []int{s.seed.PackID}
	if s.packs != nil {
		packIDs = packIDs[:0]
		for packID := range s.packs {
			packIDs = append(packIDs, packID)
		}
		sort.Ints(packIDs)
	}
	for _, packID := range packIDs {
		quests, _ := s.questsFor(packID)
		for _, quest := range s.state.ClearedQuests(packID) {
			design, ok := quests[quest]
			if !ok {
				return fmt.Errorf("cleared quest %d is absent from QuestTable%d", quest, packID)
			}
			if _, _, err := s.grantQuestRewards(packID, quest, design.Rewards[0]); err != nil {
				return fmt.Errorf("pack %d quest %d: %w", packID, quest, err)
			}
		}
	}
	return nil
}

func (s *Service) grantQuestRewards(packID, quest int, designRewards []gamedata.Reward) ([]player.Item, *player.Equipment, error) {
	identity := fmt.Sprintf("pack%d:quest%d", packID, quest)
	if s.wallet != nil {
		if _, err := s.wallet.GrantQuestOnce(identity, designRewards); err != nil {
			return nil, nil, fmt.Errorf("world: grant quest currency: %w", err)
		}
	}
	var itemRewards []gamedata.BattleReward
	var equipmentReward *gamedata.Reward
	for i := range designRewards {
		reward := designRewards[i]
		switch reward.Type {
		case 3, 4:
			if reward.Count == 0 {
				return nil, nil, errors.New("world: zero currency reward")
			}
		case 10:
			if reward.ID == 0 || equipmentReward != nil {
				return nil, nil, errors.New("world: invalid equipment reward")
			}
			equipmentReward = &reward
		case 11:
			// Quest 26's character/costume instances come from the versioned
			// story seed and are encoded below; they are not stackable items.
			if packID != s.seed.PackID || quest != s.seed.BattleUnlockQuestID || reward.ID != s.seed.RewardCostume.ID {
				return nil, nil, fmt.Errorf("world: unsupported costume reward %d", reward.ID)
			}
		default:
			if reward.ID == 0 || reward.Count == 0 {
				return nil, nil, fmt.Errorf("world: invalid item reward type=%d id=%d count=%d", reward.Type, reward.ID, reward.Count)
			}
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
		}
	}
	if packID == s.seed.PackID {
		for _, reward := range questPictorialItems[quest] {
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
		}
	}
	var items []player.Item
	if len(itemRewards) != 0 {
		if s.inventory == nil {
			return nil, nil, errors.New("world: inventory unavailable")
		}
		var err error
		items, err = s.inventory.GrantOnce(identity+":items", itemRewards)
		if err != nil {
			return nil, nil, fmt.Errorf("world: grant quest items: %w", err)
		}
		if len(items) == 0 {
			items = s.inventory.GrantedItems(identity + ":items")
		}
	}
	var equipment *player.Equipment
	if equipmentReward != nil {
		if s.equipment == nil {
			return nil, nil, errors.New("world: equipment inventory unavailable")
		}
		entry, err := s.equipment.GrantOnce(fmt.Sprintf("%s:equip%d", identity, equipmentReward.ID), equipmentReward.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("world: grant quest equipment: %w", err)
		}
		equipment = &entry
	}
	return items, equipment, nil
}

// packInfo is the canonical protobuf encoding of the semantic new-account
// starter-pack state. Its first-call bytes match the 2.34.13 observed response.
func (s *Service) packInfo() []byte {
	return s.packInfoFor(s.seed.PackID)
}

func (s *Service) packInfoFor(packID int) []byte {
	var out []byte
	if packID == s.seed.PackID && s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID) {
		for _, fallback := range s.seed.StoryCharacters {
			character, found := s.characters.Find(fallback.InvenIndex)
			if !found {
				character = fallback
			}
			out = wire.AppendBytes(out, 1, encodeCharacter(character))
		}
		character, found := s.characters.Find(s.seed.RewardCharacter.InvenIndex)
		if !found {
			character = s.seed.RewardCharacter
		}
		out = wire.AppendBytes(out, 1, encodeCharacter(character))
	}
	if active := s.firstUnclearedQuestFor(packID); active != 0 {
		quest := wire.AppendVarint(nil, 1, uint64(active))
		quest = wire.AppendVarint(quest, 6, uint64(packID))
		out = wire.AppendBytes(out, 2, quest)
	}
	cleared := s.state.ClearedQuests(packID)
	if len(cleared) != 0 {
		var packed []byte
		for _, id := range cleared {
			packed = binary.AppendUvarint(packed, uint64(id))
		}
		out = wire.AppendBytes(out, 3, packed)
	}
	position := "{}"
	if saved, found := s.state.Position(); found && saved.PackID == packID && saved.RawJSON != "" {
		position = saved.RawJSON
	}
	out = wire.AppendString(out, 4, position)
	// The remaining starter-only records were observed in the official
	// starter-pack response. They represent reputation, hunting-ground, statue,
	// and reward state, not generic defaults, so a newly entered later pack must
	// not inherit them.
	if packID != s.seed.PackID {
		visit := wire.AppendVarint(nil, 5, uint64(packID))
		return wire.AppendBytes(out, 12, visit)
	}
	open := wire.AppendVarint(nil, 1, 1)
	open = wire.AppendVarint(open, 2, 1)
	out = wire.AppendBytes(out, 9, open)
	visit := wire.AppendVarint(nil, 5, uint64(packID))
	out = wire.AppendBytes(out, 12, visit)
	stat := wire.AppendVarint(nil, 1, 3)
	stat = wire.AppendVarint(stat, 2, 77)
	stat = wire.AppendVarint(stat, 3, 1)
	out = wire.AppendBytes(out, 14, stat)
	reward := wire.AppendVarint(nil, 3, 3)
	reward = wire.AppendVarint(reward, 4, 150)
	group := wire.AppendBytes(nil, 1, reward)
	group = wire.AppendBytes(group, 6, reward)
	return wire.AppendBytes(out, 16, group)
}

func (s *Service) firstUnclearedQuest() int {
	return s.firstUnclearedQuestFor(s.seed.PackID)
}

func (s *Service) firstUnclearedQuestFor(packID int) int {
	quests, found := s.questsFor(packID)
	if !found {
		return 0
	}
	ids := make([]int, 0, len(quests))
	for id := range quests {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if !s.state.QuestCleared(id, packID) {
			return id
		}
	}
	return 0
}

func (s *Service) clearResponse(packID, quest int, designRewards []gamedata.Reward, items []player.Item, questEquipment *player.Equipment) []byte {
	var rewards []byte
	for _, reward := range designRewards {
		if reward.Type != 3 && reward.Type != 4 {
			continue
		}
		currency := wire.AppendVarint(nil, 3, reward.Type)
		currency = wire.AppendVarint(currency, 4, reward.Count)
		rewards = wire.AppendBytes(rewards, 1, currency)
	}
	for _, item := range items {
		entry := player.ItemWire(item)
		if item.Type == 17 {
			pictorial := wire.AppendVarint(nil, 1, 5)
			pictorial = wire.AppendVarint(pictorial, 2, item.ID)
			entry = wire.AppendBytes(entry, 7, pictorial)
		}
		rewards = wire.AppendBytes(rewards, 1, entry)
		view := wire.AppendVarint(nil, 2, item.ID)
		view = wire.AppendVarint(view, 3, item.Type)
		view = wire.AppendVarint(view, 4, item.Count)
		rewards = wire.AppendBytes(rewards, 6, view)
	}
	if questEquipment != nil {
		// RewardDBInfoBundle field 4 is EquipDBInfo. Equipment is an instance,
		// not an ItemDBInfo with a fabricated stack count.
		rewards = wire.AppendBytes(rewards, 4, player.EquipmentWire(*questEquipment))
	}
	if packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID {
		rewardCharacter := encodeCharacter(s.seed.RewardCharacter)
		// Pictorial state: acquired level 1, progress 60.
		rewardCharacter = wire.AppendBytes(rewardCharacter, 12, wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 60))
		rewards = wire.AppendBytes(rewards, 2, rewardCharacter)
		costume := encodeCostume(s.seed.RewardCostume)
		// Pictorial state observed for the starter costume.
		costume = wire.AppendBytes(costume, 5, wire.AppendVarint(wire.AppendVarint(nil, 1, 7), 2, 116))
		rewards = wire.AppendBytes(rewards, 3, costume)
		for _, character := range s.seed.StoryCharacters {
			view := wire.AppendVarint(nil, 2, character.ID)
			view = wire.AppendVarint(view, 3, 6)
			rewards = wire.AppendBytes(rewards, 6, view)
		}
		viewCostume := wire.AppendVarint(nil, 2, s.seed.RewardCostume.ID)
		viewCostume = wire.AppendVarint(viewCostume, 3, 11)
		rewards = wire.AppendBytes(rewards, 6, viewCostume)
		viewCharacter := wire.AppendVarint(nil, 2, s.seed.RewardCharacter.ID)
		viewCharacter = wire.AppendVarint(viewCharacter, 3, 6)
		viewCharacter = wire.AppendVarint(viewCharacter, 4, 1)
		rewards = wire.AppendBytes(rewards, 6, viewCharacter)
	}
	var out []byte
	out = wire.AppendBytes(out, 1, rewards)
	next := s.nextQuestFor(packID, quest)
	if next != 0 {
		out = wire.AppendBytes(out, 2, wire.AppendVarint(nil, 1, uint64(next)))
	} else {
		// QuestClearResponse.QuestInfo is dereferenced by the 2.34.13 client
		// even when this is the final quest of a pack. An explicitly present,
		// empty QuestDBInfo gives that generated protobuf property a non-null
		// object whose Id is the client-recognized zero sentinel. Omitting the
		// field parses as null and makes the completion coroutine throw before
		// it can mark the pack complete. A final-pack official capture has not
		// yet been obtained, so this exact wire choice remains marked for parity
		// verification even though its client behavior is deterministic.
		out = wire.AppendBytes(out, 2, nil)
	}
	out = wire.AppendVarint(out, 3, uint64(quest))
	if next == 0 && s.packCompleteFor(packID) {
		// The final normal quest unlocks PackTable.NextPackId. Without these
		// PackDBInfo updates the client cannot find the next story pack and
		// falls back to presenting the hard-difficulty objective.
		for _, info := range s.packDBInfoRows() {
			out = wire.AppendBytes(out, 11, info)
		}
	}
	if packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID {
		deckIDs := []uint64{s.seed.RewardCharacter.InvenIndex, s.seed.StoryCharacters[0].InvenIndex, s.seed.StoryCharacters[1].InvenIndex, s.seed.StoryCharacters[2].InvenIndex, s.starter.Characters[0].InvenIndex}
		for index, characterID := range deckIDs {
			deck := wire.AppendVarint(nil, 1, characterID)
			deck = wire.AppendVarint(deck, 2, ^uint64(0))
			deck = wire.AppendVarint(deck, 3, uint64(index+1))
			out = wire.AppendBytes(out, 4, deck)
		}
		for _, character := range s.seed.StoryCharacters {
			out = wire.AppendBytes(out, 5, encodeCharacter(character))
		}
	} else if s.decks != nil {
		// Official 2.34.13 QuestClear always echoes the authoritative current
		// DeckInfo. Omitting it after quest 29 makes the client rebuild a party
		// from ordinary owned characters and overwrite the story formation.
		for _, current := range s.decks.CurrentDeck() {
			entry := wire.AppendVarint(nil, 1, current.CharacterInvenIndex)
			if current.CostumeInvenIndex != 0 {
				entry = wire.AppendVarint(entry, 2, current.CostumeInvenIndex)
			}
			entry = wire.AppendVarint(entry, 3, current.Slot)
			out = wire.AppendBytes(out, 4, entry)
		}
	}
	out = wire.AppendBytes(out, 12, nil)
	return wire.AppendBytes(out, 13, nil)
}

func (s *Service) packComplete() bool {
	return s.packCompleteFor(s.seed.PackID)
}

func (s *Service) packCompleteFor(packID int) bool {
	quests, found := s.questsFor(packID)
	if !found || len(quests) == 0 {
		return false
	}
	for id := range quests {
		if !s.state.QuestCleared(id, packID) {
			return false
		}
	}
	return true
}

func (s *Service) packDBInfoRows() [][]byte {
	var rows [][]byte
	packID := s.seed.PackID
	for steps := 0; steps < 64 && packID != 0; steps++ {
		cleared := s.state.ClearedQuests(packID)
		current := wire.AppendVarint(nil, 1, uint64(packID))
		if len(cleared) != 0 {
			current = wire.AppendVarint(current, 2, uint64(len(cleared)))
		}
		complete := s.packCompleteFor(packID)
		if complete {
			current = wire.AppendVarint(current, 3, 1)
		}
		// The seed pack was purchased by the account bootstrap. A later pack is
		// considered entered once it has its own progress or saved position;
		// the newly unlocked, untouched row intentionally remains unpurchased.
		purchased := packID == s.seed.PackID || len(cleared) != 0
		if saved, found := s.state.Position(); found && saved.PackID == packID {
			purchased = true
		}
		if purchased {
			current = wire.AppendVarint(current, 8, 1)
		}
		rows = append(rows, current)
		if !complete {
			break
		}
		nextPackID := s.transitionFor(packID).NextPackID
		if nextPackID == 0 {
			break
		}
		if _, found := s.questsFor(nextPackID); !found {
			// Unit-sized services and partial deployments may know the real
			// transition before the next QuestTable is attached. Preserve the
			// protocol's unlocked row, but packUnlocked still refuses entry until
			// that table is actually available.
			rows = append(rows, wire.AppendVarint(nil, 1, uint64(nextPackID)))
			break
		}
		packID = nextPackID
	}
	return rows
}

func (s *Service) accountPackInfo() []byte {
	var out []byte
	for _, info := range s.packDBInfoRows() {
		out = wire.AppendBytes(out, 1, info)
	}
	if s.packComplete() {
		level := wire.AppendVarint(nil, 1, uint64(s.seed.PackID))
		level = wire.AppendVarint(level, 3, uint64(len(s.state.ClearedQuests(s.seed.PackID))))
		level = wire.AppendVarint(level, 5, 1)
		out = wire.AppendBytes(out, 2, level)
	}
	return wire.AppendVarint(out, 5, 3)
}

var questPictorialItems = map[int][]gamedata.Reward{
	3:  {{ID: 2101, Type: 17, Count: 1}},
	10: {{ID: 2102, Type: 17, Count: 1}},
	28: {{ID: 2103, Type: 17, Count: 1}},
}

// PictorialCharacters hides quest-26 rewards until they are earned, even
// though their instances already exist in the versioned world seed.
func (s *Service) PictorialCharacters() []player.Character {
	if s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID) && s.characters != nil {
		return s.characters.RawAll()
	}
	return append([]player.Character(nil), s.starter.Characters...)
}

func (s *Service) PictorialCostumes() []player.Costume {
	result := append([]player.Costume(nil), s.starter.Costumes...)
	if s.collection != nil {
		result = s.collection.Costumes()
	}
	if s.collection == nil && s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID) {
		result = append(result, s.seed.RewardCostume)
	}
	return result
}

func (s *Service) PictorialItems() []player.Item {
	var result []player.Item
	if s.inventory != nil {
		result = s.inventory.All()
	} else {
		result = append(result, s.starter.Items...)
	}
	return result
}

func (s *Service) PictorialEquipment() []player.Equipment {
	if s.equipment == nil {
		return nil
	}
	return s.equipment.All()
}

func (s *Service) PictorialDiscovered() []player.Pictorial {
	return append([]player.Pictorial(nil), s.starter.Pictorialbook...)
}

func encodeCostume(c player.Costume) []byte {
	return player.CostumeWire(c)
}

func (s *Service) nextQuest(current int) int {
	return s.nextQuestFor(s.seed.PackID, current)
}

func (s *Service) nextQuestFor(packID, current int) int {
	quests, found := s.questsFor(packID)
	if !found {
		return 0
	}
	ids := make([]int, 0, len(quests))
	for id := range quests {
		if id > current {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

func encodeCharacter(c player.Character) []byte {
	return player.CharacterWire(c)
}
