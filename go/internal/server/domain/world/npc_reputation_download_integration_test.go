//go:build integration

package world

import (
	"testing"
	"time"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/world/progress"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

func TestReputationRefreshRestoresEarnedDiscountAndExpiresWithoutRenewingIt(t *testing.T) {
	v, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	source := gamedata.NewSource(v.Resolve("data/resources/GameData"), v.GameDataVersion)
	catalog, err := gamedata.LoadStoryCatalog(source.Root(), source.Version())
	if err != nil {
		t.Fatal(err)
	}
	design, err := source.NPCReputation(1)
	if err != nil {
		t.Fatal(err)
	}
	var rule gamedata.NPCReputationRule
	for _, candidate := range design.Groups {
		if candidate.DownHours > 0 {
			rule = candidate
			break
		}
	}
	if rule.ID == 0 {
		t.Fatal("chapter has no expiring reputation")
	}
	now := time.Unix(1800000000, 0)
	ctx := command.Context{Identity: command.Identity{AccountID: "reputation-player", SessionID: "session"}, State: stateio.NewMemory()}
	open := func() *Service {
		s := &Service{seed: Seed{PackID: 1}, activePack: 1, state: progress.NewStore(), storyCatalog: catalog}
		s.squadLevel = func() (uint64, error) { return 1000, nil }
		var err error
		s.inventory, err = assets.OpenInventory(ctx, stateio.RootStore{}, []assets.Item{{InvenIndex: 98, Type: 19, ID: catalog.Packs[2].Open.TicketID, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ConfigureNPCRuntime(source, stateio.RootStore{}); err != nil {
			t.Fatal(err)
		}
		s.npcReputation.now = func() time.Time { return now }
		return s
	}
	read := func(s *Service, wantState, wantElapsed uint64) {
		t.Helper()
		_, body, handled, err := s.Handle(ctx, "/ReputationInfo", wire.AppendVarint(nil, 1, 1))
		if err != nil || !handled {
			t.Fatalf("refresh: %v", err)
		}
		rows, err := nestedMessages(body, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			id, _, _ := wire.Varint(row, 1)
			if id != rule.ID {
				continue
			}
			state, _, _ := wire.Varint(row, 2)
			elapsed, _, _ := wire.Varint(row, 3)
			if state != wantState || elapsed != wantElapsed {
				t.Fatalf("discount state=%d elapsed=%d", state, elapsed)
			}
			return
		}
		t.Fatal("refresh omitted the reputation group used by the field/shop")
	}
	s := open()
	read(s, 1, 0)
	if _, err := s.CompleteNPCReputation(ctx, "completed-commission", 1, rule.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	s = open()
	read(s, 2, 10)
	s.activePack = 2
	read(s, 1, 0)
	s.activePack = 1
	read(s, 2, 10)
	if _, err := s.CompleteNPCReputation(ctx, "completed-commission", 1, rule.ID); err != nil {
		t.Fatal(err)
	}
	read(open(), 2, 10)
	now = now.Add(time.Duration(rule.DownHours) * time.Hour)
	read(open(), 1, 0)
	ctx.State = stateio.NewMemory()
	read(open(), 1, 0)
}
