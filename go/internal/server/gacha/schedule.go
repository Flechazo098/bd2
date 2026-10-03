package gacha

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bd2server/internal/server/gamedata"
)

// ScheduleSeed contains server-owned, versioned dynamic gacha facts. GameData
// supplies pools and product definitions; it deliberately does not contain the
// official server's start/end windows or the account's initial reroll preview.
type ScheduleSeed struct {
	ClientVersion string           `json:"client_version"`
	Schedules     []ScheduleWindow `json:"schedules"`
	StepUps       []ScheduleWindow `json:"step_ups"`
}

// ActivePickupCostumes returns only explicitly featured costume IDs from
// schedule windows active at the supplied Unix millisecond. The client uses
// the half-open interval [start,end); unopened and expired GameData groups are
// never treated as current UP banners.
func ActivePickupCostumes(catalog *gamedata.RegularGachaCatalog, seed *ScheduleSeed, now uint64) map[uint64]bool {
	result := map[uint64]bool{}
	if catalog == nil || seed == nil {
		return result
	}
	active := func(window ScheduleWindow) bool {
		return window.StartTime <= now && now < window.EndTime
	}
	for _, window := range seed.Schedules {
		if !active(window) {
			continue
		}
		if group, ok := catalog.Group(window.GroupID); ok && group.GachaType == 1 && group.PickUpCostumeID != 0 {
			result[group.PickUpCostumeID] = true
		}
	}
	for _, window := range seed.StepUps {
		if !active(window) {
			continue
		}
		stepUp, ok := catalog.StepUp(window.GroupID)
		if !ok {
			continue
		}
		for _, step := range stepUp.Steps {
			if group, ok := catalog.Group(step.GroupID); ok && group.GachaType == 1 && group.PickUpCostumeID != 0 {
				result[group.PickUpCostumeID] = true
			}
		}
	}
	return result
}

type ScheduleWindow struct {
	GroupID        uint64 `json:"group_id"`
	StartTime      uint64 `json:"start_time"`
	EndTime        uint64 `json:"end_time"`
	FreeCountBonus bool   `json:"free_count_bonus,omitempty"`
	CashCountBonus bool   `json:"cash_count_bonus,omitempty"`
}

func LoadScheduleSeed(path, clientVersion string) (*ScheduleSeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gacha: read schedule seed: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var seed ScheduleSeed
	if err := decoder.Decode(&seed); err != nil {
		return nil, fmt.Errorf("gacha: decode schedule seed: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, errors.New("gacha: schedule seed has trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("gacha: decode schedule seed trailing data: %w", err)
	}
	if err := seed.Validate(clientVersion); err != nil {
		return nil, err
	}
	return &seed, nil
}

func (s *ScheduleSeed) Validate(clientVersion string) error {
	if s == nil || clientVersion == "" || s.ClientVersion != clientVersion {
		return fmt.Errorf("gacha: schedule seed client version %q does not match %q", seedVersion(s), clientVersion)
	}
	if len(s.Schedules) == 0 || len(s.StepUps) == 0 {
		return errors.New("gacha: schedule seed requires regular and step-up windows")
	}
	seen := make(map[uint64]bool, len(s.Schedules))
	for _, window := range s.Schedules {
		if err := validateWindow(window); err != nil {
			return err
		}
		if seen[window.GroupID] {
			return fmt.Errorf("gacha: duplicate schedule group %d", window.GroupID)
		}
		seen[window.GroupID] = true
	}
	seen = make(map[uint64]bool, len(s.StepUps))
	for _, window := range s.StepUps {
		if window.FreeCountBonus || window.CashCountBonus {
			return fmt.Errorf("gacha: step-up group %d has unsupported bonus flags", window.GroupID)
		}
		if err := validateWindow(window); err != nil {
			return err
		}
		if seen[window.GroupID] {
			return fmt.Errorf("gacha: duplicate step-up group %d", window.GroupID)
		}
		seen[window.GroupID] = true
	}
	return nil
}

func validateWindow(window ScheduleWindow) error {
	if window.GroupID == 0 || window.StartTime == 0 || window.EndTime <= window.StartTime {
		return fmt.Errorf("gacha: invalid schedule window for group %d", window.GroupID)
	}
	return nil
}

func seedVersion(seed *ScheduleSeed) string {
	if seed == nil {
		return ""
	}
	return seed.ClientVersion
}
