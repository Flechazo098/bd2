package accountstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Problem is one stable, machine-readable player-state invariant violation.
// The codes and messages preserve the effective rules of the former external
// state validator.
type Problem struct {
	Code       string
	Path       []string
	Message    string
	RelatedIDs []uint64
}

type validationSnapshot struct {
	formatVersion      int
	clientVersion      string
	gameDataVersion    string
	itemIndices        []uint64
	equipmentIndices   []uint64
	characterIndices   []uint64
	costumeIndices     []uint64
	itemNextIndex      uint64
	equipmentNextIndex uint64
	characterNextIndex uint64
	costumeNextIndex   uint64
	equipmentGrants    []namedIndex
	equipmentUsers     []namedIndex
	quests             []questKey
	cleared            []questKey
	granted            map[string]bool
	itemGrants         []indexedGrant
}

type namedIndex struct {
	identity string
	index    uint64
}

type indexedGrant struct {
	identity string
	indices  []uint64
}

type questKey struct {
	pack  uint32
	quest uint32
}

func validateSnapshot(snapshot validationSnapshot) []Problem {
	var problems []Problem
	require := func(condition bool, code string, path []string, message string, related ...uint64) {
		if !condition {
			problems = append(problems, Problem{Code: code, Path: path, Message: message, RelatedIDs: related})
		}
	}
	require(snapshot.formatVersion == 1, "snapshot.format_version", []string{"format_version"}, "snapshot format version must be 1")
	require(snapshot.clientVersion != "", "snapshot.client_version", []string{"client_version"}, "client version is required")
	require(snapshot.gameDataVersion != "", "snapshot.game_data_version", []string{"game_data_version"}, "game data version is required")
	problems = append(problems, duplicateIndexProblems("inventory.duplicate_index", []string{"inventory", "items"}, snapshot.itemIndices)...)
	problems = append(problems, duplicateIndexProblems("equipment.duplicate_index", []string{"equipment", "equipment"}, snapshot.equipmentIndices)...)
	problems = append(problems, duplicateIndexProblems("characters.duplicate_index", []string{"characters"}, snapshot.characterIndices)...)
	problems = append(problems, duplicateIndexProblems("costumes.duplicate_index", []string{"collection", "costumes"}, snapshot.costumeIndices)...)
	problems = append(problems, nextIndexProblems("inventory.next_index", []string{"inventory", "next_index"}, snapshot.itemNextIndex, snapshot.itemIndices)...)
	problems = append(problems, nextIndexProblems("equipment.next_index", []string{"equipment", "next_index"}, snapshot.equipmentNextIndex, snapshot.equipmentIndices)...)
	problems = append(problems, nextIndexProblems("collection.next_character_index", []string{"collection", "next_character_index"}, snapshot.characterNextIndex, snapshot.characterIndices)...)
	problems = append(problems, nextIndexProblems("collection.next_costume_index", []string{"collection", "next_costume_index"}, snapshot.costumeNextIndex, snapshot.costumeIndices)...)

	ownedEquipment := make(map[uint64]bool, len(snapshot.equipmentIndices))
	for _, index := range snapshot.equipmentIndices {
		ownedEquipment[index] = true
	}
	for _, grant := range snapshot.equipmentGrants {
		if grant.index == 0 || !ownedEquipment[grant.index] {
			problems = append(problems, Problem{
				Code: "equipment.grant_missing_equipment", Path: []string{"equipment", "grants", grant.identity},
				Message: "equipment grant must point to an owned equipment instance", RelatedIDs: []uint64{grant.index},
			})
		}
	}
	ownedCharacters := make(map[uint64]bool, len(snapshot.characterIndices))
	for _, index := range snapshot.characterIndices {
		ownedCharacters[index] = true
	}
	for _, entry := range snapshot.equipmentUsers {
		if entry.index != 0 && !ownedCharacters[entry.index] {
			problems = append(problems, Problem{
				Code: "equipment.unknown_user", Path: []string{"equipment", "equipment"},
				Message: "equipped character must be owned", RelatedIDs: []uint64{parseIdentityIndex(entry.identity), entry.index},
			})
		}
	}
	problems = append(problems, keyProblems("progress.invalid_quest_key", []string{"progress", "quests"}, snapshot.quests)...)
	problems = append(problems, keyProblems("progress.invalid_cleared_key", []string{"progress", "cleared_quests"}, snapshot.cleared)...)
	problems = append(problems, duplicateKeyProblems("progress.duplicate_quest_key", []string{"progress", "quests"}, snapshot.quests)...)
	problems = append(problems, duplicateKeyProblems("progress.duplicate_cleared_key", []string{"progress", "cleared_quests"}, snapshot.cleared)...)
	for _, grant := range snapshot.itemGrants {
		path := []string{"inventory", "grant_items", grant.identity}
		if grant.identity == "" || !snapshot.granted[grant.identity] {
			problems = append(problems, Problem{Code: "inventory.grant_without_marker", Path: path, Message: "historical grant must have an idempotency marker"})
		}
		for _, index := range grant.indices {
			if index == 0 || index >= snapshot.itemNextIndex {
				problems = append(problems, Problem{Code: "inventory.grant_invalid_index", Path: path, Message: "issued index must be nonzero and below next_index", RelatedIDs: []uint64{index}})
			}
		}
		problems = append(problems, duplicateIndexProblems("inventory.grant_duplicate_index", path, grant.indices)...)
	}
	return problems
}

