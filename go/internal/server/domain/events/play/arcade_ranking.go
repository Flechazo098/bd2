package eventplay

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/ranking"
	"bd2server/internal/server/protocol/wire"
)

func (s *Service) AttachArcadeRankings(ranks *ranking.Arcade, profile func(command.Context) (ranking.ArcadeProfile, error)) {
	s.arcadeRanks, s.arcadeProfile = ranks, profile
}

func (s *Service) AttachRankingGuilds(load func(command.Context, []uint64) (map[uint64][]byte, error)) {
	s.arcadeGuilds = load
}

func (s *Service) RankRecords() []ranking.ArcadeRecord {
	return s.arcadeRecords(&s.state)
}

func (s *Service) arcadeRecords(state *snapshot) []ranking.ArcadeRecord {
	keys := make([]string, 0, len(state.Records))
	for key := range state.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var records []ranking.ArcadeRecord
	for _, key := range keys {
		row := state.Records[key]
		entry := ranking.ArcadeRecord{ArcadeScope: ranking.ArcadeScope{Family: row.Family, EventUID: row.UID, Stage: row.Stage, Mode: row.Mode}, Score: float64(row.Best), CapturedArea: row.CapturedArea, ClearTime: row.ClearTime, AchievedAt: row.AchievedAt, PlayInfo: row.PlayInfo}
		switch row.Family {
		case "Survival":
			mapRow, err := s.survivalMap(row.Mode, row.Stage)
			if err != nil || num(mapRow, 11) != 1 {
				continue
			}
		case "PackMan", "Field", "Run":
		case "Sichuan":
			if row.Mode != 2 {
				continue
			}
			entry.Score = row.BestScore
		case "Rhythm":
			if row.Mode == 0 {
				continue
			}
		case "Hopscotch":
			if !row.Clears["clear"] {
				continue
			}
			entry.Score = float64(row.ClearTime)
		default:
			continue
		}
		if entry.Score > 0 {
			records = append(records, entry)
		}
	}
	return records
}

func (s *Service) arcadeWorldBest(ctx command.Context, uid uint64, state *snapshot, ownerField, nameField, scoreField int) ([]byte, error) {
	if s.arcadeRanks == nil || s.arcadeProfile == nil {
		return nil, fmt.Errorf("eventplay: arcade ranking unavailable")
	}
	profile, err := s.arcadeProfile(ctx)
	if err != nil {
		return nil, err
	}
	rows := s.arcadeRanks.Board(ranking.ArcadeScope{Family: "Sichuan", EventUID: uid}, profile, s.arcadeRecords(state), 1)
	if len(rows) == 0 {
		return nil, nil
	}
	entry := rows[0]
	out := wire.AppendVarint(nil, ownerField, entry.Profile.Owner)
	out = wire.AppendString(out, nameField, entry.Profile.Name)
	return wire.AppendDouble(out, scoreField, entry.Score), nil
}

