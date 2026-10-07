// Package progress owns mutable player progress that is independent of the
// HTTP, encryption, and captured-fixture compatibility layers.
package progress

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
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
	Difficulty int
	PackID     int
	Position   Position
	RawJSON    string
}

type QuestProgress struct {
	QuestID    int
	Difficulty int
	PackID     int
	Values     []int
}

type QuestSelection struct {
	QuestID    int
	Difficulty int
	Option     int
}

type Store struct {
	activePackID   int
	startingPackID int
	selections     map[string]QuestSelection

	storage   stateio.Store
	position  SavedPosition
	tutorials map[int]struct{}
	quests    map[string]QuestProgress
	cleared   map[string]struct{}
}

func NewStore() *Store {
	return &Store{selections: make(map[string]QuestSelection), tutorials: make(map[int]struct{}), quests: make(map[string]QuestProgress), cleared: make(map[string]struct{})}
}

type snapshot struct {
	ActivePackID int                        `json:"active_pack_id"`
	StartPackID  int                        `json:"start_pack_id"`
	Version      int                        `json:"version"`
	Selections   map[string]QuestSelection  `json:"selections"`
	Position     SavedPosition              `json:"position"`
	Tutorials    []int                      `json:"tutorials"`
	Quests       map[string]QuestProgress   `json:"quests"`
	Cleared      map[string]json.RawMessage `json:"cleared_quests"`
}

const snapshotVersion = 3

func questDifficulty(level []int) int {
	if len(level) > 0 {
		return level[0]
	}
	return 0
}
func questKey(packID, questID int, level ...int) string {
	return strconv.Itoa(packID) + ":" + strconv.Itoa(questDifficulty(level)) + ":" + strconv.Itoa(questID)
}
func parseQuestKey(key string) (int, int, int, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	pack, e1 := strconv.Atoi(parts[0])
	level, e2 := strconv.Atoi(parts[1])
	quest, e3 := strconv.Atoi(parts[2])
	return pack, level, quest, e1 == nil && e2 == nil && e3 == nil && pack > 0 && level >= 0 && level <= 4 && quest > 0
}

// OpenStore recovers player progress from its domain snapshot.
func OpenStore(ctx command.Context, storage stateio.Store) (*Store, error) {
	s := NewStore()
	if storage == nil {
		return nil, errors.New("progress: nil storage")
	}
	s.storage = storage
	data, err := storage.Load(ctx.State, "progress")
	if err != nil {
		return nil, fmt.Errorf("progress: load state: %w", err)
	}
	if data == nil {
		return s, nil
	}
	if err := stateio.RequireExactJSONObject(data, "version", "position", "tutorials", "quests", "cleared_quests", "selections", "start_pack_id", "active_pack_id"); err != nil {
		return nil, fmt.Errorf("progress: incompatible save layout: %w", err)
	}
	var state snapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("progress: malformed save: %w", err)
	}
	if state.Version != snapshotVersion {
		return nil, fmt.Errorf("progress: unsupported save version %d", state.Version)
	}
	s.activePackID = state.ActivePackID
	if s.activePackID < 0 {
		return nil, ErrInvalidQuest
	}
	s.startingPackID = state.StartPackID
	if s.startingPackID < 0 {
		return nil, ErrInvalidQuest
	}
	if state.Position.Difficulty < 0 || state.Position.Difficulty > 4 {
		return nil, ErrInvalidPosition
	}
	s.position = state.Position
	for key, selection := range state.Selections {
		pack, err := strconv.Atoi(key)
		if err != nil || pack <= 0 || selection.QuestID < 0 || selection.Difficulty < 0 || selection.Difficulty > 4 || selection.Option < 0 {
			return nil, ErrInvalidQuest
		}
		s.selections[key] = selection
	}
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
		packID, difficulty, questID, ok := parseQuestKey(key)
		if !ok || packID != quest.PackID || questID != quest.QuestID || difficulty != quest.Difficulty {
			return nil, errors.New("progress: invalid saved quest key")
		}
		s.quests[questKey(quest.PackID, quest.QuestID, quest.Difficulty)] = quest
	}
	for key, raw := range state.Cleared {
		packID, difficulty, questID, ok := parseQuestKey(key)
		var cleared bool
		if !ok || json.Unmarshal(raw, &cleared) != nil || !cleared {
			return nil, errors.New("progress: invalid cleared quest key")
		}
		s.cleared[questKey(packID, questID, difficulty)] = struct{}{}
	}
	return s, nil
}

func (s *Store) EnsurePersisted(ctx command.Context) error {

	data, err := s.storage.Load(ctx.State, "progress")
	if err != nil {
		return err
	}
	if data != nil {
		return nil
	}
	return s.commit(ctx, s.position, s.tutorials, s.quests, s.cleared)
}

// commit writes a complete snapshot before exposing the new state. Caller
// holds mu; failure leaves the in-memory player state unchanged.
func (s *Store) commit(ctx command.Context, position SavedPosition, tutorials map[int]struct{}, quests map[string]QuestProgress, cleared map[string]struct{}) error {
	if s.storage != nil {
		state := snapshot{ActivePackID: s.activePackID, StartPackID: s.startingPackID, Selections: s.selections, Version: snapshotVersion, Position: position, Quests: quests, Cleared: make(map[string]json.RawMessage, len(cleared))}
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
		if err := s.storage.Save(ctx.State, "progress", data); err != nil {
			return fmt.Errorf("progress: save state: %w", err)
		}
	}
	s.position, s.tutorials, s.quests, s.cleared = position, tutorials, quests, cleared
	return nil
}

func (s *Store) Position() (SavedPosition, bool) {

	return s.position, s.position.PackID != 0
}