func duplicateIndexProblems(code string, path []string, values []uint64) []Problem {
	counts := make(map[uint64]int, len(values))
	for _, value := range values {
		counts[value]++
	}
	duplicates := make([]uint64, 0)
	for value, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, value)
		}
	}
	sort.Slice(duplicates, func(i, j int) bool { return duplicates[i] < duplicates[j] })
	problems := make([]Problem, 0, len(duplicates))
	for _, value := range duplicates {
		problems = append(problems, Problem{Code: code, Path: path, Message: "duplicate inventory identity", RelatedIDs: []uint64{value}})
	}
	return problems
}

func nextIndexProblems(code string, path []string, next uint64, values []uint64) []Problem {
	if len(values) == 0 {
		return nil
	}
	maximum := values[0]
	for _, value := range values[1:] {
		maximum = max(maximum, value)
	}
	if next > maximum {
		return nil
	}
	return []Problem{{Code: code, Path: path, Message: "next index must be greater than every existing index", RelatedIDs: []uint64{next}}}
}

func keyProblems(code string, path []string, values []questKey) []Problem {
	var problems []Problem
	for _, value := range values {
		if value.pack == 0 || value.quest == 0 {
			problems = append(problems, Problem{Code: code, Path: path, Message: "pack_id and quest_id must both be positive", RelatedIDs: []uint64{uint64(value.pack), uint64(value.quest)}})
		}
	}
	return problems
}

func duplicateKeyProblems(code string, path []string, values []questKey) []Problem {
	counts := make(map[questKey]int, len(values))
	for _, value := range values {
		counts[value]++
	}
	var duplicates []questKey
	for value, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, value)
		}
	}
	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].pack != duplicates[j].pack {
			return duplicates[i].pack < duplicates[j].pack
		}
		return duplicates[i].quest < duplicates[j].quest
	})
	problems := make([]Problem, 0, len(duplicates))
	for _, value := range duplicates {
		problems = append(problems, Problem{Code: code, Path: path, Message: "duplicate pack/quest pair", RelatedIDs: []uint64{uint64(value.pack), uint64(value.quest)}})
	}
	return problems
}

func parseIdentityIndex(identity string) uint64 {
	index, _ := strconv.ParseUint(identity, 10, 64)
	return index
}

// Validate checks the repository snapshot. When called inside BeginOperation,
// it reads the same SQLite transaction as all startup writes and migrations.
func (r *Repository) Validate() ([]Problem, error) {
	r.activeMu.RLock()
	if r.active != nil {
		problems, err := r.active.Validate()
		r.activeMu.RUnlock()
		return problems, err
	}
	r.activeMu.RUnlock()
	tx, err := r.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return tx.Validate()
}

func (t *Tx) Validate() ([]Problem, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil, ErrClosed
	}
	return validateState(t.tx)
}

