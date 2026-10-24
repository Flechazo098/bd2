package social

import (
	"encoding/base64"
	"fmt"

	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/profile"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
)

func SupporterSnapshot(member PublicMember, details profile.SupporterDetails, characterIndex, costumeIndex uint64, version string) (SupporterSlot, error) {
	if member.AccountID == "" || member.OwnerIndex == 0 || version == "" || details.Character.InvenIndex != characterIndex {
		return SupporterSlot{}, fmt.Errorf("%w: supporter snapshot of %q (owner %d, version %q) names character %d, details hold %d", ErrInvalid, member.AccountID, member.OwnerIndex, version, characterIndex, details.Character.InvenIndex)
	}
	slot := SupporterSlot{AccountID: member.AccountID, OwnerIndex: member.OwnerIndex, Name: member.Name, CharacterIndex: characterIndex, CostumeIndex: costumeIndex, Version: version}
	slot.CharacterID = details.Character.ID
	for _, costume := range details.Costumes {
		if costume.InvenIndex == costumeIndex {
			if costume.UseChar != characterIndex {
				return SupporterSlot{}, fmt.Errorf("%w: costume %d is not for character %d", ErrIneligible, costumeIndex, characterIndex)
			}
			slot.CostumeID, slot.CostumeLevel, slot.CostumeDesignID = costume.ID, costume.Level, costume.DesignID
		}
	}
	if slot.CostumeID == 0 {
		return SupporterSlot{}, fmt.Errorf("%w: costume %d is not owned", ErrIneligible, costumeIndex)
	}
	character := details.Character
	character.UseCostume = costumeIndex
	character.CostumeID = slot.CostumeID
	bundle := wire.AppendBytes(nil, 1, roster.CharacterWire(character))
	for _, v := range details.Costumes {
		bundle = wire.AppendBytes(bundle, 2, roster.CostumeWire(v))
	}
	for _, v := range details.Equipment {
		bundle = wire.AppendBytes(bundle, 3, assets.EquipmentWire(v))
	}
	awake := wire.AppendVarint(nil, 1, details.UniqueID)
	for i, v := range details.Awake.ImprintLevels {
		awake = wire.AppendVarint(awake, i+2, v)
	}
	if details.Awake.IsAwake {
		awake = wire.AppendVarint(awake, 5, 1)
	}
	awake = wire.AppendVarint(awake, 6, member.OwnerIndex)
	bundle = wire.AppendBytes(bundle, 4, awake)
	slot.Bundle = base64.StdEncoding.EncodeToString(bundle)
	return slot, nil
}
