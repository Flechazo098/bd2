package gamedata

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"bd2server/internal/server/protocol/wire"
)

// MailCatalogItem is one attachment an operator can send through the dynamic
// mail spool. Category is a stable key; presentation text belongs to clients.
type MailCatalogItem struct {
	ElementType uint64   `json:"element_type"`
	ID          uint64   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Aliases     []string `json:"aliases,omitempty"`
	// Set when a deterministic RandomBox wraps this item: the catalog offers
	// the contained item directly instead of the box.
	SourceBoxes  []uint64 `json:"source_boxes,omitempty"`
	OriginalName string   `json:"original_name,omitempty"`
	BoxName      string   `json:"box_name,omitempty"`
	ProductName  string   `json:"product_name,omitempty"`

	sourceTable  string
	resourceType uint64
}

type mailItemSource struct {
	table               string
	elementType         uint64
	idField, nameField  int
	category, textTable string
}

// ItemDBInfo-backed tables and their text namespaces. Item names use distinct
// GameData text tables, so the owning namespace is part of this mapping.
var mailItemSources = []mailItemSource{
	{"ResourceTable", 8, 4, 7, "resource", "NameTextTable"},
	{"FoodTable", 5, 7, 10, "food", "NameTextTable"},
	{"CookingTable", 7, 3, 11, "cooking", "NameTextTable"},
	{"RandomBoxTable", 9, 4, 7, "random_box", "RandomBoxTextTable"},
	{"QuestItemTable", 13, 2, 5, "quest_item", "NameTextTable"},
	{"UseItemTable", 14, 3, 6, "use_item", "NameTextTable"},
	{"CollectionTable", 17, 3, 7, "collection", "NameTextTable"},
	{"MyRoomItemTable", 27, 7, 17, "my_room", "NameTextTable"},
	{"InstantUseItemTable", 29, 1, 3, "instant_use", "NameTextTable"},
}

// MailCurrencyTypes are the account currencies whose mail claim is credited
// directly; their reward ID is zero.
var MailCurrencyTypes = []uint64{2, 3, 4, 12, 20}

// LoadMailCatalog returns every safe direct-mail attachment in GameData.
func LoadMailCatalog(root, version string) ([]MailCatalogItem, error) {
	var items []MailCatalogItem
	err := WithDatabase(root, version, "common", func(db *sql.DB) error {
		var err error
		items, err = loadMailCatalog(db)
		return err
	})
	return items, err
}

