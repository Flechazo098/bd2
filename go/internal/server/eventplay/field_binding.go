package eventplay

import (
	"bd2server/internal/server/versionconfig"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// FieldBinding records client constants that are not expressed as foreign
// keys in GameData. Concrete pack identities belong in the version seed.
type FieldBinding struct {
	PackID          int    `json:"pack_id"`
	EventType       uint64 `json:"event_type"`
	ContentOpenType uint64 `json:"content_open_type"`
}

func (s *Service) AttachFieldBindingsFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var seed struct {
		Version  string         `json:"version"`
		Bindings []FieldBinding `json:"bindings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&seed); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("eventplay: trailing field binding data")
	}
	if seed.Version != versionconfig.Current().GameVersion {
		return fmt.Errorf("eventplay: field binding version mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[int]bool{}
	for _, b := range seed.Bindings {
		pack, ok := s.design.FieldPacks[b.PackID]
		if !ok || len(pack.MapIDs) == 0 || b.EventType == 0 || seen[b.PackID] {
			return fmt.Errorf("eventplay: invalid field binding pack %d", b.PackID)
		}
		seen[b.PackID] = true
	}
	s.fieldBindings = append([]FieldBinding(nil), seed.Bindings...)
	return nil
}
