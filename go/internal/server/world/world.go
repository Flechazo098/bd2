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
	"sync"
	"time"

	"bd2server/internal/server/deck"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/progress"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/todayquest"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

var ErrInvalidRequest = errors.New("world: invalid request")

type InitialReputation struct {
	GroupID        uint64 `json:"group_id"`
	State          uint64 `json:"state"`
	ElapsedSeconds uint64 `json:"elapsed_seconds,omitempty"`
}
type InitialRankStatue struct {
	ID     uint64 `json:"id"`
	Season uint64 `json:"season"`
	Error  bool   `json:"error"`
}

type Seed struct {
	SquareSceneID      uint64              `json:"square_scene_id,omitempty"`
	InitialReputations []InitialReputation `json:"initial_reputations,omitempty"`
	InitialRankStatues []InitialRankStatue `json:"initial_rank_statues,omitempty"`
	// Versioned story slot placeholder; confirmed current CostumeTable row, slot semantics await current capture.
	PlaceholderCostumeID uint64             `json:"placeholder_costume_id,omitempty"`
	Version              string             `json:"version"`
	PackID               int                `json:"pack_id"`
	StartQuestID         int                `json:"start_quest_id"`
	BattleUnlockQuestID  int                `json:"battle_unlock_quest_id"`
	RewardCharacter      player.Character   `json:"reward_character"`
	RewardCostume        player.Costume     `json:"reward_costume"`
	StoryCharacters      []player.Character `json:"story_characters"`
}

