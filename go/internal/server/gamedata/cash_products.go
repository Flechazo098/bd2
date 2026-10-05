package gamedata

import (
	"database/sql"
	"fmt"
	"math"

	"bd2server/internal/server/wire"
)

// CashProductKey is the official three-part identity; an SDK SKU can be shared
// by several product variants and must not replace this key.
type CashProductKey struct{ GroupID, ProductID, SaleGroup uint64 }
type CashProductDesign struct {
	Key                                                                         CashProductKey
	GoogleSKU, AppleSKU                                                         string
	PriceType, PriceID, PriceCount                                              uint64
	RandomBoxID, BonusRandomBoxID                                               uint64
	PurchaseLimitType, PurchaseLimitCount, TimeLimitType, BulkOrderAvailability uint64
	LocalTextID                                                                 uint64
	Recharge                                                                    bool
	NominalPaidDiamonds                                                         uint64
}
type CashShopDesign struct{ GroupID, ID, ProductGroupID, BulkOrderShow uint64 }
type EventShopDesign struct{ ID, ProductGroupID uint64 }
type CashPackageDesign struct{ GroupID, ID, SaleGroup, PackageType, PaidShopGroupID, PaidShopID, ContentsGroupID, ContentsSortID uint64 }
type CashCatalog struct {
	Products   []CashProductDesign
	Shops      []CashShopDesign
	Packages   []CashPackageDesign
	EventShops []EventShopDesign
}

