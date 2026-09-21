package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bd2server/internal/gamedata"
)

type CostumeUpgrade struct {
	InvenIndex uint64 `json:"inven_index"`
	CostumeID  uint64 `json:"costume_id"`
	Before     uint64 `json:"before"`
	After      uint64 `json:"after"`
	SortID     uint64 `json:"sort_id"`
}

type CostumeExchange struct {
	InvenIndex       uint64 `json:"inven_index,omitempty"`
	OriginalItemType uint64 `json:"original_item_type"`
	OriginalItemID   uint64 `json:"original_item_id"`
	OriginalCount    uint64 `json:"original_count"`
	ExchangeItemType uint64 `json:"exchange_item_type"`
	ExchangeItemID   uint64 `json:"exchange_item_id,omitempty"`
	ExchangeCount    uint64 `json:"exchange_count"`
	SortID           uint64 `json:"sort_id"`
}

type CollectionGrant struct {
	CharacterIndices      []uint64          `json:"character_indices,omitempty"`
	CostumeIndices        []uint64          `json:"costume_indices,omitempty"`
	Upgrades              []CostumeUpgrade  `json:"upgrades,omitempty"`
	Exchanges             []CostumeExchange `json:"exchanges,omitempty"`
	ViewCostumeIDs        []uint64          `json:"view_costume_ids"`
	GachaGroupID          uint64            `json:"gacha_group_id,omitempty"`
	GachaPoint            uint64            `json:"gacha_point,omitempty"`
	GachaFixed            []GachaFixedState `json:"gacha_fixed,omitempty"`
	SelectionApplySortIDs []uint64          `json:"selection_apply_sort_ids,omitempty"`
}

type GachaUserState struct {
	GroupID              uint64 `json:"group_id"`
	Point                uint64 `json:"point"`
	TotalBuyCount        uint64 `json:"total_buy_count,omitempty"`
	OneFreePickCount     uint64 `json:"one_free_pick_count,omitempty"`
	OneCashPickCount     uint64 `json:"one_cash_pick_count,omitempty"`
	TenFreePickCount     uint64 `json:"ten_free_pick_count,omitempty"`
	TenCashPickCount     uint64 `json:"ten_cash_pick_count,omitempty"`
	ExchangeItemCount    uint64 `json:"exchange_item_count,omitempty"`
	ExchangeMileageCount uint64 `json:"exchange_mileage_count,omitempty"`
}

type GachaFixedState struct {
	FixedID   uint64 `json:"fixed_id"`
	Type      uint64 `json:"type"`
	Count     uint64 `json:"count"`
	ApplySort int    `json:"apply_sort_id"`
}

type GachaPointExchange struct {
	GroupID uint64 `json:"group_id"`
	Count   uint64 `json:"count"`
}

type GachaPurchase struct {
	Group                 gamedata.GachaGroupDesign
	BuyType               uint64
	Fixed                 []GachaFixedState
	SelectionApplySortIDs []uint64
}

type GachaSelection struct {
	GroupID uint64 `json:"group_id"`
	Slot    uint64 `json:"slot"`
	ItemID  uint64 `json:"item_id"`
}

// FirstGachaCompletedIdentity is the persisted account flag represented in
// the existing collection grant ledger. It is written atomically with the
// official GachaSubType=3 first-pick transaction.
const FirstGachaCompletedIdentity = "account:first-gacha-completed"

type collectionSnapshot struct {
	Version             string                        `json:"version"`
	NextCharacterIndex  uint64                        `json:"next_character_index"`
	NextCostumeIndex    uint64                        `json:"next_costume_index"`
	LatestPreview       []uint64                      `json:"latest_preview,omitempty"`
	PreviewEventIndex   uint64                        `json:"preview_event_index,omitempty"`
	PreviewLocked       bool                          `json:"preview_locked,omitempty"`
	Characters          []Character                   `json:"characters,omitempty"`
	Costumes            []Costume                     `json:"costumes,omitempty"`
	BaseCostumeLevels   map[string]uint64             `json:"base_costume_levels,omitempty"`
	CostumePotential    map[string][]uint64           `json:"costume_potential"`
	GachaSelections     map[string][]GachaSelection   `json:"gacha_selections,omitempty"`
	StepUpProgress      map[string]uint64             `json:"step_up_progress,omitempty"`
	GachaUsers          map[string]GachaUserState     `json:"gacha_users,omitempty"`
	GachaFixed          map[string]GachaFixedState    `json:"gacha_fixed,omitempty"`
	GachaApplied        map[string]bool               `json:"gacha_applied,omitempty"`
	GachaPointExchange  map[string]GachaPointExchange `json:"gacha_point_exchanges,omitempty"`
	GachaCountCorrected bool                          `json:"gacha_count_corrected,omitempty"`
	Grants              map[string]CollectionGrant    `json:"grants"`
}

