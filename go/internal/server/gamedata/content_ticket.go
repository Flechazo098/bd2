package gamedata

import "fmt"

type GachaContentTicketDesign struct{ IDs map[uint64]bool }

func LoadGachaContentTicketDesign(root, version string) (*GachaContentTicketDesign, error) {
	db, closeDB, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	rows, err := db.Query("SELECT id,ProtoBuf FROM ContentTicketTable")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	d := &GachaContentTicketDesign{IDs: map[uint64]bool{}}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		typ, err := optionalScalar(raw, 8)
		if err != nil {
			return nil, err
		}
		if typ == 4 {
			if id == 0 {
				return nil, fmt.Errorf("gamedata: invalid gacha content ticket")
			}
			d.IDs[id] = true
		}
	}
	return d, rows.Err()
}
