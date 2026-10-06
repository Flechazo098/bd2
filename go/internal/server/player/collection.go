package player

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
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
	ViewCostumeSortIDs    []uint64          `json:"view_costume_sort_ids,omitempty"`
	GachaGroupID          uint64            `json:"gacha_group_id,omitempty"`
	GachaPoint            uint64            `json:"gacha_point,omitempty"`
	GachaFixed            []GachaFixedState `json:"gacha_fixed,omitempty"`
	SelectionApplySortIDs []uint64          `json:"selection_apply_sort_ids,omitempty"`
	GachaRequestDigest    string            `json:"gacha_request_digest,omitempty"`
	GachaResponse         []byte            `json:"gacha_response,omitempty"`
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
	DailyKey              string
	DailyLimit            uint64
	CompletionGrant       string
	Fixed                 []GachaFixedState
	SelectionApplySortIDs []uint64
	StepUpGroupID         uint64
	StepUpStep            uint64
	RewardCount           uint64
	// RewardSortIDs preserves positions from mixed reward programs. Ordinary
	// costume-only gachas leave it empty and use their slice positions.
	RewardSortIDs []uint64
}

type GachaSelection struct {
	GroupID uint64 `json:"group_id"`
	Slot    uint64 `json:"slot"`
	ItemID  uint64 `json:"item_id"`
}

type GachaSelectionChangeCount struct {
	GroupID uint64 `json:"group_id"`
	Count   uint64 `json:"count"`
}

// CharAwakeProgress is account-wide character state keyed by CharTable's
// UniqueCharId. Promotion changes CharTable.Id, but never this identity.
type CharAwakeProgress struct {
	ImprintLevels [3]uint64 `json:"imprint_levels"`
	IsAwake       bool      `json:"is_awake"`
}

// CostumeBurstUpgradeRecord is the durable result of one costume burst level
// transition. Digest identifies the exact protobuf request whose successful
// response is stored in Body, allowing a committed request to be replayed
// after a process restart without charging its materials again.
type CostumeBurstUpgradeRecord struct {
	CostumeID uint64 `json:"costume_id"`
	Level     uint64 `json:"level"`
	Digest    string `json:"digest"`
	Code      int    `json:"code"`
	Body      []byte `json:"body"`
}

// FirstGachaCompletedIdentity is the persisted account flag represented in
// the existing collection grant ledger. It is written atomically with the
// official GachaSubType=3 first-pick transaction.
const FirstGachaCompletedIdentity = "account:first-gacha-completed"

type collectionSnapshot struct {
	Version               string                               `json:"version"`
	NextCharacterIndex    uint64                               `json:"next_character_index"`
	NextCostumeIndex      uint64                               `json:"next_costume_index"`
	LatestPreview         []uint64                             `json:"latest_preview"`
	PreviewEventIndex     uint64                               `json:"preview_event_index"`
	PreviewLocked         bool                                 `json:"preview_locked"`
	Characters            []Character                          `json:"characters,omitempty"`
	Costumes              []Costume                            `json:"costumes,omitempty"`
	BaseCostumeLevels     map[string]uint64                    `json:"base_costume_levels"`
	CostumePotential      map[string][]uint64                  `json:"costume_potential,omitempty"`
	CostumeBurstLevels    map[string]uint64                    `json:"-"`
	CostumeBurstUpgrades  map[string]CostumeBurstUpgradeRecord `json:"-"`
	CharAwake             map[string]CharAwakeProgress         `json:"char_awake,omitempty"`
	Friendships           map[string]FriendshipEntry           `json:"-"`
	GachaSelections       map[string][]GachaSelection          `json:"gacha_selections,omitempty"`
	GachaSelectionChanges map[string]uint64                    `json:"gacha_selection_changes,omitempty"`
	StepUpProgress        map[string]uint64                    `json:"step_up_progress,omitempty"`
	GachaUsers            map[string]GachaUserState            `json:"gacha_users,omitempty"`
	GachaFixed            map[string]GachaFixedState           `json:"gacha_fixed,omitempty"`
	GachaApplied          map[string]bool                      `json:"gacha_applied,omitempty"`
	GachaPointExchange    map[string]GachaPointExchange        `json:"gacha_point_exchanges,omitempty"`
	GachaCountCorrected   bool                                 `json:"gacha_count_corrected"`
	Grants                map[string]CollectionGrant           `json:"grants,omitempty"`
}

