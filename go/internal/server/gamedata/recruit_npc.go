package gamedata

import (
	"database/sql"
	"fmt"
	"math"
)

type RecruitNPC struct {
	ID, MapID, ScoutID            uint64
	QuestEnableTypes, QuestRanges []uint64
	QuestTypes                    map[uint64]uint64
}

// LoadRecruitNPC resolves the Scout interaction in the selected pack database;
// FieldNpcTable.PackId is often zero and is not the database's pack identity.
func LoadRecruitNPC(root, version string, packID int, npcID uint64) (RecruitNPC, error) {
	if packID <= 0 || npcID == 0 || npcID > math.MaxInt32 {
		return RecruitNPC{}, fmt.Errorf("gamedata: invalid recruit NPC")
	}
	db, release, err := OpenDatabase(root, version, fmt.Sprintf("pack%d", packID))
	if err != nil {
		return RecruitNPC{}, err
	}
	defer release()
	npc, err := loadRecruitNPC(db, npcID)
	if err != nil {
		return RecruitNPC{}, err
	}
	if len(npc.QuestRanges) == 0 {
		return npc, nil
	}
	common, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return RecruitNPC{}, err
	}
	defer closeDB()
	if err = loadRecruitNPCQuestTypes(common, packID, &npc); err != nil {
		return RecruitNPC{}, err
	}
	return npc, nil
}

func loadRecruitNPC(db *sql.DB, npcID uint64) (RecruitNPC, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM FieldNpcTable WHERE id=?", npcID).Scan(&raw); err != nil {
		return RecruitNPC{}, fmt.Errorf("gamedata: recruit NPC %d: %w", npcID, err)
	}
	npc := RecruitNPC{QuestTypes: map[uint64]uint64{}}
	for field, dst := range map[int]*uint64{8: &npc.ID, 14: &npc.MapID} {
		values, err := packedInts(raw, field)
		if err != nil || len(values) != 1 || values[0] == 0 || values[0] > math.MaxInt32 {
			return RecruitNPC{}, fmt.Errorf("gamedata: invalid recruit NPC %d field%d", npcID, field)
		}
		*dst = values[0]
	}
	interactions, err := packedInts(raw, 9)
	if err != nil {
		return RecruitNPC{}, err
	}
	values, err := packedInts(raw, 12)
	if err != nil {
		return RecruitNPC{}, err
	}
	if npc.ID != npcID || len(interactions) != len(values) {
		return RecruitNPC{}, fmt.Errorf("gamedata: invalid recruit NPC interactions %d", npcID)
	}
	for i, interaction := range interactions {
		if interaction == 4 {
			npc.ScoutID = values[i]
			break
		}
	}
	if npc.ScoutID == 0 || npc.ScoutID > math.MaxInt32 {
		return RecruitNPC{}, fmt.Errorf("gamedata: NPC %d has no Scout interaction", npcID)
	}
	npc.QuestEnableTypes, err = packedInts(raw, 18)
	if err != nil {
		return RecruitNPC{}, err
	}
	npc.QuestRanges, err = packedInts(raw, 19)
	if err != nil {
		return RecruitNPC{}, err
	}
	if len(npc.QuestEnableTypes) != len(npc.QuestRanges) {
		return RecruitNPC{}, fmt.Errorf("gamedata: invalid recruit NPC quest ranges %d", npcID)
	}
	for i, quest := range npc.QuestRanges {
		if npc.QuestEnableTypes[i] > 3 || quest > math.MaxInt32 {
			return RecruitNPC{}, fmt.Errorf("gamedata: invalid recruit NPC quest gate %d", npcID)
		}
	}
	return npc, nil
}

func loadRecruitNPCQuestTypes(db *sql.DB, packID int, npc *RecruitNPC) error {
	rows, err := db.Query(fmt.Sprintf("SELECT id,ProtoBuf FROM QuestTable%d", packID))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var quest uint64
		var raw []byte
		if err := rows.Scan(&quest, &raw); err != nil {
			return err
		}
		types, err := packedInts(raw, 63)
		if err != nil || len(types) > 1 {
			return fmt.Errorf("gamedata: invalid NPC quest type %d", quest)
		}
		var kind uint64
		if len(types) == 1 {
			kind = types[0]
		}
		npc.QuestTypes[quest] = kind
	}
	return rows.Err()
}
