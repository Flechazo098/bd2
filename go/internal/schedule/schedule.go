// Package schedule serves the versioned regular-content calendar used by the
// client to decide season, calculation, and off-season UI states.
package schedule

import (
	"errors"

	"bd2server/internal/wire"
)

// Season is the named semantic form of Proto.Net.SeasonInfo.
type Season struct {
	ID                uint64
	StartMilliseconds uint64
	EndMilliseconds   uint64
	RankRewardGroupID uint64
}

type Content struct {
	ID      uint64
	Current Season
	Next    Season
}

type RegularSeason struct {
	ContentID uint64
	Season    uint64
}

// Service contains the official 2.34.13 calendar active for this fixed
// client/GameData version. It is server configuration, not a captured packet:
// Handle encodes every protobuf field from these named values.
type Service struct {
	CalculateMilliseconds uint64
	Contents              []Content
	Regular               []RegularSeason
}

func Version23413() *Service {
	return &Service{
		CalculateMilliseconds: 32400000,
		Contents: []Content{
			{ID: 1, Current: Season{160, 1789344000000, 1789916399000, 1}, Next: Season{161, 1789948800000, 1790521199000, 1}},
			{ID: 2, Current: Season{58, 1755129600000, 253370678399000, 1}, Next: Season{58, 1755129600000, 253370678399000, 1}},
			{ID: 3, Current: Season{129, 1789657200000, 1790262000000, 1}, Next: Season{999999, 253370764800000, 253370764800000, 1}},
			{ID: 4, Current: Season{862, 1789830000000, 1789916400000, 1}, Next: Season{999999, 253370764800000, 253370764800000, 1}},
			{ID: 5, Current: Season{29, 1787788800000, 1790089199000, 1}, Next: Season{30, 1790121600000, 1791385199000, 1}},
			{ID: 6, Current: Season{26, 1787788800000, 1788361199000, 1}, Next: Season{27, 1790121600000, 1790693999000, 1}},
			{ID: 7, Current: Season{14, 1788220800000, 1790812800000, 1}, Next: Season{999999, 253370764800000, 253370764800000, 1}},
			{ID: 8, Current: Season{42, 1789603200000, 1790089199000, 1}, Next: Season{43, 1790121600000, 1790780399000, 1}},
			{ID: 9, Current: Season{2, 1787788800000, 1792594799000, 1}, Next: Season{999999, 253370764800000, 253370764800000, 1}},
		},
		Regular: []RegularSeason{{1, 5}, {2, 6}, {5, 5}, {8, 0}, {9, 0}},
	}
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
	if season.RankRewardGroupID != 0 {
		result = wire.AppendVarint(result, 6, season.RankRewardGroupID)
	}
	return result
}