// CollectionStore owns non-stackable character/costume rewards as one atomic
// save. It is shared by gacha, CharInfo, CostumeInfo and CharGrowth so a roll
// cannot exist only in the result animation and disappear after relogging.
type CollectionStore struct {
	mu             sync.Mutex
	store          stateio.AtomicEntryStore
	base           []Costume
	baseCharacters []Character
	data           collectionSnapshot
	persisted      bool
}

func OpenCollectionStore(store stateio.Store, base []Costume) (*CollectionStore, error) {
	if store == nil {
		return nil, errors.New("player: nil collection store")
	}
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok {
		return nil, errors.New("player: collection store requires atomic entry storage")
	}
	s := &CollectionStore{store: entries, base: append([]Costume(nil), base...), data: collectionSnapshot{
		Version: versionconfig.State(), NextCharacterIndex: 920000001, NextCostumeIndex: 930000001,
		BaseCostumeLevels: map[string]uint64{}, GachaSelections: map[string][]GachaSelection{}, GachaSelectionChanges: map[string]uint64{},
		CostumePotential:   map[string][]uint64{},
		CostumeBurstLevels: map[string]uint64{}, CostumeBurstUpgrades: map[string]CostumeBurstUpgradeRecord{},
		CharAwake:      map[string]CharAwakeProgress{},
		Friendships:    map[string]FriendshipEntry{},
		StepUpProgress: map[string]uint64{}, GachaUsers: map[string]GachaUserState{}, GachaFixed: map[string]GachaFixedState{},
		GachaApplied: map[string]bool{}, GachaPointExchange: map[string]GachaPointExchange{}, Grants: map[string]CollectionGrant{},
	}}
	b, err := store.Load("collection")
	if err != nil {
		return nil, err
	}
	if b == nil {
		if err := stateio.RequireNoEntries(entries, collectionDomain, collectionEntryBuckets[:]...); err != nil {
			return nil, fmt.Errorf("player: invalid collection storage: %w", err)
		}
		return s, nil
	}
	if err := rejectInlineCollectionEntries(b); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("player: decode collection: %w", err)
	}
	if err := loadCollectionEntries(entries, &s.data); err != nil {
		return nil, err
	}
	if s.data.Version != versionconfig.State() || s.data.NextCharacterIndex < 920000001 || s.data.NextCostumeIndex < 930000001 {
		return nil, errors.New("player: invalid collection save")
	}
	if s.data.BaseCostumeLevels == nil {
		s.data.BaseCostumeLevels = map[string]uint64{}
	}
	if s.data.CostumePotential == nil {
		return nil, errors.New("player: collection save requires costume_potential; migrate the development save")
	}
	if s.data.CharAwake == nil {
		return nil, errors.New("player: collection save requires char_awake; migrate the development save")
	}
	if err := s.validateCostumeBurstStorage(); err != nil {
		return nil, err
	}
	if err := validateFriendshipEntries(s.data.Friendships); err != nil {
		return nil, err
	}
	for key, progress := range s.data.CharAwake {
		uniqueID, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || uniqueID == 0 || (progress.ImprintLevels == [3]uint64{} && !progress.IsAwake) {
			return nil, errors.New("player: invalid char_awake ledger")
		}
	}
	if s.data.GachaSelections == nil {
		s.data.GachaSelections = map[string][]GachaSelection{}
	}
	if s.data.GachaSelectionChanges == nil {
		s.data.GachaSelectionChanges = map[string]uint64{}
	}
	for key, count := range s.data.GachaSelectionChanges {
		groupID, parseErr := strconv.ParseUint(key, 10, 64)
		if parseErr != nil || groupID == 0 || key != strconv.FormatUint(groupID, 10) || count == 0 {
			return nil, errors.New("player: invalid gacha selection change ledger")
		}
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
	s.persisted = true
	return s, nil
}

func (s *CollectionStore) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.store.Load("collection")
	if err != nil {
		return err
	}
	if b != nil {
		return nil
	}
	return s.commit(cloneCollection(s.data))
}

func emptyCollectionGrant(grant CollectionGrant) bool {
	return len(grant.CharacterIndices) == 0 && len(grant.CostumeIndices) == 0 &&
		len(grant.Upgrades) == 0 && len(grant.Exchanges) == 0 &&
		len(grant.ViewCostumeIDs) == 0 && len(grant.ViewCostumeSortIDs) == 0 && grant.GachaGroupID == 0 &&
		grant.GachaPoint == 0 && len(grant.GachaFixed) == 0 &&
		len(grant.SelectionApplySortIDs) == 0 && grant.GachaRequestDigest == "" && len(grant.GachaResponse) == 0
}

