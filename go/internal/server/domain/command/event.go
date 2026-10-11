package command

import "errors"

type Event struct {
	ID      string `json:"id"`
	Topic   string `json:"topic"`
	Payload []byte `json:"payload"`
}

func (c Context) Publish(event Event) error {
	if c.Emit == nil {
		return errors.New("command has no transactional event publisher")
	}
	return c.Emit(event)
}
