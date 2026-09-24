// Package account owns the local-account representation used by LoginUser.
// It deliberately stores decoded protobuf data, never a captured HTTP reply
// or a captured session key.
package account

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bd2server/internal/cryptox"
	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
)

func ProtocolVersion() string { return versionconfig.Protocol() }

var (
	ErrInvalidSeed = errors.New("account: invalid LoginUser seed")
)

// LoginSeed is the versioned, decoded representation of a LoginUser response.
// UserInfo is UserDBInfo protobuf field 1 with its field 3 (user_key)
// deliberately absent. ResponseFields contains the remaining top-level
// protobuf fields (such as client-notification state), never an HTTP envelope.
type LoginSeed struct {
	Version        string
	PacketCode     int
	UserInfo       []byte
	ResponseFields []byte
	currencies     CurrencyProvider
	purchaseCounts PurchaseCountProvider
	presetSlots    PresetSlotProvider
	firstGacha     *bool
}

func (s *LoginSeed) SetFirstGacha(value bool) { s.firstGacha = &value }

// CurrencyProvider supplies the authoritative mutable UserDBInfo wallet.
type CurrencyProvider interface {
	Currencies() (gold, freeJewelry, jewelry, mileage uint64)
}

type HopePowderProvider interface {
	HopePowderBalance() uint64
}

type EquipmentMileageProvider interface {
	EquipmentMileageBalances() (mileage, exchangeGage uint64)
}

// PurchaseCountProvider supplies the current PurchaseCountDBInfo messages for
// UserDBInfo field 26. Implementations must derive them from authoritative
// account state rather than the immutable login seed.
type PurchaseCountProvider interface {
	PurchaseCountDBInfos() [][]byte
}

// PresetSlotProvider supplies the authoritative number of ordinary party
// preset slots for UserDBInfo field 28. The deck domain owns both purchased
// slot state and the preset records stored in those slots.
type PresetSlotProvider interface {
	PresetSlotCount() uint64
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

// SeedCurrencies returns the immutable starting balances embedded in the
// versioned UserDBInfo template. Mutable balances live in player.Wallet.
func (s *LoginSeed) SeedCurrencies() (gold, freeJewelry, jewelry, mileage uint64, err error) {
	if err = s.Validate(); err != nil {
		return 0, 0, 0, 0, err
	}
	read := func(field int) (uint64, error) {
		value, found, readErr := wire.Varint(s.UserInfo, field)
		if readErr != nil {
			return 0, readErr
		}
		if !found {
			return 0, nil
		}
		return value, nil
	}
	if gold, err = read(7); err != nil {
		return 0, 0, 0, 0, err
	}
	if freeJewelry, err = read(8); err != nil {
		return 0, 0, 0, 0, err
	}
	if jewelry, err = read(9); err != nil {
		return 0, 0, 0, 0, err
	}
	if mileage, err = read(23); err != nil {
		return 0, 0, 0, 0, err
	}
	return gold, freeJewelry, jewelry, mileage, nil
}

func (s *LoginSeed) SeedHopePowder() (uint64, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	value, found, err := wire.Varint(s.UserInfo, 24)
	if err != nil || !found {
		return value, err
	}
	return value, nil
}

func (s *LoginSeed) SeedEquipmentMileage() (mileage, exchangeGage uint64, err error) {
	if err = s.Validate(); err != nil {
		return 0, 0, err
	}
	if mileage, _, err = wire.Varint(s.UserInfo, 67); err != nil {
		return 0, 0, err
	}
	if exchangeGage, _, err = wire.Varint(s.UserInfo, 68); err != nil {
		return 0, 0, err
	}
	return mileage, exchangeGage, nil
}

type diskSeed struct {
	Version              string `json:"version"`
	PacketCode           int    `json:"packet_code"`
	UserInfoBase64       string `json:"user_info_base64"`
	ResponseFieldsBase64 string `json:"response_fields_base64,omitempty"`
}

// Load reads one versioned seed file. Relative paths are intentionally left to
// the caller; production code can therefore choose an explicit local asset.
func Load(path string) (*LoginSeed, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("account: read seed: %w", err)
	}
	var disk diskSeed
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("account: decode seed JSON: %w", err)
	}
	user, err := base64.StdEncoding.DecodeString(disk.UserInfoBase64)
	if err != nil {
		return nil, fmt.Errorf("account: decode user_info_base64: %w", err)
	}
	other, err := base64.StdEncoding.DecodeString(disk.ResponseFieldsBase64)
	if err != nil {
		return nil, fmt.Errorf("account: decode response_fields_base64: %w", err)
	}
	seed := &LoginSeed{Version: disk.Version, PacketCode: disk.PacketCode, UserInfo: user, ResponseFields: other}
	if err := seed.Validate(); err != nil {
		return nil, err
	}
	return seed, nil
}

