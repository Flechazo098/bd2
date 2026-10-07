package roster

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"time"
	"unicode"
	"unicode/utf8"
)

type masterTitle struct {
	Name  string `json:"name"`
	Month uint64 `json:"month"`
	Day   uint64 `json:"day"`
}

// MasterTitleService persists the identity whose empty name sends IntroUI
// back to the tutorial even when LoginUser advertises another saved pack.
// It uses one entry in the existing progress domain, leaving its core intact.
type MasterTitleService struct {
	store        stateio.ScopedEntryStore
	defaultTitle masterTitle
}

func OpenMasterTitleService(ctx command.Context, store stateio.Store, defaultName string) (*MasterTitleService, error) {
	entries, ok := store.(stateio.ScopedEntryStore)
	if !ok || !validMasterTitleName(defaultName, false) {
		return nil, errors.New("player: invalid master title service")
	}
	s := &MasterTitleService{store: entries, defaultTitle: masterTitle{Name: defaultName}}
	if _, err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func validMasterTitleName(name string, fromRequest bool) bool {
	if !utf8.ValidString(name) || len(name) == 0 || len(name) > 24 || (fromRequest && len(name) < 4) {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		if fromRequest {
			lower := unicode.ToLower(r)
			if !((lower >= '0' && lower <= '9') || (lower >= 'a' && lower <= 'z') || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x4E00 && r <= 0x9FD5) || (r >= 0x3041 && r <= 0x30FE) || (r >= 0x0180 && r <= 0x024F)) { //nolint:staticcheck // QF1001
				return false
			}
		}
	}
	return true
}

func validMasterBirthday(month, day uint64) bool {
	if month == 0 && day == 0 {
		return true
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	// The client explicitly uses leap year 2024 for birthday selection.
	return uint64(time.Date(2024, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()) >= day
}

func (s *MasterTitleService) load(ctx command.Context) (masterTitle, error) {
	raw, found, err := s.store.LoadEntry(ctx.State, "progress", "master_title", "identity")
	if err != nil {
		return masterTitle{}, err
	}
	if !found {
		return s.defaultTitle, nil
	}
	if err := stateio.RequireExactJSONObject(raw, "name", "month", "day"); err != nil {
		return masterTitle{}, err
	}
	var title masterTitle
	if json.Unmarshal(raw, &title) != nil || !validMasterTitleName(title.Name, false) || !validMasterBirthday(title.Month, title.Day) {
		return masterTitle{}, errors.New("player: invalid saved master title")
	}
	return title, nil
}

// EnsurePersisted records the existing account display name once, with an
// unknown birthday. Startup calls it after progress core initialization.
func (s *MasterTitleService) EnsurePersisted(ctx command.Context) error {
	_, found, err := s.store.LoadEntry(ctx.State, "progress", "master_title", "identity")
	if err != nil {
		return err
	}
	if found {
		_, err = s.load(ctx)
		return err
	}
	core, err := s.store.Load(ctx.State, "progress")
	if err != nil {
		return err
	}
	if len(core) == 0 {
		return errors.New("player: master title requires initialized progress")
	}
	payload, err := json.Marshal(s.defaultTitle)
	if err != nil {
		return err
	}
	return s.store.SaveWithEntries(ctx.State, "progress", nil, []stateio.EntryMutation{{Bucket: "master_title", Key: "identity", Payload: payload}})
}
