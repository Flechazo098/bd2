package gamedata

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// InventorySlotRule is one of the four capacities represented directly in
// UserDBInfo. Prices and limits belong to the current GameData version.
type InventorySlotRule struct {
	Default   uint64
	Maximum   uint64
	PriceType uint64
	BasePrice uint64
	MaxPrice  uint64
}

type InventorySlotDesign struct {
	Items            InventorySlotRule
	Storage          InventorySlotRule
	Equipment        InventorySlotRule
	EquipmentStorage InventorySlotRule
}

func LoadInventorySlotDesign(root, version string) (*InventorySlotDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-inventory-slots-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadInventorySlotDesign(db)
}

func loadInventorySlotDesign(db *sql.DB) (*InventorySlotDesign, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, fmt.Errorf("gamedata: inventory slot defaults: %w", err)
	}
	read := func(field int) (uint64, error) {
		values, err := packedInts(raw, field)
		if err != nil || len(values) != 1 || values[0] == 0 {
			return 0, fmt.Errorf("gamedata: inventory slot field %d is missing or invalid", field)
		}
		return values[0], nil
	}
	maxPrice, err := read(79)
	if err != nil {
		return nil, err
	}
	makeRule := func(defaultField, maximumField, priceTypeField, basePriceField int) (InventorySlotRule, error) {
		initial, err := read(defaultField)
		if err != nil {
			return InventorySlotRule{}, err
		}
		maximum, err := read(maximumField)
		if err != nil {
			return InventorySlotRule{}, err
		}
		priceType, err := read(priceTypeField)
		if err != nil {
			return InventorySlotRule{}, err
		}
		basePrice, err := read(basePriceField)
		if err != nil {
			return InventorySlotRule{}, err
		}
		rule := InventorySlotRule{Default: initial, Maximum: maximum, PriceType: priceType, BasePrice: basePrice, MaxPrice: maxPrice}
		if rule.Default > rule.Maximum || rule.PriceType != 4 {
			return InventorySlotRule{}, errors.New("gamedata: unsupported inventory slot rule")
		}
		return rule, nil
	}
	design := &InventorySlotDesign{}
	if design.Items, err = makeRule(34, 80, 14, 13); err != nil {
		return nil, err
	}
	if design.Storage, err = makeRule(37, 83, 16, 15); err != nil {
		return nil, err
	}
	if design.Equipment, err = makeRule(30, 73, 10, 9); err != nil {
		return nil, err
	}
	if design.EquipmentStorage, err = makeRule(31, 74, 12, 11); err != nil {
		return nil, err
	}
	return design, nil
}
