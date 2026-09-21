// Package player owns the initial, mutable inventory/character snapshot used
// by the local account. GameData defines what each ID means; this file holds
// only the new player's ownership and progress.
package player

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"bd2server/internal/wire"
)

type Item struct {
	InvenIndex    uint64     `json:"inven_index"`
	ID            uint64     `json:"id"`
	Type          uint64     `json:"type"`
	Count         uint64     `json:"count"`
	KeepFlag      uint64     `json:"keep_flag,omitempty"`
	TimeValue     uint64     `json:"time_value,omitempty"`
	Pictorialbook *Pictorial `json:"pictorialbook,omitempty"`
	SortID        uint64     `json:"sort_id,omitempty"`
	UseCount      uint64     `json:"use_count,omitempty"`
}

type Costume struct {
	InvenIndex    uint64      `json:"inven_index"`
	ID            uint64      `json:"id"`
	Level         uint64      `json:"level,omitempty"`
	UseChar       uint64      `json:"use_char,omitempty"`
	SortID        uint64      `json:"sort_id,omitempty"`
	PotentialID   uint64      `json:"potential_id,omitempty"`
	DesignID      uint64      `json:"design_id,omitempty"`
	BurstLevel    uint64      `json:"burst_level,omitempty"`
	TimeValue     uint64      `json:"time_value,omitempty"`
	Pictorialbook []Pictorial `json:"pictorialbook,omitempty"`
}

type Pictorial struct {
	ID      uint64 `json:"id"`
	GroupID uint64 `json:"group_id"`
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

type Starter struct {
	Version                  string      `json:"version"`
	Items                    []Item      `json:"items"`
	Costumes                 []Costume   `json:"costumes"`
	Characters               []Character `json:"characters"`
	Pictorialbook            []Pictorial `json:"pictorialbook,omitempty"`
	FieldCharControlDeckType uint64      `json:"field_char_control_deck_type"`
}

func Load(path string) (*Starter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("player: read starter: %w", err)
	}
	var starter Starter
	if err := json.Unmarshal(data, &starter); err != nil {
		return nil, fmt.Errorf("player: decode starter: %w", err)
	}
	if err := starter.Validate(); err != nil {
		return nil, err
	}
	return &starter, nil
}

func (s *Starter) Validate() error {
	if s == nil || s.Version != "2.34.13" {
		return errors.New("player: wrong starter version")
	}
	for _, item := range s.Items {
		if item.ID == 0 || item.Count == 0 {
			return errors.New("player: invalid item")
		}
	}
	for _, costume := range s.Costumes {
		if costume.ID == 0 || costume.InvenIndex == 0 {
			return errors.New("player: invalid costume")
		}
	}
	for _, character := range s.Characters {
		if character.ID == 0 || character.InvenIndex == 0 {
			return errors.New("player: invalid character")
		}
	}
	return nil
}

func (s *Starter) Write(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func add(dst []byte, field int, value uint64) []byte {
	if value != 0 {
		return wire.AppendVarint(dst, field, value)
	}
	return dst
}

func (s *Starter) Handle(path string, request []byte) (int, []byte, bool, error) {
	var code int
	var response []byte
	switch path {
	case "/ItemInfo":
		code = 21
		for _, entry := range s.Items {
			response = wire.AppendBytes(response, 1, ItemWire(entry))
		}
	case "/CostumeInfo":
		code = 40
		for _, entry := range s.Costumes {
			response = wire.AppendBytes(response, 1, CostumeWire(entry))
		}
	case "/CharInfo":
		code = 9
		for _, entry := range s.Characters {
			var character []byte
			character = add(character, 1, entry.InvenIndex)
			character = add(character, 2, entry.ID)
			character = add(character, 3, entry.HP)
			character = add(character, 4, entry.Level)
			character = add(character, 5, entry.CostumeID)
			character = add(character, 6, entry.Exp)
			character = add(character, 7, entry.UseCostume)
			character = add(character, 8, entry.TalentLevel)
			character = add(character, 9, entry.TalentExp)
			character = add(character, 10, entry.SolidarityReward)
			character = add(character, 11, entry.ExpiryTime)
			character = add(character, 13, entry.ConnectPotentialCostume)
			response = wire.AppendBytes(response, 1, character)
		}
		response = add(response, 2, s.FieldCharControlDeckType)
	default:
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("player: %s invalid sequence", path)
	}
	return code, response, true, nil
}

func CostumeWire(entry Costume) []byte {
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
	costume = add(costume, 8, entry.PotentialID)
	costume = add(costume, 9, entry.DesignID)
	costume = add(costume, 10, entry.BurstLevel)
	costume = add(costume, 12, entry.TimeValue)
	return costume
}

func decodeVarints(proto []byte, fields map[int]*uint64) error {
	return wire.Walk(proto, func(field wire.Field) error {
		value, known := fields[field.Number]
		if !known {
			return fmt.Errorf("player: unsupported starter field %d", field.Number)
		}
		if field.Type != 0 {
			return fmt.Errorf("player: starter field %d wire type %d", field.Number, field.Type)
		}
		*value, _ = binary.Uvarint(field.Value)
		return nil
	})
}
