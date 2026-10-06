package player

import (
	"encoding/json"
	"errors"
	"math"
	"time"
	"unicode"
	"unicode/utf8"

	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
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
	store        stateio.AtomicEntryStore
	defaultTitle masterTitle
}

func OpenMasterTitleService(store stateio.Store, defaultName string) (*MasterTitleService, error) {
	entries, ok := store.(stateio.AtomicEntryStore)
	if !ok || !validMasterTitleName(defaultName, false) {
		return nil, errors.New("player: invalid master title service")
	}
	s := &MasterTitleService{store: entries, defaultTitle: masterTitle{Name: defaultName}}
	if _, err := s.load(); err != nil {
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

func (s *MasterTitleService) load() (masterTitle, error) {
	raw, found, err := s.store.LoadEntry("progress", "master_title", "identity")
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
func (s *MasterTitleService) EnsurePersisted() error {
	_, found, err := s.store.LoadEntry("progress", "master_title", "identity")
	if err != nil {
		return err
	}
	if found {
		_, err = s.load()
		return err
	}
	core, err := s.store.Load("progress")
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
	return s.store.SaveWithEntries("progress", nil, []stateio.EntryMutation{{Bucket: "master_title", Key: "identity", Payload: payload}})
}

func (s *MasterTitleService) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/MasterTitleInfo" && path != "/MasterTitleInfoUpdate" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 {
		return 0, nil, true, errors.New("player: invalid master title sequence")
	}
	if path == "/MasterTitleInfo" {
		title, err := s.load()
		if err != nil {
			return 0, nil, true, err
		}
		body := wire.AppendString(nil, 1, title.Name)
		if title.Month != 0 {
			body = wire.AppendVarint(body, 2, title.Month)
		}
		if title.Day != 0 {
			body = wire.AppendVarint(body, 3, title.Day)
		}
		return 590, body, true, nil
	}
	rawName, found, err := wire.Bytes(request, 2)
	if err != nil || !found || !validMasterTitleName(string(rawName), true) {
		return 0, nil, true, errors.New("player: invalid master title name")
	}
	month, _, err := wire.Varint(request, 3)
	if err != nil {
		return 0, nil, true, err
	}
	day, _, err := wire.Varint(request, 4)
	if err != nil || !validMasterBirthday(month, day) {
		return 0, nil, true, errors.New("player: invalid master title birthday")
	}
	next := masterTitle{Name: string(rawName), Month: month, Day: day}
	current, err := s.load()
	if err != nil {
		return 0, nil, true, err
	}
	if current == next {
		return 592, nil, true, nil
	}
	core, err := s.store.Load("progress")
	if err != nil {
		return 0, nil, true, err
	}
	if len(core) == 0 {
		return 0, nil, true, errors.New("player: master title requires initialized progress")
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.store.SaveWithEntries("progress", nil, []stateio.EntryMutation{{Bucket: "master_title", Key: "identity", Payload: payload}}); err != nil {
		return 0, nil, true, err
	}
	return 592, nil, true, nil
}
