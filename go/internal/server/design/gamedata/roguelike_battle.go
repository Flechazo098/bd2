package gamedata

import "fmt"

func (d *RogueLikeDesign) loadEnemies(root, version string) error {
	db, done, e := openStatDatabase(root, version)
	if e != nil {
		return e
	}
	defer done()
	rows, e := db.Query("SELECT id,ProtoBuf FROM PackTable")
	if e != nil {
		return e
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			break
		}
		kind, _ := optionalScalar(raw, 55)
		if kind == 4 {
			d.Pack = id
		}
	}
	if e == nil {
		e = rows.Err()
	}
	_ = rows.Close()
	if e != nil {
		return e
	}
	if d.Pack == 0 {
		return fmt.Errorf("roguelike: missing castle pack")
	}
	var access []byte
	if e = db.QueryRow("SELECT ProtoBuf FROM ContentOpenTable WHERE groupId=1 AND id=?", d.Pack).Scan(&access); e != nil {
		return e
	}
	d.PackOpen.TicketID, e = optionalScalar(access, 6)
	if e != nil {
		return e
	}
	d.PackOpen.SquadLevel, e = optionalScalar(access, 5)
	if e != nil {
		return e
	}
	pack, closePack, e := OpenDatabase(root, version, fmt.Sprintf("pack%d", d.Pack))
	if e != nil {
		return e
	}
	defer closePack()
	wanted := map[uint64]bool{}
	for _, r := range d.Rooms.Rows {
		if (r.V(10) >= 1 && r.V(10) <= 3 || r.V(10) == 5 || r.V(10) == 7) && r.V(1) > 0 {
			wanted[r.V(1)] = true
		}
	}
	d.Enemies = map[uint64][]RogueEnemy{}
	rows, e = pack.Query("SELECT id,ProtoBuf FROM BattleDeckTable")
	if e != nil {
		return e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id uint64
		var raw []byte
		if e = rows.Scan(&id, &raw); e != nil {
			return e
		}
		if !wanted[id] {
			continue
		}
		ids, e := packedInts(raw, 14)
		if e != nil {
			return e
		}
		levels, e := packedInts(raw, 30)
		if e != nil {
			return e
		}
		positions, e := packedInts(raw, 32)
		if e != nil {
			return e
		}
		if len(ids) == 0 || len(ids) != len(levels) || len(ids) != len(positions) {
			return fmt.Errorf("roguelike: invalid enemy deck %d", id)
		}
		for i, c := range ids {
			d.Enemies[id] = append(d.Enemies[id], RogueEnemy{c, levels[i], positions[i]})
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	for id := range wanted {
		if len(d.Enemies[id]) == 0 {
			return fmt.Errorf("roguelike: missing enemy deck %d", id)
		}
	}
	return nil
}