// CollectionStore owns non-stackable character/costume rewards as one atomic
// save. It is shared by gacha, CharInfo, CostumeInfo and CharGrowth so a roll
// cannot exist only in the result animation and disappear after relogging.
type CollectionStore struct {
	mu             sync.Mutex
	path           string
	base           []Costume
	baseCharacters []Character
	data           collectionSnapshot
}

func OpenCollectionStore(path string, base []Costume) (*CollectionStore, error) {
	s := &CollectionStore{path: filepath.Clean(path), base: append([]Costume(nil), base...), data: collectionSnapshot{
		Version: "2.34.13", NextCharacterIndex: 920000001, NextCostumeIndex: 930000001,
		BaseCostumeLevels: map[string]uint64{}, GachaSelections: map[string][]GachaSelection{},
		CostumePotential: map[string][]uint64{},
		StepUpProgress:   map[string]uint64{}, GachaUsers: map[string]GachaUserState{}, GachaFixed: map[string]GachaFixedState{},
		GachaApplied: map[string]bool{}, GachaPointExchange: map[string]GachaPointExchange{}, Grants: map[string]CollectionGrant{},
	}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("player: decode collection: %w", err)
	}
	if s.data.Version != "2.34.13" || s.data.NextCharacterIndex < 920000001 || s.data.NextCostumeIndex < 930000001 || s.data.Grants == nil {
		return nil, errors.New("player: invalid collection save")
	}
	if s.data.BaseCostumeLevels == nil {
		s.data.BaseCostumeLevels = map[string]uint64{}
	}
	if s.data.CostumePotential == nil {
		return nil, errors.New("player: collection save requires costume_potential; migrate the development save")
	}
	if s.data.GachaSelections == nil {
		s.data.GachaSelections = map[string][]GachaSelection{}
	}
	if s.data.StepUpProgress == nil {
		s.data.StepUpProgress = map[string]uint64{}
	}
	if s.data.GachaUsers == nil {
		s.data.GachaUsers = map[string]GachaUserState{}
	}
	if s.data.GachaFixed == nil {
		s.data.GachaFixed = map[string]GachaFixedState{}
	}
	if s.data.GachaApplied == nil {
		s.data.GachaApplied = map[string]bool{}
	}
	if s.data.GachaPointExchange == nil {
		s.data.GachaPointExchange = map[string]GachaPointExchange{}
	}
	if marker, exists := s.data.Grants[FirstGachaCompletedIdentity]; exists && !emptyCollectionGrant(marker) {
		return nil, errors.New("player: invalid first-gacha completion marker")
	}
	if err := validateCharacters(s.data.Characters); err != nil && len(s.data.Characters) != 0 {
		return nil, err
	}
	return s, nil
}

func emptyCollectionGrant(grant CollectionGrant) bool {
	return len(grant.CharacterIndices) == 0 && len(grant.CostumeIndices) == 0 &&
		len(grant.Upgrades) == 0 && len(grant.Exchanges) == 0 &&
		len(grant.ViewCostumeIDs) == 0 && grant.GachaGroupID == 0 &&
		grant.GachaPoint == 0 && len(grant.GachaFixed) == 0 &&
		len(grant.SelectionApplySortIDs) == 0
}

