package profile

import (
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
)

func (s *Service) SupporterBases(ctx command.Context, settings Settings) ([]CostumeBase, error) {
	bases := make([]CostumeBase, 2)
	for index := range uint64(2) {
		if supporter, ok := settings.Supporters[strconv.FormatUint(index, 10)]; ok {
			details, err := s.source.SupporterDetails(ctx, supporter.CharacterIndex, supporter.CostumeIndex)
			if err != nil {
				return nil, err
			}
			for _, costume := range details.Costumes {
				if costume.InvenIndex == supporter.CostumeIndex {
					bases[index] = CostumeBase{ID: costume.ID, Level: costume.Level, DesignID: costume.DesignID}
					break
				}
			}
		}
	}
	return bases, nil
}

func (s *Service) PublicSupporterDetails(ctx command.Context) (map[uint64][]byte, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[uint64][]byte, len(settings.Supporters))
	for index := range uint64(2) {
		supporter, ok := settings.Supporters[strconv.FormatUint(index, 10)]
		if !ok {
			continue
		}
		details, err := s.source.SupporterDetails(ctx, supporter.CharacterIndex, supporter.CostumeIndex)
		if err != nil {
			return nil, err
		}
		row, err := s.supporterSlot(ctx, index, supporter, details)
		if err != nil {
			return nil, err
		}
		result[index] = wire.AppendBytes(nil, 1, row)
	}
	return result, nil
}

func (s *Service) handleSupporters(ctx command.Context, path string, request []byte, settings *Settings) ([]byte, bool, error) {
	if path == "/SupporterRegister" {
		index, err := profileScalar(request, 2, 1)
		if err != nil {
			return nil, false, err
		}
		char, err := profileScalar(request, 3, math.MaxInt64)
		if err != nil {
			return nil, false, err
		}
		costume, err := profileScalar(request, 4, math.MaxInt64)
		if err != nil {
			return nil, false, err
		}
		power, err := profileScalar(request, 5, math.MaxInt32)
		if err != nil {
			return nil, false, err
		}
		details, err := s.source.SupporterDetails(ctx, char, costume)
		if err != nil {
			return nil, false, err
		}
		for key, other := range settings.Supporters {
			if key == strconv.FormatUint(index, 10) {
				continue
			}
			otherDetails, err := s.source.SupporterDetails(ctx, other.CharacterIndex, other.CostumeIndex)
			if err != nil {
				return nil, false, err
			}
			if otherDetails.UniqueID == details.UniqueID {
				return nil, false, fmt.Errorf("profile: this character is already registered")
			}
		}
		supporter := Supporter{CharacterIndex: char, CostumeIndex: costume, Power: power, Date: uint64(s.now().UnixMilli())}
		settings.Supporters[strconv.FormatUint(index, 10)] = supporter
		row, err := s.supporterSlot(ctx, index, supporter, details)
		if err != nil {
			return nil, false, err
		}
		return wire.AppendBytes(nil, 1, row), true, nil
	}
	if path == "/SupporterRemove" {
		index, err := profileScalar(request, 2, 1)
		if err != nil {
			return nil, false, err
		}
		delete(settings.Supporters, strconv.FormatUint(index, 10))
		return nil, true, nil
	}
	if path == "/SupporterDetail" {
		target, err := profileScalar(request, 2, math.MaxInt64)
		if err != nil {
			return nil, false, err
		}
		if target == 0 {
			return nil, false, fmt.Errorf("%w: SupporterDetail names no owner", ErrNotFound)
		}
		if _, err := s.source.PublicProfile(ctx, target); err != nil {
			return nil, false, err
		}
		index, err := profileScalar(request, 3, 1)
		if err != nil {
			return nil, false, err
		}
		supporter, ok := settings.Supporters[strconv.FormatUint(index, 10)]
		if !ok {
			return nil, false, fmt.Errorf("%w: no supporter in slot %d", ErrNotFound, index)
		}
		details, err := s.source.SupporterDetails(ctx, supporter.CharacterIndex, supporter.CostumeIndex)
		if err != nil {
			return nil, false, err
		}
		row, err := s.supporterSlot(ctx, index, supporter, details)
		if err != nil {
			return nil, false, err
		}
		return wire.AppendBytes(nil, 1, row), false, nil
	}
	if path == "/SupporterStatus" {
		if _, err := profileScalar(request, 2, math.MaxInt32); err != nil {
			return nil, false, err
		}
		if _, err := profileScalar(request, 3, math.MaxInt64); err != nil {
			return nil, false, err
		}
	}
	bases, err := s.SupporterBases(ctx, *settings)
	if err != nil {
		return nil, false, err
	}
	field := 1
	if path == "/SupporterStatus" {
		field = 2
	}
	var out []byte
	for _, base := range bases {
		out = wire.AppendBytes(out, field, costumeBaseWire(base))
	}
	return out, false, nil
}

func (s *Service) supporterSlot(ctx command.Context, index uint64, supporter Supporter, details SupporterDetails) ([]byte, error) {
	owner, err := s.source.ProfileOwner(ctx)
	if err != nil {
		return nil, err
	}
	var costumeID uint64
	for _, costume := range details.Costumes {
		if costume.InvenIndex == supporter.CostumeIndex {
			costumeID = costume.ID
		}
	}
	row := wire.AppendVarint(nil, 1, owner)
	row = wire.AppendVarint(row, 2, index)
	row = wire.AppendVarint(row, 3, costumeID)
	row = wire.AppendVarint(row, 4, supporter.Power)
	row = wire.AppendVarint(row, 7, supporter.Date)
	character := details.Character
	character.UseCostume = supporter.CostumeIndex
	character.CostumeID = costumeID
	bundle := wire.AppendBytes(nil, 1, roster.CharacterWire(character))
	for _, costume := range details.Costumes {
		bundle = wire.AppendBytes(bundle, 2, roster.CostumeWire(costume))
	}
	for _, equipment := range details.Equipment {
		bundle = wire.AppendBytes(bundle, 3, assets.EquipmentWire(equipment))
	}
	awake := wire.AppendVarint(nil, 1, details.UniqueID)
	for i, n := range details.Awake.ImprintLevels {
		awake = wire.AppendVarint(awake, i+2, n)
	}
	if details.Awake.IsAwake {
		awake = wire.AppendVarint(awake, 5, 1)
	}
	awake = wire.AppendVarint(awake, 6, owner)
	bundle = wire.AppendBytes(bundle, 4, awake)
	return wire.AppendString(row, 6, base64.StdEncoding.EncodeToString(bundle)), nil
}
