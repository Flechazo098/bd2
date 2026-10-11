package eventplay

import (
	"bd2server/internal/server/domain/command"
	"errors"
)

var ErrRoomAbandoned = errors.New("native room ended without a retained result")
var ErrRoomNotFinished = errors.New("native room result is not ready")
var ErrNoRoom = errors.New("no native room membership")

type NativeRun struct {
	InstanceID, GUID, Family, SessionID string
	UID, Group, Stage, Monster          uint64
	MapID                               uint64
	Members                             uint64
	BoatLevel                           uint64
	Host                                bool
	Started                             int64
}

type NativeResult struct {
	Clear    bool
	Score    uint64
	Evidence []byte
}

// StartRun reads an immutable committed membership projection; neither method
// may call a player or room mailbox while a player transaction is open.
type RoomRuntime interface {
	StartRun(command.Context, string) (NativeRun, error)
	Result(command.Context, NativeRun) (NativeResult, error)
}

type RelayEndpoint struct{ Region, Host, TCPPort string }
type RelayEndpointSource interface{ RelayEndpoints() []RelayEndpoint }

func (s *Service) AttachRooms(r RoomRuntime) { s.rooms = r }

func (s *Service) NativeRuns() []NativeRun {
	var runs []NativeRun
	for _, run := range s.state.Runs {
		if run.Native != nil {
			runs = append(runs, *run.Native)
		}
	}
	return runs
}
