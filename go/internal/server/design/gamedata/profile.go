package gamedata

import (
	"bd2server/internal/server/protocol/wire"
	"fmt"
	"math"
	"strings"
)

type ProfileCardItem struct {
	ID, Kind, MaxCount, Unlock uint64
	Magic                      []uint64
}

type ProfileCardShop struct {
	MaxCount uint64
	Costs    []Reward
}

type ProfileDesign struct {
	Defaults                    []IdCardDefault
	CardItems                   map[uint64]ProfileCardItem
	Shop                        map[uint64]ProfileCardShop
	Titles                      map[uint64]bool
	Colors                      map[string]bool
	KindLimits                  map[uint64]uint64
	PresetLimit, PlacementLimit uint64
	DefaultReward               Reward
	DefaultPortraitCostumeID    uint64
}

func LoadProfileDesign(root, version string) (*ProfileDesign, error) {
	defaults, err := LoadIdCardDefaults(root, version)
	if err != nil {
		return nil, err
	}
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	d := &ProfileDesign{Defaults: defaults, CardItems: map[uint64]ProfileCardItem{}, Shop: map[uint64]ProfileCardShop{}, Titles: map[uint64]bool{}, Colors: map[string]bool{}, KindLimits: map[uint64]uint64{}}
	var raw []byte
	if err = db.QueryRow("SELECT ProtoBuf FROM GameDefaultTable WHERE id=0").Scan(&raw); err != nil {
		return nil, err
	}
	d.DefaultPortraitCostumeID, err = optionalScalar(raw, 29)
	if err != nil {
		return nil, err
	}
	if d.DefaultPortraitCostumeID == 0 || d.DefaultPortraitCostumeID > math.MaxInt32 {
		return nil, fmt.Errorf("gamedata: invalid default portrait costume")
	}
	if err := readCashMetadata(db, "IdCardItemTable", func(raw []byte) error {
		v, err := cashScalars(raw, 3, 13, 10, 14)
		if err != nil {
			return err
		}
		magic, err := packedInts(raw, 9)
		if err != nil {
			return err
		}
		if v[0] == 0 || v[1] > 6 || v[2] == 0 {
			return fmt.Errorf("gamedata: invalid ID card item")
		}
		d.CardItems[v[0]] = ProfileCardItem{ID: v[0], Kind: v[1], MaxCount: v[2], Unlock: v[3], Magic: magic}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readCashMetadata(db, "IdCardItemtShopTable", func(raw []byte) error {
		v, err := cashScalars(raw, 2, 1)
		if err != nil {
			return err
		}
		costs, err := parallelRewards(raw, 5, 4, 3)
		if err != nil {
			return err
		}
		if v[0] == 0 || v[1] == 0 || len(costs) == 0 {
			return fmt.Errorf("gamedata: invalid ID card shop")
		}
		d.Shop[v[0]] = ProfileCardShop{MaxCount: v[1], Costs: costs}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readCashMetadata(db, "TitleItemTable", func(raw []byte) error {
		id, err := optionalScalar(raw, 2)
		if err == nil {
			d.Titles[id] = true
		}
		return err
	}); err != nil {
		return nil, err
	}
	if err := readCashMetadata(db, "IdCardItemEnumTable", func(raw []byte) error {
		v, err := cashScalars(raw, 1, 2)
		if err == nil {
			d.KindLimits[v[0]] = v[1]
		}
		return err
	}); err != nil {
		return nil, err
	}
	for table, field := range map[string]int{"IdCardTextColortTable": 2, "IdCardBgEffectColortTable": 1} {
		if err := readCashMetadata(db, table, func(raw []byte) error {
			color, _, err := wire.Bytes(raw, field)
			if err == nil {
				d.Colors[strings.ToUpper(string(color))] = true
			}
			return err
		}); err != nil {
			return nil, err
		}
	}
	if err := readCashMetadata(db, "IdCardDefaultTable", func(raw []byte) error {
		v, err := cashScalars(raw, 5, 6, 7, 9, 10)
		if err != nil {
			return err
		}
		d.DefaultReward = Reward{Type: v[2], ID: v[1], Count: v[0]}
		d.PresetLimit, d.PlacementLimit = v[3], v[4]
		return nil
	}); err != nil {
		return nil, err
	}
	if d.PresetLimit == 0 || d.PlacementLimit == 0 || d.DefaultReward.Count == 0 {
		return nil, fmt.Errorf("gamedata: invalid ID card defaults")
	}
	return d, nil
}

type IdCardDefault struct {
	Kind, ID, Layer uint64
	X, Y            int32
	Rotate, Scale   float32
	Color           string
}

func LoadIdCardDefaults(root, version string) ([]IdCardDefault, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := db.Query("SELECT ProtoBuf FROM DefaultIdCardSettingTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []IdCardDefault
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		v, err := cashScalars(raw, 1, 4, 5, 8)
		if err != nil {
			return nil, err
		}
		color, _, err := wire.Bytes(raw, 2)
		if err != nil {
			return nil, err
		}
		x, err := optionalDouble(raw, 6)
		if err != nil {
			return nil, err
		}
		y, err := optionalDouble(raw, 7)
		if err != nil {
			return nil, err
		}
		scale, err := optionalDouble(raw, 9)
		if err != nil {
			return nil, err
		}
		if v[0] < 1 || v[0] > 6 || v[1] > math.MaxInt32 || v[2] > math.MaxInt32 || v[3] > math.MaxInt32 || math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(scale) || x < math.MinInt32 || x > math.MaxInt32 || y < math.MinInt32 || y > math.MaxInt32 || scale <= 0 || scale > math.MaxFloat32 {
			return nil, fmt.Errorf("gamedata: invalid default ID card layout")
		}
		out = append(out, IdCardDefault{Kind: v[0], ID: v[1], Layer: v[2], X: int32(x), Y: int32(y), Rotate: float32(v[3]), Scale: float32(scale), Color: string(color)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gamedata: missing default ID card layout")
	}
	return out, nil
}
