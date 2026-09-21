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
	return BaseStats{Health: math.Trunc(baseHP * (1 + hpRatio)), Attack: math.Trunc(baseAttack * (1 + attackRatio)), Magic: math.Trunc(baseMagic * (1 + magicRatio))}, nil
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
