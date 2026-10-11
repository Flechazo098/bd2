package command

import "fmt"

type GameError struct {
	PacketCode, ErrorType int
	Message               string
}

func (e *GameError) Error() string {
	return fmt.Sprintf("game packet %d error %d: %s", e.PacketCode, e.ErrorType, e.Message)
}