func validateState(tx *sql.Tx) ([]Problem, error) {
	snapshot := validationSnapshot{formatVersion: 1, clientVersion: "sqlite", gameDataVersion: "sqlite", granted: make(map[string]bool)}
	var err error
	if snapshot.itemNextIndex, err = coreUint(tx, "items", "next_index"); err != nil {
		return nil, err
	}
	if snapshot.equipmentNextIndex, err = coreUint(tx, "equipment", "next_index"); err != nil {
		return nil, err
	}
	if snapshot.characterNextIndex, snapshot.costumeNextIndex, err = collectionNextIndices(tx); err != nil {
		return nil, err
	}
	if snapshot.itemIndices, _, err = indexedEntries(tx, "items", "items", false); err != nil {
		return nil, err
	}
	if snapshot.equipmentIndices, snapshot.equipmentUsers, err = indexedEntries(tx, "equipment", "equipment", true); err != nil {
		return nil, err
	}
	baseCharacters, _, err := indexedEntries(tx, "characters", "characters", false)
	if err != nil {
		return nil, err
	}
	acquiredCharacters, _, err := indexedEntries(tx, "collection", "characters", false)
	if err != nil {
		return nil, err
	}
	snapshot.characterIndices = append(baseCharacters, acquiredCharacters...)
	if snapshot.costumeIndices, _, err = indexedEntries(tx, "collection", "costumes", false); err != nil {
		return nil, err
	}
	if snapshot.equipmentGrants, err = namedUintEntries(tx, "equipment", "granted"); err != nil {
		return nil, err
	}
	if snapshot.granted, err = boolEntries(tx, "items", "granted"); err != nil {
		return nil, err
	}
	if snapshot.itemGrants, err = indexedGrantEntries(tx); err != nil {
		return nil, err
	}
	if snapshot.quests, snapshot.cleared, err = progressKeys(tx); err != nil {
		return nil, err
	}
	return validateSnapshot(snapshot), nil
}

