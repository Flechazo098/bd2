package gamedata

import (
	"fmt"
	"math"
)

type RoomProduct struct {
	ID, Limit uint64
	Reward    Reward
	Prices    []Reward
}

type RoomCommerceDesign struct {
	Products  map[uint64]RoomProduct
	Expansion map[uint64]Reward
}

func LoadRoomCommerceDesign(root, version string) (*RoomCommerceDesign, error) {
	db, release, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer release()
	d := &RoomCommerceDesign{Products: map[uint64]RoomProduct{}, Expansion: map[uint64]Reward{}}
	if err := readCashMetadata(db, "MyRoomItemShopTable", func(raw []byte) error {
		v, err := cashScalars(raw, 5, 1, 4, 3, 2)
		if err != nil {
			return err
		}
		types, err := packedInts(raw, 8)
		if err != nil {
			return err
		}
		ids, err := packedInts(raw, 7)
		if err != nil {
			return err
		}
		counts, err := packedInts(raw, 6)
		if err != nil || len(types) == 0 || len(types) != len(ids) || len(types) != len(counts) || v[0] == 0 || v[1] == 0 || v[4] == 0 {
			return fmt.Errorf("gamedata: invalid room product")
		}
		p := RoomProduct{ID: v[0], Limit: v[1], Reward: Reward{Type: v[2], ID: v[3], Count: v[4]}}
		for i, typ := range types {
			if typ > math.MaxInt32 || ids[i] > math.MaxInt32 || counts[i] > math.MaxInt32 {
				return fmt.Errorf("gamedata: invalid room price")
			}
			p.Prices = append(p.Prices, Reward{Type: typ, ID: ids[i], Count: counts[i]})
		}
		d.Products[p.ID] = p
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readCashMetadata(db, "MyRoomExpandTable", func(raw []byte) error {
		v, err := cashScalars(raw, 4, 3, 8, 7, 6)
		if err != nil {
			return err
		}
		if v[1] == 0 {
			d.Expansion[v[0]] = Reward{Type: v[2], ID: v[3], Count: v[4]}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}
