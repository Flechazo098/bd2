// Package world implements the local map/quest state that is not part of a
// player's assets.  It deliberately stores semantic seed values, never a
// captured response payload.
package world

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/domain/roster/deck"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/domain/world/todayquest"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"time"
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
	RewardCharacter      roster.Character   `json:"reward_character"`
	RewardCostume        roster.Costume     `json:"reward_costume"`
	StoryCharacters      []roster.Character `json:"story_characters"`
}

func LoadSeed(path string) (Seed, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Seed{}, fmt.Errorf("world: read seed: %w", err)
	}
	var seed Seed
	if err := json.Unmarshal(b, &seed); err != nil {
		return Seed{}, fmt.Errorf("world: decode seed: %w", err)
	}
	if seed.Version != versionconfig.State() || seed.PackID <= 0 || seed.StartQuestID <= 0 || seed.BattleUnlockQuestID <= 0 || seed.RewardCharacter.ID == 0 || seed.RewardCostume.ID == 0 || len(seed.StoryCharacters) == 0 {
		return Seed{}, errors.New("world: invalid seed")
	}
	return seed, nil
}

func New(ctx command.Context, seed Seed, rules *gamedata.WorldRules, source *gamedata.Source, storage stateio.Store, state *progress.Store, starter *roster.Starter, equipment *assets.EquipmentInventory, inventory *assets.Inventory, wallet *assets.Wallet) (*Service, error) {
	if rules == nil || source == nil || state == nil || starter == nil || equipment == nil || inventory == nil || wallet == nil {
		return nil, errors.New("world: incomplete player configuration")
	}
	ownedCharacters := append([]roster.Character(nil), starter.Characters...)
	ownedCharacters = append(ownedCharacters, seed.RewardCharacter)
	ownedCharacters = append(ownedCharacters, seed.StoryCharacters...)
	characters, err := roster.OpenCharacterStore(ctx, storage, ownedCharacters, inventory, source.Root(), source.Version())
	if err != nil {
		return nil, err
	}
	quests := rules.Packs[seed.PackID]
	if _, ok := quests[seed.StartQuestID]; !ok {
		return nil, fmt.Errorf("world: start quest %d is absent from QuestTable%d", seed.StartQuestID, seed.PackID)
	}
	activePack := seed.PackID
	if id := state.ActivePackID(); id != 0 {
		activePack = id
	}
	if saved, found := state.Position(); found && state.ActivePackID() == 0 {
		_, storyKnown := rules.Packs[saved.PackID]
		_, fieldKnown := rules.FieldPacks[saved.PackID]
		if storyKnown || fieldKnown {
			activePack = saved.PackID
		}
	}
	service := &Service{seed: seed, state: state, starter: starter, equipment: equipment, inventory: inventory, wallet: wallet, characters: characters,
		quests: quests, transition: rules.Transitions[seed.PackID], packs: rules.Packs, transitions: rules.Transitions, activePack: activePack, fieldPacks: rules.FieldPacks,
		questDifficulties: rules.Difficulties, packJamDesign: rules.Jam, packSummaryTargets: rules.SummaryTargets, storyCatalog: rules.Story, questCostumes: rules.QuestCostumes, storyRoster: rules.Characters}
	service.attachPackDetailDesign(source)
	service.attachFieldMonsterDesign(source)
	service.trapLoader = source.FieldTraps
	return service, nil
}

func (s *Service) CharacterService() *roster.CharacterStore { return s.characters }

func (s *Service) EarnedQuestCostume() (roster.Costume, bool) {
	return s.seed.RewardCostume, s.state.QuestCleared(s.seed.BattleUnlockQuestID, s.seed.PackID)
}

// CurrentPackID returns the story pack selected by the latest successful
// PackInGameInfo request. BattleEnter does not carry a pack field, so battle
// sessions lock this value when they begin. On restart, Load seeds it from the
// persisted position and finally falls back to the versioned starter pack.
func (s *Service) CurrentPackID(ctx command.Context) (int, error) {

	packID := s.activePack

	if packID == 0 {
		packID = s.seed.PackID
	}
	if _, known := s.questsFor(ctx, packID); !known || !s.packUnlocked(ctx, packID) {
		return 0, fmt.Errorf("world: current pack %d is unavailable", packID)
	}
	return packID, nil
}

func (s *Service) setCurrentPack(packID int) {

	if s.activePack != packID {
		s.transientVersion++
	}
	s.activePack = packID

}

func (s *Service) TransientVersion() uint64 {

	return s.transientVersion
}

