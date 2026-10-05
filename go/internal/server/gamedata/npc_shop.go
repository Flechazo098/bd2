package gamedata

import (
	"database/sql"
	"fmt"
)

// NPCShopDesign is the ordinary in-game shop, distinct from CashShopTable.
type NPCShopDesign struct {
	Shops    map[uint64]NPCShop
	Products map[uint64]map[uint64]NPCProduct
	Sell     map[uint64]NPCProduct
}
type NPCShop struct{ ID, PackID, ResetType, ResetCount, StartDay uint64 }
type NPCProduct struct {
	ID, GroupID, MaxCount, Discount, Premium, NoBargain, Reputation uint64
	Reward, Price                                                   Reward
	PremiumPriceType, HighPremium, HighDay, HighShop                uint64
}

func LoadNPCShopDesign(root, version string) (NPCShopDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return NPCShopDesign{}, err
	}
	defer closeDB()
	return loadNPCShopDesign(db)
}
func loadNPCShopDesign(db *sql.DB) (NPCShopDesign, error) {
	d := NPCShopDesign{Shops: map[uint64]NPCShop{}, Products: map[uint64]map[uint64]NPCProduct{}, Sell: map[uint64]NPCProduct{}}
	err := friendshipRows(db, "SELECT id,ProtoBuf FROM ShopTable ORDER BY id", func(id uint64, p []byte) error {
		r := NPCShop{}
		for i, dst := range []*uint64{&r.ID, &r.PackID, &r.ResetType, &r.ResetCount, &r.StartDay} {
			v, e := friendshipScalar(p, []int{5, 8, 12, 11, 14}[i])
			if e != nil {
				return e
			}
			*dst = v
		}
		if r.ID != id || id == 0 || r.ResetType > 3 {
			return fmt.Errorf("gamedata: invalid shop %d", id)
		}
		d.Shops[id] = r
		return nil
	})
	if err != nil {
		return d, err
	}
	err = friendshipRows(db, "SELECT id,ProtoBuf FROM ProductTable ORDER BY groupId,id", func(id uint64, p []byte) error {
		r := NPCProduct{}
		for i, dst := range []*uint64{&r.ID, &r.GroupID, &r.MaxCount, &r.Discount, &r.Premium, &r.NoBargain, &r.Reputation, &r.Reward.Type, &r.Reward.ID, &r.Reward.Count, &r.Price.Type, &r.Price.ID, &r.Price.Count} {
			v, e := friendshipScalar(p, []int{7, 6, 1, 2, 9, 8, 13, 5, 4, 3, 12, 11, 10}[i])
			if e != nil {
				return e
			}
			*dst = v
		}
		if r.ID != id || r.Reward.Count == 0 || r.Price.Count == 0 || r.Discount > 100 {
			return fmt.Errorf("gamedata: invalid shop product %d", id)
		}
		if _, ok := d.Shops[r.GroupID]; !ok {
			return fmt.Errorf("gamedata: product missing shop %d", r.GroupID)
		}
		if d.Products[r.GroupID] == nil {
			d.Products[r.GroupID] = map[uint64]NPCProduct{}
		}
		d.Products[r.GroupID][id] = r
		return nil
	})
	if err != nil {
		return d, err
	}
	err = friendshipRows(db, "SELECT id,ProtoBuf FROM SellItemTable ORDER BY id", func(id uint64, p []byte) error {
		r := NPCProduct{}
		for i, dst := range []*uint64{&r.ID, &r.Discount, &r.Premium, &r.Reward.Type, &r.Reward.ID, &r.Reward.Count, &r.Price.Type, &r.Price.ID, &r.Price.Count, &r.PremiumPriceType, &r.HighPremium, &r.HighDay, &r.HighShop} {
			v, e := friendshipScalar(p, []int{8, 1, 10, 4, 3, 2, 13, 12, 11, 9, 6, 5, 7}[i])
			if e != nil {
				return e
			}
			*dst = v
		}
		if r.ID != id || r.Reward.Count == 0 || r.Price.Count == 0 || r.Discount > 100 {
			return fmt.Errorf("gamedata: invalid sell product %d", id)
		}
		d.Sell[id] = r
		return nil
	})
	return d, err
}
