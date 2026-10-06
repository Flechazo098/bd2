package gamedata

import (
	"database/sql"
	"fmt"
	"slices"
)

// FriendshipKey identifies a costume's level or counseling session.
type FriendshipKey struct{ GroupID, ID uint64 }

type FriendshipDefaultDesign struct {
	CorrectEXP, CorrectSelectDialogIndex, IncorrectEXP uint64
	MaxCounselingAP, MaxCounselingAPByCostume          uint64
	QuickCounselingUnlockCount                         uint64
	MaxLevels                                          [3]uint64
	CounselingRewards                                  []Reward
}

type FriendshipGiftDesign struct {
	ID, Type, ItemID, GiftType, EXP, FavoriteEXP uint64
	FavoriteCostumeIDs                           []uint64
}

// Experience uses the original friendship costume ID, even when the mapped
// owned costume ID is different. GiftType 1 is favorite for every costume.
func (g FriendshipGiftDesign) Experience(costumeID uint64) uint64 {
	if g.GiftType == 1 {
		return g.FavoriteEXP
	}
	if slices.Contains(g.FavoriteCostumeIDs, costumeID) {
		return g.FavoriteEXP
	}
	return g.EXP
}

type FriendshipLevelDesign struct {
	NextEXP uint64
	Rewards []Reward
}

type FriendshipSessionDesign struct {
	DialogGroupID, ChoiceCount uint64
}

// FriendshipDesign holds immutable tables, not account progress. Gift keys
// are ItemType and item design ID; ItemType Resource differs from its subtype.
type FriendshipDesign struct {
	Default  FriendshipDefaultDesign
	Costumes map[uint64]uint64
	Gifts    map[[2]uint64]FriendshipGiftDesign
	Levels   map[FriendshipKey]FriendshipLevelDesign
	Sessions map[FriendshipKey]FriendshipSessionDesign
}

// LoadFriendshipDesign decrypts and opens the common database once for all
// friendship tables. The query helper is shared with other common designs.
func LoadFriendshipDesign(root, version string) (FriendshipDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return FriendshipDesign{}, err
	}
	defer closeDB()
	return loadFriendshipDesign(db)
}