// AttachBaseCharacters supplies the character instances owned by the world
// and starter stores. Collection characters must share those instances: a new
// costume for an already-owned CharTable ID is not a second character. The
// repair also migrates saves written by the old one-character-per-costume
// implementation without deleting any costume, level, or gacha grant.
func (s *CollectionStore) AttachBaseCharacters(base []Character) error {
	if err := validateCharacters(base); err != nil && len(base) != 0 {
		return fmt.Errorf("player: invalid base characters: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneCollection(s.data)
	canonical := make(map[uint64]uint64, len(base)+len(next.Characters))
	for _, character := range base {
		if _, exists := canonical[character.ID]; !exists {
			canonical[character.ID] = character.InvenIndex
		}
	}

	redirect := make(map[uint64]uint64)
	kept := make([]Character, 0, len(next.Characters))
	for _, character := range next.Characters {
		if existing, exists := canonical[character.ID]; exists {
			redirect[character.InvenIndex] = existing
			continue
		}
		canonical[character.ID] = character.InvenIndex
		kept = append(kept, character)
	}

	changed := len(kept) != len(next.Characters)
	if changed {
		next.Characters = kept
		for i := range next.Costumes {
			if replacement, exists := redirect[next.Costumes[i].UseChar]; exists {
				next.Costumes[i].UseChar = replacement
			}
		}
		for identity, grant := range next.Grants {
			indices := grant.CharacterIndices[:0]
			for _, index := range grant.CharacterIndices {
				if _, removed := redirect[index]; !removed {
					indices = append(indices, index)
				}
			}
			grant.CharacterIndices = indices
			next.Grants[identity] = grant
		}
		if err := s.commit(next); err != nil {
			return err
		}
	}
	s.baseCharacters = append([]Character(nil), base...)
	return nil
}

// BindBaseCharacters attaches the authoritative base roster after the
// Haskell startup repair has removed duplicate designs. It never writes state.
func (s *CollectionStore) BindBaseCharacters(base []Character) error {
	if err := validateCharacters(base); err != nil && len(base) != 0 {
		return fmt.Errorf("player: invalid base characters: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[uint64]uint64, len(base)+len(s.data.Characters))
	for _, character := range base {
		if prior := seen[character.ID]; prior != 0 {
			return fmt.Errorf("player: duplicate base character design %d", character.ID)
		}
		seen[character.ID] = character.InvenIndex
	}
	for _, character := range s.data.Characters {
		if prior := seen[character.ID]; prior != 0 {
			return fmt.Errorf("player: unrepaired duplicate character design %d (%d and %d)", character.ID, prior, character.InvenIndex)
		}
		seen[character.ID] = character.InvenIndex
	}
	s.baseCharacters = append([]Character(nil), base...)
	return nil
}

// AttachRewardCostume joins an already-earned quest costume to the same
// account-owned view used by gacha and starter costumes. The quest condition
// must be checked by the caller; this does not grant an unearned costume.
func (s *CollectionStore) AttachRewardCostume(reward Costume) error {
	if reward.InvenIndex == 0 || reward.ID == 0 || reward.UseChar == 0 {
		return errors.New("player: invalid earned quest costume")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.base {
		if existing.InvenIndex == reward.InvenIndex || existing.ID == reward.ID {
			return fmt.Errorf("player: duplicate earned quest costume %d", reward.InvenIndex)
		}
	}
	for _, existing := range s.data.Costumes {
		if existing.InvenIndex == reward.InvenIndex || existing.ID == reward.ID {
			return fmt.Errorf("player: earned quest costume overlaps collection %d", reward.InvenIndex)
		}
	}
	s.base = append(s.base, reward)
	return nil
}

// RepairStepUpProgress derives the completed step count from durable grants
// written before step-up progress had its own save field. Grant identities may
// contain either the legacy request sequence or the newer login-session ID.
func (s *CollectionStore) RepairStepUpProgress(groupID uint64, gachaIDs []uint64) (uint64, error) {
	if groupID == 0 || len(gachaIDs) == 0 {
		return 0, errors.New("player: invalid step-up design")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatUint(groupID, 10)
	completed := s.data.StepUpProgress[key]
	if completed > uint64(len(gachaIDs)) {
		return 0, fmt.Errorf("player: step-up group %d progress %d exceeds %d", groupID, completed, len(gachaIDs))
	}
	derived := uint64(0)
	for i, gachaID := range gachaIDs {
		prefix := fmt.Sprintf("regular-gacha:%d:", gachaID)
		found := false
		for identity := range s.data.Grants {
			if strings.HasPrefix(identity, prefix) {
				found = true
				break
			}
		}
		if !found {
			break
		}
		derived = uint64(i + 1)
	}
	if derived <= completed {
		return completed, nil
	}
	next := cloneCollection(s.data)
	next.StepUpProgress[key] = derived
	if err := s.commit(next); err != nil {
		return 0, err
	}
	return derived, nil
}

func (s *CollectionStore) StepUpProgress(groupID uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.StepUpProgress[strconv.FormatUint(groupID, 10)]
}

// CompleteStepUp atomically advances one group. Repeating the same completed
// step is allowed so a lost HTTP response can be retried without reopening it.
func (s *CollectionStore) CompleteStepUp(groupID, step uint64) error {
	if groupID == 0 || step == 0 {
		return errors.New("player: invalid step-up completion")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatUint(groupID, 10)
	completed := s.data.StepUpProgress[key]
	if completed >= step {
		return nil
	}
	if completed+1 != step {
		return fmt.Errorf("player: step-up group %d expects step %d, got %d", groupID, completed+1, step)
	}
	next := cloneCollection(s.data)
	next.StepUpProgress[key] = step
	return s.commit(next)
}

func (s *CollectionStore) SaveGachaSelections(groupID uint64, selections []GachaSelection) error {
	if groupID == 0 || len(selections) == 0 {
		return errors.New("player: invalid gacha selection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	next.GachaSelections[strconv.FormatUint(groupID, 10)] = append([]GachaSelection(nil), selections...)
	return s.commit(next)
}

func (s *CollectionStore) GachaSelections(groupID uint64) []GachaSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GachaSelection(nil), s.data.GachaSelections[strconv.FormatUint(groupID, 10)]...)
}

func (s *CollectionStore) SetPreview(costumeIDs []uint64) error {
	if len(costumeIDs) != 10 {
		return errors.New("player: infinite preview must contain ten costumes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	next.LatestPreview = append([]uint64(nil), costumeIDs...)
	next.PreviewEventIndex = 0
	next.PreviewLocked = false
	return s.commit(next)
}

// LockPreview records the official Resemara confirmation boundary. The
// client sends the schedule's EventIndex after the player accepts the latest
// preview; generating another preview clears this lock again.
func (s *CollectionStore) LockPreview(eventIndex uint64) error {
	if eventIndex == 0 {
		return errors.New("player: invalid infinite preview event index")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.LatestPreview) != 10 {
		return errors.New("player: no complete infinite gacha preview to lock")
	}
	next := cloneCollection(s.data)
	next.PreviewEventIndex = eventIndex
	next.PreviewLocked = true
	return s.commit(next)
}

func (s *CollectionStore) PreviewLock() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.PreviewEventIndex, s.data.PreviewLocked
}

func (s *CollectionStore) LatestPreview() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.data.LatestPreview...)
}

func (s *CollectionStore) ConfirmInfinite(identity string, design *gamedata.InfiniteGachaDesign) (CollectionGrant, error) {
	if identity == "" || design == nil {
		return CollectionGrant{}, errors.New("player: invalid collection grant")
	}
	s.mu.Lock()
	preview := append([]uint64(nil), s.data.LatestPreview...)
	locked := s.data.PreviewLocked && s.data.PreviewEventIndex != 0
	s.mu.Unlock()
	if len(preview) != design.Count {
		return CollectionGrant{}, errors.New("player: no complete infinite gacha preview to confirm")
	}
	if !locked {
		return CollectionGrant{}, errors.New("player: infinite gacha preview is not locked")
	}
	return s.grantCostumes(identity, preview, design.Character, nil)
}

func (s *CollectionStore) GrantRegular(identity string, costumeIDs []uint64, design *gamedata.RegularGachaCatalog) (CollectionGrant, error) {
	if identity == "" || len(costumeIDs) == 0 || design == nil {
		return CollectionGrant{}, errors.New("player: invalid regular gacha grant")
	}
	return s.grantCostumes(identity, costumeIDs, design.Character, nil)
}

func (s *CollectionStore) GrantRegularPurchase(identity string, costumeIDs []uint64, design *gamedata.RegularGachaCatalog, purchase GachaPurchase) (CollectionGrant, error) {
	if identity == "" || len(costumeIDs) == 0 || design == nil || purchase.Group.ID == 0 {
		return CollectionGrant{}, errors.New("player: invalid regular gacha purchase")
	}
	return s.grantCostumes(identity, costumeIDs, design.Character, func(next *collectionSnapshot, grant *CollectionGrant) error {
		applyGachaPurchase(next, identity, costumeIDs, purchase, grant)
		if purchase.Group.GachaSubType == 3 {
			next.Grants[FirstGachaCompletedIdentity] = CollectionGrant{}
		}
		return nil
	})
}

// GrantEquipmentPurchase records only the durable gacha accounting. Equipment
// instances belong to EquipmentInventory (they have a different wire type),
// while points and fixed counters share GachaUserDBInfo/GachaFixedDBInfo with
// costume draws. The stored empty grant is the idempotency marker.
func (s *CollectionStore) GrantEquipmentPurchase(identity string, count uint64, purchase GachaPurchase) (CollectionGrant, error) {
	if identity == "" || count == 0 || purchase.Group.ID == 0 {
		return CollectionGrant{}, errors.New("player: invalid equipment gacha purchase")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.data.Grants[identity]; ok {
		return cloneGrant(grant), nil
	}
	next := cloneCollection(s.data)
	grant := CollectionGrant{}
	applyGachaPurchase(&next, identity, make([]uint64, count), purchase, &grant)
	next.Grants[identity] = cloneGrant(grant)
	if err := s.commit(next); err != nil {
		return CollectionGrant{}, err
	}
	return grant, nil
}

func (s *CollectionStore) grantCostumes(identity string, costumeIDs []uint64, character func(uint64) (gamedata.CharacterDesign, bool), mutate func(*collectionSnapshot, *CollectionGrant) error) (CollectionGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.data.Grants[identity]; ok {
		return cloneGrant(grant), nil
	}
	next := cloneCollection(s.data)
	grant := CollectionGrant{ViewCostumeIDs: append([]uint64(nil), costumeIDs...)}
	now := uint64(time.Now().UnixMilli())
	for sortIndex, costumeID := range costumeIDs {
		if costumeID == 0 {
			return CollectionGrant{}, errors.New("player: preview has invalid costume")
		}
		characterDesign, found := character(costumeID)
		if !found {
			return CollectionGrant{}, fmt.Errorf("player: costume %d has no character design", costumeID)
		}
		maxLevel := characterDesign.CostumeMaxLevel
		if maxLevel == 0 { // compatibility for small constructor-only unit fixtures
			maxLevel = 5
		}
		if position, found := findCostume(next.Costumes, costumeID); found {
			before := next.Costumes[position].Level
			if before < maxLevel {
				next.Costumes[position].Level++
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: next.Costumes[position].InvenIndex, CostumeID: costumeID, Before: before, After: next.Costumes[position].Level, SortID: uint64(sortIndex)})
			} else {
				exchange, err := costumeOverflowExchange(costumeID, next.Costumes[position].InvenIndex, uint64(sortIndex), characterDesign)
				if err != nil {
					return CollectionGrant{}, err
				}
				grant.Exchanges = append(grant.Exchanges, exchange)
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: exchange.InvenIndex, CostumeID: costumeID, Before: maxLevel, After: maxLevel, SortID: exchange.SortID})
			}
			continue
		}
		if base, found := findCostume(s.base, costumeID); found {
			key := strconv.FormatUint(s.base[base].InvenIndex, 10)
			before := next.BaseCostumeLevels[key]
			if before < s.base[base].Level {
				before = s.base[base].Level
			}
			if before < maxLevel {
				next.BaseCostumeLevels[key] = before + 1
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: s.base[base].InvenIndex, CostumeID: costumeID, Before: before, After: before + 1, SortID: uint64(sortIndex)})
			} else {
				exchange, err := costumeOverflowExchange(costumeID, s.base[base].InvenIndex, uint64(sortIndex), characterDesign)
				if err != nil {
					return CollectionGrant{}, err
				}
				grant.Exchanges = append(grant.Exchanges, exchange)
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: exchange.InvenIndex, CostumeID: costumeID, Before: maxLevel, After: maxLevel, SortID: exchange.SortID})
			}
			continue
		}
		characterIndex, characterExists := findCharacterByID(s.baseCharacters, next.Characters, characterDesign.ID)
		costumeIndex := next.NextCostumeIndex
		next.NextCostumeIndex++
		if !characterExists {
			characterIndex = next.NextCharacterIndex
			next.NextCharacterIndex++
			ownedCharacter := Character{InvenIndex: characterIndex, ID: characterDesign.ID, HP: characterDesign.HP, Level: 1,
				CostumeID: costumeID, UseCostume: costumeIndex, TalentLevel: 1, SolidarityReward: 1,
				ExpiryTime: ^uint64(0) - 32399999, ConnectPotentialCostume: costumeID}
			next.Characters = append(next.Characters, ownedCharacter)
			grant.CharacterIndices = append(grant.CharacterIndices, characterIndex)
		}
		costume := Costume{InvenIndex: costumeIndex, ID: costumeID, UseChar: characterIndex, SortID: uint64(sortIndex), TimeValue: now}
		next.Costumes = append(next.Costumes, costume)
		grant.CostumeIndices = append(grant.CostumeIndices, costumeIndex)
	}
	if mutate != nil {
		if err := mutate(&next, &grant); err != nil {
			return CollectionGrant{}, err
		}
	}
	next.Grants[identity] = cloneGrant(grant)
	if err := s.commit(next); err != nil {
		return CollectionGrant{}, err
	}
	return grant, nil
}