// Load reads the small, versioned world seed.  Quest IDs are then verified
// against the authoritative shared QuestTable<pack> GameData database.
func Load(seedPath, gameDataRoot, gameDataVersion string, storage stateio.Store, state *progress.Store, starter *player.Starter, equipment *player.EquipmentInventory, inventory *player.Inventory, wallet *player.Wallet) (*Service, error) {
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
	if seed.Version != versionconfig.State() || seed.PackID <= 0 || seed.StartQuestID <= 0 || seed.BattleUnlockQuestID <= 0 || seed.RewardCharacter.ID == 0 || seed.RewardCostume.ID == 0 || len(seed.StoryCharacters) == 0 {
		return nil, errors.New("world: invalid seed")
	}
	ownedCharacters := append([]player.Character(nil), starter.Characters...)
	ownedCharacters = append(ownedCharacters, seed.RewardCharacter)
	ownedCharacters = append(ownedCharacters, seed.StoryCharacters...)
	characters, err := player.OpenCharacterStore(storage, ownedCharacters, inventory, gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, fmt.Errorf("world: open character state: %w", err)
	}
	storyCatalog, err := gamedata.LoadStoryCatalog(gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, err
	}
	packs := make(map[int]map[int]gamedata.QuestDesign)
	transitions := make(map[int]gamedata.PackTransition)
	for id, pack := range storyCatalog.Packs {
		packs[id] = pack.Quests
		transitions[id] = gamedata.PackTransition{PackID: id, NextPackID: pack.NextPackID}
	}
	questCostumes, err := gamedata.LoadQuestCostumes(gameDataRoot, gameDataVersion, packs, storyCatalog)
	if err != nil {
		return nil, err
	}
	quests := packs[seed.PackID]
	fieldPacks, err := gamedata.LoadFieldPacks(gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, err
	}
	packSummaryTargets, err := gamedata.LoadPackSummaryTargets(gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, err
	}
	packJamDesign, err := gamedata.LoadPackJamDesign(gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, err
	}
	if _, ok := quests[seed.StartQuestID]; !ok {
		return nil, fmt.Errorf("world: start quest %d is absent from QuestTable%d", seed.StartQuestID, seed.PackID)
	}
	transition := transitions[seed.PackID]
	activePack := seed.PackID
	if id := state.ActivePackID(); id != 0 {
		activePack = id
	}
	if saved, found := state.Position(); found && state.ActivePackID() == 0 {
		_, storyKnown := packs[saved.PackID]
		_, fieldKnown := fieldPacks[saved.PackID]
		if storyKnown || fieldKnown {
			activePack = saved.PackID
		}
	}
	service := &Service{seed: seed, state: state, starter: starter, equipment: equipment, inventory: inventory, wallet: wallet, characters: characters, quests: quests, transition: transition, packs: packs, transitions: transitions, activePack: activePack, fieldPacks: fieldPacks}
	service.questDifficulties, err = gamedata.LoadQuestDifficulties(gameDataRoot, gameDataVersion)
	if err != nil {
		return nil, err
	}
	service.packJamDesign = packJamDesign
	service.packSummaryTargets = packSummaryTargets
	service.storyCatalog = storyCatalog
	if err := service.orderMainQuests(); err != nil {
		return nil, err
	}
	service.questCostumes = questCostumes
	var storyPackIDs []int
	for id := range packs {
		storyPackIDs = append(storyPackIDs, id)
	}
	service.storyRoster, err = gamedata.LoadStoryCharacterCatalog(gameDataRoot, gameDataVersion, storyPackIDs, seed.PlaceholderCostumeID)
	if err != nil {
		return nil, err
	}
	service.attachPackDetailDesign(gameDataRoot, gameDataVersion)
	service.attachFieldMonsterDesign(gameDataRoot, gameDataVersion)
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
	autoRecoveryPolicy *gamedata.PackRecoveryPolicy
	todayQuests        *todayquest.Service
	huntingGround      interface{ EnsureForPack(int) ([]byte, error) }
	battleActive       func() bool
	questDifficulties  map[int]map[int]bool
	startingPackID     int
	storyRoster        *gamedata.StoryCharacterCatalog
	storyCatalog       *gamedata.StoryCatalog
	questCostumes      player.CostumeDesignSource
	packDetailDesign   func(int) (gamedata.PackDetailDesign, error)
	packSummaryTargets map[int]bool
	packJamDesign      *gamedata.PackJamDesign
	packJamMu          sync.Mutex
	fieldPacks         map[int]gamedata.FieldPack
	eventFieldPacks    EventFieldPackSource
	fieldObjects       map[int]gamedata.FieldObjectDesign
	fieldObjectLoader  func(int) (gamedata.FieldObjectDesign, error)
	monsterLoader      func(int) ([]gamedata.FieldMonsterDesign, error)
	monsterStore       stateio.Store
	monsterNow         func() time.Time
	monsterSession     string
	monsterRewards     func(int, uint64) ([]gamedata.BattleReward, error)
	monsterMaps        func(int) (map[int][]int, error)
	monsterDamage      func(int, uint64, string) ([][]byte, error)
	fieldBuffs         map[uint64]gamedata.FieldBuffDesign
	talentPackInfo     func(int) ([]byte, error)
	overwhelmAuthorize func(string, uint64) error
	overwhelmSky       []gamedata.SkyWayOverwhelmRule
	overwhelmQuest     func(int, int) (gamedata.OverwhelmQuestRule, error)
	overwhelmHunting   interface {
		ValidateBattle(int, uint64, uint64, uint64) error
		CompleteBattle(int, uint64, uint64, uint64, string) ([]byte, [][]byte, error)
	}
	npcReputation      *npcReputationRuntime
	fieldReset         gamedata.FieldResetSchedule
	researchDesigns    map[int]gamedata.FieldResearchDesign
	researchLoader     func(int) (gamedata.FieldResearchDesign, error)
	researchCharacters map[uint64]bool
	researchEconomy    interface {
		Apply(string, []gamedata.Reward, []gamedata.Reward) ([]byte, error)
	}
	squadLevel         func() (uint64, error)
	seed               Seed
	state              *progress.Store
	starter            *player.Starter
	equipment          *player.EquipmentInventory
	inventory          *player.Inventory
	wallet             *player.Wallet
	characters         *player.CharacterStore
	collection         *player.CollectionStore
	decks              *deck.Store
	quests             map[int]gamedata.QuestDesign
	transition         gamedata.PackTransition
	packs              map[int]map[int]gamedata.QuestDesign
	transitions        map[int]gamedata.PackTransition
	activePackMu       sync.RWMutex
	activePack         int
	prestigeSelections func() (map[uint64]uint64, error)
}

