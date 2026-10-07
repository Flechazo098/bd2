package eventplay

// RoomRuntime is implemented by the native matching/relay service. HTTP
// gameplay settles only runs whose native room is authenticated.

type RoomRuntime interface {
	ValidateRoom(session, guid string, uid uint64) error
	CompleteRoom(session, guid string, score uint64) error
}

func (s *Service) AttachRooms(r RoomRuntime) { s.rooms = r }
