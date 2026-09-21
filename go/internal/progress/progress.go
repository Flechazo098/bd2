// Package progress owns mutable player progress that is independent of the
// HTTP, encryption, and captured-fixture compatibility layers.
package progress

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"bd2server/internal/wire"
)

var (
	ErrInvalidPosition = errors.New("progress: invalid saved position")
	ErrInvalidTutorial = errors.New("progress: invalid tutorial id")
	ErrInvalidQuest    = errors.New("progress: invalid quest update")
)

type Vector3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Position struct {
	MapID              int             `json:"MapId"`
	PlayerPosition     Vector3         `json:"PlayerPosition"`
	ColleaguePositions json.RawMessage `json:"ColleaguePositions"`
}

type SavedPosition struct {
	PackID   int
	Position Position
	RawJSON  string
}

type QuestProgress struct {
	QuestID int
	PackID  int
	Values  []int
}

type Store struct {
	mu        sync.RWMutex
	path      string
	position  SavedPosition
	tutorials map[int]struct{}
	quests    map[string]QuestProgress
	cleared   map[string]struct{}
}

func NewStore() *Store {
	return &Store{tutorials: make(map[int]struct{}), quests: make(map[string]QuestProgress), cleared: make(map[string]struct{})}
}

type snapshot struct {
	Version   int                        `json:"version,omitempty"`
	Position  SavedPosition              `json:"position"`
	Tutorials []int                      `json:"tutorials"`
	Quests    map[string]QuestProgress   `json:"quests"`
	Cleared   map[string]json.RawMessage `json:"cleared_quests"`
}

const snapshotVersion = 2

func questKey(packID, questID int) string {
	return strconv.Itoa(packID) + ":" + strconv.Itoa(questID)
}

func parseQuestKey(key string) (int, int, bool) {
	left, right, found := strings.Cut(key, ":")
	if !found {
		return 0, 0, false
	}
	packID, packErr := strconv.Atoi(left)
	questID, questErr := strconv.Atoi(right)
	return packID, questID, packErr == nil && questErr == nil && packID > 0 && questID > 0
}

// OpenStore recovers player progress from one local JSON save. Nothing is
// created on disk until a validated state change is committed.
func OpenStore(path string) (*Store, error) {
	s := NewStore()
	if path == "" {
		return nil, errors.New("progress: empty save path")
	}
	s.path = filepath.Clean(path)
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("progress: read save: %w", err)
	}
	var state snapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("progress: malformed save: %w", err)
	}
	if state.Version != 0 && state.Version != snapshotVersion {
		return nil, fmt.Errorf("progress: unsupported save version %d", state.Version)
	}
	legacy := state.Version == 0
	s.position = state.Position
	for _, id := range state.Tutorials {
		if id <= 0 {
			return nil, errors.New("progress: invalid saved tutorial")
		}
		s.tutorials[id] = struct{}{}
	}
	for key, quest := range state.Quests {
		if quest.QuestID <= 0 || quest.PackID <= 0 {
			return nil, errors.New("progress: invalid saved quest")
		}
		if legacy {
			id, err := strconv.Atoi(key)
			if err != nil || id != quest.QuestID {
				return nil, errors.New("progress: invalid saved quest")
			}
		} else {
			packID, questID, ok := parseQuestKey(key)
			if !ok || packID != quest.PackID || questID != quest.QuestID {
				return nil, errors.New("progress: invalid saved quest key")
			}
		}
		s.quests[questKey(quest.PackID, quest.QuestID)] = quest
	}
	for key, raw := range state.Cleared {
		if legacy {
			questID, err := strconv.Atoi(key)
			var packID int
			if err != nil || json.Unmarshal(raw, &packID) != nil || questID <= 0 || packID <= 0 {
				return nil, errors.New("progress: invalid cleared quest")
			}
			s.cleared[questKey(packID, questID)] = struct{}{}
			continue
		}
		packID, questID, ok := parseQuestKey(key)
		var cleared bool
		if !ok || json.Unmarshal(raw, &cleared) != nil || !cleared {
			return nil, errors.New("progress: invalid cleared quest key")
		}
		s.cleared[questKey(packID, questID)] = struct{}{}
	}
	if legacy {
		// Install the unambiguous pack+quest schema immediately. Pack 21 and 22
		// both number quests from one, so waiting until the first pack22 update
		// would risk overwriting valid pack21 progress.
		if err := s.commit(s.position, s.tutorials, s.quests, s.cleared); err != nil {
			return nil, fmt.Errorf("progress: migrate legacy save: %w", err)
		}
	}
	return s, nil
}