// BindBaseCharacters attaches the authoritative base roster and rejects a
// persisted collection that overlaps it. It never rewrites account state.
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

func (s *CollectionStore) SaveGachaSelections(groupID uint64, selections []GachaSelection, changeLimit uint64) error {
	if groupID == 0 || len(selections) == 0 {
		return errors.New("player: invalid gacha selection")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatUint(groupID, 10)
	normalized := append([]GachaSelection(nil), selections...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Slot < normalized[j].Slot })
	if equalGachaSelections(s.data.GachaSelections[key], normalized) {
		return nil
	}
	if changeLimit != 0 && s.data.GachaSelectionChanges[key] >= changeLimit {
		return fmt.Errorf("player: gacha selection group %d exhausted %d changes", groupID, changeLimit)
	}
	next := cloneCollection(s.data)
	next.GachaSelections[key] = normalized
	if changeLimit != 0 {
		next.GachaSelectionChanges[key]++
	}
	return s.commit(next)
}

func equalGachaSelections(a, b []GachaSelection) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *CollectionStore) GachaSelections(groupID uint64) []GachaSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GachaSelection(nil), s.data.GachaSelections[strconv.FormatUint(groupID, 10)]...)
}

func (s *CollectionStore) AllGachaSelections() []GachaSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []GachaSelection
	for _, selections := range s.data.GachaSelections {
		result = append(result, selections...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].GroupID != result[j].GroupID {
			return result[i].GroupID < result[j].GroupID
		}
		return result[i].Slot < result[j].Slot
	})
	return result
}