func costumeOverflowExchange(costumeID, invenIndex, sortID uint64, design gamedata.CharacterDesign) (CostumeExchange, error) {
	if design.OverflowItemType == 0 || design.OverflowItemCount == 0 {
		return CostumeExchange{}, fmt.Errorf("player: costume %d has no overflow exchange design", costumeID)
	}
	return CostumeExchange{
		InvenIndex:       invenIndex,
		OriginalItemType: 11, OriginalItemID: costumeID, OriginalCount: 1,
		ExchangeItemType: design.OverflowItemType, ExchangeItemID: design.OverflowItemID,
		ExchangeCount: design.OverflowItemCount, SortID: sortID,
	}, nil
}

// RepairCostumeOverflow migrates grants written by the pre-cap implementation.
// It rewrites every illegal +6/+7 upgrade into the same GameData-driven
// post-max exchange a fresh request would have produced, then caps persisted
// CostumeDBInfo levels. Returning all exchange-bearing grants lets the wallet
// idempotently recover currency even after a crash between the two commits.
func (s *CollectionStore) RepairCostumeOverflow(character func(uint64) (gamedata.CharacterDesign, bool)) (map[string][]CostumeExchange, error) {
	if character == nil {
		return nil, errors.New("player: nil costume design resolver")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	changed := false
	for i := range next.Costumes {
		design, ok := character(next.Costumes[i].ID)
		if !ok || design.CostumeMaxLevel == 0 {
			return nil, fmt.Errorf("player: costume %d has no max-level design", next.Costumes[i].ID)
		}
		if next.Costumes[i].Level > design.CostumeMaxLevel {
			next.Costumes[i].Level = design.CostumeMaxLevel
			changed = true
		}
	}
	for i := range s.base {
		key := strconv.FormatUint(s.base[i].InvenIndex, 10)
		level := next.BaseCostumeLevels[key]
		if level == 0 {
			level = s.base[i].Level
		}
		design, ok := character(s.base[i].ID)
		if !ok {
			continue // starter costumes outside the active gacha pools are untouched
		}
		if design.CostumeMaxLevel == 0 {
			return nil, fmt.Errorf("player: costume %d has no max-level design", s.base[i].ID)
		}
		if level > design.CostumeMaxLevel {
			next.BaseCostumeLevels[key] = design.CostumeMaxLevel
			changed = true
		}
	}
	for identity, grant := range next.Grants {
		kept := make([]CostumeUpgrade, 0, len(grant.Upgrades))
		for _, upgrade := range grant.Upgrades {
			design, ok := character(upgrade.CostumeID)
			if !ok || design.CostumeMaxLevel == 0 {
				return nil, fmt.Errorf("player: costume %d has no overflow repair design", upgrade.CostumeID)
			}
			if upgrade.After > design.CostumeMaxLevel {
				exchange, err := costumeOverflowExchange(upgrade.CostumeID, upgrade.InvenIndex, upgrade.SortID, design)
				if err != nil {
					return nil, err
				}
				grant.Exchanges = append(grant.Exchanges, exchange)
				changed = true
				continue
			}
			kept = append(kept, upgrade)
		}
		grant.Upgrades = kept
		for i := range grant.Exchanges {
			exchange := &grant.Exchanges[i]
			if exchange.InvenIndex == 0 {
				if position, found := findCostume(next.Costumes, exchange.OriginalItemID); found {
					exchange.InvenIndex = next.Costumes[position].InvenIndex
				} else if position, found := findCostume(s.base, exchange.OriginalItemID); found {
					exchange.InvenIndex = s.base[position].InvenIndex
				} else {
					return nil, fmt.Errorf("player: overflow costume %d has no inventory instance", exchange.OriginalItemID)
				}
				changed = true
			}
			found := false
			for _, upgrade := range grant.Upgrades {
				if upgrade.CostumeID == exchange.OriginalItemID && upgrade.SortID == exchange.SortID {
					found = true
					break
				}
			}
			if !found {
				design, _ := character(exchange.OriginalItemID)
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: exchange.InvenIndex, CostumeID: exchange.OriginalItemID, Before: design.CostumeMaxLevel, After: design.CostumeMaxLevel, SortID: exchange.SortID})
				changed = true
			}
		}
		next.Grants[identity] = grant
	}
	if changed {
		if err := s.commit(next); err != nil {
			return nil, err
		}
	}
	result := make(map[string][]CostumeExchange)
	for identity, grant := range next.Grants {
		if len(grant.Exchanges) != 0 {
			result[identity] = append([]CostumeExchange(nil), grant.Exchanges...)
		}
	}
	return result, nil
}