func loadMailCatalog(db *sql.DB) ([]MailCatalogItem, error) {
	names := map[string]map[uint64]string{}
	for _, table := range []string{"NameTextTable", "RandomBoxTextTable", "LocalTextTable"} {
		values, err := localizedNames(db, table)
		if err != nil {
			return nil, err
		}
		names[table] = values
	}
	var items []MailCatalogItem
	for _, source := range mailItemSources {
		err := eachRow(db, source.table, "ORDER BY id", func(rowID uint64, proto []byte) error {
			id := singleVarint(proto, source.idField)
			if id == 0 {
				id = rowID
			}
			item := MailCatalogItem{ElementType: source.elementType, ID: id, Category: source.category, sourceTable: source.table,
				Name: names[source.textTable][singleVarint(proto, source.nameField)]}
			if source.table == "ResourceTable" {
				item.resourceType = singleVarint(proto, 13)
			}
			items = append(items, item)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	err := eachRow(db, "CurrencyTable", "ORDER BY id", func(rowID uint64, proto []byte) error {
		elementType := singleVarint(proto, 3)
		if elementType == 0 {
			elementType = rowID
		}
		if slices.Contains(MailCurrencyTypes, elementType) {
			items = append(items, MailCatalogItem{ElementType: elementType, Category: "currency", Name: names["NameTextTable"][singleVarint(proto, 5)]})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("gamedata: mail catalog tables are empty")
	}
	fixedBoxes, err := fixedRandomBoxes(db, items)
	if err != nil {
		return nil, err
	}
	productNames := map[uint64]map[string]int{}
	err = eachRow(db, "CashProductTable", "", func(_ uint64, proto []byte) error {
		box := singleVarint(proto, 14)
		name := names["LocalTextTable"][singleVarint(proto, 11)]
		if _, fixed := fixedBoxes[box]; box != 0 && name != "" && fixed {
			if productNames[box] == nil {
				productNames[box] = map[string]int{}
			}
			productNames[box][name]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return directMailItems(items, fixedBoxes, productNames), nil
}

type boxReward struct{ elementType, id uint64 }

// fixedRandomBoxes follows only one-entry RewardGroupTable definitions: a
// deterministic GameData relationship rather than an invented unpack result.
func fixedRandomBoxes(db *sql.DB, items []MailCatalogItem) (map[uint64]boxReward, error) {
	known := map[boxReward]bool{}
	for _, item := range items {
		known[boxReward{item.ElementType, item.ID}] = true
	}
	groups := map[uint64]boxReward{}
	err := eachRow(db, "RewardGroupTable", "", func(groupID uint64, proto []byte) error {
		ids, idErr := packedInts(proto, 5)
		types, typeErr := packedInts(proto, 6)
		counts, countErr := packedInts(proto, 4)
		if err := errors.Join(idErr, typeErr, countErr); err != nil {
			return err
		}
		if len(ids) != 1 || len(types) != 1 || len(counts) != 1 || types[0] == 0 || counts[0] == 0 {
			return nil
		}
		currency := slices.Contains(MailCurrencyTypes, types[0])
		if currency != (ids[0] == 0) {
			return nil
		}
		groups[groupID] = boxReward{types[0], ids[0]}
		return nil
	})
	if err != nil {
		return nil, err
	}
	boxes := map[uint64]boxReward{}
	err = eachRow(db, "RandomBoxTable", "", func(boxID uint64, proto []byte) error {
		if reward, ok := groups[singleVarint(proto, 9)]; ok && known[reward] {
			boxes[boxID] = reward
		}
		return nil
	})
	return boxes, err
}

// directMailItems hides deterministic boxes and exposes their contained item
// directly. The quantity an operator enters is the final material count.
func directMailItems(items []MailCatalogItem, fixedBoxes map[uint64]boxReward, productNames map[uint64]map[string]int) []MailCatalogItem {
	index := map[boxReward]int{}
	for i, item := range items {
		index[boxReward{item.ElementType, item.ID}] = i
	}
	type wrapping struct {
		boxes        []uint64
		boxNames     map[string]int
		productNames map[string]int
	}
	wrapped := map[int]*wrapping{}
	boxIDs := make([]uint64, 0, len(fixedBoxes))
	for id := range fixedBoxes {
		boxIDs = append(boxIDs, id)
	}
	slices.Sort(boxIDs)
	for _, boxID := range boxIDs {
		target, ok := index[fixedBoxes[boxID]]
		box, boxOK := index[boxReward{9, boxID}]
		if !ok || !boxOK {
			continue
		}
		w := wrapped[target]
		if w == nil {
			w = &wrapping{boxNames: map[string]int{}, productNames: map[string]int{}}
			wrapped[target] = w
		}
		w.boxes = append(w.boxes, boxID)
		if usableName(items[box].Name) {
			w.boxNames[items[box].Name]++
		}
		for name, count := range productNames[boxID] {
			if usableName(name) {
				w.productNames[name] += count
			}
		}
	}
	for target, w := range wrapped {
		item := &items[target]
		original := item.Name
		product := mostFrequent(w.productNames)
		box := ""
		if len(w.boxNames) == 1 {
			for name := range w.boxNames {
				box = name
			}
		}
		// A single agreed wrapper name is the most specific label for a
		// generic resource row; product names are only a fallback because
		// some describe expiry or conversion behavior.
		if usableName(original) && box != "" && box != original {
			item.Name = box
		} else if !usableName(original) && product != "" {
			item.Name = product
		}
		for _, alias := range []string{original, box, product} {
			if usableName(alias) && alias != item.Name && !slices.Contains(item.Aliases, alias) {
				item.Aliases = append(item.Aliases, alias)
			}
		}
		item.SourceBoxes = w.boxes
		if original != item.Name && usableName(original) {
			item.OriginalName = original
		}
		if box != item.Name {
			item.BoxName = box
		}
		if product != item.Name {
			item.ProductName = product
		}
	}
	safe := items[:0]
	for _, item := range items {
		// RandomBox needs a second protocol and its entered count is not the
		// final reward count. ResourceTable Type=2 rows are field-object
		// presentation sentinels that crash the client's item popup.
		if item.ElementType == 9 || item.sourceTable == "ResourceTable" && item.resourceType == 2 || !usableName(item.Name) {
			continue
		}
		safe = append(safe, item)
	}
	return safe
}

func mostFrequent(values map[string]int) string {
	best := ""
	for value, count := range values {
		if best == "" || count > values[best] || count == values[best] &&
			(utf8.RuneCountInString(value) < utf8.RuneCountInString(best) || utf8.RuneCountInString(value) == utf8.RuneCountInString(best) && value > best) {
			best = value
		}
	}
	return best
}

func usableName(value string) bool {
	return value != "" && value != "（不使用）" && value != "(不使用)"
}

// localizedNames reads the common localized-text protobuf shape.
func localizedNames(db *sql.DB, table string) (map[uint64]string, error) {
	names := map[uint64]string{}
	err := eachRow(db, table, "", func(rowID uint64, proto []byte) error {
		textID := singleVarint(proto, 2)
		if textID == 0 {
			textID = rowID
		}
		for _, field := range []int{4, 5, 3} {
			if name := singleText(proto, field); name != "" {
				names[textID] = name
				break
			}
		}
		return nil
	})
	return names, err
}

func eachRow(db *sql.DB, table, order string, visit func(uint64, []byte) error) error {
	rows, err := db.Query(fmt.Sprintf(`SELECT id, ProtoBuf FROM "%s" %s`, strings.ReplaceAll(table, `"`, `""`), order))
	if err != nil {
		return fmt.Errorf("gamedata: read %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return fmt.Errorf("gamedata: read %s: %w", table, err)
		}
		if id < 0 {
			return fmt.Errorf("gamedata: %s has negative id %d", table, id)
		}
		if err := visit(uint64(id), proto); err != nil {
			return fmt.Errorf("gamedata: %s row %d: %w", table, id, err)
		}
	}
	return rows.Err()
}

// singleVarint returns a scalar varint field, or zero when it is absent,
// repeated or not encoded as a plain varint.
func singleVarint(proto []byte, number int) uint64 {
	var value uint64
	count := 0
	_ = wire.Walk(proto, func(field wire.Field) error {
		if field.Number == number {
			count++
			if field.Type == 0 {
				value, _ = binary.Uvarint(field.Value)
			} else {
				count = 2
			}
		}
		return nil
	})
	if count != 1 {
		return 0
	}
	return value
}

func singleText(proto []byte, number int) string {
	var value []byte
	count := 0
	_ = wire.Walk(proto, func(field wire.Field) error {
		if field.Number == number {
			count++
			if field.Type == 2 {
				value = field.Value
			} else {
				count = 2
			}
		}
		return nil
	})
	if count != 1 || !utf8.Valid(value) {
		return ""
	}
	return string(value)
}

// SortMailCatalog orders items by category position and ID for stable display.
func SortMailCatalog(items []MailCatalogItem) {
	order := map[string]int{"currency": 0}
	for i, source := range mailItemSources {
		order[source.category] = i + 1
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if order[a.Category] != order[b.Category] {
			return order[a.Category] < order[b.Category]
		}
		if a.ElementType != b.ElementType {
			return a.ElementType < b.ElementType
		}
		return a.ID < b.ID
	})
}