func (s *CollectionStore) GachaSelectionChangeCounts() []GachaSelectionChangeCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]GachaSelectionChangeCount, 0, len(s.data.GachaSelectionChanges))
	for key, count := range s.data.GachaSelectionChanges {
		groupID, err := strconv.ParseUint(key, 10, 64)
		if err == nil && groupID != 0 && count != 0 {
			result = append(result, GachaSelectionChangeCount{GroupID: groupID, Count: count})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}

func (s *CollectionStore) SetPreview(eventIndex uint64, costumeIDs []uint64) error {
	if eventIndex == 0 || len(costumeIDs) == 0 {
		return errors.New("player: infinite preview requires an event index and costumes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	next.LatestPreview = append([]uint64(nil), costumeIDs...)
	next.PreviewEventIndex = eventIndex
	next.PreviewLocked = false
	return s.commit(next)
}

// LockPreview records the official Resemara confirmation boundary. The event
// index is created and persisted with the preview; locking only changes the
// confirmation bit, so an unlocked preview remains representable in GachaInfo.
func (s *CollectionStore) LockPreview(eventIndex uint64) error {
	if eventIndex == 0 {
		return errors.New("player: invalid infinite preview event index")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.LatestPreview) == 0 || s.data.PreviewEventIndex == 0 {
		return errors.New("player: no complete infinite gacha preview to lock")
	}
	if eventIndex != s.data.PreviewEventIndex {
		return fmt.Errorf("player: stale infinite preview event index %d", eventIndex)
	}
	next := cloneCollection(s.data)
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
	return s.GrantCostumes(identity, costumeIDs, design)
}

type CostumeDesignSource interface {
	Character(costumeID uint64) (gamedata.CharacterDesign, bool)
}

// GrantCostumes grants a deterministic costume-copy sequence from any
// GameData-backed reward catalog. It is shared by gacha and new-player mail;
// the identity makes retry return the original characters, costumes and
// upgrade records without applying the copies twice.
func (s *CollectionStore) GrantCostumes(identity string, costumeIDs []uint64, design CostumeDesignSource) (CollectionGrant, error) {
	if identity == "" || len(costumeIDs) == 0 || design == nil {
		return CollectionGrant{}, errors.New("player: invalid costume reward grant")
	}
	return s.grantCostumes(identity, costumeIDs, design.Character, nil)
}

// RecordGrantMarker persists an account-level completion/purchase fact that
// has no character or costume payload. It is intentionally separate from an
// inventory reward marker so purchase-count protocol state remains correct
// even after the granted consumable has been used.
func (s *CollectionStore) RecordGrantMarker(identity string) error {
	if identity == "" {
		return errors.New("player: invalid collection marker")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Grants[identity]; exists {
		return nil
	}
	next := cloneCollection(s.data)
	next.Grants[identity] = CollectionGrant{}
	return s.commit(next)
}

// GrantGachaPointCostume debits one pickup exchange cost and grants the
// selected costume in the same collection commit. The grant identity makes a
// transport retry return the original result without spending points or
// incrementing ExchangeItemCount twice.
func (s *CollectionStore) GrantGachaPointCostume(identity string, group gamedata.GachaGroupDesign, costumeID uint64, design *gamedata.RegularGachaCatalog) (CollectionGrant, error) {
	if identity == "" || group.ID == 0 || group.PickUpExchangeCost == 0 || costumeID == 0 || design == nil {
		return CollectionGrant{}, errors.New("player: invalid gacha point costume exchange")
	}
	return s.grantCostumes(identity, []uint64{costumeID}, design.Character, func(next *collectionSnapshot, _ *CollectionGrant) error {
		key := strconv.FormatUint(group.ID, 10)
		user, ok := next.GachaUsers[key]
		if !ok || user.Point < group.PickUpExchangeCost {
			return errors.New("player: insufficient gacha point")
		}
		if user.ExchangeItemCount == ^uint64(0) {
			return errors.New("player: gacha item exchange count overflow")
		}
		user.GroupID = group.ID
		user.Point -= group.PickUpExchangeCost
		user.ExchangeItemCount++
		next.GachaUsers[key] = user
		next.GachaPointExchange[identity] = GachaPointExchange{GroupID: group.ID, Count: group.PickUpExchangeCost}
		return nil
	})
}

// GrantGachaPointEquipment records an equipment pickup exchange in the same
// collection ledger used by costume pickup exchanges. The equipment instance
// itself belongs to EquipmentInventory; the request transaction makes both
// domain writes atomic.
func (s *CollectionStore) GrantGachaPointEquipment(identity string, groupID, exchangeCost uint64) (CollectionGrant, error) {
	if identity == "" || groupID == 0 || exchangeCost == 0 {
		return CollectionGrant{}, errors.New("player: invalid gacha point equipment exchange")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.data.Grants[identity]; ok {
		return cloneGrant(grant), nil
	}
	next := cloneCollection(s.data)
	key := strconv.FormatUint(groupID, 10)
	user, ok := next.GachaUsers[key]
	if !ok || user.Point < exchangeCost {
		return CollectionGrant{}, errors.New("player: insufficient gacha point")
	}
	if user.ExchangeItemCount == ^uint64(0) {
		return CollectionGrant{}, errors.New("player: gacha item exchange count overflow")
	}
	user.GroupID = groupID
	user.Point -= exchangeCost
	user.ExchangeItemCount++
	next.GachaUsers[key] = user
	next.GachaPointExchange[identity] = GachaPointExchange{GroupID: groupID, Count: exchangeCost}
	grant := CollectionGrant{}
	next.Grants[identity] = grant
	if err := s.commit(next); err != nil {
		return CollectionGrant{}, err
	}
	return grant, nil
}

func (s *CollectionStore) GrantRegularPurchase(identity string, costumeIDs []uint64, design *gamedata.RegularGachaCatalog, purchase GachaPurchase) (CollectionGrant, error) {
	if identity == "" || len(costumeIDs) == 0 || design == nil || purchase.Group.ID == 0 {
		return CollectionGrant{}, errors.New("player: invalid regular gacha purchase")
	}
	return s.grantCostumesSorted(identity, costumeIDs, purchase.RewardSortIDs, design.Character, func(next *collectionSnapshot, grant *CollectionGrant) error {
		if err := applyStepUpProgress(next, purchase.StepUpGroupID, purchase.StepUpStep); err != nil {
			return err
		}
		if err := applyGachaPurchase(next, identity, costumeIDs, purchase, grant); err != nil {
			return err
		}
		if purchase.CompletionGrant != "" {
			if _, exists := next.Grants[purchase.CompletionGrant]; exists {
				return fmt.Errorf("player: gacha completion grant %q already exists", purchase.CompletionGrant)
			}
			next.Grants[purchase.CompletionGrant] = CollectionGrant{}
		}
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
	if err := applyGachaPurchase(&next, identity, make([]uint64, count), purchase, &grant); err != nil {
		return CollectionGrant{}, err
	}
	next.Grants[identity] = cloneGrant(grant)
	if err := s.commit(next); err != nil {
		return CollectionGrant{}, err
	}
	return grant, nil
}

// GrantEquipmentDraw records a standalone ticket draw without inventing a
// schedule group, points, purchase counts, or fixed-pity state.
func (s *CollectionStore) GrantEquipmentDraw(identity string) (CollectionGrant, error) {
	if identity == "" {
		return CollectionGrant{}, errors.New("player: invalid equipment draw identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.data.Grants[identity]; ok {
		return cloneGrant(grant), nil
	}
	next := cloneCollection(s.data)
	grant := CollectionGrant{}
	next.Grants[identity] = grant
	if err := s.commit(next); err != nil {
		return CollectionGrant{}, err
	}
	return grant, nil
}

func (s *CollectionStore) grantCostumes(identity string, costumeIDs []uint64, character func(uint64) (gamedata.CharacterDesign, bool), mutate func(*collectionSnapshot, *CollectionGrant) error) (CollectionGrant, error) {
	return s.grantCostumesSorted(identity, costumeIDs, nil, character, mutate)
}

func (s *CollectionStore) grantCostumesSorted(identity string, costumeIDs, sortIDs []uint64, character func(uint64) (gamedata.CharacterDesign, bool), mutate func(*collectionSnapshot, *CollectionGrant) error) (CollectionGrant, error) {
	if len(sortIDs) != 0 && len(sortIDs) != len(costumeIDs) {
		return CollectionGrant{}, errors.New("player: costume reward sort count mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if grant, ok := s.data.Grants[identity]; ok {
		return cloneGrant(grant), nil
	}
	next := cloneCollection(s.data)
	grant := CollectionGrant{ViewCostumeIDs: append([]uint64(nil), costumeIDs...), ViewCostumeSortIDs: append([]uint64(nil), sortIDs...)}
	now := uint64(time.Now().UnixMilli())
	for sortIndex, costumeID := range costumeIDs {
		sortID := uint64(sortIndex)
		if len(sortIDs) != 0 {
			sortID = sortIDs[sortIndex]
		}
		if costumeID == 0 {
			return CollectionGrant{}, errors.New("player: preview has invalid costume")
		}
		characterDesign, found := character(costumeID)
		if !found {
			return CollectionGrant{}, fmt.Errorf("player: costume %d has no character design", costumeID)
		}
		maxLevel := characterDesign.CostumeMaxLevel
		if maxLevel == 0 {
			return CollectionGrant{}, fmt.Errorf("player: costume %d has zero max level", costumeID)
		}
		if position, found := findCostume(next.Costumes, costumeID); found {
			before := next.Costumes[position].Level
			if before < maxLevel {
				next.Costumes[position].Level++
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: next.Costumes[position].InvenIndex, CostumeID: costumeID, Before: before, After: next.Costumes[position].Level, SortID: sortID})
			} else {
				exchange, err := costumeOverflowExchange(costumeID, next.Costumes[position].InvenIndex, sortID, characterDesign)
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
			before := max(next.BaseCostumeLevels[key], s.base[base].Level)
			if before < maxLevel {
				next.BaseCostumeLevels[key] = before + 1
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: s.base[base].InvenIndex, CostumeID: costumeID, Before: before, After: before + 1, SortID: sortID})
			} else {
				exchange, err := costumeOverflowExchange(costumeID, s.base[base].InvenIndex, sortID, characterDesign)
				if err != nil {
					return CollectionGrant{}, err
				}
				grant.Exchanges = append(grant.Exchanges, exchange)
				grant.Upgrades = append(grant.Upgrades, CostumeUpgrade{InvenIndex: exchange.InvenIndex, CostumeID: costumeID, Before: maxLevel, After: maxLevel, SortID: exchange.SortID})
			}
			continue
		}
		characterIndex, characterExists, err := findCharacterByDesign(s.baseCharacters, next.Characters, characterDesign)
		if err != nil {
			return CollectionGrant{}, err
		}
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
		costume := Costume{InvenIndex: costumeIndex, ID: costumeID, UseChar: characterIndex, SortID: sortID, TimeValue: now}
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
	return s.costumesLocked()
}

func (s *CollectionStore) costumesLocked() []Costume {
	result := append([]Costume(nil), s.base...)
	for i := range result {
		key := strconv.FormatUint(result[i].InvenIndex, 10)
		if level := s.data.BaseCostumeLevels[key]; level > result[i].Level {
			result[i].Level = level
		}
		if level, found := s.data.CostumeBurstLevels[key]; found {
			result[i].BurstLevel = level
		}
		result[i].PotentialIDs = append([]uint64(nil), s.data.CostumePotential[key]...)
	}
	collection := append([]Costume(nil), s.data.Costumes...)
	for i := range collection {
		key := strconv.FormatUint(collection[i].InvenIndex, 10)
		if level, found := s.data.CostumeBurstLevels[key]; found {
			collection[i].BurstLevel = level
		}
		collection[i].PotentialIDs = append([]uint64(nil), s.data.CostumePotential[key]...)
	}
	return append(result, collection...)
}

// CostumeByID finds the single owned costume with the logical CostumeTable ID.
// A duplicate logical ID is invalid ownership state and is deliberately not
// resolved by choosing an arbitrary inventory instance.
func (s *CollectionStore) CostumeByID(id uint64) (Costume, bool) {
	if id == 0 {
		return Costume{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Costume
	found := false
	for _, costume := range s.costumesLocked() {
		if costume.ID != id {
			continue
		}
		if found {
			return Costume{}, false
		}
		result, found = costume, true
	}
	return result, found
}

// CostumeBurstReplay returns an owned copy of the durable response recorded
// for one inventory instance and target burst level.
func (s *CollectionStore) CostumeBurstReplay(invenIndex, target uint64) (CostumeBurstUpgradeRecord, bool) {
	if invenIndex == 0 || target == 0 {
		return CostumeBurstUpgradeRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.data.CostumeBurstUpgrades[costumeBurstUpgradeKey(invenIndex, target)]
	record.Body = append([]byte(nil), record.Body...)
	return record, found
}

// ApplyCostumeBurst atomically advances one owned costume and stores the exact
// successful reply. expectedCurrent is a compare-and-swap guard against a
// stale request overwriting a newer level.
func (s *CollectionStore) ApplyCostumeBurst(invenIndex, expectedCurrent, target uint64, record CostumeBurstUpgradeRecord) error {
	if invenIndex == 0 || expectedCurrent == math.MaxUint64 || target != expectedCurrent+1 {
		return errors.New("player: invalid costume burst transition")
	}
	if record.Level != target || record.CostumeID == 0 {
		return errors.New("player: inconsistent costume burst record")
	}
	if err := validateCostumeBurstRecord(record); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	costume, found := s.costumeByIndexLocked(invenIndex)
	if !found {
		return fmt.Errorf("player: costume %d not found", invenIndex)
	}
	if costume.ID != record.CostumeID {
		return errors.New("player: costume burst record design mismatch")
	}
	if costume.BurstLevel != expectedCurrent {
		return errors.New("player: stale costume burst level")
	}
	ledgerKey := costumeBurstUpgradeKey(invenIndex, target)
	if _, exists := s.data.CostumeBurstUpgrades[ledgerKey]; exists {
		return errors.New("player: costume burst transition already recorded")
	}
	next := cloneCollection(s.data)
	next.CostumeBurstLevels[strconv.FormatUint(invenIndex, 10)] = target
	record.Body = append([]byte(nil), record.Body...)
	next.CostumeBurstUpgrades[ledgerKey] = record
	return s.commit(next)
}

func (s *CollectionStore) costumeByIndexLocked(index uint64) (Costume, bool) {
	for _, costume := range s.costumesLocked() {
		if costume.InvenIndex == index {
			return costume, true
		}
	}
	return Costume{}, false
}

func costumeBurstUpgradeKey(invenIndex, target uint64) string {
	return strconv.FormatUint(invenIndex, 10) + ":" + strconv.FormatUint(target, 10)
}

func parseCostumeBurstUpgradeKey(key string) (uint64, uint64, bool) {
	left, right, found := strings.Cut(key, ":")
	if !found || strings.Contains(right, ":") {
		return 0, 0, false
	}
	index, indexErr := strconv.ParseUint(left, 10, 64)
	level, levelErr := strconv.ParseUint(right, 10, 64)
	return index, level, indexErr == nil && levelErr == nil && index != 0 && level != 0 && key == costumeBurstUpgradeKey(index, level)
}

func validateCostumeBurstRecord(record CostumeBurstUpgradeRecord) error {
	if record.CostumeID == 0 || record.Level == 0 || record.Code != 578 || len(record.Digest) != costumeBurstDigestHexSize {
		return errors.New("player: invalid costume burst upgrade record")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil {
		return errors.New("player: invalid costume burst upgrade digest")
	}
	level, found, err := wire.Varint(record.Body, 1)
	if err != nil || !found || level != record.Level {
		return errors.New("player: invalid costume burst upgrade response")
	}
	fieldCount := 0
	if err := wire.Walk(record.Body, func(field wire.Field) error {
		if field.Number != 1 || field.Type != 0 {
			return errors.New("unexpected costume burst response field")
		}
		fieldCount++
		return nil
	}); err != nil || fieldCount != 1 {
		return errors.New("player: invalid costume burst upgrade response")
	}
	return nil
}

const costumeBurstDigestHexSize = 64

func (s *CollectionStore) validateCostumeBurstStorage() error {
	if s.data.CostumeBurstLevels == nil || s.data.CostumeBurstUpgrades == nil {
		return errors.New("player: collection save requires costume burst ledgers")
	}
	owned := make(map[uint64]Costume, len(s.base)+len(s.data.Costumes))
	for _, costume := range append(append([]Costume(nil), s.base...), s.data.Costumes...) {
		if costume.InvenIndex == 0 || costume.ID == 0 {
			return errors.New("player: invalid costume burst ownership")
		}
		if _, exists := owned[costume.InvenIndex]; exists {
			return errors.New("player: duplicate costume burst inventory index")
		}
		owned[costume.InvenIndex] = costume
	}
	for key, level := range s.data.CostumeBurstLevels {
		index, err := strconv.ParseUint(key, 10, 64)
		costume, found := owned[index]
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) || level == 0 || !found || level < costume.BurstLevel {
			return fmt.Errorf("player: invalid costume burst level entry %q", key)
		}
	}
	for key, record := range s.data.CostumeBurstUpgrades {
		index, level, valid := parseCostumeBurstUpgradeKey(key)
		costume, found := owned[index]
		current := costume.BurstLevel
		if overlay, exists := s.data.CostumeBurstLevels[strconv.FormatUint(index, 10)]; exists {
			current = overlay
		}
		if !valid || !found || record.CostumeID != costume.ID || record.Level != level || level > current {
			return fmt.Errorf("player: invalid costume burst upgrade entry %q", key)
		}
		if err := validateCostumeBurstRecord(record); err != nil {
			return fmt.Errorf("player: invalid costume burst upgrade entry %q: %w", key, err)
		}
	}
	return nil
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
	slices.Sort(next.CostumePotential[key])
	return s.commit(next)
}

func (s *CollectionStore) CharAwakeState(uniqueCharID uint64) (CharAwakeProgress, bool) {
	if uniqueCharID == 0 {
		return CharAwakeProgress{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	progress, ok := s.data.CharAwake[strconv.FormatUint(uniqueCharID, 10)]
	return progress, ok
}

func (s *CollectionStore) CharAwakeStates() map[uint64]CharAwakeProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[uint64]CharAwakeProgress, len(s.data.CharAwake))
	for key, progress := range s.data.CharAwake {
		uniqueID, err := strconv.ParseUint(key, 10, 64)
		if err == nil && uniqueID != 0 {
			result[uniqueID] = progress
		}
	}
	return result
}

// UpdateCharAwake atomically advances one UniqueCharId ledger entry. expected
// rejects stale concurrent requests before one response can overwrite another.
func (s *CollectionStore) UpdateCharAwake(uniqueCharID uint64, expected, nextProgress CharAwakeProgress) error {
	if uniqueCharID == 0 || nextProgress == (CharAwakeProgress{}) {
		return errors.New("player: invalid character awakening progress")
	}
	for i := range nextProgress.ImprintLevels {
		if nextProgress.ImprintLevels[i] < expected.ImprintLevels[i] {
			return errors.New("player: character imprint level cannot decrease")
		}
	}
	if expected.IsAwake && !nextProgress.IsAwake {
		return errors.New("player: character awakening cannot be removed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatUint(uniqueCharID, 10)
	current := s.data.CharAwake[key]
	if current != expected {
		return errors.New("player: stale character awakening progress")
	}
	next := cloneCollection(s.data)
	next.CharAwake[key] = nextProgress
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.costumeByIndexLocked(index)
}

func findCostume(costumes []Costume, id uint64) (int, bool) {
	for i, costume := range costumes {
		if costume.ID == id {
			return i, true
		}
	}
	return -1, false
}

func findCharacterByDesign(base, collection []Character, design gamedata.CharacterDesign) (uint64, bool, error) {
	var result uint64
	for _, characters := range [][]Character{base, collection} {
		for _, character := range characters {
			matches := character.ID == design.ID
			for _, id := range design.GrowthCharacterIDs {
				matches = matches || character.ID == id
			}
			if matches {
				if result != 0 && result != character.InvenIndex {
					return 0, false, fmt.Errorf("player: duplicate owned character family for design %d; repair the save", design.ID)
				}
				result = character.InvenIndex
			}
		}
	}
	return result, result != 0, nil
}

func cloneGrant(grant CollectionGrant) CollectionGrant {
	grant.GachaResponse = append([]byte(nil), grant.GachaResponse...)
	grant.CharacterIndices = append([]uint64(nil), grant.CharacterIndices...)
	grant.CostumeIndices = append([]uint64(nil), grant.CostumeIndices...)
	grant.Upgrades = append([]CostumeUpgrade(nil), grant.Upgrades...)
	grant.Exchanges = append([]CostumeExchange(nil), grant.Exchanges...)
	grant.ViewCostumeIDs = append([]uint64(nil), grant.ViewCostumeIDs...)
	grant.ViewCostumeSortIDs = append([]uint64(nil), grant.ViewCostumeSortIDs...)
	grant.GachaFixed = append([]GachaFixedState(nil), grant.GachaFixed...)
	grant.SelectionApplySortIDs = append([]uint64(nil), grant.SelectionApplySortIDs...)
	return grant
}

func cloneCollection(in collectionSnapshot) collectionSnapshot {
	out := in
	out.Friendships = cloneFriendshipEntries(in.Friendships)
	out.LatestPreview = append([]uint64(nil), in.LatestPreview...)
	out.Characters = append([]Character(nil), in.Characters...)
	out.Costumes = append([]Costume(nil), in.Costumes...)
	out.BaseCostumeLevels = make(map[string]uint64, len(in.BaseCostumeLevels))
	maps.Copy(out.BaseCostumeLevels, in.BaseCostumeLevels)
	out.CostumePotential = make(map[string][]uint64, len(in.CostumePotential))
	for k, v := range in.CostumePotential {
		out.CostumePotential[k] = append([]uint64(nil), v...)
	}
	out.CostumeBurstLevels = make(map[string]uint64, len(in.CostumeBurstLevels))
	maps.Copy(out.CostumeBurstLevels, in.CostumeBurstLevels)
	out.CostumeBurstUpgrades = make(map[string]CostumeBurstUpgradeRecord, len(in.CostumeBurstUpgrades))
	for k, v := range in.CostumeBurstUpgrades {
		v.Body = append([]byte(nil), v.Body...)
		out.CostumeBurstUpgrades[k] = v
	}
	out.CharAwake = make(map[string]CharAwakeProgress, len(in.CharAwake))
	maps.Copy(out.CharAwake, in.CharAwake)
	out.GachaSelections = make(map[string][]GachaSelection, len(in.GachaSelections))
	for k, v := range in.GachaSelections {
		out.GachaSelections[k] = append([]GachaSelection(nil), v...)
	}
	out.GachaSelectionChanges = make(map[string]uint64, len(in.GachaSelectionChanges))
	maps.Copy(out.GachaSelectionChanges, in.GachaSelectionChanges)
	out.StepUpProgress = make(map[string]uint64, len(in.StepUpProgress))
	maps.Copy(out.StepUpProgress, in.StepUpProgress)
	out.GachaUsers = make(map[string]GachaUserState, len(in.GachaUsers))
	maps.Copy(out.GachaUsers, in.GachaUsers)
	out.GachaFixed = make(map[string]GachaFixedState, len(in.GachaFixed))
	maps.Copy(out.GachaFixed, in.GachaFixed)
	out.GachaApplied = make(map[string]bool, len(in.GachaApplied))
	maps.Copy(out.GachaApplied, in.GachaApplied)
	out.GachaPointExchange = make(map[string]GachaPointExchange, len(in.GachaPointExchange))
	maps.Copy(out.GachaPointExchange, in.GachaPointExchange)
	out.Grants = make(map[string]CollectionGrant, len(in.Grants))
	for k, v := range in.Grants {
		out.Grants[k] = cloneGrant(v)
	}
	return out
}

func (s *CollectionStore) commit(next collectionSnapshot) error {
	changes, err := diffCollectionEntries(s.data, next)
	if err != nil {
		return err
	}
	var core []byte
	if !s.persisted || !sameCollectionCore(s.data, next) {
		core, err = json.Marshal(collectionCore(next))
		if err != nil {
			return err
		}
	}
	if core == nil && len(changes) == 0 {
		return nil
	}
	if err := s.store.SaveWithEntries("collection", core, changes); err != nil {
		return fmt.Errorf("player: persist collection: %w", err)
	}
	s.data = next
	s.persisted = true
	return nil
}