func (s *Service) arcadeRanking(ctx command.Context, path string, req []byte) ([]byte, error) {
	if s.arcadeRanks == nil || s.arcadeProfile == nil {
		return nil, fmt.Errorf("eventplay: arcade ranking unavailable")
	}
	profile, err := s.arcadeProfile(ctx)
	if err != nil {
		return nil, err
	}
	if path == "/MiniGameRanking" || path == "/MiniGameUserRecordInfo" {
		return s.arcadeSummary(ctx, profile, path == "/MiniGameUserRecordInfo")
	}
	f := family(path)
	calendar, err := s.calendar(num(req, 2), f)
	if err != nil {
		return nil, err
	}
	scope := ranking.ArcadeScope{Family: f, EventUID: calendar.UID}
	if f == "Rhythm" || f == "Hopscotch" {
		scope.Stage = num(req, 3)
		if f == "Rhythm" {
			scope.Mode = num(req, 4)
			if scope.Mode == 0 {
				return nil, nil
			}
		}
		if err := s.validateStage(f, scope.Stage, scope.Mode); err != nil {
			return nil, err
		}
	}
	limit := 100
	if f == "Sichuan" {
		for _, row := range s.design.Tables["SichuanDefaultTable"] {
			limit = int(num(row, 10))
			break
		}
	}
	rows := s.arcadeRanks.Board(scope, profile, s.RankRecords(), limit)
	if f == "Sichuan" && s.arcadeGuilds != nil {
		owners := make([]uint64, 0, len(rows))
		for _, entry := range rows {
			owners = append(owners, entry.Profile.Owner)
		}
		guilds, err := s.arcadeGuilds(ctx, owners)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].Profile.Guild = guilds[rows[i].Profile.Owner]
		}
	}
	ownOnly := strings.Contains(path, "UserRecord")
	var out []byte
	for _, entry := range rows {
		if ownOnly {
			if entry.Own {
				return wire.AppendBytes(nil, 1, arcadeRecordWire(entry)), nil
			}
			continue
		}
		row := arcadeRankWire(entry)
		if entry.Rank <= uint64(limit) {
			out = wire.AppendBytes(out, 1, row)
		}
		if entry.Own && (f == "Survival" || f == "Rhythm") {
			out = wire.AppendBytes(out, 2, row)
		}
	}
	return out, nil
}

func arcadeRankWire(entry ranking.ArcadeEntry) []byte {
	p := entry.Profile
	if entry.Family == "Sichuan" {
		out := wire.AppendVarint(nil, 1, p.Owner)
		out = wire.AppendString(out, 2, p.Name)
		out = wire.AppendVarint(out, 3, p.XP)
		out = wire.AppendVarint(out, 4, p.Portrait)
		out = wire.AppendVarint(out, 5, p.PortraitDesign)
		if len(p.Guild) > 0 {
			out = wire.AppendBytes(out, 6, p.Guild)
		}
		out = wire.AppendVarint(out, 7, entry.Rank)
		out = wire.AppendDouble(out, 8, entry.Score)
		return wire.AppendVarint(out, 9, p.Title)
	}
	out := wire.AppendVarint(nil, 1, entry.Rank)
	out = wire.AppendVarint(out, 2, p.Owner)
	out = wire.AppendString(out, 3, p.Name)
	if entry.Family == "Hopscotch" {
		out = wire.AppendVarint(out, 4, entry.CapturedArea)
		return wire.AppendVarint(out, 5, entry.ClearTime)
	}
	return wire.AppendVarint(out, 4, uint64(entry.Score))
}

func arcadeRecordWire(entry ranking.ArcadeEntry) []byte {
	switch entry.Family {
	case "Survival":
		return wire.AppendDouble(wire.AppendVarint(nil, 1, uint64(entry.Score)), 2, entry.TopPercent)
	case "Sichuan":
		return wire.AppendDouble(wire.AppendDouble(nil, 1, entry.Score), 2, entry.TopPercent)
	case "Rhythm":
		out := wire.AppendVarint(nil, 1, uint64(entry.Score))
		out = wire.AppendVarint(out, 2, entry.Stage)
		return wire.AppendDouble(out, 3, entry.TopPercent)
	case "Hopscotch":
		out := wire.AppendVarint(nil, 1, entry.Stage)
		out = wire.AppendVarint(out, 2, entry.CapturedArea)
		out = wire.AppendVarint(out, 3, entry.ClearTime)
		return wire.AppendDouble(out, 4, entry.TopPercent)
	}
	return nil
}

func arcadeTopWire(entry ranking.ArcadeEntry) []byte {
	if entry.Family == "Hopscotch" {
		out := wire.AppendVarint(nil, 1, entry.Stage)
		out = wire.AppendString(out, 2, entry.Profile.Name)
		out = wire.AppendVarint(out, 3, entry.CapturedArea)
		return wire.AppendVarint(out, 4, entry.ClearTime)
	}
	out := wire.AppendString(nil, 1, entry.Profile.Name)
	if entry.Family == "Sichuan" {
		return wire.AppendDouble(out, 2, entry.Score)
	}
	out = wire.AppendVarint(out, 2, uint64(entry.Score))
	if entry.Family == "Rhythm" {
		out = wire.AppendVarint(out, 3, entry.Stage)
	}
	return out
}

