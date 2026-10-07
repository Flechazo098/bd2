package ownership

import "bd2server/internal/server/protocol/wire"

func add(dst []byte, field int, value uint64) []byte {
	if value != 0 {
		return wire.AppendVarint(dst, field, value)
	}
	return dst
}

type Item struct {
	InvenIndex    uint64         `json:"inven_index"`
	ID            uint64         `json:"id"`
	Type          uint64         `json:"type"`
	Count         uint64         `json:"count"`
	KeepFlag      uint64         `json:"keep_flag,omitempty"`
	TimeValue     uint64         `json:"time_value,omitempty"`
	ExpiryTime    uint64         `json:"expiry_time,omitempty"`
	Pictorialbook *ItemPictorial `json:"pictorialbook,omitempty"`
	SortID        uint64         `json:"sort_id,omitempty"`
	UseCount      uint64         `json:"use_count,omitempty"`
}

type ItemPictorial struct {
	ID      uint64 `json:"id"`
	GroupID uint64 `json:"group_id"`
}

type Equipment struct {
	InvenIndex    uint64            `json:"inven_index"`
	ID            uint64            `json:"id"`
	Level         uint64            `json:"level"`
	UseChar       uint64            `json:"use_char,omitempty"`
	KeepFlag      uint64            `json:"keep_flag,omitempty"`
	LockFlag      uint64            `json:"lock_flag,omitempty"`
	SortID        uint64            `json:"sort_id,omitempty"`
	Mark          string            `json:"mark,omitempty"`
	MainOption    []EquipmentOption `json:"main_option,omitempty"`
	SubOption     []EquipmentOption `json:"sub_option,omitempty"`
	PrivateOption *EquipmentOption  `json:"private_option,omitempty"`
	Rank          []uint64          `json:"rank,omitempty"`
}

type EquipmentOption struct {
	GroupID uint64 `json:"group_id"`
	ID      uint64 `json:"id"`
}

type Character struct {
	InvenIndex              uint64      `json:"inven_index"`
	ID                      uint64      `json:"id"`
	HP                      uint64      `json:"hp,omitempty"`
	Level                   uint64      `json:"level,omitempty"`
	CostumeID               uint64      `json:"costume_id,omitempty"`
	Exp                     uint64      `json:"exp,omitempty"`
	UseCostume              uint64      `json:"use_costume,omitempty"`
	TalentLevel             uint64      `json:"talent_level,omitempty"`
	TalentExp               uint64      `json:"talent_exp,omitempty"`
	SolidarityReward        uint64      `json:"solidarity_reward,omitempty"`
	ExpiryTime              uint64      `json:"expiry_time,omitempty"`
	ConnectPotentialCostume uint64      `json:"connect_potential_costume,omitempty"`
	Pictorialbook           []Pictorial `json:"pictorialbook,omitempty"`
}

type Costume struct {
	InvenIndex    uint64      `json:"inven_index"`
	ID            uint64      `json:"id"`
	Level         uint64      `json:"level,omitempty"`
	UseChar       uint64      `json:"use_char,omitempty"`
	SortID        uint64      `json:"sort_id,omitempty"`
	PotentialIDs  []uint64    `json:"-"`
	DesignID      uint64      `json:"design_id,omitempty"`
	BurstLevel    uint64      `json:"burst_level,omitempty"`
	TimeValue     uint64      `json:"time_value,omitempty"`
	Pictorialbook []Pictorial `json:"pictorialbook,omitempty"`
}

type Pictorial struct {
	ID      uint64 `json:"id"`
	GroupID uint64 `json:"group_id"`
}