func (s *Service) AttachCollection(collection *player.CollectionStore) error {
	if collection == nil {
		return errors.New("world: nil collection store")
	}
	s.collection = collection
	return s.characters.AttachCollection(collection)
}

// AttachPrestigeSelections projects the durable skin choice into CostumeInfo
// responses without changing the frozen collection/deck schemas.
func (s *Service) AttachPrestigeSelections(provider func() (map[uint64]uint64, error)) error {
	if provider == nil {
		return errors.New("world: nil prestige selection provider")
	}
	s.prestigeSelections = provider
	return nil
}

func (s *Service) AttachDecks(decks *deck.Store) error {
	if decks == nil {
		return errors.New("world: nil deck store")
	}
	s.decks = decks
	return nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if s.todayQuests != nil {
		if code, body, handled, err := s.todayQuests.Handle(path, request); handled {
			if err == nil {
				body = s.commissionResponseDeck(code, body)
			}
			return code, body, handled, err
		}
	}
	switch path {
	case "/MonsterInfo":
		return s.handleMonsterInfo(request)
	case "/FieldMonsterRegen":
		return s.handleFieldMonsterRegen(request)
	case "/FieldMonsterEvent", "/FieldMonsterDamage":
		return s.handleFieldMonsterEvent(path, request)
	case "/FieldMonsterReward":
		return s.handleFieldMonsterReward(request)
	case "/Overwhelm":
		return s.handleOverwhelm(request)
	case "/QuestUpdate":
		return s.handleQuestUpdate(request)
	case "/FieldObjectInfo":
		return s.handleFieldObjectInfo(request)
	case "/FieldObjectReward":
		return s.handleFieldObjectReward(request)
	case "/FieldObjectRewardList":
		return s.handleFieldObjectRewardList(request)
	case "/FieldObjectPreview":
		return s.handleFieldObjectPreview(request)
	case "/FieldObjectRespawn":
		return s.handleFieldObjectRespawn(request)
	case "/FieldObjecPositionUpdate", "/FieldObjectPositionUpdate":
		return s.handleFieldObjectPosition(request)
	case "/FieldObjectResearch":
		return s.handleFieldResearch(request)
	case "/PackRewardObjectCount":
		return s.handlePackRewardCounts(request)
	case "/QuestInfo", "/QuestAccept", "/QuestGiveUp":
		return s.handleQuestSelection(path, request)
	case "/PackBuy":
		return s.handlePackBuy(request)
	case "/PackDetailInfo":
		return s.handlePackDetail(request)
	case "/PackSummaryInfoList":
		return s.handlePackSummary(request)
	case "/PackPreviewInfo", "/PackJamEvent":
		return s.handlePackDocking(path, request)
	case "/PackInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 0, nil, true, errors.New("world: PackInfo missing sequence")
		}
		response := s.accountPackInfo()
		rows, err := s.eventPackInfoRows()
		if err != nil {
			return 0, nil, true, err
		}
		for _, row := range rows {
			response = wire.AppendBytes(response, 1, row)
		}
		return 4, response, true, nil
	case "/CharInfo":
		seq, found, err := wire.Varint(request, 1)
		if err != nil || !found || seq == 0 {
			return 0, nil, true, errors.New("world: CharInfo missing sequence")
		}
		var response []byte
		characters := s.visibleOwnedCharacters(s.characters.All())
		if s.tutorialRosterRestricted() {
			// The tutorial roster still contains only starter identities, but
			// their field HP must come from persisted state rather than falling
			// through to Starter.Handle's immutable new-account HP.
			characters = make([]player.Character, 0, len(s.starter.Characters))
			for _, seeded := range s.starter.Characters {
				character, exists := s.characters.Find(seeded.InvenIndex)
				if !exists {
					return 0, nil, true, fmt.Errorf("world: missing starter character %d", seeded.InvenIndex)
				}
				characters = append(characters, character)
			}
		}
		if s.decks != nil {
			seen := map[uint64]bool{}
			for _, char := range characters {
				seen[char.InvenIndex] = true
			}
			deckCharacters := s.decks.CurrentDeck()
			for _, entry := range s.decks.CurrentFieldDeck() {
				deckCharacters = append(deckCharacters, deck.DeckEntry{CharacterInvenIndex: entry.CharacterInvenIndex})
			}
			for _, entry := range deckCharacters {
				if seen[entry.CharacterInvenIndex] {
					continue
				}
				if char, ok := s.characters.Find(entry.CharacterInvenIndex); ok && player.IsStoryCharacter(char) {
					characters = append(characters, char)
					seen[char.InvenIndex] = true
				}
			}
		}
		for _, character := range characters {
			response = wire.AppendBytes(response, 1, encodeCharacter(character))
		}
		control := s.starter.FieldCharControlDeckType
		if s.decks != nil {
			control = s.decks.FieldControlType()
		}
		response = wire.AppendVarint(response, 2, control)
		return 9, response, true, nil
	case "/CostumeInfo":
		if s.tutorialRosterRestricted() {
			return 0, nil, false, nil
		}
		var response []byte
		costumes := s.starter.Costumes
		if s.collection != nil {
			costumes = s.collection.Costumes()
		}
		var selections map[uint64]uint64
		if s.prestigeSelections != nil {
			var err error
			selections, err = s.prestigeSelections()
			if err != nil {
				return 0, nil, true, err
			}
		}
		for _, costume := range costumes {
			if design := selections[costume.ID]; design != 0 {
				costume.DesignID = design
			}
			response = wire.AppendBytes(response, 1, encodeCostume(costume))
		}
		if s.collection == nil {
			costume := s.seed.RewardCostume
			if design := selections[costume.ID]; design != 0 {
				costume.DesignID = design
			}
			response = wire.AppendBytes(response, 1, encodeCostume(costume))
		}
		return 40, response, true, nil
	case "/PackInGameInfo":
		if s.battleActive != nil && s.battleActive() {
			return 0, nil, true, fmt.Errorf("%w: active battle", ErrInvalidRequest)
		}
		pack, err := requestPack(request)
		if err != nil {
			return 0, nil, true, err
		}
		if eventPack, found, err := s.resolveEventFieldPack(pack); err != nil {
			return 0, nil, true, err
		} else if found {
			code, body, handled, e := s.enterEventFieldPack(eventPack)
			if e == nil && s.talentPackInfo != nil {
				extra, x := s.talentPackInfo(pack)
				if x != nil {
					return 0, nil, true, x
				}
				body = append(body, extra...)
			}
			return code, body, handled, e
		}
		if !s.packUnlocked(pack) {
			return 0, nil, true, fmt.Errorf("%w: unsupported pack %d", ErrInvalidRequest, pack)
		}
		if active := s.firstUnclearedQuestFor(pack); active != 0 {
			if _, err := s.ensureQuestItems(pack, active); err != nil {
				return 0, nil, true, err
			}
		}
		response, err := s.packInfoFor(pack)
		if err != nil {
			return 0, nil, true, err
		}
		if s.talentPackInfo != nil {
			extra, e := s.talentPackInfo(pack)
			if e != nil {
				return 0, nil, true, e
			}
			response = append(response, extra...)
		}
		if err := s.state.SetActivePackID(pack); err != nil {
			return 0, nil, true, err
		}
		s.setCurrentPack(pack)
		return 5, response, true, nil
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
		wasCleared := s.state.QuestCleared(quest, pack, s.questDifficultyFor(pack, quest))
		var previousParty []player.Character
		if design.Type == 0 && s.storyRoster != nil {
			previousParty, err = s.resolveStoryCharacters(pack, quest)
			if err != nil {
				return 0, nil, true, err
			}
		}
		items, questEquipment, err := s.grantQuestRewards(pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)])
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.state.ClearQuest(quest, pack, s.questDifficultyFor(pack, quest)); err != nil {
			return 0, nil, true, fmt.Errorf("world: clear quest: %w", err)
		}
		if s.collection != nil && quest == s.seed.BattleUnlockQuestID && pack == s.seed.PackID && s.questDifficulty(pack) == 0 && !wasCleared {
			if err := s.collection.AttachRewardCostume(s.seed.RewardCostume); err != nil {
				return 0, nil, true, fmt.Errorf("world: attach cleared quest costume: %w", err)
			}
		}
		if selection, selected := s.state.Selection(pack); selected && design.Type == 0 && !wasCleared {
			selection.QuestID = s.nextQuestFor(pack, quest)
			if err := s.state.SelectQuest(pack, selection); err != nil {
				return 0, nil, true, err
			}
		}
		var nextItems []player.Item
		var nextChars [][]byte
		if design.Type == 0 {
			if next := s.nextQuestFor(pack, quest); next != 0 {
				var err error
				nextChars, _, err = s.resolveActivePartyWires(pack, next)
				if err != nil {
					return 0, nil, true, err
				}
				nextChars, err = storyPartyChanges(previousParty, nextChars)
				if err != nil {
					return 0, nil, true, err
				}
				nextItems, err = s.ensureQuestItems(pack, next)
				if err != nil {
					return 0, nil, true, err
				}
			}
		}
		return 18, s.clearResponse(pack, quest, design.Rewards[s.questDifficultyFor(pack, quest)], items, questEquipment, nextItems, nextChars), true, nil
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
	if _, found, err := s.resolveEventFieldPack(packID); err == nil && found {
		return map[int]gamedata.QuestDesign{}, true
	}
	if _, exists := s.fieldPacks[packID]; exists {
		return map[int]gamedata.QuestDesign{}, true
	}
	if s.storyCatalog != nil {
		pack, found := s.storyCatalog.Packs[packID]
		return pack.Quests, found
	}
	return nil, false
}

