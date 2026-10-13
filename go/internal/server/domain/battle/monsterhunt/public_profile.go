package monsterhunt

import (
	"bd2server/internal/server/protocol/wire"
	"slices"
	"strconv"
)

type RankProfile struct {
	Owner, XP, Portrait, PortraitDesign, Title uint64
	Name                                       string
	Guild                                      []byte
}

type RankRecord struct {
	Season, Level, Damage uint64
	Version               uint64
	Score                 float64
}

type RankEntry struct {
	RankRecord
	Profile    RankProfile
	Rank       uint64
	TopPercent float64
}

type RankPublication struct {
	Account string
	Version uint64
	Profile RankProfile
	Records []RankRecord
}

type RankSource interface {
	Top(season uint64, limit int) []RankEntry
	Rank(owner uint64, record RankRecord) RankEntry
}

func RankWire(entry RankEntry) []byte {
	p := entry.Profile
	out := wire.AppendVarint(nil, 1, p.Owner)
	out = wire.AppendString(out, 2, p.Name)
	out = wire.AppendVarint(out, 3, p.XP)
	out = wire.AppendVarint(out, 4, p.Portrait)
	out = wire.AppendVarint(out, 5, p.PortraitDesign)
	out = wire.AppendBytes(out, 6, p.Guild)
	out = wire.AppendVarint(out, 7, entry.Rank)
	out = wire.AppendDouble(out, 8, entry.Score)
	out = wire.AppendVarint(out, 9, p.Title)
	return wire.AppendDouble(out, 10, entry.TopPercent)
}

func (s *Service) AttachRanking(owner uint64, source RankSource) {
	s.rankOwner, s.rankSource = owner, source
}

func (s *Service) CurrentSeason() uint64 { return s.current().ID }

func (s *Service) RankRecords() ([]RankRecord, error) {
	var records []RankRecord
	keys := make([]string, 0, len(s.state.Users))
	for key := range s.state.Users {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		u := s.state.Users[key]
		if !u.Played || u.RankLevel == 0 {
			continue
		}
		record, err := s.rankRecord(u)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Service) rankRecord(u user) (RankRecord, error) {
	score, err := s.score(u)
	if err != nil {
		return RankRecord{}, err
	}
	design, err := s.load(u.Hunt)
	if err != nil {
		return RankRecord{}, err
	}
	return RankRecord{Season: u.Season, Level: u.RankLevel, Damage: u.RankDamage, Score: score, Version: design.RankingVersion}, nil
}

func (s *Service) ranking(u user) (RankEntry, error) {
	record, err := s.rankRecord(u)
	if err != nil {
		return RankEntry{}, err
	}
	if record.Level == 0 {
		return RankEntry{RankRecord: record}, nil
	}
	if s.rankSource != nil {
		return s.rankSource.Rank(s.rankOwner, record), nil
	}
	return RankEntry{RankRecord: record, Rank: 1, TopPercent: 100}, nil
}

func (s *Service) ProfileRank() (uint64, float64, error) {
	u, ok := s.state.Users[strconv.FormatUint(s.current().ID, 10)]
	if !ok || !u.Played {
		return 0, 0, nil
	}
	rank, err := s.ranking(u)
	return rank.Rank, rank.TopPercent, err
}
