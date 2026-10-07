package gamedata

import (
	"database/sql"
	"fmt"
)

// ContentOpeningDesign describes the rapport prologue's prerequisite and
// completion marker. The client maps ContentOpenRequest type 1 to group 23,
// detail 2; ticket identities and level requirements remain version data.
type ContentOpeningDesign struct {
	Prerequisite, Completion ContentOpenRule
}

func LoadContentOpeningDesign(root, version string) (*ContentOpeningDesign, error) {
	db, done, err := openStatDatabase(root, version)
	if err != nil {
		return nil, err
	}
	defer done()
	return loadContentOpeningDesign(db)
}

func loadContentOpeningDesign(db *sql.DB) (*ContentOpeningDesign, error) {
	if db == nil {
		return nil, fmt.Errorf("gamedata: nil content opening database")
	}
	tickets := map[uint64]uint64{}
	rows, err := db.Query("SELECT id,ProtoBuf FROM ContentTicketTable ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uint64
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var protoID, typ uint64
		protoID, err = friendshipScalar(raw, 4)
		if err == nil {
			typ, err = friendshipScalar(raw, 8)
		}
		if err != nil || id == 0 || protoID != id || typ < 1 || typ > 6 {
			err = fmt.Errorf("gamedata: invalid content ticket %d", id)
			break
		}
		if _, exists := tickets[id]; exists {
			err = fmt.Errorf("gamedata: duplicate content ticket %d", id)
			break
		}
		tickets[id] = typ
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = db.Query("SELECT groupId,id,ticketId,ProtoBuf FROM ContentOpenTable ORDER BY groupId,id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	d := &ContentOpeningDesign{}
	seen := map[[2]uint64]bool{}
	for rows.Next() {
		var group, id, ticket uint64
		var raw []byte
		if err := rows.Scan(&group, &id, &ticket, &raw); err != nil {
			return nil, err
		}
		values := make([]uint64, 6)
		for i := range values {
			values[i], err = friendshipScalar(raw, i+1)
			if err != nil {
				return nil, fmt.Errorf("gamedata: content opening %d/%d: %w", group, id, err)
			}
		}
		key := [2]uint64{group, id}
		if group == 0 || id == 0 || values[0] != group || values[1] != id || ticket == 0 || values[5] != ticket || tickets[ticket] == 0 || seen[key] {
			return nil, fmt.Errorf("gamedata: invalid content opening %d/%d identity or ticket", group, id)
		}
		seen[key] = true
		if group != 23 || (id != 1 && id != 2) {
			continue
		}
		if tickets[ticket] != 1 {
			return nil, fmt.Errorf("gamedata: rapport opening %d requires permanent content ticket", id)
		}
		rule := ContentOpenRule{TutorialID: values[3], SquadLevel: values[4], TicketID: ticket}
		if id == 1 {
			d.Prerequisite = rule
		} else {
			d.Completion = rule
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !seen[[2]uint64{23, 1}] || !seen[[2]uint64{23, 2}] || d.Prerequisite.TicketID == d.Completion.TicketID {
		return nil, fmt.Errorf("gamedata: missing or indistinguishable rapport opening rules")
	}
	return d, nil
}
