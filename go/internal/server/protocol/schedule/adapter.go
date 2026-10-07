package scheduleadapter

import (
	"bd2server/internal/server/design/schedule"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"errors"
)

type Service struct{ Schedule *schedule.Service }

func (a *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	s := a.Schedule
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

func encodeSeason(season schedule.Season) []byte {
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
