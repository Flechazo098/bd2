package events_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/server/calendar"
	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/hunting"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type auditedLeaves struct{}

func (auditedLeaves) Resolve(rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return rewards, nil
}
func auditArray(raw []byte, number int) []uint64 {
	var result []uint64
	_ = wire.Walk(raw, func(f wire.Field) error {
		if f.Number != number {
			return nil
		}
		if f.Type == 0 {
			v, _ := binary.Uvarint(f.Value)
			result = append(result, v)
		}
		if f.Type == 2 {
			for p := f.Value; len(p) > 0; {
				v, n := binary.Uvarint(p)
				if n <= 0 {
					break
				}
				result = append(result, v)
				p = p[n:]
			}
		}
		return nil
	})
	return result
}
func TestInstalledScheduledTaskAttendanceAndPassRewardCoverage(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	const version = "20260923193640"
	design, err := gamedata.LoadEventTasksDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := calendar.LoadDirectory(filepath.Join("..", "..", "..", "..", "schedules"), "2.35.10", version)
	if err != nil {
		t.Fatal(err)
	}
	play, err := gamedata.LoadEventPlayCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	exchanges, err := gamedata.LoadEventExchangeCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	var roots []gamedata.BattleReward
	appendReward := func(r gamedata.Reward) {
		if r.Type != 0 && r.Count != 0 {
			roots = append(roots, gamedata.BattleReward(r))
		}
	}
	missionGroups := map[uint64]bool{}
	appendPass := func(id uint64) {
		pass := design.Passes[id]
		missionGroups[pass.MissionGroup] = true
		appendReward(pass.Core)
		for _, level := range design.PassLevels[pass.LevelGroup] {
			appendReward(level.Basic)
			appendReward(level.Premium)
		}
		for _, buy := range design.PassBuys[id] {
			for _, reward := range buy.Rewards {
				appendReward(reward)
			}
		}
	}
	for _, schedule := range schedules.Events {
		switch schedule.Type {
		case 0:
			group := design.Attendance[schedule.ID].Group
			groups := map[uint64]bool{group: true}
			for _, row := range design.AttendanceGroups {
				if row.Group == group {
					groups[row.ID] = true
				}
			}
			for group := range groups {
				for _, reward := range design.AttendanceRewards[group] {
					appendReward(reward.Basic)
					appendReward(reward.Premium)
				}
			}
		case 1:
			for key, box := range design.LimitRewards {
				if key[0] == schedule.ID {
					roots = append(roots, gamedata.BattleReward{Type: 9, ID: box, Count: 1})
				}
			}
		case 4:
			missionGroups[schedule.ID] = true
		case 7:
			for _, entry := range exchanges.Groups[schedule.ID].Entries {
				appendReward(entry.Reward)
			}
		case 9:
			for _, row := range play.Rows("PackEventBattleTable", 4, schedule.ID) {
				rewards, err := gamedata.EventPlayRewards(row, 9, 10, 8)
				if err != nil {
					t.Fatal(err)
				}
				roots = append(roots, rewards...)
			}
		case 10:
			for _, row := range play.Rows("PackEventStoryTable", 1, schedule.ID) {
				rewards, err := gamedata.EventPlayRewards(row, 11, 10, 9)
				if err != nil {
					t.Fatal(err)
				}
				roots = append(roots, rewards...)
			}
		case 12, 13, 17, 19:
			game, err := gamedata.LoadEventGame(root, version, schedule.Type, schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, rows := range [][]gamedata.EventGameReward{game.Cells, game.Lines, game.Complete} {
				for _, row := range rows {
					roots = append(roots, row.Rewards...)
				}
			}
		case 5:
			appendPass(schedule.ID)
		}
	}
	for id := range missionGroups {
		for _, group := range design.MissionGroups[id].Groups {
			for _, mission := range design.Missions {
				if mission.Group == group {
					for _, reward := range mission.Rewards {
						appendReward(reward)
					}
				}
			}
		}
	}
	// The installed mini-game families have verified direct reward fields;
	// validate each design row, including the next published game variants.
	for _, spec := range []struct {
		table          string
		typ, id, count int
	}{{"FieldMiniGameRewardTable", 6, 5, 4}, {"SichuanRewardTable", 6, 5, 4}, {"HopscotchRewardTable", 7, 6, 4}, {"ActionGameMissionTable", 10, 9, 8}, {"MGDRewardTable", 5, 3, 2}} {
		for _, row := range play.Tables[spec.table] {
			rewards, err := gamedata.EventPlayRewards(row, spec.typ, spec.id, spec.count)
			if err != nil {
				t.Fatal(err)
			}
			roots = append(roots, rewards...)
		}
	}
	if schedules.MonsterHunt != nil {
		seen := map[uint64]bool{}
		for _, season := range schedules.MonsterHunt.Seasons {
			if seen[season.HuntID] {
				continue
			}
			seen[season.HuntID] = true
			hunt, err := gamedata.LoadMonsterHunt(root, version, season.HuntID)
			if err != nil {
				t.Fatal(err)
			}
			for _, reward := range hunt.Rewards {
				roots = append(roots, reward.Clear...)
				roots = append(roots, reward.Daily...)
			}
			for _, rows := range hunt.Ranks {
				for _, reward := range rows {
					roots = append(roots, reward.Rewards...)
				}
			}
		}
	}
	if len(roots) == 0 {
		t.Fatal("published calendar has no audited reward definitions")
	}
	// Walk every possible OPEN-box branch, not one RNG sample. DIRECT boxes are
	// actual inventory rewards and remain intact. This is a regression oracle
	// for the verified protobuf reward graph fields.
	db, closeDB, err := gamedata.OpenDatabase(root, version, "common")
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	tables := map[string]map[uint64][]byte{}
	for _, name := range []string{"RandomBoxTable", "RewardGroupTable"} {
		rows, err := db.Query("SELECT id,ProtoBuf FROM " + name)
		if err != nil {
			t.Fatal(err)
		}
		tables[name] = map[uint64][]byte{}
		for rows.Next() {
			var id uint64
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				t.Fatal(err)
			}
			tables[name][id] = raw
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	leaves := map[[2]uint64]gamedata.BattleReward{}
	var walk func(gamedata.BattleReward, map[uint64]bool)
	walk = func(reward gamedata.BattleReward, seen map[uint64]bool) {
		if reward.Type != 9 {
			reward.Count = 1
			leaves[[2]uint64{reward.Type, reward.ID}] = reward
			return
		}
		raw, ok := tables["RandomBoxTable"][reward.ID]
		if !ok {
			t.Fatalf("unknown scheduled box%d", reward.ID)
		}
		drop, _, _ := wire.Varint(raw, 1)
		if drop == 1 {
			reward.Count = 1
			leaves[[2]uint64{9, reward.ID}] = reward
			return
		}
		if seen[reward.ID] {
			t.Fatalf("scheduled box cycle%d", reward.ID)
		}
		seen[reward.ID] = true
		defer delete(seen, reward.ID)
		gid, _, _ := wire.Varint(raw, 9)
		group, ok := tables["RewardGroupTable"][gid]
		if !ok {
			t.Fatalf("unknown scheduled reward group%d", gid)
		}
		types, ids, counts := auditArray(group, 6), auditArray(group, 5), auditArray(group, 4)
		if len(types) == 0 || len(types) != len(ids) || len(types) != len(counts) {
			t.Fatalf("malformed scheduled reward group%d", gid)
		}
		for i, typ := range types {
			if counts[i] != 0 {
				walk(gamedata.BattleReward{Type: typ, ID: ids[i], Count: 1}, seen)
			}
		}
	}
	for _, reward := range roots {
		walk(reward, map[uint64]bool{})
	}
	costumes, err := gamedata.LoadRewardCostumeCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	equipmentDesign, err := gamedata.LoadRewardEquipmentCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := gamedata.LoadOwnedEventItemDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	prestige, err := gamedata.LoadPrestigeSkins(root, version)
	if err != nil {
		t.Fatal(err)
	}
	avatars, err := gamedata.LoadAvatarRewardDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	buffDesign, err := gamedata.LoadBuffRewardDesign(root, version)
	if err != nil {
		t.Fatal(err)
	}
	store := stateio.NewMemory()
	items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := player.OpenEquipmentInventory(store)
	if err != nil {
		t.Fatal(err)
	}
	economy, err := events.NewEconomy(store, items, wallet, collection, equipment, costumes, equipmentDesign, auditedLeaves{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	economy.AttachOwnedItemDesign(owned)
	economy.AttachPrestigeSkins(prestige)
	economy.AttachAvatarRewards(avatars)
	buffs, err := events.OpenBuffRewards(store, buffDesign)
	if err != nil {
		t.Fatal(err)
	}
	economy.AttachBuffRewards(buffs)
	ap, err := hunting.Open(store, root, version, items, wallet, func() (int, error) { return 21, nil }, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	economy.AttachHuntingAP(ap)
	for key, reward := range leaves {
		if _, err = economy.Apply(fmt.Sprintf("coverage:%d:%d", key[0], key[1]), nil, []gamedata.Reward{gamedata.Reward(reward)}); err != nil {
			t.Errorf("scheduled reward leaf %d:%d unsupported: %v", key[0], key[1], err)
		}
	}
	t.Logf("validated %d published reward roots and %d distinct possible leaves against real grant domains", len(roots), len(leaves))
}