func (s *Service) currentRankingCalendars(ctx command.Context) (map[uint64]events.Schedule, error) {
	now := s.now().UnixMilli()
	bound := map[uint64]bool{}
	if s.hubCalendars != nil {
		body, err := s.miniEventHubs(ctx, wire.AppendVarint(nil, 1, 1))
		if err != nil {
			return nil, err
		}
		if err := wire.Walk(body, func(hub wire.Field) error {
			if hub.Number != 1 || hub.Type != 2 {
				return nil
			}
			return wire.Walk(hub.Value, func(slot wire.Field) error {
				if slot.Number != 6 || slot.Type != 2 || num(slot.Value, 2) != 6 {
					return nil
				}
				uid := num(slot.Value, 4)
				active := now >= int64(num(slot.Value, 5)) && now < int64(num(slot.Value, 6))
				bound[uid] = bound[uid] || active
				return nil
			})
		}); err != nil {
			return nil, err
		}
	}
	active := map[uint64]events.Schedule{}
	for _, schedule := range s.registry.List() {
		if schedule.Type != 11 || now < schedule.Start || now >= schedule.End {
			continue
		}
		if open, exists := bound[schedule.UID]; exists && !open {
			continue
		}
		active[schedule.UID] = schedule
	}
	return active, nil
}

func (s *Service) arcadeSummary(ctx command.Context, profile ranking.ArcadeProfile, ownOnly bool) ([]byte, error) {
	calendars, err := s.currentRankingCalendars(ctx)
	if err != nil {
		return nil, err
	}
	records := s.RankRecords()
	scopes := s.arcadeRanks.Scopes()
	for _, record := range records {
		if !slices.Contains(scopes, record.ArcadeScope) {
			scopes = append(scopes, record.ArcadeScope)
		}
	}
	type summaryKey struct {
		family string
		stage  uint64
	}
	selected := map[summaryKey]ranking.ArcadeEntry{}
	for _, scope := range scopes {
		if scope.Family == "PackMan" || scope.Family == "Field" || scope.Family == "Run" {
			continue
		}
		calendar, open := calendars[scope.EventUID]
		if !open {
			continue
		}
		if _, err := s.calendar(calendar.UID, scope.Family); err != nil {
			continue
		}
		key := summaryKey{family: scope.Family}
		if scope.Family == "Rhythm" || scope.Family == "Hopscotch" {
			key.stage = scope.Stage
		}
		for _, row := range s.arcadeRanks.Board(scope, profile, records, 1) {
			if ownOnly && !row.Own || !ownOnly && row.Rank != 1 {
				continue
			}
			old, found := selected[key]
			oldCalendar := calendars[old.EventUID]
			newer := calendar.Start > oldCalendar.Start || calendar.Start == oldCalendar.Start && calendar.UID > oldCalendar.UID
			if !found || newer || calendar.UID == old.EventUID && scope.Family == "Rhythm" && row.Score > old.Score {
				selected[key] = row
			}
		}
	}
	keys := make([]summaryKey, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b summaryKey) int {
		if a.family < b.family {
			return -1
		}
		if a.family > b.family {
			return 1
		}
		if a.stage < b.stage {
			return -1
		}
		if a.stage > b.stage {
			return 1
		}
		return 0
	})
	var out []byte
	for _, key := range keys {
		entry := selected[key]
		row := arcadeTopWire(entry)
		if ownOnly {
			row = arcadeRecordWire(entry)
		}
		field := map[string]int{"Survival": 1, "Sichuan": 2, "Rhythm": 5, "Hopscotch": 6}[key.family]
		out = wire.AppendBytes(out, field, row)
	}
	return out, nil
}
