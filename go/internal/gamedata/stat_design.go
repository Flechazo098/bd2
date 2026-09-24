package gamedata

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"bd2server/internal/wire"
	_ "modernc.org/sqlite"
)

// EquipmentOption identifies the exact server-owned option stored in
// EquipBaseInfo. Rank values use Define_EquipRankType: 0=None, 1=C ... 4=S.
type EquipmentOption struct {
	GroupID uint64
	ID      uint64
	Level   int
	Rank    [3]int
}

type characterStatBase struct {
	GrowthID uint64
	Base     BaseStats
}

// CharacterStatDesign is the immutable, in-memory subset of GameData needed
// to resolve character level stats. Keeping the complete curve beside the
// other loaded design data prevents request handlers from decrypting and
// materializing the roughly 180 MiB common database once per character.
type CharacterStatDesign struct {
	characters   map[uint64]characterStatBase
	growthGroups map[uint64]uint64
	levelRatios  map[[2]uint64]BaseStats
}

// BaseStats resolves one character and level without filesystem or database
// access. CharacterStatDesign is immutable after loading and is therefore
// safe for concurrent request handlers.
func (d *CharacterStatDesign) BaseStats(charID, level uint64) (BaseStats, error) {
	if d == nil || charID == 0 || level == 0 {
		return BaseStats{}, fmt.Errorf("gamedata: invalid character stat input")
	}
	character, found := d.characters[charID]
	if !found {
		return BaseStats{}, fmt.Errorf("gamedata: character stats %d: %w", charID, sql.ErrNoRows)
	}
	group, found := d.growthGroups[character.GrowthID]
	if !found {
		return BaseStats{}, fmt.Errorf("gamedata: character %d growth id %d: %w", charID, character.GrowthID, sql.ErrNoRows)
	}
	ratio, found := d.levelRatios[[2]uint64{group, level}]
	if !found {
		return BaseStats{}, fmt.Errorf("gamedata: character level group=%d level=%d: %w", group, level, sql.ErrNoRows)
	}
	return applyCharacterLevel(character.Base, ratio), nil
}

func applyCharacterLevel(base, ratio BaseStats) BaseStats {
	return BaseStats{
		Health: math.Trunc(base.Health * (1 + ratio.Health)),
		Attack: math.Trunc(base.Attack * (1 + ratio.Attack)),
		Magic:  math.Trunc(base.Magic * (1 + ratio.Magic)),
	}
}

func loadCharacterStatDesign(db *sql.DB) (*CharacterStatDesign, error) {
	design := &CharacterStatDesign{
		characters:   make(map[uint64]characterStatBase),
		growthGroups: make(map[uint64]uint64),
		levelRatios:  make(map[[2]uint64]BaseStats),
	}
	rows, err := db.Query("SELECT id, ProtoBuf FROM CharTable")
	if err != nil {
		return nil, fmt.Errorf("gamedata: query character stats: %w", err)
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		growthIDs, err := packedInts(proto, 1)
		if err != nil || len(growthIDs) != 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d growth id %v: %w", id, growthIDs, err)
		}
		health, found, err := fixed64Double(proto, 11)
		if err != nil || !found {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d health: %w", id, err)
		}
		attack, _, err := fixed64Double(proto, 17)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d attack: %w", id, err)
		}
		magic, _, err := fixed64Double(proto, 14)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character %d magic: %w", id, err)
		}
		design.characters[id] = characterStatBase{GrowthID: growthIDs[0], Base: BaseStats{Health: health, Attack: attack, Magic: magic}}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT id, ProtoBuf FROM CharGrowthTable")
	if err != nil {
		return nil, fmt.Errorf("gamedata: query character growths: %w", err)
	}
	for rows.Next() {
		var id uint64
		var proto []byte
		if err := rows.Scan(&id, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		groups, err := packedInts(proto, 1)
		if err != nil || len(groups) != 1 {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character growth %d group %v: %w", id, groups, err)
		}
		design.growthGroups[id] = groups[0]
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query("SELECT GroupId, id, ProtoBuf FROM CharLevelTable")
	if err != nil {
		return nil, fmt.Errorf("gamedata: query character levels: %w", err)
	}
	for rows.Next() {
		var group, level uint64
		var proto []byte
		if err := rows.Scan(&group, &level, &proto); err != nil {
			rows.Close()
			return nil, err
		}
		health, _, err := fixed64Double(proto, 6)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character level %d/%d health: %w", group, level, err)
		}
		attack, _, err := fixed64Double(proto, 12)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character level %d/%d attack: %w", group, level, err)
		}
		magic, _, err := fixed64Double(proto, 10)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("gamedata: character level %d/%d magic: %w", group, level, err)
		}
		design.levelRatios[[2]uint64{group, level}] = BaseStats{Health: health, Attack: attack, Magic: magic}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return design, nil
}

