package missions

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"testing"
	"time"
)

func TestEventsFollowConditionsAndLoginPersistsAcrossRestart(t *testing.T) {
	daily := gamedata.MissionKey{GroupType: 0, GroupID: 42, ID: 9876}
	weekly := gamedata.MissionKey{GroupType: 1, GroupID: 66, ID: 7654}
	gacha := gamedata.MissionKey{GroupType: 0, GroupID: 42, ID: 8765}
	locked := gamedata.MissionKey{GroupType: 0, GroupID: 42, ID: 8654}
	design := &gamedata.MissionDesign{Missions: map[gamedata.MissionKey][]gamedata.Reward{daily: nil, weekly: nil, gacha: nil, locked: nil}, Conditions: map[gamedata.MissionKey]gamedata.MissionCondition{
		daily: {Type: ConditionConnect, TargetValue: 1}, weekly: {Type: ConditionConnect, TargetValue: 3}, gacha: {Type: ConditionGachaBuy, SubType: 5, SubTypeComparison: 1, TargetValue: 7}, locked: {Type: ConditionGachaBuy, UnlockPack: 21, UnlockQuest: 4, TargetValue: 1},
	}}
	store := stateio.NewMemory()
	inv, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(store, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	if err := s.RecordLogin(nil); err != nil {
		t.Fatal(err)
	}
	s, err = Open(store, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	if err := s.RecordLogin(nil); err != nil {
		t.Fatal(err)
	}
	if s.state.Progress[missionName(weekly)] != 1 {
		t.Fatal("reconnect incremented weekly login")
	}
	if err := s.RecordEvent(ConditionGachaBuy, 4, 20, nil); err != nil {
		t.Fatal(err)
	}
	if s.state.Progress[missionName(gacha)] != 0 || s.state.Progress[missionName(locked)] != 0 {
		t.Fatal("subtype/locked mission matched")
	}
	if err := s.RecordEvent(ConditionGachaBuy, 6, 3, nil); err != nil {
		t.Fatal(err)
	}
	if s.state.Progress[missionName(gacha)] != 3 {
		t.Fatal("event did not use count")
	}
	if err := s.RecordEvent(ConditionGachaBuy, 6, 100, func(pack, quest uint64) bool { return pack == 21 && quest == 4 }); err != nil {
		t.Fatal(err)
	}
	if s.state.Progress[missionName(gacha)] != 7 || s.state.Progress[missionName(locked)] != 1 {
		t.Fatal("target cap/unlock did not apply")
	}
	wallet, err := player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := mail.OpenService(store, &mail.Starter{Version: "2.35.10", MaxMailID: 100, MailCount: 1}, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachMail(mailbox); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 1)
	if err := s.RecordLogin(nil); err != nil {
		t.Fatal(err)
	}
	// Monday resets weekly; other days increment its connection count.
	want := uint64(2)
	if now.Weekday() == time.Monday {
		want = 1
	}
	if s.state.Progress[missionName(weekly)] != want {
		t.Fatalf("weekly progress=%d want=%d", s.state.Progress[missionName(weekly)], want)
	}
}