// Write stores a seed in the portable JSON form used by the one-shot importer.
func (s *LoginSeed) Write(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(diskSeed{
		Version: s.Version, PacketCode: s.PacketCode,
		UserInfoBase64:       base64.StdEncoding.EncodeToString(s.UserInfo),
		ResponseFieldsBase64: base64.StdEncoding.EncodeToString(s.ResponseFields),
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Clean(path), data, 0o644); err != nil {
		return fmt.Errorf("account: write seed: %w", err)
	}
	return nil
}

// Validate verifies the minimum protocol contract and makes sure a captured
// user_key cannot accidentally be committed to a local seed.
func (s *LoginSeed) Validate() error {
	if s == nil || s.Version == "" || s.PacketCode <= 0 || len(s.UserInfo) == 0 {
		return ErrInvalidSeed
	}
	if _, found, err := wire.Bytes(s.UserInfo, 3); err != nil {
		return fmt.Errorf("%w: malformed UserInfo: %v", ErrInvalidSeed, err)
	} else if found {
		return fmt.Errorf("%w: UserInfo contains user_key", ErrInvalidSeed)
	}
	if err := wire.Walk(s.ResponseFields, func(field wire.Field) error {
		if field.Number == 1 {
			return fmt.Errorf("%w: response fields contain UserInfo", ErrInvalidSeed)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// Encode makes a fresh encrypted LoginUser HTTP envelope. sessionKey belongs
// to the new local session; no value from a capture is used at runtime.
func (s *LoginSeed) Encode(sessionKey string, now time.Time) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	proto, err := s.Login(nil, []byte(sessionKey))
	if err != nil {
		return nil, err
	}
	data, err := cryptox.EncryptBase64Payload(proto, cryptox.Key())
	if err != nil {
		return nil, err
	}
	envelope := struct {
		ErrorType     int    `json:"errorType"`
		PacketCode    int    `json:"packetCode"`
		Length        int    `json:"length"`
		Data          string `json:"data"`
		ServerNowTime int64  `json:"serverNowTime"`
	}{
		ErrorType: 0, PacketCode: s.PacketCode,
		Length: base64.StdEncoding.EncodedLen(len(proto)), Data: data,
		ServerNowTime: now.UnixMilli(),
	}
	return json.Marshal(envelope)
}

// Login constructs the decoded LoginUser protobuf for one fresh local
// session. request is the already-decrypted LoginUser protobuf. The reply
// is deliberately protobuf-only so the session/transport layer owns its
// packet-code and HTTP envelope policy.
func (s *LoginSeed) Login(request, sessionKey []byte) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if len(request) != 0 {
		if _, found, err := wire.Varint(request, 1); err != nil || !found {
			return nil, fmt.Errorf("account: LoginUser request has no sequence: %w", err)
		}
	}
	if len(sessionKey) != cryptox.AESKeySize {
		return nil, fmt.Errorf("account: session key: %w", cryptox.ErrInvalidKey)
	}
	if _, err := cryptox.SessionKey(string(sessionKey)); err != nil {
		return nil, fmt.Errorf("account: session key: %w", err)
	}
	user := append([]byte(nil), s.UserInfo...)
	if s.currencies != nil {
		gold, freeJewelry, jewelry, mileage := s.currencies.Currencies()
		var err error
		if user, _, err = wire.ReplaceVarint(user, 7, gold); err != nil {
			return nil, fmt.Errorf("account: replace gold: %w", err)
		}
		if user, _, err = wire.ReplaceVarint(user, 8, freeJewelry); err != nil {
			return nil, fmt.Errorf("account: replace free jewelry: %w", err)
		}
		if user, _, err = wire.ReplaceVarint(user, 9, jewelry); err != nil {
			return nil, fmt.Errorf("account: replace jewelry: %w", err)
		}
		if user, _, err = wire.ReplaceVarint(user, 23, mileage); err != nil {
			return nil, fmt.Errorf("account: replace mileage: %w", err)
		}
		if provider, ok := s.currencies.(HopePowderProvider); ok {
			if user, _, err = wire.ReplaceVarint(user, 24, provider.HopePowderBalance()); err != nil {
				return nil, fmt.Errorf("account: replace hope powder: %w", err)
			}
		}
		if provider, ok := s.currencies.(EquipmentMileageProvider); ok {
			equipMileage, exchangeGage := provider.EquipmentMileageBalances()
			if user, _, err = wire.ReplaceVarint(user, 67, equipMileage); err != nil {
				return nil, fmt.Errorf("account: replace equipment mileage: %w", err)
			}
			if user, _, err = wire.ReplaceVarint(user, 68, exchangeGage); err != nil {
				return nil, fmt.Errorf("account: replace equipment mileage exchange gauge: %w", err)
			}
		}
	}
	if s.firstGacha != nil {
		value := uint64(0)
		if *s.firstGacha {
			value = 1
		}
		var err error
		if user, _, err = wire.ReplaceVarint(user, 27, value); err != nil {
			return nil, fmt.Errorf("account: replace first gacha: %w", err)
		}
	}
	if s.purchaseCounts != nil {
		var err error
		if user, err = replaceRepeatedBytes(user, 26, s.purchaseCounts.PurchaseCountDBInfos()); err != nil {
			return nil, fmt.Errorf("account: replace purchase counts: %w", err)
		}
	}
	if s.presetSlots != nil {
		var err error
		if user, _, err = wire.ReplaceVarint(user, 28, s.presetSlots.PresetSlotCount()); err != nil {
			return nil, fmt.Errorf("account: replace preset slots: %w", err)
		}
	}
	user = wire.AppendBytes(user, 3, sessionKey)
	return append(wire.AppendBytes(nil, 1, user), s.ResponseFields...), nil
}

func replaceRepeatedBytes(data []byte, number int, values [][]byte) ([]byte, error) {
	result := make([]byte, 0, len(data))
	if err := wire.Walk(data, func(field wire.Field) error {
		if field.Number != number {
			result = append(result, data[field.Start:field.End]...)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, value := range values {
		result = wire.AppendBytes(result, number, value)
	}
	return result, nil
}
