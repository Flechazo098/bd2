package castle

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
)

type RankProfile struct {
	Owner, XP, Portrait, PortraitDesign, Title uint64
	Name                                       string
	Guild                                      []byte
}
type RankRecord struct{ Season, Stage, Point, Date uint64 }
type RankEntry struct {
	RankRecord
	Profile RankProfile
	Rank    uint64
}
type RankPublication struct {
	Account string
	Version uint64
	Profile RankProfile
	Records []RankRecord
}
type RankSource interface {
	Top(season, stage uint64, limit int) []RankEntry
	Rank(owner uint64, record RankRecord) RankEntry
}

func (s *Service) AttachRanking(owner uint64, source RankSource) {
	s.rankOwner, s.rankSource = owner, source
}

// AttachRankProfile supplies the player's current public profile for their
// own ranking row when they have no published score.
func (s *Service) AttachRankProfile(f func(command.Context) (RankProfile, error)) { s.rankProfile = f }
func (s *Service) AttachSeasonSettlement(f func(uint64) bool)                     { s.settled = f }
func RankWire(entry RankEntry) []byte {
	p := entry.Profile
	out := wire.AppendVarint(nil, 1, p.Owner)
	out = wire.AppendString(out, 2, p.Name)
	for f, v := range map[int]uint64{3: p.XP, 4: p.Portrait, 5: p.PortraitDesign, 7: entry.Rank, 8: entry.Point, 9: p.Title, 10: entry.Date} {
		out = wire.AppendVarint(out, f, v)
	}
	return wire.AppendBytes(out, 6, p.Guild)
}
func (s *Service) seasonFloors(ctx command.Context, v *state) (map[uint64]map[uint64]floor, error) {
	rows, e := ctx.State.ListEntries("missions", "castle_seasons")
	if e != nil {
		return nil, e
	}
	out := map[uint64]map[uint64]floor{}
	for key, raw := range rows {
		season, e := strconv.ParseUint(key, 10, 64)
		if e != nil || season == 0 {
			return nil, fmt.Errorf("castle: invalid season history")
		}
		var floors map[uint64]floor
		if e = json.Unmarshal(raw, &floors); e != nil {
			return nil, e
		}
		out[season] = floors
	}
	if v.Season != 0 {
		out[v.Season] = v.Floors
	}
	return out, nil
}
func recordsFor(season uint64, floors map[uint64]floor) []RankRecord {
	var out []RankRecord
	var total, date uint64
	for stage, f := range floors {
		if f.Highest == 0 {
			continue
		}
		out = append(out, RankRecord{season, stage, f.Highest, f.Achieved})
		total += f.Highest
		date = max(date, f.Achieved)
	}
	if total != 0 {
		out = append(out, RankRecord{season, 0, total, date})
	}
	return out
}
func (s *Service) RankRecords(ctx command.Context) ([]RankRecord, error) {
	v, _, _, e := s.load(ctx)
	if e != nil {
		return nil, e
	}
	seasons, e := s.seasonFloors(ctx, v)
	if e != nil {
		return nil, e
	}
	var out []RankRecord
	for season, floors := range seasons {
		out = append(out, recordsFor(season, floors)...)
	}
	slices.SortFunc(out, func(a, b RankRecord) int {
		if a.Season != b.Season {
			return cmp(a.Season, b.Season)
		}
		return cmp(a.Stage, b.Stage)
	})
	return out, nil
}
func cmp(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func (s *Service) placed(record RankRecord) (RankEntry, error) {
	if record.Point == 0 {
		return RankEntry{RankRecord: record}, nil
	}
	if s.rankSource == nil {
		return RankEntry{}, fmt.Errorf("castle: ranking unavailable")
	}
	return s.rankSource.Rank(s.rankOwner, record), nil
}
func (s *Service) rankInfo(ctx command.Context, v *state, path string, req []byte) ([]byte, error) {
	if n(req, 2) != s.design.PackID {
		return nil, fmt.Errorf("castle: invalid ranking pack")
	}
	season, stage := n(req, 3), n(req, 4)
	if season == 0 {
		season = v.Season
	}
	if stage != 0 {
		if _, ok := s.design.Floors[stage]; !ok {
			return nil, fmt.Errorf("castle: invalid ranking stage")
		}
	}
	seasons, e := s.seasonFloors(ctx, v)
	if e != nil {
		return nil, e
	}
	records := recordsFor(season, seasons[season])
	var own, total RankEntry
	for _, record := range records {
		if record.Stage != stage && record.Stage != 0 {
			continue
		}
		entry, e := s.placed(record)
		if e != nil {
			return nil, e
		}
		if record.Stage == stage {
			own = entry
		}
		if record.Stage == 0 {
			total = entry
		}
	}
	var out []byte
	if path == "/EvilCastleStageRankingInfo" {
		for field, value := range map[int]uint64{1: own.Rank, 2: own.Point, 3: total.Rank, 4: total.Point} {
			out = wire.AppendVarint(out, field, value)
		}
		return out, nil
	}
	if s.rankSource == nil {
		return nil, fmt.Errorf("castle: ranking unavailable")
	}
	for _, entry := range s.rankSource.Top(season, stage, 100) {
		out = wire.AppendBytes(out, 1, RankWire(entry))
	}
	// EvilCastleRankUI.UpdateRanking reads my_ranking_info unconditionally,
	// so a player without a score still gets their own row: unranked, no
	// points.
	if own.Point == 0 {
		own = RankEntry{RankRecord: RankRecord{Season: season, Stage: stage}, Profile: RankProfile{Owner: s.rankOwner}}
		if s.rankProfile != nil {
			profile, e := s.rankProfile(ctx)
			if e != nil {
				return nil, e
			}
			own.Profile = profile
		}
	}
	return wire.AppendBytes(out, 2, RankWire(own)), nil
}
func castleRankRewards(rows []gamedata.CastleRankReward, rank uint64) []gamedata.Reward {
	var out []gamedata.Reward
	if rank == 0 {
		return nil
	}
	for _, r := range rows {
		if r.Rank > rank {
			break
		}
		out = r.Rewards
	}
	return out
}
func rewardWire(r gamedata.Reward) []byte {
	out := wire.AppendVarint(nil, 1, r.ID)
	out = wire.AppendVarint(out, 2, r.Type)
	return wire.AppendVarint(out, 3, r.Count)
}
func (s *Service) seasonRewards(ctx command.Context, v *state, req []byte) ([]byte, error) {
	if n(req, 2) != s.design.PackID {
		return nil, fmt.Errorf("castle: invalid reward pack")
	}
	if s.settled == nil || s.grant == nil {
		return nil, fmt.Errorf("castle: season settlement unavailable")
	}
	seasons, e := s.seasonFloors(ctx, v)
	if e != nil {
		return nil, e
	}
	ids := make([]uint64, 0, len(seasons))
	for id := range seasons {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, season := range ids {
		if !s.settled(season) {
			continue
		}
		key := fmt.Sprint(season)
		_, claimed, e := ctx.State.LoadEntry("missions", "castle_season_claims", key)
		if e != nil {
			return nil, e
		}
		if claimed {
			continue
		}
		records := recordsFor(season, seasons[season])
		if len(records) == 0 {
			continue
		}
		slices.SortFunc(records, func(a, b RankRecord) int { return cmp(a.Stage, b.Stage) })
		var out []byte
		var rewards []gamedata.Reward
		for _, record := range records {
			entry, e := s.placed(record)
			if e != nil {
				return nil, e
			}
			rows := s.design.FloorRanks
			if record.Stage == 0 {
				rows = s.design.TotalRanks
			}
			rs := castleRankRewards(rows, entry.Rank)
			rewards = append(rewards, rs...)
			row := wire.AppendVarint(nil, 1, entry.Rank)
			if record.Stage == 0 {
				row = wire.AppendVarint(row, 2, record.Point)
				row = wire.AppendVarint(row, 3, 1)
				for _, r := range rs {
					row = wire.AppendBytes(row, 4, rewardWire(r))
				}
				out = wire.AppendBytes(out, 2, row)
			} else {
				row = wire.AppendVarint(row, 2, record.Stage)
				row = wire.AppendVarint(row, 3, record.Point)
				row = wire.AppendVarint(row, 4, 1)
				for _, r := range rs {
					row = wire.AppendBytes(row, 5, rewardWire(r))
				}
				out = wire.AppendBytes(out, 1, row)
			}
		}
		bundle, e := s.grant(ctx, "castle:season:"+key, rewards)
		if e != nil {
			return nil, e
		}
		out = wire.AppendBytes(out, 3, bundle)
		if e = ctx.State.PutEntry("missions", "castle_season_claims", key, []byte("true")); e != nil {
			return nil, e
		}
		return out, nil
	}
	return wire.AppendBytes(nil, 3, nil), nil
}
