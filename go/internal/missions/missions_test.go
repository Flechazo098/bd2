package missions

import (
	"testing"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/mail"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
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

func TestDailyRolloverMailsCompletedMissionAndSectionRewards(t *testing.T) {
	mission := gamedata.MissionKey{GroupType: 0, GroupID: 1, ID: 101}
	section := gamedata.SectionRewardKey{GroupType: 0, ID: 10}
	design := &gamedata.MissionDesign{
		Missions:   map[gamedata.MissionKey][]gamedata.Reward{mission: {{Type: 4, Count: 100}}},
		Conditions: map[gamedata.MissionKey]gamedata.MissionCondition{mission: {TargetValue: 1}},
		Sections: map[gamedata.SectionRewardKey]gamedata.SectionRewardDesign{section: {
			SectionValue: 1, Rewards: []gamedata.Reward{{Type: 3, Count: 20}},
		}},
		Achievements: map[gamedata.AchievementKey]gamedata.AchievementDesign{},
	}
	storage := stateio.NewMemory()
	starter := &player.Starter{Version: "2.34.13"}
	inv, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := mail.OpenService(storage, &mail.Starter{Version: "2.34.13", MailCount: 1, MaxMailID: 100}, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(storage, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	if err := service.AttachMail(mailbox); err != nil {
		t.Fatal(err)
	}
	service.state.DailyPeriod = "2026-09-23"
	service.state.WeeklyPeriod = "2026-09-21"
	service.now = func() time.Time { return time.Date(2026, 9, 24, 0, 1, 0, 0, time.UTC) }
	service.state.Progress[missionName(mission)] = 1
	if _, _, _, err := service.Handle("/MissionInfo", wire.AppendVarint(nil, 1, 1)); err != nil {
		t.Fatal(err)
	}
	_, mailInfo, _, err := mailbox.Handle("/MailInfo", wire.AppendVarint(nil, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	mailCount := 0
	if err := wire.Walk(mailInfo, func(field wire.Field) error {
		if field.Number == 1 {
			mailCount++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if service.state.DailyPeriod != "2026-09-24" || len(service.state.Progress) != 0 || mailCount != 2 {
		t.Fatalf("mission=%+v mailCount=%d", service.state, mailCount)
	}
	for id := uint64(101); id <= 102; id++ {
		request := wire.AppendVarint(nil, 1, id)
		request = wire.AppendVarint(request, 2, id)
		if _, _, _, err := mailbox.Handle("/MailOpen", request); err != nil {
			t.Fatal(err)
		}
	}
	if got := wallet.Snapshot(); got.Gold != 100 || got.FreeJewelry != 20 {
		t.Fatalf("wallet=%+v", got)
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
	inventory, err := player.OpenInventory(stateio.NewMemory(), starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(stateio.NewMemory(), design, inventory)
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
	inventory, err := player.OpenInventory(stateio.NewMemory(), starter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(stateio.NewMemory(), design, inventory)
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
	starter := &player.Starter{Version: "2.34.13"}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(storage, design, inv)
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
