package gamedata

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Reward is an immutable GameData reward definition. Type, ID and Count are
// the exact parallel arrays stored by the client design tables; it is not a
// player inventory row.
type Reward struct {
	Type  uint64
	ID    uint64
	Count uint64
}

type MissionKey struct{ GroupType, GroupID, ID uint64 }
type SectionRewardKey struct{ GroupType, ID uint64 }
type AchievementKey struct{ ContentsGroup, GroupID, ID uint64 }

type MissionDesign struct {
	Missions     map[MissionKey][]Reward
	Conditions   map[MissionKey]MissionCondition
	Sections     map[SectionRewardKey]SectionRewardDesign
	Achievements map[AchievementKey]AchievementDesign
}

type MissionCondition struct {
	Type        uint64
	SubType     uint64
	Params      []uint64
	TargetValue uint64
	UnlockPack  uint64
	UnlockQuest uint64
}

type SectionRewardDesign struct {
	SectionValue uint64
	Rewards      []Reward
}

type AchievementDesign struct {
	AddExp  uint64
	Rewards []Reward
}

// LoadMissionDesign loads the three non-event design tables from the shared
// 2.34.13 database. Event missions intentionally remain outside this reader:
// their eligibility depends on an active server schedule, not just GameData.
func LoadMissionDesign(root, version string) (*MissionDesign, error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-mission-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadMissionDesignDB(db)
}

func loadMissionDesignDB(db *sql.DB) (*MissionDesign, error) {
	if db == nil {
		return nil, fmt.Errorf("gamedata: nil mission database")
	}
	design := &MissionDesign{
		Missions: map[MissionKey][]Reward{}, Conditions: map[MissionKey]MissionCondition{}, Sections: map[SectionRewardKey]SectionRewardDesign{},
		Achievements: map[AchievementKey]AchievementDesign{},
	}
	if err := loadMissionRows(db, design); err != nil {
		return nil, err
	}
	if err := loadSectionRows(db, design); err != nil {
		return nil, err
	}
	if err := loadAchievementRows(db, design); err != nil {
		return nil, err
	}
	return design, nil
}

func loadMissionRows(db *sql.DB, design *MissionDesign) error {
	rows, err := db.Query("SELECT ProtoBuf FROM MissionTable")
	if err != nil {
		return fmt.Errorf("gamedata: query MissionTable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var proto []byte
		if err := rows.Scan(&proto); err != nil {
			return err
		}
		groupID, err := optionalScalar(proto, 6)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable group id: %w", err)
		}
		groupType, err := optionalScalar(proto, 7)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable group type: %w", err)
		}
		id, err := optionalScalar(proto, 8)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable id: %w", err)
		}
		// GroupType zero is DAILY, a valid proto3 default. Rows without a
		// group/id identity remain non-claimable templates.
		if groupID == 0 || id == 0 {
			continue
		}
		rewards, err := parallelRewards(proto, 15, 14, 13)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable %d/%d/%d: %w", groupType, groupID, id, err)
		}
		key := MissionKey{groupType, groupID, id}
		if _, exists := design.Missions[key]; exists {
			return fmt.Errorf("gamedata: duplicate MissionTable key %+v", key)
		}
		design.Missions[key] = rewards
		conditionType, err := optionalScalar(proto, 3)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable condition type: %w", err)
		}
		conditionSubType, err := optionalScalar(proto, 1)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable condition subtype: %w", err)
		}
		params, err := packedInts(proto, 2)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable params: %w", err)
		}
		target, err := optionalScalar(proto, 4)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable target: %w", err)
		}
		unlockPack, err := optionalScalar(proto, 19)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable unlock pack: %w", err)
		}
		unlockQuest, err := optionalScalar(proto, 20)
		if err != nil {
			return fmt.Errorf("gamedata: MissionTable unlock quest: %w", err)
		}
		design.Conditions[key] = MissionCondition{Type: conditionType, SubType: conditionSubType, Params: params, TargetValue: target, UnlockPack: unlockPack, UnlockQuest: unlockQuest}
	}
	return rows.Err()
}

