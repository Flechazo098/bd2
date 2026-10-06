package world

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type AchievementService struct {
	mu      sync.Mutex
	design  *gamedata.AchievementCounterDesign
	store   stateio.AtomicEntryStore
	session string
	claims  AchievementClaimSource
}
type AchievementClaimSource interface {
	ClaimedAchievementIDs() map[gamedata.AchievementKey]bool
}
type achievementReceipt struct {
	Sequence uint64 `json:"sequence"`
	Group    int    `json:"group"`
	Add      int    `json:"add"`
}
type achievementSnapshot struct {
	Counts   map[string]int64              `json:"counts"`
	Receipts map[string]achievementReceipt `json:"receipts"`
}

func NewAchievementService(design *gamedata.AchievementCounterDesign, store stateio.Store, claims ...AchievementClaimSource) (*AchievementService, error) {
	if design == nil || len(design.Groups) == 0 || store == nil {
		return nil, fmt.Errorf("achievement: missing design or storage")
	}
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok {
		return nil, fmt.Errorf("achievement: storage requires atomic entries")
	}
	service := &AchievementService{design: design, store: entries}
	if len(claims) > 0 {
		service.claims = claims[0]
	}
	return service, nil
}
func (s *AchievementService) BeginSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = id
}

// Load each request from the transaction snapshot. A rolled-back request must
// never leave an increment or replay receipt in an in-memory cache.
func (s *AchievementService) load() (achievementSnapshot, error) {
	state := achievementSnapshot{Counts: map[string]int64{}, Receipts: map[string]achievementReceipt{}}
	counts, err := s.store.ListEntries("missions", "achievement_counts")
	if err != nil {
		return state, err
	}
	for key, raw := range counts {
		var value int64
		if err = json.Unmarshal(raw, &value); err != nil {
			return state, fmt.Errorf("achievement: invalid counter: %w", err)
		}
		state.Counts[key] = value
	}
	if s.session != "" {
		raw, found, err := s.store.LoadEntry("missions", "achievement_receipts", s.session)
		if err != nil {
			return state, err
		}
		if found {
			var receipt achievementReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				return state, fmt.Errorf("achievement: invalid receipt: %w", err)
			}
			state.Receipts[s.session] = receipt
		}
	}
	if state.Counts == nil || state.Receipts == nil {
		return state, fmt.Errorf("achievement: incomplete state")
	}
	for key, value := range state.Counts {
		group, err := strconv.Atoi(key)
		if err != nil || len(s.design.Groups[group]) == 0 || value < 0 {
			return state, fmt.Errorf("achievement: invalid persisted counter")
		}
	}
	return state, nil
}

func (s *AchievementService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/AchievementInfo" && path != "/AchievementUpdate" {
		return 0, nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(err error) (int, []byte, bool, error) { return 0, nil, true, err }
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return fail(ErrInvalidRequest)
	}
	state, err := s.load()
	if err != nil {
		return fail(err)
	}
	if path == "/AchievementInfo" {
		claimed := map[gamedata.AchievementKey]bool{}
		if s.claims != nil {
			claimed = s.claims.ClaimedAchievementIDs()
		}
		for key := range claimed {
			group := strconv.Itoa(int(key.GroupID))
			if _, found := state.Counts[group]; !found && len(s.design.Groups[int(key.GroupID)]) > 0 {
				state.Counts[group] = 0
			}
		}
		groups := make([]int, 0, len(state.Counts))
		for key := range state.Counts {
			group, _ := strconv.Atoi(key)
			groups = append(groups, group)
		}
		sort.Ints(groups)
		var response []byte
		for _, group := range groups {
			for _, content := range s.design.Groups[group] {
				row := wire.AppendVarint(nil, 1, uint64(group))
				row = wire.AppendVarint(row, 2, uint64(state.Counts[strconv.Itoa(group)]))
				var maxID uint64
				// AchievementPacket.UpdateAchievementCount initializes title MaxClearId
				// to 1000; this is the client protocol default, not a granted tier.
				if content == 1 {
					maxID = 1000
				}
				for key := range claimed {
					if key.GroupID == uint64(group) && key.ContentsGroup == uint64(content) && key.ID > maxID {
						maxID = key.ID
					}
				}
				if maxID > 0 {
					row = wire.AppendVarint(row, 3, maxID)
				}
				if content != 0 {
					row = wire.AppendVarint(row, 4, uint64(content))
				}
				response = wire.AppendBytes(response, 1, row)
			}
		}
		// Only the mission domain's actual claims advance max_clear_id.
		return 166, response, true, nil
	}
	group, found, err := wire.Varint(request, 2)
	if err != nil || !found || group == 0 || group > math.MaxInt32 || len(s.design.Groups[int(group)]) == 0 {
		return fail(ErrInvalidRequest)
	}
	add, found, err := wire.Varint(request, 3)
	if err != nil || !found || add == 0 || add > math.MaxInt32 || s.session == "" {
		return fail(ErrInvalidRequest)
	}
	receipt := achievementReceipt{Sequence: seq, Group: int(group), Add: int(add)}
	replayKey := s.session + "/" + strconv.FormatUint(seq, 10)
	if raw, found, err := s.store.LoadEntry("missions", "achievement_replays", replayKey); err != nil {
		return fail(err)
	} else if found {
		var previous achievementReceipt
		if err = json.Unmarshal(raw, &previous); err != nil {
			return fail(err)
		}
		if previous == receipt {
			return 167, nil, true, nil
		}
		return fail(fmt.Errorf("achievement: conflicting request sequence"))
	}
	if previous, ok := state.Receipts[s.session]; ok && seq <= previous.Sequence {
		if previous == receipt {
			return 167, nil, true, nil
		}
		return fail(fmt.Errorf("achievement: stale or conflicting request sequence"))
	}
	key := strconv.Itoa(int(group))
	current := state.Counts[key]
	if current > math.MaxInt64-int64(add) {
		return fail(fmt.Errorf("achievement: counter overflow"))
	}
	state.Counts[key] = current + int64(add)
	state.Receipts[s.session] = receipt
	raw, err := json.Marshal(state.Counts[key])
	if err != nil {
		return fail(err)
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return fail(err)
	}
	changes := []stateio.EntryMutation{{Bucket: "achievement_counts", Key: key, Payload: raw}, {Bucket: "achievement_receipts", Key: s.session, Payload: receiptRaw}, {Bucket: "achievement_replays", Key: replayKey, Payload: receiptRaw}}
	// Retain a complete retry window so a committed BatchRequest whose response
	// was lost can replay several updates, not just the final update in the batch.
	if seq > 256 {
		entries, err := s.store.ListEntries("missions", "achievement_replays")
		if err != nil {
			return fail(err)
		}
		prefix := s.session + "/"
		for replayKey := range entries {
			if suffix, found := strings.CutPrefix(replayKey, prefix); found {
				old, err := strconv.ParseUint(suffix, 10, 64)
				if err != nil {
					return fail(err)
				}
				if old <= seq-256 {
					changes = append(changes, stateio.EntryMutation{Bucket: "achievement_replays", Key: replayKey, Delete: true})
				}
			}
		}
	}
	if err = s.store.SaveWithEntries("missions", nil, changes); err != nil {
		return fail(err)
	}
	return 167, nil, true, nil
}
