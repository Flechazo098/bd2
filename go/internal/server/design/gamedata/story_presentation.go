package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"fmt"
)

type StoryInteraction struct {
	Timeline          string
	SimpleTalkGroupID uint64
	QuestIDs          []uint64
}

func (s *Source) StoryInteraction(pack, object int) (StoryInteraction, error) {
	db, release, err := OpenDatabase(s.root, s.version, fmt.Sprintf("pack%d", pack))
	if err != nil {
		return StoryInteraction{}, err
	}
	defer release()
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM FieldQuestObjectTable WHERE id=?", object).Scan(&raw); err != nil {
		return StoryInteraction{}, err
	}
	var out StoryInteraction
	value, _, err := wire.Bytes(raw, 14)
	if err != nil {
		return out, err
	}
	out.Timeline = string(value)
	out.SimpleTalkGroupID, err = optionalScalar(raw, 13)
	if err != nil {
		return out, err
	}
	out.QuestIDs, err = packedInts(raw, 12)
	return out, err
}