// CharacterBaseStats reads CharTable -> CharGrowthTable -> CharLevelTable and
// applies the client's level formula. It returns design stats only; account
// systems such as equipment and costumes are aggregated separately.
func CharacterBaseStats(root, version string, charID, level int) (BaseStats, error) {
	if charID <= 0 || level <= 0 {
		return BaseStats{}, fmt.Errorf("gamedata: invalid character stat input")
	}
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return BaseStats{}, err
	}
	defer closeDB()
	var character []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharTable WHERE id=?", charID).Scan(&character); err != nil {
		return BaseStats{}, fmt.Errorf("gamedata: character stats %d: %w", charID, err)
	}
	growthIDs, err := packedInts(character, 1)
	if err != nil || len(growthIDs) != 1 {
		return BaseStats{}, fmt.Errorf("gamedata: character %d growth id %v: %w", charID, growthIDs, err)
	}
	var growth []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharGrowthTable WHERE id=?", growthIDs[0]).Scan(&growth); err != nil {
		return BaseStats{}, err
	}
	groups, err := packedInts(growth, 1)
	if err != nil || len(groups) != 1 {
		return BaseStats{}, fmt.Errorf("gamedata: character growth group %v: %w", groups, err)
	}
	var levelProto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM CharLevelTable WHERE GroupId=? AND id=?", groups[0], level).Scan(&levelProto); err != nil {
		return BaseStats{}, fmt.Errorf("gamedata: character level group=%d level=%d: %w", groups[0], level, err)
	}
	baseHP, ok, err := fixed64Double(character, 11)
	if err != nil || !ok {
		return BaseStats{}, fmt.Errorf("gamedata: character %d health: %w", charID, err)
	}
	baseAttack, _, err := fixed64Double(character, 17)
	if err != nil {
		return BaseStats{}, err
	}
	baseMagic, _, err := fixed64Double(character, 14)
	if err != nil {
		return BaseStats{}, err
	}
	hpRatio, _, err := fixed64Double(levelProto, 6)
	if err != nil {
		return BaseStats{}, err
	}
	attackRatio, _, err := fixed64Double(levelProto, 12)
	if err != nil {
		return BaseStats{}, err
	}
	magicRatio, _, err := fixed64Double(levelProto, 10)
	if err != nil {
		return BaseStats{}, err
	}
	return applyCharacterLevel(
		BaseStats{Health: baseHP, Attack: baseAttack, Magic: baseMagic},
		BaseStats{Health: hpRatio, Attack: attackRatio, Magic: magicRatio},
	), nil
}

// EquipmentOptionContribution resolves an EquipOptionInfo through the real
// EquipmentOptionTable, including level and three rank breakpoints.
func EquipmentOptionContribution(root, version string, option EquipmentOption) (StatContribution, error) {
	if option.GroupID == 0 || option.ID == 0 || option.Level < 0 {
		return StatContribution{}, fmt.Errorf("gamedata: invalid equipment option")
	}
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return StatContribution{}, err
	}
	defer closeDB()
	var proto []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM EquipmentOptionTable WHERE GroupId=? AND id=?", option.GroupID, option.ID).Scan(&proto); err != nil {
		return StatContribution{}, fmt.Errorf("gamedata: equipment option %d/%d: %w", option.GroupID, option.ID, err)
	}
	defaultValue, _, err := fixed64Double(proto, 1)
	if err != nil {
		return StatContribution{}, err
	}
	growthValue, _, err := fixed64Double(proto, 4)
	if err != nil {
		return StatContribution{}, err
	}
	levels, err := fixed32Floats(proto, 6)
	if err != nil {
		return StatContribution{}, err
	}
	if len(levels) != 0 && option.Level >= len(levels) {
		return StatContribution{}, fmt.Errorf("gamedata: equipment level %d outside option curve", option.Level)
	}
	levelValue := 0.0
	if len(levels) != 0 {
		levelValue = levels[option.Level]
	}
	rankValue := 0.0
	for i, field := range []int{7, 8, 9} {
		values, err := fixed32Floats(proto, field)
		if err != nil {
			return StatContribution{}, err
		}
		rank := option.Rank[i]
		if rank == 0 {
			continue
		}
		if rank < 1 || rank > len(values) {
			return StatContribution{}, fmt.Errorf("gamedata: invalid equipment rank %d", rank)
		}
		rankValue += values[rank-1]
	}
	return optionContribution(option.ID, defaultValue+growthValue*(levelValue+rankValue))
}

func optionContribution(id uint64, value float64) (StatContribution, error) {
	percentage := id != 1 && id != 3 && id != 5
	digits := 0
	if percentage {
		digits = 4
	}
	factor := math.Pow10(digits)
	value = math.Round(value*factor) / factor
	contribution := StatContribution{}
	switch id {
	case 1, 2:
		contribution.Stat = StatHealth
	case 3, 4:
		contribution.Stat = StatAttack
	case 5, 6:
		contribution.Stat = StatMagic
	case 7:
		contribution.Stat = StatDefencePercent
	case 8:
		contribution.Stat = StatMagicResistancePercent
	default:
		return StatContribution{}, fmt.Errorf("gamedata: unsupported equipment stat option %d", id)
	}
	if percentage {
		contribution.Percent = value / 100
	} else {
		contribution.Flat = value
	}
	return contribution, nil
}

func fixed32Floats(proto []byte, number int) ([]float64, error) {
	var values []float64
	err := wire.Walk(proto, func(field wire.Field) error {
		if field.Number != number {
			return nil
		}
		if field.Type == 5 {
			values = append(values, float64(math.Float32frombits(binary.LittleEndian.Uint32(field.Value))))
			return nil
		}
		if field.Type != 2 || len(field.Value)%4 != 0 {
			return fmt.Errorf("field %d invalid float encoding", number)
		}
		for data := field.Value; len(data) != 0; data = data[4:] {
			values = append(values, float64(math.Float32frombits(binary.LittleEndian.Uint32(data))))
		}
		return nil
	})
	return values, err
}

func openStatDatabase(root, version string) (*sql.DB, func(), error) {
	plain, err := ReadQuestDatabase(root, version)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "bd2-stats-")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "common.db")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	return db, func() { _ = db.Close(); _ = os.RemoveAll(dir) }, nil
}
