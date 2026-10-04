// Package schedule serves the versioned regular-content calendar used by the
// client to decide season, calculation, and off-season UI states.
package schedule

import (
	"bd2server/internal/server/versionconfig"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	"bd2server/internal/server/wire"
)

// Season is the named semantic form of Proto.Net.SeasonInfo.
type Season struct {
	ID                uint64 `json:"id"`
	StartMilliseconds uint64 `json:"start_milliseconds"`
	EndMilliseconds   uint64 `json:"end_milliseconds"`
	RankRewardGroupID uint64 `json:"rank_reward_group_id"`
	Error             bool   `json:"error,omitempty"`
	Return            bool   `json:"return,omitempty"`
}

type Content struct {
	ID      uint64 `json:"id"`
	Current Season `json:"current"`
	Next    Season `json:"next"`
}

type RegularSeason struct {
	ContentID uint64 `json:"content_id"`
	Season    uint64 `json:"season"`
}

// Service contains the configured regular-content calendar for this fixed
// client/GameData version. It is server configuration, not a captured packet:
// Handle encodes every protobuf field from these named values.
type Service struct {
	Version               string          `json:"version"`
	CalculateMilliseconds uint64          `json:"calculate_milliseconds"`
	Contents              []Content       `json:"contents"`
	Regular               []RegularSeason `json:"regular"`
}

// Load reads this server's versioned calendar policy. It does not infer live
// official seasons from today's clock or from static reward design tables.
func Load(path string) (*Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("schedule: read seed: %w", err)
	}
	var s Service
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schedule: decode seed: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Service) Validate() error {
	if s == nil || s.Version != versionconfig.State() || s.CalculateMilliseconds == 0 || s.CalculateMilliseconds > math.MaxInt32 || len(s.Contents) == 0 {
		return errors.New("schedule: invalid calendar version/calculation interval")
	}
	ids := map[uint64]bool{}
	for _, content := range s.Contents {
		if content.ID == 0 || content.ID > math.MaxInt32 || ids[content.ID] {
			return errors.New("schedule: invalid/duplicate content identity")
		}
		ids[content.ID] = true
		for _, season := range []Season{content.Current, content.Next} {
			if season.ID == 0 || season.ID > math.MaxInt32 || season.StartMilliseconds == 0 || season.StartMilliseconds > season.EndMilliseconds || season.EndMilliseconds > math.MaxInt64 || season.RankRewardGroupID > math.MaxInt32 {
				return errors.New("schedule: invalid season")
			}
		}
	}
	seen := map[uint64]bool{}
	for _, regular := range s.Regular {
		if !ids[regular.ContentID] || seen[regular.ContentID] || regular.Season > math.MaxInt32 {
			return errors.New("schedule: invalid regular season")
		}
		seen[regular.ContentID] = true
	}
	return nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/ScheduleInfo" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("schedule: invalid request sequence")
	}
	if s == nil || s.CalculateMilliseconds == 0 || len(s.Contents) == 0 {
		return 0, nil, true, errors.New("schedule: unavailable calendar")
	}
	response := wire.AppendVarint(nil, 1, s.CalculateMilliseconds)
	for _, content := range s.Contents {
		entry := wire.AppendVarint(nil, 1, content.ID)
		entry = wire.AppendBytes(entry, 2, encodeSeason(content.Current))
		entry = wire.AppendBytes(entry, 3, encodeSeason(content.Next))
		response = wire.AppendBytes(response, 2, entry)
	}
	for _, regular := range s.Regular {
		entry := wire.AppendVarint(nil, 1, regular.ContentID)
		if regular.Season != 0 {
			entry = wire.AppendVarint(entry, 2, regular.Season)
		}
		response = wire.AppendBytes(response, 3, entry)
	}
	return 117, response, true, nil
}

func encodeSeason(season Season) []byte {
	result := wire.AppendVarint(nil, 1, season.ID)
	result = wire.AppendVarint(result, 2, season.StartMilliseconds)
	result = wire.AppendVarint(result, 3, season.EndMilliseconds)
	if season.Error {
		result = wire.AppendVarint(result, 4, 1)
	}
	if season.Return {
		result = wire.AppendVarint(result, 5, 1)
	}
	if season.RankRewardGroupID != 0 {
		result = wire.AppendVarint(result, 6, season.RankRewardGroupID)
	}
	return result
}
