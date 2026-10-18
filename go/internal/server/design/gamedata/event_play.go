package gamedata

import (
	"fmt"
	"sort"
)

// EventPlayCatalog keeps the installed static rows for event gameplay.
type EventPlayCatalog struct {
	Tables        map[string][][]byte
	FieldPacks    map[int]EventFieldPack
	root, version string
}

func LoadEventPlayCatalog(root, version string) (*EventPlayCatalog, error) {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return nil, e
	}
	defer done()
	c := &EventPlayCatalog{Tables: map[string][][]byte{}, root: root, version: version}
	c.FieldPacks, e = loadEventFieldPacks(db)
	if e != nil {
		return nil, e
	}
	names := []string{"PackEventHubTable", "PackEventListTable", "PackEventStoryGroupTable", "PackEventStoryTable", "PackEventBattleGroupTable", "PackEventBattleTable", "PackEventMiniGameTable", "FieldMiniGameRewardTable", "FieldMiniGameSpeedTable", "RhythmGameMusicTable", "RhythmGameGradeTable", "SichuanStageTable", "SichuanEventTable", "SichuanRewardTable", "HopscotchStageTable", "HopscotchRewardTable", "HopscotchDefaultTable", "FieldMiniGameSurvivalTable", "ActionGameDefaultTable", "ActionGameMissionTable", "ActionGameStageTable", "FieldMiniGameCharTable", "FieldMiniGameMapTable", "FieldMiniGameUpgradeTable", "FieldMiniGameUpgradeGroupTable", "FieldMiniGameSkillGroupTable", "FieldMiniGameSkillTable", "FieldMiniGameCharLevelTable", "FieldMiniGameMonsterTable", "FieldMiniGameSurvivalItemTable", "FieldMiniGameSurvivalBoxTable", "MGDRewardTable", "MGDWaveTable", "MGDDefaultTable"}
	names = append(names, "EventLostCoinTable")
	names = append(names, "FieldMiniGameCollectionTable", "FieldMiniGameMapGroupTable", "FieldMiniGameStarterTable", "SichuanDefaultTable", "SichuanBlockDesignTable", "RhythmGameDefaultTable", "RhythmGameCharTable", "HopscotchGalleryTable", "HopscotchRankingTable", "HopscotchAbuseTable")
	names = append(names, "PackManDefaultTable", "PackManModeTable", "PackManModeRuleTable", "PackManScoreTable", "PackManMapTable", "PackManObjectTable", "PackManItemTable", "PackManChaserTable", "PackManChaserSpawnTable", "PackManChainTable", "PackManGalleryTable", "PackManGuideTable")
	names = append(names, "ActionGameTable", "ActionGameCharTable")
	names = append(names, "PickUpCostumeEventTable")
	names = append(names, "FishingDefaultTable", "FishingMapTable", "FishingMapGroupTable", "FishingBoatTable", "FishingBoatDesignTable", "FishingRodTable", "FishingItemTable", "FishingCharTable", "FishingFishTable", "FishingFishPoolTable", "FishingFishSizePoolTable", "FishingGradePoolTable", "FishingCastingTable", "FishingBuffTable", "FishingBoatBuffTable", "FishingFishSkillTable", "FishingTimeTable")
	names = append(names, "FishingBuyItemTable", "FishingSellItemTable", "FishingShopTable", "FishingPricePremiumDefaultTable")
	names = append(names, "GameDefaultTable", "FishingInventoryBuyTable")
	for _, name := range names {
		rows, e := db.Query("SELECT ProtoBuf FROM " + name)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				_ = rows.Close()
				return nil, e
			}
			c.Tables[name] = append(c.Tables[name], append([]byte(nil), raw...))
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return c, nil
}
func (c *EventPlayCatalog) Row(table string, n int, id uint64) ([]byte, error) {
	for _, r := range c.Tables[table] {
		v, e := optionalScalar(r, n)
		if e != nil {
			return nil, e
		}
		if v == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("gamedata: %s row %d missing", table, id)
}
func (c *EventPlayCatalog) Rows(table string, n int, id uint64) [][]byte {
	var out [][]byte
	for _, r := range c.Tables[table] {
		v, _ := optionalScalar(r, n)
		if v == id {
			out = append(out, r)
		}
	}
	return out
}
func EventPlayRewards(raw []byte, typ, id, count int) ([]BattleReward, error) {
	return eventGameRewards(raw, typ, id, count)
}
func (c *EventPlayCatalog) Story(group, id uint64) ([]byte, error) {
	for _, r := range c.Rows("PackEventStoryTable", 1, group) {
		v, _ := optionalScalar(r, 2)
		if v == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("gamedata: missing event story")
}
func (c *EventPlayCatalog) ScoreRewards(group, score, kind uint64) (uint64, []BattleReward, error) {
	if score == 0 {
		return 0, nil, nil
	}
	var rewards []BattleReward
	point := uint64(0)
	rows := c.Rows("FieldMiniGameRewardTable", 1, group)
	sort.Slice(rows, func(i, j int) bool {
		a, _ := optionalScalar(rows[i], 3)
		b, _ := optionalScalar(rows[j], 3)
		return a < b
	})
	for _, r := range rows {
		p, e := optionalScalar(r, 3)
		if e != nil {
			return 0, nil, e
		}
		if kind == 3 || kind == 4 {
			p *= 1000
		}
		if kind == 3 && score > p || kind != 3 && score < p {
			continue
		}
		if point == 0 || kind == 3 && p < point || kind != 3 && p > point {
			point = p
		}
		earned, e := eventGameRewards(r, 6, 5, 4)
		if e != nil {
			return 0, nil, e
		}
		rewards = append(rewards, earned...)
	}
	return point, rewards, nil
}