func loadFriendshipDesign(db *sql.DB) (FriendshipDesign, error) {
	d := FriendshipDesign{Costumes: map[uint64]uint64{}, Gifts: map[[2]uint64]FriendshipGiftDesign{}, Levels: map[FriendshipKey]FriendshipLevelDesign{}, Sessions: map[FriendshipKey]FriendshipSessionDesign{}}
	if db == nil {
		return d, fmt.Errorf("gamedata: nil friendship database")
	}
	var proto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM FriendshipDefaultTable WHERE id=0").Scan(&proto); err != nil {
		return d, fmt.Errorf("gamedata: friendship defaults: %w", err)
	}
	defaults := []*uint64{&d.Default.CorrectEXP, &d.Default.CorrectSelectDialogIndex, &d.Default.IncorrectEXP, &d.Default.MaxCounselingAP, &d.Default.MaxCounselingAPByCostume, &d.Default.QuickCounselingUnlockCount, &d.Default.MaxLevels[0], &d.Default.MaxLevels[1], &d.Default.MaxLevels[2]}
	for i, field := range []int{1, 2, 12, 13, 14, 18, 7, 8, 9} {
		value, err := friendshipScalar(proto, field)
		if err != nil {
			return d, fmt.Errorf("gamedata: friendship defaults: %w", err)
		}
		*defaults[i] = value
	}
	var err error
	d.Default.CounselingRewards, err = friendshipRewards(proto, 5, 4, 3, true)
	if err != nil {
		return d, fmt.Errorf("gamedata: friendship counseling rewards: %w", err)
	}
	if d.Default.CorrectEXP == 0 || d.Default.IncorrectEXP == 0 || d.Default.MaxCounselingAP == 0 || d.Default.MaxCounselingAPByCostume == 0 || d.Default.QuickCounselingUnlockCount == 0 || d.Default.MaxLevels[0] <= 1 || d.Default.MaxLevels[0] >= d.Default.MaxLevels[1] || d.Default.MaxLevels[1] >= d.Default.MaxLevels[2] {
		return d, fmt.Errorf("gamedata: invalid friendship defaults")
	}
	if err := friendshipRows(db, "SELECT id,ProtoBuf FROM FriendshipCostumeTable ORDER BY id", func(id uint64, proto []byte) error {
		protoID, err := friendshipScalar(proto, 2)
		if err != nil {
			return err
		}
		costumeID, err := friendshipScalar(proto, 1)
		if err != nil {
			return err
		}
		if id == 0 || protoID != id || costumeID == 0 {
			return fmt.Errorf("invalid costume identity %d", id)
		}
		d.Costumes[id] = costumeID
		return nil
	}); err != nil {
		return d, fmt.Errorf("gamedata: friendship costumes: %w", err)
	}
	if err := friendshipRows(db, "SELECT id,ProtoBuf FROM FriendshipGiftTable ORDER BY id", func(id uint64, proto []byte) error {
		g := FriendshipGiftDesign{}
		for i, dest := range []*uint64{&g.ID, &g.Type, &g.ItemID, &g.GiftType, &g.EXP, &g.FavoriteEXP} {
			value, err := friendshipScalar(proto, []int{5, 7, 6, 4, 1, 2}[i])
			if err != nil {
				return err
			}
			*dest = value
		}
		var err error
		g.FavoriteCostumeIDs, err = packedInts(proto, 3)
		if err != nil {
			return err
		}
		if id == 0 || g.ID != id || g.Type == 0 || g.ItemID == 0 || g.GiftType > 1 || g.FavoriteEXP == 0 || (g.GiftType == 0 && g.EXP == 0) {
			return fmt.Errorf("invalid gift %d", id)
		}
		key := [2]uint64{g.Type, g.ItemID}
		if _, exists := d.Gifts[key]; exists {
			return fmt.Errorf("duplicate gift item %v", key)
		}
		for _, costumeID := range g.FavoriteCostumeIDs {
			if _, ok := d.Costumes[costumeID]; !ok {
				return fmt.Errorf("gift %d references unknown costume %d", id, costumeID)
			}
		}
		d.Gifts[key] = g
		return nil
	}); err != nil {
		return d, fmt.Errorf("gamedata: friendship gifts: %w", err)
	}
	if err := friendshipGroupRows(db, "SELECT groupId,id,ProtoBuf FROM FriendshipLevelTable ORDER BY groupId,id", func(key FriendshipKey, proto []byte) error {
		if err := friendshipIdentity(proto, key); err != nil {
			return err
		}
		next, err := friendshipScalar(proto, 3)
		if err != nil {
			return err
		}
		rewards, err := friendshipRewards(proto, 6, 5, 4, false)
		if err != nil {
			return err
		}
		if _, ok := d.Costumes[key.GroupID]; !ok || key.ID == 0 || key.ID > d.Default.MaxLevels[2] || (key.ID < d.Default.MaxLevels[2] && next == 0) {
			return fmt.Errorf("invalid level %v", key)
		}
		d.Levels[key] = FriendshipLevelDesign{NextEXP: next, Rewards: rewards}
		return nil
	}); err != nil {
		return d, fmt.Errorf("gamedata: friendship levels: %w", err)
	}
	if err := friendshipGroupRows(db, "SELECT groupId,id,ProtoBuf FROM CounselingSessionTable ORDER BY groupId,id", func(key FriendshipKey, proto []byte) error {
		if err := friendshipIdentity(proto, key); err != nil {
			return err
		}
		groupID, err := friendshipScalar(proto, 4)
		if err != nil {
			return err
		}
		if _, ok := d.Costumes[key.GroupID]; !ok || key.ID == 0 || groupID == 0 {
			return fmt.Errorf("invalid session %v", key)
		}
		d.Sessions[key] = FriendshipSessionDesign{DialogGroupID: groupID}
		return nil
	}); err != nil {
		return d, fmt.Errorf("gamedata: friendship sessions: %w", err)
	}
	// Dialog type 4 is EVisualNovelDialogType.Select. Resolve option counts
	// from the same design used by TimelineVisualNovelManager.StartSelectTalk.
	for key, session := range d.Sessions {
		rows, err := db.Query("SELECT s.ProtoBuf FROM VisualNovelDialogTable v JOIN SelectDialogTable s ON s.id=v.selectDialogId WHERE v.groupId=? AND v.type=4", session.DialogGroupID)
		if err != nil {
			return d, err
		}
		var matches int
		for rows.Next() {
			var selection []byte
			if err := rows.Scan(&selection); err != nil {
				_ = rows.Close()
				return d, err
			}
			choices, err := packedInts(selection, 1)
			if err != nil {
				_ = rows.Close()
				return d, err
			}
			session.ChoiceCount = uint64(len(choices))
			matches++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return d, err
		}
		if matches != 1 || session.ChoiceCount <= d.Default.CorrectSelectDialogIndex {
			return d, fmt.Errorf("gamedata: invalid counseling choices %v", key)
		}
		d.Sessions[key] = session
	}
	for costumeID := range d.Costumes {
		for level := uint64(1); level <= d.Default.MaxLevels[2]; level++ {
			if _, ok := d.Levels[FriendshipKey{costumeID, level}]; !ok {
				return d, fmt.Errorf("gamedata: missing friendship level %d/%d", costumeID, level)
			}
		}
	}
	if len(d.Costumes) == 0 || len(d.Gifts) == 0 || len(d.Sessions) == 0 {
		return d, fmt.Errorf("gamedata: empty friendship design")
	}
	return d, nil
}

