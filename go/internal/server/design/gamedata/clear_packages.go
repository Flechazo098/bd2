package gamedata

import "fmt"

// ClearPackageRewardDesign matches ClearPackTable and ClearEvilCastleTable.
// Kind is the wire enum: zero story pack, one evil-castle tower.
type ClearPackageRewardDesign struct{ Kind, GroupID, TicketID, TargetID, Level, RandomBoxID, Type uint64 }
type ClearPackageCatalog struct{ Rewards []ClearPackageRewardDesign }

func LoadClearPackageCatalog(root, version string) (*ClearPackageCatalog, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	c := &ClearPackageCatalog{}
	for kind, table := range []string{"ClearPackTable", "ClearEvilCastleTable"} {
		rows, err := db.Query("SELECT ProtoBuf FROM " + table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				_ = rows.Close()
				return nil, err
			}
			v, err := cashScalars(raw, 1, 2, 3, 4, 5, 6)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			r := ClearPackageRewardDesign{Kind: uint64(kind), TicketID: v[0], GroupID: v[1], Type: v[5]}
			if kind == 0 {
				r.TargetID = v[2]
				r.Level = v[3]
				r.RandomBoxID = v[4]
			} else {
				r.RandomBoxID = v[2]
				r.Level = v[3]
				r.TargetID = v[4]
			}
			if r.GroupID == 0 || r.TicketID == 0 || r.TargetID == 0 || r.RandomBoxID == 0 || r.Type > 1 {
				_ = rows.Close()
				return nil, fmt.Errorf("gamedata: invalid clear-package reward")
			}
			c.Rewards = append(c.Rewards, r)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return c, nil
}