// LastPlayedPackID is the pack of the latest committed field position. The
// client suppresses SaveUserPosition in hidden packs, retaining the outside
// field position rather than choosing a temporary hidden scene on relogin.
// Zero means no position has been committed, so LoginUser can keep its seed.
func (s *Store) LastPlayedPackID(ctx command.Context) (uint64, error) {

	if s.position.PackID == 0 {
		return 0, nil
	}
	if s.position.PackID < 0 || s.position.PackID > int(^uint32(0)>>1) || s.position.Position.MapID <= 0 || s.position.RawJSON == "" {
		return 0, ErrInvalidPosition
	}
	return uint64(s.position.PackID), nil
}

func (s *Store) TutorialCleared(id int) bool {

	_, found := s.tutorials[id]
	return found
}

func (s *Store) Tutorials() []int {

	ids := make([]int, 0, len(s.tutorials))
	for id := range s.tutorials {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (s *Store) Quest(id int) (QuestProgress, bool) {

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

func (s *Store) QuestInPack(questID, packID int, level ...int) (QuestProgress, bool) {

	quest, found := s.quests[questKey(packID, questID, level...)]
	quest.Values = append([]int(nil), quest.Values...)
	return quest, found
}

// ClearQuest atomically records a completed quest. Quest identity is the
// (pack, quest) pair because every story pack starts numbering from one.
func (s *Store) ClearQuest(ctx command.Context, questID, packID int, level ...int) error {
	if questID <= 0 || packID <= 0 || questDifficulty(level) < 0 || questDifficulty(level) > 4 {
		return ErrInvalidQuest
	}

	key := questKey(packID, questID, level...)
	if _, ok := s.cleared[key]; ok {
		return nil
	}
	cleared := make(map[string]struct{}, len(s.cleared)+1)
	for current := range s.cleared {
		cleared[current] = struct{}{}
	}
	cleared[key] = struct{}{}
	return s.commit(ctx, s.position, s.tutorials, s.quests, cleared)
}

func (s *Store) QuestCleared(questID, packID int, level ...int) bool {

	_, found := s.cleared[questKey(packID, questID, level...)]
	return found
}

// ClearedQuests returns sorted quest IDs for one pack. The detached slice is
// safe for response construction without holding the store lock.
func (s *Store) ClearedQuests(packID int, level ...int) []int {

	ids := make([]int, 0, len(s.cleared))
	for key := range s.cleared {
		currentPack, difficulty, questID, ok := parseQuestKey(key)
		if ok && currentPack == packID && difficulty == questDifficulty(level) {
			ids = append(ids, questID)
		}
	}
	sort.Ints(ids)
	return ids
}

// Selection returns the committed quest selection for a pack.
func (s *Store) Selection(packID int) (QuestSelection, bool) {

	value, ok := s.selections[strconv.Itoa(packID)]
	return value, ok
}
func (s *Store) SelectQuest(ctx command.Context, packID int, selection QuestSelection) error {
	if packID <= 0 || selection.QuestID < 0 || selection.Difficulty < 0 || selection.Difficulty > 4 || selection.Option < 0 {
		return ErrInvalidQuest
	}

	old := s.selections
	next := make(map[string]QuestSelection, len(old)+1)
	maps.Copy(next, old)
	next[strconv.Itoa(packID)] = selection
	s.selections = next
	if err := s.commit(ctx, s.position, s.tutorials, s.quests, s.cleared); err != nil {
		s.selections = old
		return err
	}
	return nil
}

func (s *Store) StartPackID() int { return s.startingPackID }
func (s *Store) SetStartPack(ctx command.Context, packID int) error {
	if packID <= 0 {
		return ErrInvalidQuest
	}

	previous := s.startingPackID
	s.startingPackID = packID
	if err := s.commit(ctx, s.position, s.tutorials, s.quests, s.cleared); err != nil {
		s.startingPackID = previous
		return err
	}
	return nil
}

// AcceptQuest commits an explicit active quest without replacing main selection.
func (s *Store) AcceptQuest(ctx command.Context, questID, packID, difficulty int) error {
	if questID <= 0 || packID <= 0 || difficulty < 0 || difficulty > 4 {
		return ErrInvalidQuest
	}

	quests := make(map[string]QuestProgress, len(s.quests)+1)
	maps.Copy(quests, s.quests)
	key := questKey(packID, questID, difficulty)
	if _, exists := quests[key]; !exists {
		quests[key] = QuestProgress{QuestID: questID, PackID: packID, Difficulty: difficulty}
	}
	return s.commit(ctx, s.position, s.tutorials, quests, s.cleared)
}
func (s *Store) RemoveQuest(ctx command.Context, questID, packID, difficulty int) error {

	quests := make(map[string]QuestProgress, len(s.quests))
	for key, value := range s.quests {
		if key != questKey(packID, questID, difficulty) {
			quests[key] = value
		}
	}
	return s.commit(ctx, s.position, s.tutorials, quests, s.cleared)
}
func (s *Store) QuestsInPack(packID, difficulty int) []QuestProgress {

	var out []QuestProgress
	for _, quest := range s.quests {
		if quest.PackID == packID && quest.Difficulty == difficulty {
			quest.Values = append([]int(nil), quest.Values...)
			out = append(out, quest)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QuestID < out[j].QuestID })
	return out
}

func (s *Store) ActivePackID() int { return s.activePackID }
func (s *Store) SetActivePackID(ctx command.Context, packID int) error {
	if packID <= 0 {
		return ErrInvalidQuest
	}

	old := s.activePackID
	s.activePackID = packID
	if err := s.commit(ctx, s.position, s.tutorials, s.quests, s.cleared); err != nil {
		s.activePackID = old
		return err
	}
	return nil
}