func (s *CollectionStore) Characters() []Character {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Character(nil), s.data.Characters...)
}

func (s *CollectionStore) FindCharacter(index uint64) (Character, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Characters {
		if c.InvenIndex == index {
			return c, true
		}
	}
	return Character{}, false
}

// CanUpdateCharacter validates a growth mutation before any currency or
// inventory is charged. Promotion changes character.ID but not InvenIndex.
func (s *CollectionStore) CanUpdateCharacter(oldID uint64, character Character) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateCharacterUpdate(oldID, character)
}

func (s *CollectionStore) validateCharacterUpdate(oldID uint64, character Character) error {
	if oldID == 0 || character.ID == 0 || character.InvenIndex == 0 {
		return errors.New("player: invalid collection character update")
	}
	found := false
	for _, existing := range s.data.Characters {
		if existing.InvenIndex == character.InvenIndex {
			if existing.ID != oldID {
				return fmt.Errorf("player: collection character %d changed before growth", character.InvenIndex)
			}
			found = true
		} else if existing.ID == character.ID {
			return fmt.Errorf("player: duplicate collection character design %d", character.ID)
		}
	}
	if !found {
		return fmt.Errorf("player: collection character %d not found", character.InvenIndex)
	}
	for _, existing := range s.baseCharacters {
		if existing.ID == character.ID {
			return fmt.Errorf("player: duplicate base character design %d", character.ID)
		}
	}
	return nil
}