func loadCore(tx *sql.Tx, domain string) (map[string]json.RawMessage, bool, error) {
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM domain_state WHERE name = ?`, domain).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("accountstate: load %s for validation: %w", domain, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, false, fmt.Errorf("accountstate: decode %s for validation", domain)
	}
	return fields, true, nil
}

func coreUint(tx *sql.Tx, domain, field string) (uint64, error) {
	fields, found, err := loadCore(tx, domain)
	if err != nil || !found {
		return 0, err
	}
	var value uint64
	if raw, ok := fields[field]; !ok || json.Unmarshal(raw, &value) != nil {
		return 0, fmt.Errorf("accountstate: invalid %s.%s", domain, field)
	}
	return value, nil
}

func collectionNextIndices(tx *sql.Tx) (uint64, uint64, error) {
	fields, found, err := loadCore(tx, "collection")
	if err != nil || !found {
		return 0, 0, err
	}
	var characters, costumes uint64
	if json.Unmarshal(fields["next_character_index"], &characters) != nil || json.Unmarshal(fields["next_costume_index"], &costumes) != nil {
		return 0, 0, errors.New("accountstate: invalid collection next indices")
	}
	return characters, costumes, nil
}

func indexedEntries(tx *sql.Tx, domain, bucket string, readUseChar bool) ([]uint64, []namedIndex, error) {
	rows, err := tx.Query(`SELECT entry_key, payload FROM domain_entry WHERE domain_name = ? AND bucket = ? ORDER BY entry_key`, domain, bucket)
	if err != nil {
		return nil, nil, fmt.Errorf("accountstate: list %s.%s for validation: %w", domain, bucket, err)
	}
	defer rows.Close()
	var indices []uint64
	var users []namedIndex
	for rows.Next() {
		var key string
		var payload []byte
		if err := rows.Scan(&key, &payload); err != nil {
			return nil, nil, err
		}
		index, err := strconv.ParseUint(key, 10, 64)
		if err != nil || index == 0 || key != strconv.FormatUint(index, 10) {
			return nil, nil, fmt.Errorf("accountstate: invalid %s.%s entry key %q", domain, bucket, key)
		}
		var identity struct {
			Index   uint64 `json:"inven_index"`
			UseChar uint64 `json:"use_char"`
		}
		if err := json.Unmarshal(payload, &identity); err != nil || identity.Index != index {
			return nil, nil, fmt.Errorf("accountstate: invalid %s.%s entry %q", domain, bucket, key)
		}
		indices = append(indices, identity.Index)
		if readUseChar {
			users = append(users, namedIndex{identity: key, index: identity.UseChar})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return indices, users, nil
}

func namedUintEntries(tx *sql.Tx, domain, bucket string) ([]namedIndex, error) {
	rows, err := tx.Query(`SELECT entry_key, payload FROM domain_entry WHERE domain_name = ? AND bucket = ? ORDER BY entry_key`, domain, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []namedIndex
	for rows.Next() {
		var entry namedIndex
		var payload []byte
		if err := rows.Scan(&entry.identity, &payload); err != nil {
			return nil, err
		}
		if entry.identity == "" || json.Unmarshal(payload, &entry.index) != nil {
			return nil, fmt.Errorf("accountstate: invalid %s.%s entry %q", domain, bucket, entry.identity)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func boolEntries(tx *sql.Tx, domain, bucket string) (map[string]bool, error) {
	rows, err := tx.Query(`SELECT entry_key, payload FROM domain_entry WHERE domain_name = ? AND bucket = ? ORDER BY entry_key`, domain, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make(map[string]bool)
	for rows.Next() {
		var key string
		var payload []byte
		if err := rows.Scan(&key, &payload); err != nil {
			return nil, err
		}
		if key == "" || string(payload) != "true" {
			return nil, fmt.Errorf("accountstate: invalid %s.%s entry %q", domain, bucket, key)
		}
		entries[key] = true
	}
	return entries, rows.Err()
}

func indexedGrantEntries(tx *sql.Tx) ([]indexedGrant, error) {
	rows, err := tx.Query(`SELECT entry_key, payload FROM domain_entry WHERE domain_name = 'items' AND bucket = 'grant_items' ORDER BY entry_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []indexedGrant
	for rows.Next() {
		var grant indexedGrant
		var payload []byte
		if err := rows.Scan(&grant.identity, &payload); err != nil {
			return nil, err
		}
		if json.Unmarshal(payload, &grant.indices) != nil {
			return nil, fmt.Errorf("accountstate: invalid items.grant_items entry %q", grant.identity)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func progressKeys(tx *sql.Tx) ([]questKey, []questKey, error) {
	fields, found, err := loadCore(tx, "progress")
	if err != nil || !found {
		return nil, nil, err
	}
	var quests map[string]struct {
		QuestID int `json:"QuestID"`
		PackID  int `json:"PackID"`
	}
	if json.Unmarshal(fields["quests"], &quests) != nil {
		return nil, nil, errors.New("accountstate: invalid progress.quests")
	}
	questKeys := make([]questKey, 0, len(quests))
	for key, value := range quests {
		parsed, parseErr := parseQuestKey(key)
		if parseErr != nil || value.PackID != int(parsed.pack) || value.QuestID != int(parsed.quest) {
			return nil, nil, fmt.Errorf("accountstate: invalid progress quest key %q", key)
		}
		questKeys = append(questKeys, parsed)
	}
	var cleared map[string]json.RawMessage
	if json.Unmarshal(fields["cleared_quests"], &cleared) != nil {
		return nil, nil, errors.New("accountstate: invalid progress.cleared_quests")
	}
	clearedKeys := make([]questKey, 0, len(cleared))
	for key, raw := range cleared {
		parsed, parseErr := parseQuestKey(key)
		var value bool
		if parseErr != nil || json.Unmarshal(raw, &value) != nil || !value {
			return nil, nil, fmt.Errorf("accountstate: invalid cleared quest key %q", key)
		}
		clearedKeys = append(clearedKeys, parsed)
	}
	sortQuestKeys(questKeys)
	sortQuestKeys(clearedKeys)
	return questKeys, clearedKeys, nil
}

func parseQuestKey(value string) (questKey, error) {
	left, right, found := strings.Cut(value, ":")
	if !found {
		return questKey{}, errors.New("missing separator")
	}
	pack, packErr := strconv.ParseUint(left, 10, 32)
	quest, questErr := strconv.ParseUint(right, 10, 32)
	if packErr != nil || questErr != nil {
		return questKey{}, errors.New("invalid integer")
	}
	return questKey{pack: uint32(pack), quest: uint32(quest)}, nil
}

func sortQuestKeys(keys []questKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pack != keys[j].pack {
			return keys[i].pack < keys[j].pack
		}
		return keys[i].quest < keys[j].quest
	})
}

func validationError(problems []Problem) error {
	parts := make([]string, 0, len(problems))
	for _, problem := range problems {
		parts = append(parts, problem.Code+": "+problem.Message)
	}
	return fmt.Errorf("accountstate: state validation rejected: %s", strings.Join(parts, "; "))
}