func loadSectionRows(db *sql.DB, design *MissionDesign) error {
	rows, err := db.Query("SELECT ProtoBuf FROM MissionSectionRewardTable")
	if err != nil {
		return fmt.Errorf("gamedata: query MissionSectionRewardTable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var proto []byte
		if err := rows.Scan(&proto); err != nil {
			return err
		}
		groupType, err := optionalScalar(proto, 1)
		if err != nil {
			return fmt.Errorf("gamedata: MissionSectionRewardTable group type: %w", err)
		}
		id, err := optionalScalar(proto, 2)
		if err != nil {
			return fmt.Errorf("gamedata: MissionSectionRewardTable id: %w", err)
		}
		if id == 0 {
			continue
		}
		rewards, err := parallelRewards(proto, 5, 4, 3)
		if err != nil {
			return fmt.Errorf("gamedata: MissionSectionRewardTable %d/%d: %w", groupType, id, err)
		}
		key := SectionRewardKey{groupType, id}
		if _, exists := design.Sections[key]; exists {
			return fmt.Errorf("gamedata: duplicate MissionSectionRewardTable key %+v", key)
		}
		sectionValue, err := optionalScalar(proto, 6)
		if err != nil {
			return fmt.Errorf("gamedata: MissionSectionRewardTable section value: %w", err)
		}
		design.Sections[key] = SectionRewardDesign{SectionValue: sectionValue, Rewards: rewards}
	}
	return rows.Err()
}

func loadAchievementRows(db *sql.DB, design *MissionDesign) error {
	rows, err := db.Query("SELECT ProtoBuf FROM AchievementTable")
	if err != nil {
		return fmt.Errorf("gamedata: query AchievementTable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var proto []byte
		if err := rows.Scan(&proto); err != nil {
			return err
		}
		contents, err := optionalScalar(proto, 4)
		if err != nil {
			return fmt.Errorf("gamedata: AchievementTable contents group: %w", err)
		}
		groupID, err := optionalScalar(proto, 9)
		if err != nil {
			return fmt.Errorf("gamedata: AchievementTable group id: %w", err)
		}
		id, err := optionalScalar(proto, 11)
		if err != nil {
			return fmt.Errorf("gamedata: AchievementTable id: %w", err)
		}
		if contents == 0 || groupID == 0 || id == 0 {
			continue
		}
		exp, err := optionalScalar(proto, 8)
		if err != nil {
			return fmt.Errorf("gamedata: AchievementTable exp: %w", err)
		}
		rewards, err := parallelRewards(proto, 16, 15, 14)
		if err != nil {
			return fmt.Errorf("gamedata: AchievementTable %d/%d/%d: %w", contents, groupID, id, err)
		}
		key := AchievementKey{contents, groupID, id}
		if _, exists := design.Achievements[key]; exists {
			return fmt.Errorf("gamedata: duplicate AchievementTable key %+v", key)
		}
		design.Achievements[key] = AchievementDesign{AddExp: exp, Rewards: rewards}
	}
	return rows.Err()
}

func parallelRewards(proto []byte, typeField, idField, countField int) ([]Reward, error) {
	types, err := packedInts(proto, typeField)
	if err != nil {
		return nil, err
	}
	ids, err := packedInts(proto, idField)
	if err != nil {
		return nil, err
	}
	counts, err := packedInts(proto, countField)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 && len(types) == len(counts) {
		ids = make([]uint64, len(types))
	}
	if len(types) != len(ids) || len(ids) != len(counts) {
		// A design row may be a mission marker with only display/condition
		// values populated. Keeping an empty reward list preserves the
		// claimable identity without inventing an item from partial arrays.
		return nil, nil
	}
	rewards := make([]Reward, len(types))
	for i := range rewards {
		if types[i] == 0 || counts[i] == 0 {
			return nil, fmt.Errorf("invalid reward at %d", i)
		}
		rewards[i] = Reward{Type: types[i], ID: ids[i], Count: counts[i]}
	}
	return rewards, nil
}