func (s *CollectionStore) UpdateCharacter(oldID uint64, character Character) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateCharacterUpdate(oldID, character); err != nil {
		return err
	}
	next := cloneCollection(s.data)
	for i := range next.Characters {
		if next.Characters[i].InvenIndex == character.InvenIndex {
			next.Characters[i] = character
			return s.commit(next)
		}
	}
	return fmt.Errorf("player: collection character %d not found", character.InvenIndex)
}

func (s *CollectionStore) Costumes() []Costume {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]Costume(nil), s.base...)
	for i := range result {
		if level := s.data.BaseCostumeLevels[strconv.FormatUint(result[i].InvenIndex, 10)]; level > result[i].Level {
			result[i].Level = level
		}
		result[i].PotentialIDs = append([]uint64(nil), s.data.CostumePotential[strconv.FormatUint(result[i].InvenIndex, 10)]...)
	}
	collection := append([]Costume(nil), s.data.Costumes...)
	for i := range collection {
		collection[i].PotentialIDs = append([]uint64(nil), s.data.CostumePotential[strconv.FormatUint(collection[i].InvenIndex, 10)]...)
	}
	return append(result, collection...)
}

func (s *CollectionStore) ValidateCostumePotentialActivation(costumeIndex uint64, nodes []uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateCostumePotentialActivation(costumeIndex, nodes)
}

