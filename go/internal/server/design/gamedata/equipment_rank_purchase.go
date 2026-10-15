package gamedata

import "fmt"

type EquipmentRankPurchase struct {
	Key                            CashProductKey
	PriceType, PriceID, PriceCount uint64
	Ranks                          []uint64
}
type EquipmentRankPurchaseDesign struct {
	Products map[[2]uint64]EquipmentRankPurchase
	Eligible map[uint64]bool
}

func LoadEquipmentRankPurchaseDesign(root, version string) (*EquipmentRankPurchaseDesign, error) {
	db, release, err := OpenDatabase(root, version, "common")
	if err != nil {
		return nil, err
	}
	defer release()
	result := &EquipmentRankPurchaseDesign{Products: map[[2]uint64]EquipmentRankPurchase{}, Eligible: map[uint64]bool{}}
	ranks := map[uint64][]uint64{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM EquipmentRankChangeItemTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		vector, err := packedInts(raw, 5)
		if err != nil || len(vector) != 3 {
			_ = rows.Close()
			return nil, fmt.Errorf("gamedata: invalid rank change item %d", id)
		}
		for _, v := range vector {
			if v < 1 || v > 4 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid rank change item %d value", id)
			}
		}
		ranks[id] = vector
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	rows, err = db.Query("SELECT ProtoBuf FROM CashProductTable")
	if err != nil {
		return nil, err
	}
	var products []CashProductDesign
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		product, err := decodeCashProduct(raw)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		if product.PriceType == 68 {
			products = append(products, product)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for _, product := range products {
		var raw []byte
		if err := db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", product.RandomBoxID).Scan(&raw); err != nil {
			return nil, err
		}
		group, err := optionalScalar(raw, 9)
		if err != nil {
			return nil, err
		}
		if err := db.QueryRow("SELECT ProtoBuf FROM RewardGroupTable WHERE id=?", group).Scan(&raw); err != nil {
			return nil, err
		}
		ids, idErr := packedInts(raw, 5)
		types, typeErr := packedInts(raw, 6)
		counts, countErr := packedInts(raw, 4)
		if idErr != nil || typeErr != nil || countErr != nil || len(ids) != 1 || len(types) != 1 || len(counts) != 1 || types[0] != 69 || counts[0] != 1 {
			continue
		}
		vector, exists := ranks[ids[0]]
		if !exists || product.PriceCount == 0 || product.PriceID != 0 || product.BonusRandomBoxID != 0 {
			return nil, fmt.Errorf("gamedata: malformed rank purchase %+v", product.Key)
		}
		key := [2]uint64{product.Key.GroupID, product.Key.ProductID}
		if _, exists := result.Products[key]; exists {
			return nil, fmt.Errorf("gamedata: ambiguous rank product sale group %+v", key)
		}
		result.Products[key] = EquipmentRankPurchase{Key: product.Key, PriceType: product.PriceType, PriceID: product.PriceID, PriceCount: product.PriceCount, Ranks: append([]uint64(nil), vector...)}
	}
	rows, err = db.Query("SELECT id,ProtoBuf FROM EquipmentTable")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		fields := map[int]uint64{}
		for _, number := range []int{3, 7, 18, 20} {
			value, err := optionalScalar(raw, number)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			fields[number] = value
		}
		result.Eligible[id] = fields[3] >= 4 && fields[7] == 0 && fields[18] >= 3 && fields[20] <= 4
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(result.Products) == 0 {
		return nil, fmt.Errorf("gamedata: no equipment rank products")
	}
	return result, nil
}
