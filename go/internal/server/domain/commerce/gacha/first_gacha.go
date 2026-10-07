package gacha

import (
	"bd2server/internal/server/design/gamedata"

	assets "bd2server/internal/server/domain/inventory"
	"errors"
)

const firstGachaResponseDigest = "first-gacha-confirm-v1"

type firstGachaPreview struct {
	sequence  uint64
	rewards   []gamedata.FirstGachaReward
	equipment map[int]assets.Equipment
	response  []byte
}

func firstGachaEquipment(catalog *gamedata.EquipmentGachaCatalog, id, sortID uint64) (assets.Equipment, error) {
	if catalog == nil {
		return assets.Equipment{}, errors.New("gacha: first gacha equipment catalog missing")
	}
	main, sub, private, err := catalog.RollOptions(id)
	if err != nil {
		return assets.Equipment{}, err
	}
	entry := assets.Equipment{ID: id, SortID: sortID, Rank: []uint64{0, 0, 0}}
	for _, option := range main {
		entry.MainOption = append(entry.MainOption, assets.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
	}
	for _, option := range sub {
		entry.SubOption = append(entry.SubOption, assets.EquipmentOption{GroupID: option.GroupID, ID: option.ID})
	}
	if private != nil {
		entry.PrivateOption = &assets.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
	}
	return entry, nil
}
