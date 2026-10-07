// Package login adapts player projections to LoginUser responses.
// It deliberately stores decoded protobuf data, never a captured HTTP reply
// or a captured session key.
package login

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/platform/versionconfig"
	"errors"
)

func StateVersion() string { return versionconfig.State() }

var (
	ErrInvalidSeed = errors.New("account: invalid LoginUser seed")
)

// LoginSeed is the versioned, decoded representation of a LoginUser response.
// UserInfo is UserDBInfo protobuf field 1 with its field 3 (user_key)
// deliberately absent. ResponseFields contains the remaining top-level
// protobuf fields (such as client-notification state), never an HTTP envelope.
type LoginSeed struct {
	Version            string
	PacketCode         int
	UserInfo           []byte
	ResponseFields     []byte
	currencies         CurrencyProvider
	purchaseCounts     PurchaseCountProvider
	presetSlots        PresetSlotProvider
	inventorySlots     InventorySlotProvider
	firstGacha         FirstGachaProvider
	friendshipAP       FriendshipAPProvider
	lastPlayedPack     LastPlayedPackProvider
	achievementExp     AchievementExperienceProvider
	autoReviveSettings interface {
		AutoReviveSettings(command.Context) (bool, uint64, error)
	}
	portrait             interface{ PortraitCostume() uint64 }
	levelReward          LevelRewardProvider
	huntingAP            HuntingAPProvider
	monsterHuntSlots     PresetSlotProvider
	additionalCurrencies interface {
		AdditionalCurrencies(command.Context) (map[int]uint64, error)
	}
	newbieStep interface{ NewbieStep() uint64 }
}

func (s *LoginSeed) AttachNewbieStep(provider interface{ NewbieStep() uint64 }) error {
	if provider == nil {
		return errors.New("account: missing newbie step provider")
	}
	s.newbieStep = provider
	return nil
}

// AttachAdditionalCurrencies supplies balances owned by optional gameplay
// domains without altering the stable wallet representation.
func (s *LoginSeed) AttachAdditionalCurrencies(provider interface {
	AdditionalCurrencies(command.Context) (map[int]uint64, error)
}) error {
	if provider == nil {
		return errors.New("account: missing additional currency provider")
	}
	s.additionalCurrencies = provider
	return nil
}

func (s *LoginSeed) AttachMonsterHuntSlots(provider PresetSlotProvider) error {
	if provider == nil {
		return errors.New("account: missing monster hunt preset provider")
	}
	s.monsterHuntSlots = provider
	return nil
}

type HuntingAPProvider interface {
	HuntingAP(command.Context) (free, bonus uint64, err error)
}

func (s *LoginSeed) AttachHuntingAP(provider HuntingAPProvider) error {
	if provider == nil {
		return errors.New("account: missing hunting AP provider")
	}
	s.huntingAP = provider
	return nil
}

type LevelRewardProvider interface {
	LevelRewardCount() (uint64, error)
}

func (s *LoginSeed) AttachLevelReward(provider LevelRewardProvider) error {
	if provider == nil {
		return errors.New("account: missing level reward provider")
	}
	s.levelReward = provider
	return nil
}

type AchievementExperienceProvider interface {
	AchievementExperience() (uint64, error)
}

func (s *LoginSeed) AttachAchievementExperience(provider AchievementExperienceProvider) error {
	if provider == nil {
		return errors.New("account: missing achievement experience provider")
	}
	s.achievementExp = provider
	return nil
}

// LastPlayedPackProvider reads the persisted return destination. A zero value
// means there is no saved destination yet, so a new account uses its seed.
type LastPlayedPackProvider interface {
	LastPlayedPackID(command.Context) (uint64, error)
}

func (s *LoginSeed) AttachLastPlayedPack(provider LastPlayedPackProvider) error {
	if provider == nil {
		return errors.New("account: missing last played pack provider")
	}
	s.lastPlayedPack = provider
	return nil
}

// FirstGachaProvider reads the mutable account flag for every login, including
// a login after confirming the starter draw without restarting the server.
type FirstGachaProvider interface {
	FirstGachaCompleted() bool
}

func (s *LoginSeed) AttachFirstGacha(provider FirstGachaProvider) error {
	if provider == nil {
		return errors.New("account: missing first gacha provider")
	}
	s.firstGacha = provider
	return nil
}

// CurrencyProvider supplies the authoritative mutable UserDBInfo wallet.
type CurrencyProvider interface {
	Currencies(command.Context) (gold, freeJewelry, jewelry, mileage uint64)
}

type HopePowderProvider interface {
	HopePowderBalance(command.Context) uint64
}

type CatalystProvider interface {
	CatalystBalance(command.Context) uint64
}

type EquipmentMileageProvider interface {
	EquipmentMileageBalances(command.Context) (mileage, exchangeGage uint64)
}

// PurchaseCountProvider supplies the current PurchaseCountDBInfo messages for
// UserDBInfo field 26. Implementations must derive them from authoritative
// account state rather than the immutable login seed.
type PurchaseCountProvider interface {
	PurchaseCountDBInfos(command.Context) [][]byte
}

// PresetSlotProvider supplies the authoritative number of ordinary party
// preset slots for UserDBInfo field 28. The deck domain owns both purchased
// slot state and the preset records stored in those slots.
type PresetSlotProvider interface {
	PresetSlotCount() uint64
}

// InventorySlotProvider owns the four mutable UserDBInfo capacity fields.
// Development overrides are applied by the provider as a login-time view;
// the immutable account seed is never rewritten.
type InventorySlotProvider interface {
	UserInventorySlots(command.Context) (items, storage, equipment, equipmentStorage uint64, err error)
}

// FriendshipAPProvider supplies the account's remaining daily counseling
// points; LoginUser must not restore points from its immutable seed.
type FriendshipAPProvider interface {
	FriendshipAP() (uint64, error)
}

func (s *LoginSeed) AttachFriendshipAP(provider FriendshipAPProvider) error {
	if provider == nil {
		return errors.New("account: nil friendship AP provider")
	}
	s.friendshipAP = provider
	return nil
}

func (s *LoginSeed) AttachCurrencies(provider CurrencyProvider) error {
	if provider == nil {
		return errors.New("account: nil currency provider")
	}
	s.currencies = provider
	return nil
}

func (s *LoginSeed) AttachPurchaseCounts(provider PurchaseCountProvider) error {
	if provider == nil {
		return errors.New("account: nil purchase count provider")
	}
	s.purchaseCounts = provider
	return nil
}

func (s *LoginSeed) AttachPresetSlots(provider PresetSlotProvider) error {
	if provider == nil {
		return errors.New("account: nil preset slot provider")
	}
	s.presetSlots = provider
	return nil
}

func (s *LoginSeed) AttachInventorySlots(provider InventorySlotProvider) error {
	if provider == nil {
		return errors.New("account: nil inventory slot provider")
	}
	s.inventorySlots = provider
	return nil
}

type diskSeed struct {
	Version              string `json:"version"`
	PacketCode           int    `json:"packet_code"`
	UserInfoBase64       string `json:"user_info_base64"`
	ResponseFieldsBase64 string `json:"response_fields_base64,omitempty"`
}

// Write stores a seed in the portable JSON form used by the one-shot importer.