// commit writes a complete snapshot before exposing the new state. Caller
// holds mu; failure leaves the in-memory player state unchanged.
func (s *Store) commit(position SavedPosition, tutorials map[int]struct{}, quests map[string]QuestProgress, cleared map[string]struct{}) error {
	if s.path != "" {
		state := snapshot{Version: snapshotVersion, Position: position, Quests: quests, Cleared: make(map[string]json.RawMessage, len(cleared))}
		for id := range tutorials {
			state.Tutorials = append(state.Tutorials, id)
		}
		sort.Ints(state.Tutorials)
		for key := range cleared {
			state.Cleared[key] = json.RawMessage("true")
		}
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		dir := filepath.Dir(s.path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("progress: create save dir: %w", err)
		}
		file, err := os.CreateTemp(dir, ".progress-*.tmp")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("progress: write save: %w", err)
		}
		if err := os.Rename(file.Name(), s.path); err != nil {
			return fmt.Errorf("progress: install save: %w", err)
		}
	}
	s.position, s.tutorials, s.quests, s.cleared = position, tutorials, quests, cleared
	return nil
}

// UpdateQuest consumes QuestUpdateRequest: field 1 seq, field 2 quest_id,
// field 3 pack_id, and repeated packed/unpacked int32 field 4 quest_value.
// It returns the quest id that the response must echo as update_quest_id.
func (s *Store) UpdateQuest(request []byte) (int, error) {
	questID, found, err := wire.Varint(request, 2)
	if err != nil || !found || questID == 0 || questID > uint64(^uint32(0)>>1) {
		return 0, fmt.Errorf("%w: quest id", ErrInvalidQuest)
	}
	packID, found, err := wire.Varint(request, 3)
	if err != nil || !found || packID == 0 || packID > uint64(^uint32(0)>>1) {
		return 0, fmt.Errorf("%w: pack id", ErrInvalidQuest)
	}
	values := make([]int, 0, 4)
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 4 {
			return nil
		}
		switch field.Type {
		case 0:
			value, count := binary.Uvarint(field.Value)
			if count <= 0 || value > uint64(^uint32(0)>>1) {
				return ErrInvalidQuest
			}
			values = append(values, int(value))
		case 2:
			for remaining := field.Value; len(remaining) > 0; {
				value, count := binary.Uvarint(remaining)
				if count <= 0 || value > uint64(^uint32(0)>>1) {
					return ErrInvalidQuest
				}
				values = append(values, int(value))
				remaining = remaining[count:]
			}
		default:
			return ErrInvalidQuest
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("%w: quest values", ErrInvalidQuest)
	}

	progress := QuestProgress{QuestID: int(questID), PackID: int(packID), Values: append([]int(nil), values...)}
	s.mu.Lock()
	defer s.mu.Unlock()
	quests := make(map[string]QuestProgress, len(s.quests)+1)
	for key, current := range s.quests {
		quests[key] = current
	}
	quests[questKey(progress.PackID, progress.QuestID)] = progress
	if err := s.commit(s.position, s.tutorials, quests, s.cleared); err != nil {
		return 0, err
	}
	return progress.QuestID, nil
}