func (s *CollectionStore) validateCostumePotentialActivation(costumeIndex uint64, nodes []uint64) error {
	if costumeIndex == 0 || len(nodes) == 0 {
		return errors.New("player: invalid costume potential activation")
	}
	found := false
	for _, costume := range s.base {
		found = found || costume.InvenIndex == costumeIndex
	}
	for _, costume := range s.data.Costumes {
		found = found || costume.InvenIndex == costumeIndex
	}
	if !found {
		return fmt.Errorf("player: costume %d not found", costumeIndex)
	}
	key := strconv.FormatUint(costumeIndex, 10)
	active := make(map[uint64]bool, len(s.data.CostumePotential[key])+len(nodes))
	for _, id := range s.data.CostumePotential[key] {
		if id == 0 || active[id] {
			return fmt.Errorf("player: invalid saved potential node for costume %d", costumeIndex)
		}
		active[id] = true
	}
	for _, id := range nodes {
		if id == 0 || active[id] {
			return fmt.Errorf("player: duplicate potential node %d for costume %d", id, costumeIndex)
		}
		active[id] = true
	}
	return nil
}

func (s *CollectionStore) ActivateCostumePotential(costumeIndex uint64, nodes []uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateCostumePotentialActivation(costumeIndex, nodes); err != nil {
		return err
	}
	next := cloneCollection(s.data)
	key := strconv.FormatUint(costumeIndex, 10)
	next.CostumePotential[key] = append(next.CostumePotential[key], nodes...)
	sort.Slice(next.CostumePotential[key], func(i, j int) bool { return next.CostumePotential[key][i] < next.CostumePotential[key][j] })
	return s.commit(next)
}

