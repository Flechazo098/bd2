package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"database/sql"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

type GuildRules struct {
	RenameCost                                                                                   Reward
	Flags, Emblems                                                                               map[uint64]bool
	MaxMembers, ApplicationLimit, ApplicationHours, LeaveDelayHours, DeleteDelayHours, ListCount uint64
	CreateCost, Attendance                                                                       Reward
}

type GuildRaidRules struct {
	FlagVersions                                                              []GuildRaidFlagVersion
	TurnAddScore, TurnScoreValue, SupportScore                                float64
	DailySupporterUses, BasicSupportReward, RentalSupportReward, RewardMailID uint64
	Bosses                                                                    map[uint64]map[uint64]GuildRaidBoss
	ScoreRewards                                                              []GuildRaidScoreReward
	RankRewards                                                               []GuildRaidRankReward
}

type GuildRaidFlagVersion struct{ GroupID, MinSeason, MaxSeason uint64 }

type GuildRaidBoss struct {
	PackID, CharID                                            uint64
	healthRate, healthSlope                                   float64
	GroupID, Level, AddScore, DeckID, MonsterID, PartsGroupID uint64
	BattleMap, ReadyMap                                       string
	MaxHP                                                     uint64
	Rewards                                                   []Reward
}

type GuildRaidScoreReward struct {
	ID, Score uint64
	Rewards   []Reward
}
type GuildRaidRankReward struct {
	ID         uint64
	TopPercent float64
	Rewards    []Reward
}
type SupporterRules struct {
	DailyUses, DailyRecommendedUses, DailyRewardClaims uint64
	Reward                                             Reward
}
type GuildDesign struct {
	Open       ContentOpenRule
	Guild      GuildRules
	Raid       GuildRaidRules
	Supporters SupporterRules
}

