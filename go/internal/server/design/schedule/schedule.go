// Package schedule serves the versioned regular-content calendar used by the
// client to decide season, calculation, and off-season UI states.
package schedule

import (
	"bd2server/internal/server/platform/versionconfig"
	"errors"
	"math"
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
// Values remain immutable after validation.
type Service struct {
	Version               string          `json:"version"`
	CalculateMilliseconds uint64          `json:"calculate_milliseconds"`
	Contents              []Content       `json:"contents"`
	Regular               []RegularSeason `json:"regular"`
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
