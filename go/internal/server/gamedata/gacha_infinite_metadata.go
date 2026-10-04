package gamedata

import (
	"database/sql"
	"fmt"
)

func loadInfiniteGachaIdentity(db *sql.DB, selectedGroups []uint64) (groupID, gachaID uint64, err error) {
	rows, err := db.Query("SELECT id,ProtoBuf FROM GachaGroupTable ORDER BY id")
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return
		}
		typ, e := optionalScalar(raw, 17)
		if e != nil {
			return 0, 0, e
		}
		sub, e := optionalScalar(raw, 16)
		if e != nil {
			return 0, 0, e
		}
		selected := false
		for _, wanted := range selectedGroups {
			if wanted == id {
				selected = true
			}
		}
		if !selected || typ != 1 || sub != 5 {
			continue
		}
		if groupID != 0 {
			return 0, 0, fmt.Errorf("gamedata: multiple infinite gacha groups require schedule selection")
		}
		groupID = id
		gachaID, err = optionalScalar(raw, 33)
		if err != nil {
			return
		}
	}
	if err = rows.Err(); err != nil {
		return
	}
	if groupID == 0 || gachaID == 0 {
		err = fmt.Errorf("gamedata: infinite gacha group missing")
	}
	return
}

func loadInfiniteCashIdentity(db *sql.DB, rewardID uint64) (groupID, productID, saleGroup uint64, err error) {
	rows, err := db.Query("SELECT ProtoBuf FROM CashProductTable ORDER BY GroupId,id,saleGroup")
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return
		}
		box, e := optionalScalar(raw, 14)
		if e != nil {
			return 0, 0, 0, e
		}
		if box == 0 {
			continue
		}
		var boxRaw []byte
		if e = db.QueryRow("SELECT ProtoBuf FROM RandomBoxTable WHERE id=?", box).Scan(&boxRaw); e != nil {
			return 0, 0, 0, e
		}
		reward, e := optionalScalar(boxRaw, 9)
		if e != nil {
			return 0, 0, 0, e
		}
		if reward != rewardID {
			continue
		}
		if productID != 0 {
			return 0, 0, 0, fmt.Errorf("gamedata: ambiguous infinite cash product")
		}
		groupID, err = optionalScalar(raw, 5)
		if err != nil {
			return
		}
		productID, err = optionalScalar(raw, 6)
		if err != nil {
			return
		}
		saleGroup, err = optionalScalar(raw, 15)
		if err != nil {
			return
		}
	}
	if err = rows.Err(); err != nil {
		return
	}
	if groupID == 0 || productID == 0 {
		err = fmt.Errorf("gamedata: infinite cash product missing")
	}
	return
}