func LoadGuildDesign(root, version string) (*GuildDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &GuildDesign{Raid: GuildRaidRules{Bosses: map[uint64]map[uint64]GuildRaidBoss{}}}
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM ContentOpenTable WHERE groupId=17 AND id=1").Scan(&raw); err != nil {
		return nil, err
	}
	opening, err := cashScalars(raw, 4, 5, 6)
	if err != nil {
		return nil, err
	}
	d.Open = ContentOpenRule{TutorialID: opening[0], SquadLevel: opening[1], TicketID: opening[2]}
	if err = db.QueryRow("SELECT ProtoBuf FROM GuildDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	v, err := cashScalars(raw, 12, 14, 15, 13, 9, 8, 7, 6, 5, 4, 3, 11)
	if err != nil {
		return nil, err
	}
	d.Guild = GuildRules{MaxMembers: v[0], ApplicationLimit: v[1], ApplicationHours: v[2], LeaveDelayHours: v[3], DeleteDelayHours: v[4], CreateCost: Reward{Type: v[5], ID: v[6], Count: v[7]}, Attendance: Reward{Type: v[8], ID: v[9], Count: v[10]}}
	d.Guild.ListCount = v[11]
	d.Guild.Flags, d.Guild.Emblems = map[uint64]bool{}, map[uint64]bool{}
	for _, entry := range []struct {
		table string
		out   map[uint64]bool
	}{{"GuildFlagResourceTable", d.Guild.Flags}, {"GuildEmblemResourceTable", d.Guild.Emblems}} {
		if err = readCashMetadata(db, entry.table, func(raw []byte) error {
			id, e := optionalScalar(raw, 2)
			if e != nil {
				return e
			}
			if id > math.MaxInt32 {
				return fmt.Errorf("gamedata: invalid guild icon")
			}
			entry.out[id] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err = readCashMetadata(db, "UseItemTable", func(raw []byte) error {
		kind, e := optionalScalar(raw, 11)
		if e != nil {
			return e
		}
		if kind != 2 {
			return nil
		}
		id, e := optionalScalar(raw, 3)
		if e != nil {
			return e
		}
		if id == 0 || d.Guild.RenameCost.ID != 0 {
			return fmt.Errorf("gamedata: ambiguous guild rename ticket")
		}
		d.Guild.RenameCost = Reward{Type: 14, ID: id, Count: 1}
		return nil
	}); err != nil {
		return nil, err
	}
	if d.Guild.RenameCost.ID == 0 || len(d.Guild.Flags) == 0 || len(d.Guild.Emblems) == 0 {
		return nil, fmt.Errorf("gamedata: incomplete guild customization")
	}
	if d.Guild.MaxMembers == 0 || d.Guild.ListCount == 0 || d.Guild.ApplicationLimit == 0 || d.Guild.ApplicationHours == 0 || d.Guild.LeaveDelayHours == 0 || d.Guild.DeleteDelayHours == 0 || d.Guild.CreateCost.Type == 0 || d.Guild.CreateCost.Count == 0 {
		return nil, fmt.Errorf("gamedata: invalid guild defaults")
	}
	if err = db.QueryRow("SELECT ProtoBuf FROM GuildRaidDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	v, err = cashScalars(raw, 3, 21, 22, 15)
	if err != nil {
		return nil, err
	}
	d.Raid.DailySupporterUses, d.Raid.BasicSupportReward, d.Raid.RentalSupportReward, d.Raid.RewardMailID = v[0], v[1], v[2], v[3]
	if d.Raid.TurnAddScore, err = optionalDouble(raw, 24); err != nil {
		return nil, err
	}
	if d.Raid.TurnScoreValue, err = optionalDouble(raw, 25); err != nil {
		return nil, err
	}
	if d.Raid.SupportScore, err = optionalDouble(raw, 23); err != nil {
		return nil, err
	}
	if err = db.QueryRow("SELECT ProtoBuf FROM SupportCharacterDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	v, err = cashScalars(raw, 3, 2, 1, 7, 6, 8)
	if err != nil {
		return nil, err
	}
	d.Supporters = SupporterRules{DailyUses: v[0], DailyRecommendedUses: v[1], DailyRewardClaims: v[2], Reward: Reward{Type: v[3], ID: v[4], Count: v[5]}}
	if err = readCashMetadata(db, "GuildRaidBossTable", func(raw []byte) error {
		v, e := cashScalars(raw, 6, 7, 1, 15, 12, 13)
		if e != nil {
			return e
		}
		r := GuildRaidBoss{GroupID: v[0], Level: v[1], AddScore: v[2], DeckID: v[3], MonsterID: v[4], PartsGroupID: v[5]}
		battleMap, _, e := wire.Bytes(raw, 2)
		if e != nil {
			return e
		}
		readyMap, _, e := wire.Bytes(raw, 16)
		if e != nil {
			return e
		}
		r.BattleMap, r.ReadyMap = string(battleMap), string(readyMap)
		healthRate, e := optionalDouble(raw, 10)
		if e != nil {
			return e
		}
		healthSlope, e := optionalDouble(raw, 11)
		if e != nil {
			return e
		}
		r.healthRate, r.healthSlope = healthRate, healthSlope

		if r.GroupID == 0 || r.Level == 0 || r.DeckID == 0 {
			return fmt.Errorf("gamedata: invalid guild raid boss")
		}
		if r.Rewards, e = parallelRewards(raw, 19, 18, 17); e != nil {
			return e
		}
		if d.Raid.Bosses[r.GroupID] == nil {
			d.Raid.Bosses[r.GroupID] = map[uint64]GuildRaidBoss{}
		}
		if _, exists := d.Raid.Bosses[r.GroupID][r.Level]; exists {
			return fmt.Errorf("gamedata: duplicate guild raid boss")
		}
		d.Raid.Bosses[r.GroupID][r.Level] = r
		return nil
	}); err != nil {
		return nil, err
	}
	packDBs := map[uint64]*sql.DB{}
	for group, bosses := range d.Raid.Bosses {
		for level, r := range bosses {
			mapName, _, ok := strings.Cut(strings.TrimPrefix(r.BattleMap, "Map"), "_")
			if !ok {
				return nil, fmt.Errorf("gamedata: invalid guild raid map %q", r.BattleMap)
			}
			pack, e := strconv.ParseUint(mapName, 10, 32)
			if e != nil || pack == 0 {
				return nil, fmt.Errorf("gamedata: invalid guild raid map %q", r.BattleMap)
			}
			r.PackID = pack
			packDB := packDBs[pack]
			if packDB == nil {
				var release func()
				packDB, release, e = OpenDatabase(root, version, fmt.Sprintf("pack%d", pack))
				if e != nil {
					return nil, e
				}
				defer release()
				packDBs[pack] = packDB
			}
			var deckRaw []byte
			if e = packDB.QueryRow("SELECT ProtoBuf FROM BattleDeckTable WHERE id=?", r.DeckID).Scan(&deckRaw); e != nil {
				return nil, fmt.Errorf("guild boss %d/%d deck %d: %w", group, level, r.DeckID, e)
			}
			chars, e := packedInts(deckRaw, 14)
			if e != nil || len(chars) == 0 {
				return nil, fmt.Errorf("guild boss %d/%d has no main character", group, level)
			}
			r.CharID = chars[0]
			var charRaw []byte
			if e := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", r.CharID).Scan(&charRaw); e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			baseHP, e := optionalDouble(charRaw, 11)
			if e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			growth, e := optionalScalar(charRaw, 1)
			if e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			var growthRaw []byte
			if e = db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growth).Scan(&growthRaw); e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			levelGroup, e := optionalScalar(growthRaw, 1)
			if e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			var levelRaw []byte
			if e = db.QueryRow("SELECT ProtoBuf FROM CharLevelTable WHERE groupId=? AND id=?", levelGroup, r.Level).Scan(&levelRaw); e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			levelHP, e := optionalDouble(levelRaw, 6)
			if e != nil {
				return nil, fmt.Errorf("guild boss %d/%d: %w", group, level, e)
			}
			hp := math.Floor(baseHP + (1+levelHP)*math.Pow(float64(r.Level-1), 2)*r.healthRate*r.healthSlope)
			if math.IsNaN(hp) || math.IsInf(hp, 0) || hp < 1 || hp >= float64(math.MaxInt64) {
				return nil, fmt.Errorf("gamedata: invalid guild boss health")
			}
			r.MaxHP = uint64(hp)
			d.Raid.Bosses[group][level] = r
		}
	}
	if err = readCashMetadata(db, "GuildRaidRewardTable", func(raw []byte) error {
		v, e := cashScalars(raw, 1, 5)
		if e != nil {
			return e
		}
		r := GuildRaidScoreReward{ID: v[0], Score: v[1]}
		if r.Rewards, e = parallelRewards(raw, 4, 3, 2); e != nil {
			return e
		}
		if r.ID == 0 || r.Score == 0 || len(r.Rewards) == 0 {
			return fmt.Errorf("gamedata: invalid guild raid score reward")
		}
		d.Raid.ScoreRewards = append(d.Raid.ScoreRewards, r)
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "GuildRaidRankTable", func(raw []byte) error {
		id, e := optionalScalar(raw, 1)
		if e != nil {
			return e
		}
		percent, e := optionalDouble(raw, 2)
		if e != nil {
			return e
		}
		r := GuildRaidRankReward{ID: id, TopPercent: percent}
		if r.Rewards, e = parallelRewards(raw, 6, 5, 4); e != nil {
			return e
		}
		if id == 0 || math.IsNaN(percent) || math.IsInf(percent, 0) || percent <= 0 || percent > 100 || len(r.Rewards) == 0 {
			return fmt.Errorf("gamedata: invalid guild raid rank reward")
		}
		d.Raid.RankRewards = append(d.Raid.RankRewards, r)
		return nil
	}); err != nil {
		return nil, err
	}
	slices.SortFunc(d.Raid.ScoreRewards, func(a, b GuildRaidScoreReward) int {
		if a.Score < b.Score {
			return -1
		}
		if a.Score > b.Score {
			return 1
		}
		return 0
	})
	slices.SortFunc(d.Raid.RankRewards, func(a, b GuildRaidRankReward) int {
		if a.TopPercent < b.TopPercent {
			return -1
		}
		if a.TopPercent > b.TopPercent {
			return 1
		}
		return 0
	})
	if len(d.Raid.Bosses) == 0 || len(d.Raid.ScoreRewards) == 0 || len(d.Raid.RankRewards) == 0 || d.Raid.DailySupporterUses == 0 {
		return nil, fmt.Errorf("gamedata: incomplete guild raid design")
	}
	flagGroups := map[uint64]bool{}
	if err = readCashMetadata(db, "GuildRaidFlagTable", func(raw []byte) error {
		group, e := optionalScalar(raw, 1)
		if e != nil {
			return e
		}
		if group == 0 {
			return fmt.Errorf("gamedata: invalid guild raid flag group")
		}
		flagGroups[group] = true
		return nil
	}); err != nil {
		return nil, err
	}
	if len(flagGroups) != 1 {
		return nil, fmt.Errorf("gamedata: guild raid flag groups require an explicit season mapping")
	}
	minimum, maximum := uint64(math.MaxUint64), uint64(0)
	for season := range d.Raid.Bosses {
		minimum = min(minimum, season)
		maximum = max(maximum, season)
	}
	for group := range flagGroups {
		d.Raid.FlagVersions = []GuildRaidFlagVersion{{GroupID: group, MinSeason: minimum, MaxSeason: maximum}}
	}
	return d, nil
}