func friendshipScalar(proto []byte, field int) (uint64, error) {
	values, err := packedInts(proto, field)
	if err != nil {
		return 0, err
	}
	if len(values) > 1 {
		return 0, fmt.Errorf("field %d has multiple scalar values", field)
	}
	if len(values) == 0 {
		return 0, nil
	}
	return values[0], nil
}

func friendshipIdentity(proto []byte, key FriendshipKey) error {
	group, err := friendshipScalar(proto, 1)
	if err != nil {
		return err
	}
	id, err := friendshipScalar(proto, 2)
	if err != nil {
		return err
	}
	if group != key.GroupID || id != key.ID {
		return fmt.Errorf("identity mismatch %v", key)
	}
	return nil
}

func friendshipRewards(proto []byte, typeField, idField, countField int, scalar bool) ([]Reward, error) {
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
	// The default reward is scalar, so omitted ID means protobuf's zero ID.
	if scalar && len(ids) == 0 && len(types) == 1 {
		ids = []uint64{0}
	}
	if len(types) != len(ids) || len(types) != len(counts) || (scalar && len(types) > 1) {
		return nil, fmt.Errorf("mismatched reward arrays")
	}
	rewards := make([]Reward, len(types))
	for i := range types {
		if types[i] == 0 || counts[i] == 0 {
			return nil, fmt.Errorf("invalid reward %d", i)
		}
		rewards[i] = Reward{Type: types[i], ID: ids[i], Count: counts[i]}
	}
	return rewards, nil
}

func friendshipRows(db *sql.DB, query string, visit func(uint64, []byte) error) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			return err
		}
		if err := visit(id, proto); err != nil {
			return err
		}
	}
	return rows.Err()
}

func friendshipGroupRows(db *sql.DB, query string, visit func(FriendshipKey, []byte) error) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key FriendshipKey
		var proto []byte
		if err := rows.Scan(&key.GroupID, &key.ID, &proto); err != nil {
			return err
		}
		if err := visit(key, proto); err != nil {
			return err
		}
	}
	return rows.Err()
}