// packUnlocked uses installed ContentOpen rules and real account tickets.
func (s *Service) packUnlocked(packID int) bool {
	if pack, found, err := s.resolveEventFieldPack(packID); err != nil {
		return false
	} else if found {
		return s.eventPackPurchased(pack.ID)
	}
	if pack, exists := s.fieldPacks[packID]; exists {
		return s.fieldPackUnlocked(pack)
	}
	return s.storyCatalog != nil && s.storyPackUnlocked(packID)
}

func (s *Service) storyCharacters(packID int) []player.Character {
	if packID != s.seed.PackID {
		return nil
	}
	return s.seed.StoryCharacters
}

func (s *Service) canClear(packID, quest int) bool {
	if s.state.QuestCleared(quest, packID, s.questDifficultyFor(packID, quest)) {
		return true
	}
	quests, found := s.questsFor(packID)
	if !found || s.storyCatalog == nil {
		return false
	}
	design, found := quests[quest]
	if !found {
		return false
	}
	if design.PriorQuestID != 0 && !s.state.QuestCleared(design.PriorQuestID, packID, s.questDifficultyFor(packID, quest)) {
		return false
	}
	if design.Type == 0 {
		return quest == s.firstUnclearedQuestFor(packID)
	}
	_, active := s.state.QuestInPack(quest, packID, s.questDifficultyFor(packID, quest))
	return active
}

