package player

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"bd2server/internal/server/wire"
)

type FriendshipState struct {
	CostumeID          uint64   `json:"costume_id"`
	Level              uint64   `json:"level"`
	EXP                uint64   `json:"exp"`
	LastCounselingDate uint64   `json:"last_counseling_date"`
	CounselingDay      string   `json:"counseling_day,omitempty"`
	CounselingCount    uint64   `json:"counseling_count,omitempty"`
	Sessions           []uint64 `json:"sessions,omitempty"`
}

type FriendshipDaily struct {
	Day  string `json:"day"`
	Used uint64 `json:"used"`
}

type FriendshipReply struct {
	Digest string `json:"digest"`
	Code   int    `json:"code"`
	Body   []byte `json:"body"`
}

// FriendshipEntry keeps progress and successful request replies in the existing
// collection domain. Each record has exactly one kind, so collection commits
// can update one costume, the daily counter and a replay together.
type FriendshipEntry struct {
	State *FriendshipState `json:"state,omitempty"`
	Daily *FriendshipDaily `json:"daily,omitempty"`
	Reply *FriendshipReply `json:"reply,omitempty"`
}

func friendshipStateKey(id uint64) string { return "costume:" + strconv.FormatUint(id, 10) }

func cloneFriendshipEntries(in map[string]FriendshipEntry) map[string]FriendshipEntry {
	out := make(map[string]FriendshipEntry, len(in))
	for key, entry := range in {
		if entry.State != nil {
			state := *entry.State
			state.Sessions = append([]uint64(nil), state.Sessions...)
			entry.State = &state
		}
		if entry.Daily != nil {
			daily := *entry.Daily
			entry.Daily = &daily
		}
		if entry.Reply != nil {
			reply := *entry.Reply
			reply.Body = append([]byte(nil), reply.Body...)
			entry.Reply = &reply
		}
		out[key] = entry
	}
	return out
}

func validateFriendshipEntries(entries map[string]FriendshipEntry) error {
	for key, entry := range entries {
		kinds := 0
		if entry.State != nil {
			kinds++
		}
		if entry.Daily != nil {
			kinds++
		}
		if entry.Reply != nil {
			kinds++
		}
		if kinds != 1 {
			return fmt.Errorf("player: invalid friendship entry %q", key)
		}
		if state := entry.State; state != nil {
			if key != friendshipStateKey(state.CostumeID) || state.CostumeID == 0 || state.CostumeID > math.MaxInt32 || state.Level == 0 || state.Level > math.MaxInt32 || state.EXP > math.MaxInt32 || state.LastCounselingDate > math.MaxInt64 {
				return fmt.Errorf("player: invalid friendship state %q", key)
			}
			if state.LastCounselingDate == 0 {
				if state.CounselingDay != "" || state.CounselingCount != 0 || len(state.Sessions) != 0 {
					return errors.New("player: friendship counseling state has no date")
				}
			} else if state.CounselingCount == 0 || state.CounselingDay != time.UnixMilli(int64(state.LastCounselingDate)).UTC().Format("2006-01-02") {
				return errors.New("player: inconsistent friendship counseling date")
			}
			seen := map[uint64]bool{}
			for _, id := range state.Sessions {
				if id == 0 || id > math.MaxInt32 || seen[id] {
					return errors.New("player: invalid friendship counseling sessions")
				}
				seen[id] = true
			}
		}
		if entry.Daily != nil && (key != "daily" || entry.Daily.Day == "") {
			return errors.New("player: invalid friendship daily entry")
		}
		if entry.Daily != nil {
			if parsed, err := time.Parse("2006-01-02", entry.Daily.Day); err != nil || parsed.Format("2006-01-02") != entry.Daily.Day {
				return errors.New("player: invalid friendship daily date")
			}
		}
		if reply := entry.Reply; reply != nil {
			if !strings.HasPrefix(key, "reply:") || len(key) != len("reply:")+64 || (reply.Code != 613 && reply.Code != 614) || len(reply.Digest) != 64 || len(reply.Body) == 0 {
				return errors.New("player: invalid friendship replay")
			}
			if _, err := hex.DecodeString(reply.Digest); err != nil {
				return errors.New("player: invalid friendship request digest")
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(key, "reply:")); err != nil {
				return errors.New("player: invalid friendship replay key")
			}
			if err := validateFriendshipReply(*reply); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFriendshipReply(reply FriendshipReply) error {
	infoCount := 0
	err := wire.Walk(reply.Body, func(field wire.Field) error {
		switch field.Number {
		case 1:
			if field.Type != 2 {
				return errors.New("invalid friendship reward bundle")
			}
			return wire.Walk(field.Value, func(wire.Field) error { return nil })
		case 2:
			if field.Type != 2 {
				return errors.New("invalid friendship response info")
			}
			infoCount++
			id, found, err := wire.Varint(field.Value, 1)
			if err != nil || !found || id == 0 || id > math.MaxInt32 {
				return errors.New("invalid friendship response costume")
			}
			level, found, err := wire.Varint(field.Value, 2)
			if err != nil || !found || level == 0 || level > math.MaxInt32 {
				return errors.New("invalid friendship response level")
			}
		case 3, 4:
			if field.Type != 0 || (reply.Code == 614 && field.Number == 4) {
				return errors.New("invalid friendship response scalar")
			}
			value, _, err := wire.Varint(reply.Body, field.Number)
			if err != nil || value > math.MaxInt32 || (reply.Code == 613 && field.Number == 3 && value > 1) {
				return errors.New("invalid friendship response value")
			}
		default:
			return errors.New("unexpected friendship response field")
		}
		return nil
	})
	if err != nil || infoCount != 1 {
		return errors.New("player: invalid friendship replay response")
	}
	return nil
}

func (s *CollectionStore) FriendshipEntries() map[string]FriendshipEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneFriendshipEntries(s.data.Friendships)
}

func (s *CollectionStore) ApplyFriendship(state FriendshipState, daily *FriendshipDaily, replayKey string, reply FriendshipReply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneCollection(s.data)
	next.Friendships[friendshipStateKey(state.CostumeID)] = FriendshipEntry{State: &state}
	if daily != nil {
		next.Friendships["daily"] = FriendshipEntry{Daily: daily}
	}
	if _, exists := next.Friendships[replayKey]; exists {
		return errors.New("player: friendship request already applied")
	}
	next.Friendships[replayKey] = FriendshipEntry{Reply: &reply}
	if err := validateFriendshipEntries(next.Friendships); err != nil {
		return err
	}
	return s.commit(next)
}