// SaveUserPosition consumes SaveUserPositionRequest:
// field 1 seq, field 2 pack_id, field 3 pack_position JSON.
func (s *Store) SaveUserPosition(request []byte) error {
	packID, found, err := wire.Varint(request, 2)
	if err != nil || !found || packID == 0 || packID > uint64(^uint32(0)>>1) {
		return fmt.Errorf("%w: pack id", ErrInvalidPosition)
	}
	raw, found, err := wire.Bytes(request, 3)
	if err != nil || !found || len(raw) == 0 || len(raw) > 64<<10 {
		return fmt.Errorf("%w: position JSON", ErrInvalidPosition)
	}
	var position Position
	if err := json.Unmarshal(raw, &position); err != nil || position.MapID <= 0 {
		return fmt.Errorf("%w: decode JSON", ErrInvalidPosition)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(SavedPosition{PackID: int(packID), Position: position, RawJSON: string(raw)}, s.tutorials, s.quests, s.cleared)
}

// ClearTutorial consumes TutorialClearRequest: field 1 seq, field 2 id.
// Repeated requests are idempotent.
func (s *Store) ClearTutorial(request []byte) error {
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 || id > uint64(^uint32(0)>>1) {
		return ErrInvalidTutorial
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tutorials := make(map[int]struct{}, len(s.tutorials)+1)
	for cleared := range s.tutorials {
		tutorials[cleared] = struct{}{}
	}
	tutorials[int(id)] = struct{}{}
	return s.commit(s.position, tutorials, s.quests, s.cleared)
}

func (s *Store) Position() (SavedPosition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.position, s.position.PackID != 0
}

func (s *Store) TutorialCleared(id int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, found := s.tutorials[id]
	return found
}

func (s *Store) Tutorials() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int, 0, len(s.tutorials))
	for id := range s.tutorials {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// Handle exposes the persisted tutorial set through TutorialInfoResponse.
// TutorialClear itself remains in session so both routes share this store.
func (s *Store) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/TutorialInfo" {
		return 0, nil, false, nil
	}
	if seq, found, err := wire.Varint(request, 1); err != nil || !found || seq == 0 {
		return 0, nil, true, ErrInvalidTutorial
	}
	var packed []byte
	for _, id := range s.Tutorials() {
		packed = binary.AppendUvarint(packed, uint64(id))
	}
	var response []byte
	if len(packed) > 0 {
		response = wire.AppendBytes(response, 1, packed)
	}
	return 101, response, true, nil
}

func (s *Store) Quest(id int) (QuestProgress, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var quest QuestProgress
	found := false
	for _, candidate := range s.quests {
		if candidate.QuestID != id {
			continue
		}
		if found {
			// A quest ID alone becomes ambiguous once more than one pack has
			// progress. Callers that know the pack must use QuestInPack.
			return QuestProgress{}, false
		}
		quest, found = candidate, true
	}
	quest.Values = append([]int(nil), quest.Values...)
	return quest, found
}

func (s *Store) QuestInPack(questID, packID int) (QuestProgress, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	quest, found := s.quests[questKey(packID, questID)]
	quest.Values = append([]int(nil), quest.Values...)
	return quest, found
}

// ClearQuest atomically records a completed quest. Quest identity is the
// (pack, quest) pair because every story pack starts numbering from one.
func (s *Store) ClearQuest(questID, packID int) error {
	if questID <= 0 || packID <= 0 {
		return ErrInvalidQuest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := questKey(packID, questID)
	if _, ok := s.cleared[key]; ok {
		return nil
	}
	cleared := make(map[string]struct{}, len(s.cleared)+1)
	for current := range s.cleared {
		cleared[current] = struct{}{}
	}
	cleared[key] = struct{}{}
	return s.commit(s.position, s.tutorials, s.quests, cleared)
}

func (s *Store) QuestCleared(questID, packID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, found := s.cleared[questKey(packID, questID)]
	return found
}

// ClearedQuests returns sorted quest IDs for one pack. The detached slice is
// safe for response construction without holding the store lock.
func (s *Store) ClearedQuests(packID int) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int, 0, len(s.cleared))
	for key := range s.cleared {
		currentPack, questID, ok := parseQuestKey(key)
		if ok && currentPack == packID {
			ids = append(ids, questID)
		}
	}
	sort.Ints(ids)
	return ids
}
