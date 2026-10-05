package eventplay

import (
	"encoding/json"
	"fmt"
)

// RoomRuntime is implemented by the native matching/relay service. HTTP
// gameplay settles only runs whose native room is authenticated.
type RoomRuntime interface {
	ValidateRoom(session, guid string, uid uint64) error
	CompleteRoom(session, guid string, score uint64) error
}

func (s *Service) AttachRooms(r RoomRuntime) { s.rooms = r }

// LockRoomRun installs the stage selected by native room matching before the
// client's HTTP Start. It does not award rewards or trust HTTP stage changes.
func (s *Service) LockRoomRun(session, family, guid string, uid, group, stage, monster uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if family != "Defense" && family != "Action" {
		return fmt.Errorf("eventplay: invalid room family")
	}
	c, e := s.calendar(uid, family)
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(s.state)
	var next snapshot
	_ = json.Unmarshal(raw, &next)
	next.Runs[session+":"+family+":native"] = Run{UID: uid, Game: c.ID, Stage: stage, Mode: group, Char: monster, Family: family, Session: guid}
	raw, _ = json.Marshal(next)
	if e = s.store.Save("eventplay", raw); e != nil {
		return e
	}
	s.state = next
	return nil
}