func (s *Service) grantQuestRewards(packID, quest int, designRewards []gamedata.Reward) ([]player.Item, *player.Equipment, error) {
	identity := questRewardIdentity(packID, quest, s.questDifficultyFor(packID, quest))
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
		case 2, 3, 4, 12, 20:
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
			if packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID && s.questDifficultyFor(packID, quest) == 0 && reward.ID == s.seed.RewardCostume.ID {
				continue
			}
			if s.collection == nil || s.questCostumes == nil {
				return nil, nil, fmt.Errorf("world: costume reward service unavailable")
			}
			if _, ok := s.questCostumes.Character(reward.ID); !ok {
				return nil, nil, fmt.Errorf("world: missing quest costume %d", reward.ID)
			}
		default:
			if reward.ID == 0 || reward.Count == 0 {
				return nil, nil, fmt.Errorf("world: invalid item reward type=%d id=%d count=%d", reward.Type, reward.ID, reward.Count)
			}
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
		}
	}
	var costumeIDs []uint64
	for _, reward := range designRewards {
		if reward.Type == 11 && !(packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID && s.questDifficultyFor(packID, quest) == 0 && reward.ID == s.seed.RewardCostume.ID) {
			costumeIDs = append(costumeIDs, reward.ID)
		}
	}
	if len(costumeIDs) != 0 {
		grant, err := s.collection.GrantCostumes(identity+":costumes", costumeIDs, s.questCostumes)
		if err != nil {
			return nil, nil, fmt.Errorf("world: grant quest costume: %w", err)
		}
		var exchanges []gamedata.Reward
		for _, exchange := range grant.Exchanges {
			if exchange.ExchangeItemType != 20 {
				return nil, nil, fmt.Errorf("world: unsupported quest costume exchange type %d", exchange.ExchangeItemType)
			}
			exchanges = append(exchanges, gamedata.Reward{Type: exchange.ExchangeItemType, ID: exchange.ExchangeItemID, Count: exchange.ExchangeCount})
		}
		if len(exchanges) > 0 {
			if s.wallet == nil {
				return nil, nil, fmt.Errorf("world: quest exchange wallet unavailable")
			}
			if _, err := s.wallet.GrantQuestOnce(identity+":costumes:exchange", exchanges); err != nil {
				return nil, nil, err
			}
		}
	}
	if s.questDifficultyFor(packID, quest) == 0 {
		quests, known := s.questsFor(packID)
		if !known {
			return nil, nil, fmt.Errorf("world: unknown collection reward pack%d", packID)
		}
		for _, reward := range quests[quest].CollectionRewards {
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
// starter-pack state. Its response is generated from the authoritative local progress state.
func (s *Service) packInfo() ([]byte, error) {
	return s.packInfoFor(s.seed.PackID)
}

func (s *Service) packInfoFor(packID int) ([]byte, error) {
	out, err := s.basePackInfoFor(packID)
	if err != nil {
		return out, err
	}
	buffs, err := s.fieldBuffInfo()
	if err != nil {
		return nil, err
	}
	out = append(out, buffs...)
	if s.huntingGround == nil {
		return out, nil
	}
	ground, err := s.huntingGround.EnsureForPack(packID)
	if err != nil {
		return nil, err
	}
	if len(ground) == 0 {
		return out, nil
	}
	out, _, err = wire.ReplaceBytes(out, 12, ground)
	return out, err
}

func (s *Service) basePackInfoFor(packID int) ([]byte, error) {
	var out []byte
	if active := s.firstUnclearedQuestFor(packID); active != 0 {
		quest := s.questInfoWire(packID, active)
		chars, _, err := s.resolveActivePartyWires(packID, active)
		if err != nil {
			return nil, err
		}
		for _, char := range chars {
			out = wire.AppendBytes(out, 1, char)
		}
		out = wire.AppendBytes(out, 2, quest)
	}
	for _, quest := range s.activeSideQuestWires(packID) {
		out = wire.AppendBytes(out, 2, quest)
	}
	cleared := s.state.ClearedQuests(packID, s.questDifficulty(packID))
	// CommonPacket requests TodayQuestInfo after parsing this response, before
	// the waypoint callback calls PackManager.Enter. That separate response owns
	// commission restoration; including commissions here lets Enter append them
	// a second time when TodayQuestInfo arrives first, crashing the quest HUD.
	if len(cleared) != 0 {
		var packed []byte
		for _, id := range cleared {
			packed = binary.AppendUvarint(packed, uint64(id))
		}
		out = wire.AppendBytes(out, 3, packed)
	}
	position := "{}"
	mapID := 0
	restored := false
	if saved, found := s.state.Position(); found && saved.PackID == packID && saved.Difficulty == s.questDifficulty(packID) && saved.RawJSON != "" {
		if pack, arena := s.fieldPacks[packID]; arena && !pack.MapIDs[saved.Position.MapID] {
			return nil, fmt.Errorf("world: saved map %d does not belong to arena pack %d", saved.Position.MapID, packID)
		}
		position = saved.RawJSON
		mapID = saved.Position.MapID
		restored = true
	}
	slog.Info("world: deliver field position", "pack", packID, "map", mapID, "restored", restored)
	out = wire.AppendString(out, 4, position)
	if s.npcReputation != nil {
		rows, err := s.npcReputationRows(packID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = wire.AppendBytes(out, 9, row)
		}
	}
	// The remaining starter-only records were observed in the official
	// starter-pack response. They represent reputation, hunting-ground, statue,
	// and reward state, not generic defaults, so a newly entered later pack must
	// not inherit them.
	if packID != s.seed.PackID {
		visit := wire.AppendVarint(nil, 5, uint64(packID))
		return wire.AppendBytes(out, 12, visit), nil
	}
	for _, state := range s.seed.InitialReputations {
		if s.npcReputation != nil {
			break
		}
		row := wire.AppendVarint(nil, 1, state.GroupID)
		row = wire.AppendVarint(row, 2, state.State)
		if state.ElapsedSeconds != 0 {
			row = wire.AppendVarint(row, 3, state.ElapsedSeconds)
		}
		out = wire.AppendBytes(out, 9, row)
	}
	visit := wire.AppendVarint(nil, 5, uint64(packID))
	out = wire.AppendBytes(out, 12, visit)
	for _, state := range s.seed.InitialRankStatues {
		row := wire.AppendVarint(nil, 1, state.ID)
		row = wire.AppendVarint(row, 2, state.Season)
		if state.Error {
			row = wire.AppendVarint(row, 3, 1)
		}
		out = wire.AppendBytes(out, 14, row)
	}
	if s.packJamDesign == nil {
		return out, nil
	}
	if err := s.packJamDesign.ValidateReward(); err != nil {
		return nil, err
	}
	reward := wire.AppendVarint(nil, 3, s.packJamDesign.Reward.Type)
	reward = wire.AppendVarint(reward, 4, s.packJamDesign.Reward.Count)
	group := wire.AppendBytes(nil, 1, reward)
	group = wire.AppendBytes(group, 6, reward)
	return wire.AppendBytes(out, 16, group), nil
}

func (s *Service) firstUnclearedQuest() int {
	return s.firstUnclearedQuestFor(s.seed.PackID)
}

func (s *Service) firstUnclearedQuestFor(packID int) int {
	if s.storyCatalog == nil {
		return 0
	}
	for _, id := range s.storyCatalog.Packs[packID].MainQuestIDs {
		if !s.state.QuestCleared(id, packID, s.questDifficulty(packID)) {
			return id
		}
	}
	return 0
}

func (s *Service) clearResponse(packID, quest int, designRewards []gamedata.Reward, items []player.Item, questEquipment *player.Equipment, nextItems []player.Item, nextChars [][]byte) []byte {
	var rewards []byte
	if s.collection != nil {
		if grant, found := s.collection.Grant(questRewardIdentity(packID, quest, s.questDifficultyFor(packID, quest)) + ":costumes"); found {
			rewards = append(rewards, player.CollectionRewardBundle(s.collection, grant)...)
		}
	}
	for _, reward := range designRewards {
		if reward.Type != 2 && reward.Type != 3 && reward.Type != 4 && reward.Type != 12 && reward.Type != 20 {
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
	if packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID && s.questDifficultyFor(packID, quest) == 0 {
		rewardCharacter := encodeCharacter(s.seed.RewardCharacter)
		rewards = wire.AppendBytes(rewards, 2, rewardCharacter)
		costume := encodeCostume(s.seed.RewardCostume)
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
	if s.storyCatalog.Packs[packID].Quests[quest].Type != 0 {
		next = 0
	}
	if next != 0 {
		out = wire.AppendBytes(out, 2, s.questInfoWire(packID, next))
	} else {
		// QuestClearResponse.QuestInfo is dereferenced by the client
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
	for _, item := range nextItems {
		out = wire.AppendBytes(out, 6, player.ItemWire(item))
	}
	if s.storyCatalog.Packs[packID].Quests[quest].Type == 0 && next == 0 && s.packCompleteFor(packID) {
		// The final normal quest unlocks PackTable.NextPackId. Without these
		// PackDBInfo updates the client cannot find the next story pack and
		// falls back to presenting the hard-difficulty objective.
		for _, info := range s.packDBInfoRows() {
			out = wire.AppendBytes(out, 11, info)
		}
	}
	// Receiving a character is not a request to change the saved formation.
	out = s.appendCurrentBattleDeck(out, 4)
	for _, char := range nextChars {
		out = wire.AppendBytes(out, 5, char)
	}
	out = wire.AppendBytes(out, 9, s.questLevelInfoWire(packID, s.questDifficultyFor(packID, quest)))
	out = wire.AppendBytes(out, 12, nil)
	return wire.AppendBytes(out, 13, nil)
}

func (s *Service) packComplete() bool {
	return s.packCompleteFor(s.seed.PackID)
}

func (s *Service) packCompleteFor(packID int) bool {
	if s.storyCatalog == nil {
		return false
	}
	ids := s.storyCatalog.Packs[packID].MainQuestIDs
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !s.state.QuestCleared(id, packID, s.questDifficulty(packID)) {
			return false
		}
	}
	return true
}

func (s *Service) packDBInfoRows() [][]byte {
	if s.storyCatalog == nil {
		return nil
	}
	return s.storyPackDBInfoRows()
}

func (s *Service) accountPackInfo() []byte {
	var out []byte
	for _, info := range s.packDBInfoRows() {
		out = wire.AppendBytes(out, 1, info)
	}
	for _, info := range s.packDBInfoRows() {
		id, _, _ := wire.Varint(info, 1)
		for level := 0; level <= 4; level++ {
			if len(s.state.ClearedQuests(int(id), level)) > 0 || s.questDifficulty(int(id)) == level {
				out = wire.AppendBytes(out, 2, s.questLevelInfoWire(int(id), level))
			}
		}
	}
	if s.seed.SquareSceneID != 0 {
		out = wire.AppendVarint(out, 5, s.seed.SquareSceneID)
	}
	return out
}

// PictorialCharacters hides quest-26 rewards until they are earned, even
// though their instances already exist in the versioned world seed.
func (s *Service) PictorialCharacters() []player.Character {
	if !s.tutorialRosterRestricted() && s.characters != nil {
		var permanent []player.Character
		for _, c := range s.visibleOwnedCharacters(s.characters.RawAll()) {
			if !player.IsCharmCharacter(c) {
				permanent = append(permanent, c)
			}
		}
		return permanent
	}
	return append([]player.Character(nil), s.starter.Characters...)
}

func (s *Service) PictorialCostumes() []player.Costume {
	result := append([]player.Costume(nil), s.starter.Costumes...)
	if s.collection != nil {
		result = s.collection.Costumes()
	}
	if s.collection == nil && !s.tutorialRosterRestricted() && s.startingPack() == s.seed.PackID {
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
	return quests[current].NextQuestID
}

func encodeCharacter(c player.Character) []byte {
	return player.CharacterWire(c)
}