func (s *CollectionStore) Grant(identity string) (CollectionGrant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.data.Grants[identity]
	return cloneGrant(grant), ok
}

func (s *CollectionStore) FirstGachaCompleted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, completed := s.data.Grants[FirstGachaCompletedIdentity]
	return completed
}

func (s *CollectionStore) CharacterByIndex(index uint64) (Character, bool) {
	for _, character := range s.Characters() {
		if character.InvenIndex == index {
			return character, true
		}
	}
	return Character{}, false
}

func (s *CollectionStore) CostumeByIndex(index uint64) (Costume, bool) {
	for _, costume := range s.Costumes() {
		if costume.InvenIndex == index {
			return costume, true
		}
	}
	return Costume{}, false
}

func findCostume(costumes []Costume, id uint64) (int, bool) {
	for i, costume := range costumes {
		if costume.ID == id {
			return i, true
		}
	}
	return -1, false
}

func findCharacterByID(base, collection []Character, id uint64) (uint64, bool) {
	for _, character := range base {
		if character.ID == id {
			return character.InvenIndex, true
		}
	}
	for _, character := range collection {
		if character.ID == id {
			return character.InvenIndex, true
		}
	}
	return 0, false
}

func cloneGrant(grant CollectionGrant) CollectionGrant {
	grant.CharacterIndices = append([]uint64(nil), grant.CharacterIndices...)
	grant.CostumeIndices = append([]uint64(nil), grant.CostumeIndices...)
	grant.Upgrades = append([]CostumeUpgrade(nil), grant.Upgrades...)
	grant.Exchanges = append([]CostumeExchange(nil), grant.Exchanges...)
	grant.ViewCostumeIDs = append([]uint64(nil), grant.ViewCostumeIDs...)
	grant.GachaFixed = append([]GachaFixedState(nil), grant.GachaFixed...)
	grant.SelectionApplySortIDs = append([]uint64(nil), grant.SelectionApplySortIDs...)
	return grant
}

func cloneCollection(in collectionSnapshot) collectionSnapshot {
	out := in
	out.LatestPreview = append([]uint64(nil), in.LatestPreview...)
	out.Characters = append([]Character(nil), in.Characters...)
	out.Costumes = append([]Costume(nil), in.Costumes...)
	out.BaseCostumeLevels = make(map[string]uint64, len(in.BaseCostumeLevels))
	for k, v := range in.BaseCostumeLevels {
		out.BaseCostumeLevels[k] = v
	}
	out.CostumePotential = make(map[string][]uint64, len(in.CostumePotential))
	for k, v := range in.CostumePotential {
		out.CostumePotential[k] = append([]uint64(nil), v...)
	}
	out.GachaSelections = make(map[string][]GachaSelection, len(in.GachaSelections))
	for k, v := range in.GachaSelections {
		out.GachaSelections[k] = append([]GachaSelection(nil), v...)
	}
	out.StepUpProgress = make(map[string]uint64, len(in.StepUpProgress))
	for k, v := range in.StepUpProgress {
		out.StepUpProgress[k] = v
	}
	out.GachaUsers = make(map[string]GachaUserState, len(in.GachaUsers))
	for k, v := range in.GachaUsers {
		out.GachaUsers[k] = v
	}
	out.GachaFixed = make(map[string]GachaFixedState, len(in.GachaFixed))
	for k, v := range in.GachaFixed {
		out.GachaFixed[k] = v
	}
	out.GachaApplied = make(map[string]bool, len(in.GachaApplied))
	for k, v := range in.GachaApplied {
		out.GachaApplied[k] = v
	}
	out.GachaPointExchange = make(map[string]GachaPointExchange, len(in.GachaPointExchange))
	for k, v := range in.GachaPointExchange {
		out.GachaPointExchange[k] = v
	}
	out.Grants = make(map[string]CollectionGrant, len(in.Grants))
	for k, v := range in.Grants {
		out.Grants[k] = cloneGrant(v)
	}
	return out
}

func (s *CollectionStore) commit(next collectionSnapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".collection-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("player: persist collection: %w", err)
	}
	s.data = next
	return nil
}
