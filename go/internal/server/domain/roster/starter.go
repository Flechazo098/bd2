// Package player owns the initial, mutable inventory/character snapshot used
// by the local account. GameData defines what each ID means; this file holds
// only the new player's ownership and progress.
package roster

import (
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/platform/versionconfig"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

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
	Version                  string        `json:"version"`
	CookingRecipes           []uint64      `json:"cooking_recipes,omitempty"`
	Items                    []assets.Item `json:"items"`
	Costumes                 []Costume     `json:"costumes"`
	Characters               []Character   `json:"characters"`
	Pictorialbook            []Pictorial   `json:"pictorialbook,omitempty"`
	FieldCharControlDeckType uint64        `json:"field_char_control_deck_type"`
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
	if s == nil || s.Version != versionconfig.State() {
		return errors.New("player: wrong starter version")
	}
	seenRecipes := map[uint64]bool{}
	for _, id := range s.CookingRecipes {
		if id == 0 || id > 0x7fffffff || seenRecipes[id] {
			return errors.New("player: invalid initial cooking recipe")
		}
		seenRecipes[id] = true
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
