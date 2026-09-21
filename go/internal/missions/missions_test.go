package missions

import (
	"path/filepath"
	"testing"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

func TestAchievementClaimsAcceptPackedAndUnpackedIDs(t *testing.T) {
	info := wire.AppendVarint(nil, 1, 7)
	packed := []byte{1, 2, 3}
	info = wire.AppendBytes(info, 2, packed)
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 1)
	request = wire.AppendBytes(request, 3, info)
	claims, err := achievementClaims(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].GroupID != 7 || len(claims[0].IDs) != 3 || claims[0].IDs[2] != 3 {
		t.Fatalf("claims=%+v", claims)
	}
}

func TestRequireSeqRejectsMissingSequence(t *testing.T) {
	if err := requireSeq(nil); err == nil {
		t.Fatal("expected missing sequence error")
	}
}

func TestOfficialBulkAchievementRequestMayOmitContentsGroup(t *testing.T) {
	design := &gamedata.MissionDesign{
		Missions: map[gamedata.MissionKey][]gamedata.Reward{}, Sections: map[gamedata.SectionRewardKey]gamedata.SectionRewardDesign{},
		Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{
			{ContentsGroup: 7, GroupID: 1, ID: 1}: {AddExp: 3},
		},
	}
	starter := &player.Starter{Version: "2.34.13"}
	inventory, err := player.OpenInventory(filepath.Join(t.TempDir(), "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(filepath.Join(t.TempDir(), "missions.json"), design, inventory)
	if err != nil {
		t.Fatal(err)
	}
	info := wire.AppendVarint(nil, 1, 1)
	info = wire.AppendVarint(info, 2, 1)
	request := wire.AppendVarint(nil, 1, 242)
	request = wire.AppendBytes(request, 3, info)
	code, response, handled, err := service.Handle("/AchievementClear", request)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || code != 168 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	exp, found, err := wire.Varint(response, 1)
	if err != nil || !found || exp != 3 {
		t.Fatalf("exp=%d found=%v err=%v", exp, found, err)
	}
}

func TestOfficialBulkSectionRequestAcceptsDefaultGroupType(t *testing.T) {
	design := &gamedata.MissionDesign{
		Missions: map[gamedata.MissionKey][]gamedata.Reward{}, Sections: map[gamedata.SectionRewardKey]gamedata.SectionRewardDesign{},
		Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{},
	}
	starter := &player.Starter{Version: "2.34.13"}
	inventory, err := player.OpenInventory(filepath.Join(t.TempDir(), "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(filepath.Join(t.TempDir(), "missions.json"), design, inventory)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 233)
	request = wire.AppendVarint(request, 3, 1)
	code, _, handled, err := service.Handle("/MissionSectionReward", request)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || code != 122 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
}

func TestMissionProgressIsNotClaimedAndDailyTypeZeroIsValid(t *testing.T) {
	key := gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 101}
	design := &gamedata.MissionDesign{
		Missions:   map[gamedata.MissionKey][]gamedata.Reward{key: {{Type: 4, Count: 1000}}},
		Conditions: map[gamedata.MissionKey]gamedata.MissionCondition{key: {TargetValue: 1}},
		Sections:   map[gamedata.SectionRewardKey]gamedata.SectionRewardDesign{}, Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{},
	}
	dir := t.TempDir()
	starter := &player.Starter{Version: "2.34.13"}
	inv, err := player.OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(filepath.Join(dir, "missions.json"), design, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteMission(key); err != nil {
		t.Fatal(err)
	}
	_, response, _, err := service.Handle("/MissionInfo", wire.AppendVarint(nil, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	entry, found, _ := wire.Bytes(response, 1)
	if !found {
		t.Fatal("mission missing")
	}
	value, _, _ := wire.Varint(entry, 4)
	claimed, claimedFound, _ := wire.Varint(entry, 5)
	if value != 1 || claimedFound || claimed != 0 {
		t.Fatalf("value=%d claimed=%d/%v", value, claimed, claimedFound)
	}
	clear := wire.AppendVarint(nil, 1, 2)
	clear = wire.AppendVarint(clear, 4, 1)
	clear = wire.AppendVarint(clear, 5, 101)
	if _, _, _, err := service.Handle("/MissionClear", clear); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Snapshot().Gold; got != 1000 {
		t.Fatalf("gold=%d", got)
	}
}