func LoadCashCatalog(root, version string) (*CashCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	return loadCashCatalog(db)
}
func loadCashCatalog(db *sql.DB) (*CashCatalog, error) {
	c := &CashCatalog{}
	rows, err := db.Query("SELECT ProtoBuf FROM CashProductTable ORDER BY groupId,id,saleGroup")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		p, e := decodeCashProduct(raw)
		if e != nil {
			rows.Close()
			return nil, e
		}
		c.Products = append(c.Products, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	seen := map[CashProductKey]bool{}
	// Direct single paid-diamond rewards identify recharge offers. The least
	// grant among variants of the same group/sale tier is the regular amount,
	// so first-purchase doubling never doubles the configurable price.
	nominal := map[[2]uint64]uint64{}
	for i := range c.Products {
		p := &c.Products[i]
		if seen[p.Key] {
			return nil, fmt.Errorf("gamedata: duplicate cash product %+v", p.Key)
		}
		seen[p.Key] = true
		if p.PriceType != 1 || p.RandomBoxID == 0 {
			continue
		}
		n, e := cashPaidDiamondReward(db, p.RandomBoxID)
		if e != nil {
			return nil, fmt.Errorf("gamedata: cash product %+v: %w", p.Key, e)
		}
		if n == 0 {
			continue
		}
		p.Recharge = true
		k := [2]uint64{p.Key.GroupID, p.Key.SaleGroup}
		if nominal[k] == 0 || n < nominal[k] {
			nominal[k] = n
		}
	}
	for i := range c.Products {
		p := &c.Products[i]
		if p.Recharge {
			p.NominalPaidDiamonds = nominal[[2]uint64{p.Key.GroupID, p.Key.SaleGroup}]
		}
	}
	if err = readCashMetadata(db, "CashShopTable", func(raw []byte) error {
		v, e := cashScalars(raw, 4, 5, 10, 2)
		if e != nil {
			return e
		}
		c.Shops = append(c.Shops, CashShopDesign{v[0], v[1], v[2], v[3]})
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "CashPackageTable", func(raw []byte) error {
		v, e := cashScalars(raw, 8, 9, 17, 11, 12, 13, 4, 7)
		if e != nil {
			return e
		}
		c.Packages = append(c.Packages, CashPackageDesign{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]})
		return nil
	}); err != nil {
		return nil, err
	}
	if err = readCashMetadata(db, "EventShopTable", func(raw []byte) error {
		v, e := cashScalars(raw, 2, 5)
		if e != nil {
			return e
		}
		if v[0] == 0 || v[1] == 0 {
			return fmt.Errorf("gamedata: invalid event shop")
		}
		c.EventShops = append(c.EventShops, EventShopDesign{v[0], v[1]})
		return nil
	}); err != nil {
		return nil, err
	}
	return c, nil
}
func readCashMetadata(db *sql.DB, table string, visit func([]byte) error) error {
	rows, err := db.Query("SELECT ProtoBuf FROM " + table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = visit(raw); err != nil {
			return err
		}
	}
	return rows.Err()
}
func cashScalars(raw []byte, fields ...int) ([]uint64, error) {
	out := make([]uint64, len(fields))
	for i, f := range fields {
		v, e := optionalScalar(raw, f)
		if e != nil {
			return nil, e
		}
		if v > math.MaxInt32 {
			return nil, fmt.Errorf("gamedata: cash field %d outside int32", f)
		}
		out[i] = v
	}
	return out, nil
}
func decodeCashProduct(raw []byte) (CashProductDesign, error) {
	v, e := cashScalars(raw, 5, 6, 15, 9, 8, 7, 14, 2, 13, 12, 16, 3, 11)
	if e != nil {
		return CashProductDesign{}, e
	}
	google, _, e := wire.Bytes(raw, 4)
	if e != nil {
		return CashProductDesign{}, e
	}
	apple, _, e := wire.Bytes(raw, 1)
	if e != nil {
		return CashProductDesign{}, e
	}
	p := CashProductDesign{Key: CashProductKey{v[0], v[1], v[2]}, GoogleSKU: string(google), AppleSKU: string(apple), PriceType: v[3], PriceID: v[4], PriceCount: v[5], RandomBoxID: v[6], BonusRandomBoxID: v[7], PurchaseLimitType: v[8], PurchaseLimitCount: v[9], TimeLimitType: v[10], BulkOrderAvailability: v[11], LocalTextID: v[12]}
	if p.Key.GroupID == 0 || p.Key.ProductID == 0 {
		return p, fmt.Errorf("gamedata: invalid cash product key %+v", p.Key)
	}
	if p.PriceType == 1 && (p.GoogleSKU == "" && p.AppleSKU == "" || p.PriceCount == 0) {
		return p, fmt.Errorf("gamedata: cash product lacks SKU/price %+v", p.Key)
	}
	return p, nil
}

// Mixed packages, random selection, nested boxes and non-diamond leaves are
// ordinary goods. Only the audited pure direct paid-diamond shape qualifies.
func cashPaidDiamondReward(db *sql.DB, box uint64) (uint64, error) {
	var raw []byte
	if err := db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", box).Scan(&raw); err != nil {
		return 0, err
	}
	gid, e := optionalScalar(raw, 9)
	if e != nil {
		return 0, e
	}
	if gid == 0 {
		return 0, nil
	}
	if e = db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", gid).Scan(&raw); e != nil {
		return 0, e
	}
	drop, e := optionalScalar(raw, 2)
	if e != nil {
		return 0, e
	}
	types, e := packedInts(raw, 6)
	if e != nil {
		return 0, e
	}
	ids, e := packedInts(raw, 5)
	if e != nil {
		return 0, e
	}
	counts, e := packedInts(raw, 4)
	if e != nil {
		return 0, e
	}
	ratios, e := packedInts(raw, 8)
	if e != nil {
		return 0, e
	}
	if drop != 1 || len(types) != 1 || len(ids) != 1 || len(counts) != 1 || len(ratios) != 1 || types[0] != 2 || ids[0] != 0 || ratios[0] != 100 {
		return 0, nil
	}
	if counts[0] == 0 || counts[0] > math.MaxInt32 {
		return 0, fmt.Errorf("gamedata: invalid paid-diamond recharge amount")
	}
	return counts[0], nil
}
