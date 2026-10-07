package roster

import (
	"strconv"
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

func (s *CollectionStore) FriendshipEntries() map[string]FriendshipEntry {

	return cloneFriendshipEntries(s.data.Friendships)
}
