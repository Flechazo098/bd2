//go:build integration

package app

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/protocol/wire"
	"context"
	"encoding/binary"
	"testing"
	"time"
)

// RacingHUD reports point-table IDs, shared by multiple ordinary map objects.
// Its score packets and the reward guide define cumulative daily tiers. This
// runs those requests with installed GameData, the production economy and an
// account SQLite transaction, then closes and reopens the player.
func TestArcadeRacingMapObjectsCommitDailyInventoryRewardsOnce(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	catalog := factory.design.eventPlay
	var gameID, pointID, target, itemID, itemType, itemReward uint64
	var field *gamedata.EventField
	for _, game := range catalog.Tables["PackEventMiniGameTable"] {
		if appValue(game, 3) != 2 {
			continue
		}
		candidate, err := catalog.Field(appValue(game, 8))
		if err != nil {
			t.Fatal(err)
		}
		for id, object := range candidate.Objects {
			if object.RandomGroup != 0 || object.Type != 1 || object.Point <= 0 {
				continue
			}
			for _, row := range catalog.Rows("FieldMiniGameRewardTable", 1, appValue(game, 10)) {
				types, e := arcadeNumbers(row, 6)
				if e != nil {
					t.Fatal(e)
				}
				ids, e := arcadeNumbers(row, 5)
				if e != nil {
					t.Fatal(e)
				}
				counts, e := arcadeNumbers(row, 4)
				if e != nil {
					t.Fatal(e)
				}
				if len(types) != 1 || types[0] != 8 || len(ids) != 1 || len(counts) != 1 {
					continue
				}
				if appValue(row, 3) > uint64(object.Point) {
					gameID, pointID, target = appValue(game, 8), id, appValue(row, 3)
					field, itemID, itemType, itemReward = candidate, ids[0], types[0], counts[0]
					break
				}
			}
			if gameID != 0 {
				break
			}
		}
		if gameID != 0 {
			break
		}
	}
	if gameID == 0 {
		t.Fatal("installed Racing design has no ordinary shared point objects with an inventory reward tier")
	}
	now := time.Now()
	schedule := events.Schedule{UID: gameID, Type: 11, ID: gameID, Start: now.Add(-time.Hour).UnixMilli(), End: now.Add(time.Hour).UnixMilli()}
	factory.options.calendars.Events = []events.Schedule{schedule}
	shared, err := openSharedRuntime(context.Background(), factory.options, factory.design)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := shared.store.Close(); err != nil {
			t.Error(err)
		}
	})
	factory.shared = shared
	owner, err := factory.open(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	g := &puzzleGameplay{t: t, factory: factory, account: accounts[0], owner: owner}
	t.Cleanup(func() {
		if g.owner != nil {
			if err := g.owner.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	before := g.itemCount(itemType, itemID)
	start := g.request("/MiniGameFieldStart", appScalar(2, schedule.UID))
	body := g.execute(start, false)
	if retry := g.execute(start, false); string(retry) != string(body) {
		t.Fatal("committed start retry changed selected map objects")
	}
	selected, err := arcadeNumbers(body, 1)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[uint64]bool{}
	for _, id := range selected {
		allowed[id] = true
	}
	if !allowed[pointID] {
		t.Fatal("ordinary point objects disabled at start")
	}
	for id, object := range field.Objects {
		if object.RandomGroup > 0 && !allowed[id] {
			g.execute(g.request("/MiniGameFieldScore", append(appScalar(2, schedule.UID), appScalar(3, id)...)), true)
			break
		}
	}
	point := uint64(field.Objects[pointID].Point)
	count := (target + point - 1) / point
	var request []byte
	request = wire.AppendVarint(request, 2, schedule.UID)
	for range count {
		request = wire.AppendVarint(request, 3, pointID)
	}
	result := g.call("/MiniGameFieldScore", request)
	if appValue(result, 1) != count*point {
		t.Fatal("distinct ordinary map objects sharing the same point-table ID were dropped")
	}
	end := g.request("/MiniGameFieldEnd", nil)
	g.execute(end, false)
	game, err := catalog.Row("PackEventMiniGameTable", 8, gameID)
	if err != nil {
		t.Fatal(err)
	}
	var expected uint64
	for _, row := range catalog.Rows("FieldMiniGameRewardTable", 1, appValue(game, 10)) {
		if appValue(row, 3) > count*point {
			continue
		}
		types, e := arcadeNumbers(row, 6)
		if e != nil {
			t.Fatal(e)
		}
		ids, e := arcadeNumbers(row, 5)
		if e != nil {
			t.Fatal(e)
		}
		counts, e := arcadeNumbers(row, 4)
		if e != nil {
			t.Fatal(e)
		}
		if len(types) != len(ids) || len(types) != len(counts) {
			t.Fatal("inconsistent authored reward arrays")
		}
		for i, typ := range types {
			if typ == itemType && ids[i] == itemID {
				expected += counts[i]
			}
		}
	}
	if expected < itemReward {
		t.Fatal("selected inventory reward tier not included")
	}
	if g.itemCount(itemType, itemID) != before+expected {
		t.Fatal("completed score did not deposit the authored inventory reward")
	}
	paid := g.itemCount(itemType, itemID)
	g.reopen()
	g.execute(end, false)
	if g.itemCount(itemType, itemID) != paid {
		t.Fatal("reopened settlement retry duplicated inventory rewards")
	}
	info := g.call("/MiniGameFieldInfo", appScalar(2, schedule.UID))
	record, _, err := wire.Bytes(info, 1)
	if err != nil || appValue(record, 3) != count*point || appValue(record, 2) == 0 || appValue(record, 4) != 0 {
		t.Fatal("reopened score or claimed daily reward differs from actual settlement")
	}
}

func arcadeNumbers(raw []byte, field int) ([]uint64, error) {
	var result []uint64
	err := wire.Walk(raw, func(value wire.Field) error {
		if value.Number != field {
			return nil
		}
		if value.Type != 0 && value.Type != 2 {
			return wire.ErrMalformed
		}
		for data := value.Value; len(data) > 0; {
			number, n := binary.Uvarint(data)
			if n <= 0 {
				return wire.ErrMalformed
			}
			result = append(result, number)
			data = data[n:]
		}
		return nil
	})
	return result, err
}