func EncodeItem(item Item) []byte {
	var b []byte
	for _, f := range []struct {
		n int
		v uint64
	}{{1, item.InvenIndex}, {2, item.ID}, {3, item.Type}, {4, item.Count}, {5, item.KeepFlag}, {6, item.TimeValue}} {
		if f.v != 0 {
			b = wire.AppendVarint(b, f.n, f.v)
		}
	}
	if item.ExpiryTime != 0 {
		b = wire.AppendVarint(b, 8, item.ExpiryTime)
	}
	if item.SortID != 0 {
		b = wire.AppendVarint(b, 9, item.SortID)
	}
	if item.UseCount != 0 {
		b = wire.AppendVarint(b, 10, item.UseCount)
	}
	return b
}

func EncodeEquipment(entry Equipment) []byte {
	base := wire.AppendVarint(nil, 1, entry.ID)
	if entry.Level != 0 {
		base = wire.AppendVarint(base, 2, entry.Level)
	}
	for _, option := range entry.MainOption {
		base = wire.AppendBytes(base, 3, EncodeEquipmentOption(option))
	}
	for _, option := range entry.SubOption {
		base = wire.AppendBytes(base, 4, EncodeEquipmentOption(option))
	}
	// The native ResolvedPrivateOption getter dereferences PrivateOption before
	// checking IsValid. A present empty message selects its GameData fallback;
	// an omitted field leaves the protobuf object null and crashes the UI.
	var private []byte
	if entry.PrivateOption != nil {
		private = EncodeEquipmentOption(*entry.PrivateOption)
	}
	base = wire.AppendBytes(base, 5, private)
	for _, rank := range entry.Rank {
		base = wire.AppendVarint(base, 6, rank)
	}
	var out []byte
	out = wire.AppendVarint(out, 1, entry.InvenIndex)
	if entry.UseChar != 0 {
		out = wire.AppendVarint(out, 2, entry.UseChar)
	}
	if entry.KeepFlag != 0 {
		out = wire.AppendVarint(out, 3, entry.KeepFlag)
	}
	if entry.LockFlag != 0 {
		out = wire.AppendVarint(out, 4, entry.LockFlag)
	}
	out = wire.AppendBytes(out, 5, base)
	if entry.SortID != 0 {
		out = wire.AppendVarint(out, 7, entry.SortID)
	}
	if entry.Mark != "" {
		out = wire.AppendBytes(out, 8, []byte(entry.Mark))
	}
	return out
}

func EncodeEquipmentOption(option EquipmentOption) []byte {
	out := wire.AppendVarint(nil, 1, option.GroupID)
	return wire.AppendVarint(out, 2, option.ID)
}

func EncodeCharacter(c Character) []byte {
	fields := []struct {
		n int
		v uint64
	}{{1, c.InvenIndex}, {2, c.ID}, {3, c.HP}, {4, c.Level}, {5, c.CostumeID}, {6, c.Exp}, {7, c.UseCostume}, {8, c.TalentLevel}, {9, c.TalentExp}, {10, c.SolidarityReward}, {11, c.ExpiryTime}, {13, c.ConnectPotentialCostume}}
	var out []byte
	for _, field := range fields {
		if field.v != 0 {
			out = wire.AppendVarint(out, field.n, field.v)
		}
	}
	for _, p := range c.Pictorialbook {
		book := wire.AppendVarint(wire.AppendVarint(nil, 1, p.ID), 2, p.GroupID)
		out = wire.AppendBytes(out, 12, book)
	}
	return out
}

func EncodeCostume(entry Costume) []byte {
	var costume []byte
	costume = add(costume, 1, entry.InvenIndex)
	costume = add(costume, 2, entry.ID)
	costume = add(costume, 3, entry.Level)
	costume = add(costume, 4, entry.UseChar)
	for _, p := range entry.Pictorialbook {
		book := add(add(nil, 1, p.ID), 2, p.GroupID)
		costume = wire.AppendBytes(costume, 5, book)
	}
	costume = add(costume, 6, entry.SortID)
	for _, id := range entry.PotentialIDs {
		costume = wire.AppendVarint(costume, 8, id)
	}
	costume = add(costume, 9, entry.DesignID)
	costume = add(costume, 10, entry.BurstLevel)
	costume = add(costume, 12, entry.TimeValue)
	return costume
}
