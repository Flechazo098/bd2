package session

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/cryptox"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/missions"
	"bd2server/internal/server/player"
	"bd2server/internal/server/protocol"
	"bd2server/internal/server/wire"
)

func TestLevelRewardBatchRollsBackWalletInventoryAndClaimTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	inv, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(repo, player.Currency{Gold: 12})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	design := &gamedata.MissionDesign{}
	m, err := missions.Open(repo, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AttachWallet(wallet); err != nil {
		t.Fatal(err)
	}
	levels := &gamedata.AchievementLevelDesign{Levels: []gamedata.AchievementLevel{{ID: 1, NeedEXP: 10, Rewards: []gamedata.Reward{{Type: 4, Count: 100}, {Type: 8, ID: 987, Count: 3}}}}}
	if err := m.AttachUserLevelRewards(levels); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(fakeLogin{}, m, &mutatingDomain{store: repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.AttachStateStore(repo); err != nil {
		t.Fatal(err)
	}
	session := login(t, server)
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 1)
	batch := []protocol.BatchRequest{
		{Path: "/UserLevelReward", RequestData: base64.StdEncoding.EncodeToString(request)},
		{Path: "/InjectedFailure", RequestData: base64.StdEncoding.EncodeToString(wire.AppendVarint(nil, 1, 3))},
	}
	plain, _ := json.Marshal(batch)
	body, _ := cryptox.EncryptBase64(plain, server.KeyForTest())
	if _, err := server.DispatchRaw("/BatchRequest", []byte(body), "s="+session.Cookie); err == nil {
		t.Fatal("failing batch accepted")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	inv, err = player.OpenInventory(reopened, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err = player.OpenWallet(reopened, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	m, err = missions.Open(reopened, design, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AttachUserLevelRewards(levels); err != nil {
		t.Fatal(err)
	}
	if got, err := m.LevelRewardCount(); err != nil || got != 0 {
		t.Fatalf("claim survived rollback: %d %v", got, err)
	}
	if len(inv.All()) != 0 || wallet.Snapshot().Gold != 12 {
		t.Fatalf("rewards survived rollback: %+v %+v", inv.All(), wallet.Snapshot())
	}
}