type Service struct {
	autoRecoveryPolicy *gamedata.PackRecoveryPolicy
	todayQuests        *todayquest.Service
	huntingGround      interface {
		EnsureForPack(ctx command.Context, _ int) ([]byte, error)
	}
	battleActive       func(command.Context) bool
	questDifficulties  map[int]map[int]bool
	startingPackID     int
	storyRoster        *gamedata.StoryCharacterCatalog
	storyCatalog       *gamedata.StoryCatalog
	questCostumes      roster.CostumeDesignSource
	packDetailDesign   func(int) (gamedata.PackDetailDesign, error)
	packSummaryTargets map[int]bool
	packJamDesign      *gamedata.PackJamDesign

	fieldPacks        map[int]gamedata.FieldPack
	eventFieldPacks   EventFieldPackSource
	fieldObjects      map[int]gamedata.FieldObjectDesign
	fieldObjectLoader func(int) (gamedata.FieldObjectDesign, error)
	monsterLoader     func(int) ([]gamedata.FieldMonsterDesign, error)
	monsterStore      stateio.Store
	monsterNow        func() time.Time

	monsterRewards     func(int, uint64) ([]gamedata.BattleReward, error)
	monsterMaps        func(int) (map[int][]int, error)
	monsterDamage      func(command.Context, int, uint64, string) ([][]byte, error)
	trapLoader         func(int) (gamedata.FieldTrapDesign, error)
	fieldBuffs         map[uint64]gamedata.FieldBuffDesign
	talentPackInfo     func(ctx command.Context, _ int) ([]byte, error)
	overwhelmAuthorize func(ctx command.Context, _ string, _ uint64) error
	skyway             *skyWayRuntime
	overwhelmQuest     func(int, int) (gamedata.OverwhelmQuestRule, error)
	overwhelmHunting   interface {
		ValidateBattle(ctx command.Context, _ int, _ uint64, _ uint64, _ uint64) error
		CompleteBattle(ctx command.Context, _ int, _ uint64, _ uint64, _ uint64, _ string) ([]byte, [][]byte, error)
	}
	npcReputation      *npcReputationRuntime
	fieldReset         gamedata.FieldResetSchedule
	researchDesigns    map[int]gamedata.FieldResearchDesign
	researchLoader     func(int) (gamedata.FieldResearchDesign, error)
	researchCharacters map[uint64]bool
	researchEconomy    interface {
		Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
	}
	squadLevel  func() (uint64, error)
	seed        Seed
	state       *progress.Store
	starter     *roster.Starter
	equipment   *assets.EquipmentInventory
	inventory   *assets.Inventory
	wallet      *assets.Wallet
	characters  *roster.CharacterStore
	collection  *roster.CollectionStore
	decks       *deck.Store
	quests      map[int]gamedata.QuestDesign
	transition  gamedata.PackTransition
	packs       map[int]map[int]gamedata.QuestDesign
	transitions map[int]gamedata.PackTransition

	activePack         int
	transientVersion   uint64
	prestigeSelections func(ctx command.Context) (map[uint64]uint64, error)
}

func (s *Service) AttachCollection(ctx command.Context, collection *roster.CollectionStore) error {
	if collection == nil {
		return errors.New("world: nil collection store")
	}
	s.collection = collection
	return s.characters.AttachCollection(ctx, collection)
}

// AttachPrestigeSelections projects the durable skin choice into CostumeInfo
// responses without changing the frozen collection/deck schemas.
func (s *Service) AttachPrestigeSelections(ctx command.Context, provider func(ctx command.Context) (map[uint64]uint64, error)) error {
	if provider == nil {
		return errors.New("world: nil prestige selection provider")
	}
	s.prestigeSelections = provider
	return nil
}

func (s *Service) AttachDecks(ctx command.Context, decks *deck.Store) error {
	if decks == nil {
		return errors.New("world: nil deck store")
	}
	s.decks = decks
	return nil
}

func (s *Service) questsFor(ctx command.Context, packID int) (map[int]gamedata.QuestDesign, bool) {
	if _, found, err := s.resolveEventFieldPack(ctx, packID); err == nil && found {
		return map[int]gamedata.QuestDesign{}, true
	}
	if s.storyCatalog != nil {
		pack, found := s.storyCatalog.Packs[packID]
		if found {
			return pack.Quests, true
		}
	}
	if _, exists := s.fieldPacks[packID]; exists {
		return map[int]gamedata.QuestDesign{}, true
	}
	return nil, false
}

// packUnlocked uses installed ContentOpen rules and real account tickets.
func (s *Service) packUnlocked(ctx command.Context, packID int) bool {
	if pack, found, err := s.resolveEventFieldPack(ctx, packID); err != nil {
		return false
	} else if found {
		return s.eventPackPurchased(pack.ID)
	}
	if pack, exists := s.fieldPacks[packID]; exists {
		return s.fieldPackUnlocked(ctx, pack)
	}
	return s.storyCatalog != nil && s.storyPackUnlocked(ctx, packID)
}

