package monsterhunt

import (
	"bd2server/internal/server/wire"
	"encoding/binary"
	"fmt"
	"math"
)

func validateRequest(req []byte) error {
	return wire.Walk(req, func(f wire.Field) error {
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			if v > math.MaxInt32 {
				return fmt.Errorf("monsterhunt: integer outside protocol range")
			}
		}
		return nil
	})
}

func (s *Service) ValidatePack(pack int, req []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, e := scalar(req, 6)
	if e != nil {
		return e
	}
	d, e := s.load(id)
	if e != nil {
		return e
	}
	if pack <= 0 || uint64(pack) != d.PackID {
		return fmt.Errorf("monsterhunt: hunt does not belong to current pack")
	}
	return nil
}