func (s *Service) canClear(ctx command.Context, packID, quest int) bool {
	if s.state.QuestCleared(quest, packID, s.questDifficultyFor(packID, quest)) {
		return true
	}
	quests, found := s.questsFor(ctx, packID)
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

func (s *Service) grantQuestRewards(ctx command.Context, packID, quest int, designRewards []gamedata.Reward) ([]assets.Item, *assets.Equipment, error) {
	identity := questRewardIdentity(packID, quest, s.questDifficultyFor(packID, quest))
	if s.wallet != nil {
		if _, err := s.wallet.GrantQuestOnce(ctx, identity, designRewards); err != nil {
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
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
		}
	}
	var costumeIDs []uint64
	for _, reward := range designRewards {
		if reward.Type == 11 && !(packID == s.seed.PackID && quest == s.seed.BattleUnlockQuestID && s.questDifficultyFor(packID, quest) == 0 && reward.ID == s.seed.RewardCostume.ID) { //nolint:staticcheck // QF1001
			costumeIDs = append(costumeIDs, reward.ID)
		}
	}
	if len(costumeIDs) != 0 {
		grant, err := s.collection.GrantCostumes(ctx, identity+":costumes", costumeIDs, s.questCostumes)
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
			if _, err := s.wallet.GrantQuestOnce(ctx, identity+":costumes:exchange", exchanges); err != nil {
				return nil, nil, err
			}
		}
	}
	if s.questDifficultyFor(packID, quest) == 0 {
		quests, known := s.questsFor(ctx, packID)
		if !known {
			return nil, nil, fmt.Errorf("world: unknown collection reward pack%d", packID)
		}
		for _, reward := range quests[quest].CollectionRewards {
			itemRewards = append(itemRewards, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
		}
	}
	var items []assets.Item
	if len(itemRewards) != 0 {
		if s.inventory == nil {
			return nil, nil, errors.New("world: inventory unavailable")
		}
		var err error
		items, err = s.inventory.GrantOnce(ctx, identity+":items", itemRewards)
		if err != nil {
			return nil, nil, fmt.Errorf("world: grant quest items: %w", err)
		}
		if len(items) == 0 {
			items = s.inventory.GrantedItems(identity + ":items")
		}
	}
	var equipment *assets.Equipment
	if equipmentReward != nil {
		if s.equipment == nil {
			return nil, nil, errors.New("world: equipment inventory unavailable")
		}
		entry, err := s.equipment.GrantOnce(ctx, fmt.Sprintf("%s:equip%d", identity, equipmentReward.ID), equipmentReward.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("world: grant quest equipment: %w", err)
		}
		equipment = &entry
	}
	return items, equipment, nil
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

// PictorialCharacters hides quest-26 rewards until they are earned, even
// though their instances already exist in the versioned world seed.
func (s *Service) PictorialCharacters() []roster.Character {
	if !s.tutorialRosterRestricted() && s.characters != nil {
		var permanent []roster.Character
		for _, c := range s.visibleOwnedCharacters(s.characters.RawAll()) {
			if !roster.IsCharmCharacter(c) {
				permanent = append(permanent, c)
			}
		}
		return permanent
	}
	return append([]roster.Character(nil), s.starter.Characters...)
}

func (s *Service) PictorialCostumes() []roster.Costume {
	result := append([]roster.Costume(nil), s.starter.Costumes...)
	if s.collection != nil {
		result = s.collection.Costumes()
	}
	if s.collection == nil && !s.tutorialRosterRestricted() && s.startingPack() == s.seed.PackID {
		result = append(result, s.seed.RewardCostume)
	}
	return result
}

func (s *Service) PictorialItems(ctx command.Context) []assets.Item {
	var result []assets.Item
	if s.inventory != nil {
		result = s.inventory.All(ctx)
	} else {
		result = append(result, s.starter.Items...)
	}
	return result
}

func (s *Service) PictorialEquipment(ctx command.Context) []assets.Equipment {
	if s.equipment == nil {
		return nil
	}
	return s.equipment.All(ctx)
}

func (s *Service) PictorialDiscovered() []roster.Pictorial {
	return append([]roster.Pictorial(nil), s.starter.Pictorialbook...)
}

func encodeCostume(c roster.Costume) []byte {
	return roster.CostumeWire(c)
}

func (s *Service) nextQuestFor(ctx command.Context, packID, current int) int {
	quests, found := s.questsFor(ctx, packID)
	if !found {
		return 0
	}
	return quests[current].NextQuestID
}

func encodeCharacter(c roster.Character) []byte {
	return roster.CharacterWire(c)
}
