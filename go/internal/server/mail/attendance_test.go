package mail

import (
	"bytes"
	"testing"
	"time"

	"bd2server/internal/server/events"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type attendanceDesign struct{ selections int }

func (*attendanceDesign) Character(uint64) (gamedata.CharacterDesign, bool) {
	return gamedata.CharacterDesign{}, false
}
func (*attendanceDesign) RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error) {
	return nil, nil, nil, nil
}
func (d *attendanceDesign) Resolve(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return d.ResolveGranted(r)
}
func (d *attendanceDesign) ResolveGranted(r []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	var out []gamedata.BattleReward
	for _, reward := range r {
		if reward.Type == 9 && reward.ID == 100 {
			d.selections++
			out = append(out, gamedata.BattleReward{Type: 4, Count: 10 * reward.Count}, gamedata.BattleReward{Type: 8, ID: 1000, Count: 2 * reward.Count}, gamedata.BattleReward{Type: 9, ID: 200, Count: reward.Count})
		} else {
			out = append(out, reward)
		}
	}
	return out, nil
}

func attendanceMailFixture(t *testing.T, store stateio.Store, seed *Starter, d *attendanceDesign) (*Service, *player.Inventory, *player.Wallet) {
	t.Helper()
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
	for _, persist := range []func() error{items.EnsurePersisted, wallet.EnsurePersisted, collection.EnsurePersisted, equipment.EnsurePersisted} {
		if err := persist(); err != nil {
			t.Fatal(err)
		}
	}
	economy, err := events.NewEconomy(store, items, wallet, collection, equipment, d, d, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(store, seed, items, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachAttendanceRewardEconomy(economy); err != nil {
		t.Fatal(err)
	}
	return service, items, wallet
}

func TestAttendanceMailDefersRewardsAndReplaysAcrossRestart(t *testing.T) {
	store := stateio.NewMemory()
	seed := &Starter{Version: "2.35.10", MailCount: 1, MaxMailID: 100}
	design := &attendanceDesign{}
	s, items, wallet := attendanceMailFixture(t, store, seed, design)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	reward := []gamedata.Reward{{Type: 9, ID: 100, Count: 1}}
	if err := s.IssueAttachmentsOnce("daily:1", "签到奖励", "请领取", reward, now); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueAttachmentsOnce("daily:1", "签到奖励", "请领取", reward, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueAttachmentsOnce("daily:1", "签到奖励", "请领取", []gamedata.Reward{{Type: 4, Count: 999}}, now); err == nil {
		t.Fatal("identity accepted changed attachments")
	}
	if len(s.dynamic) != 1 || wallet.Snapshot().Gold != 0 || len(items.All()) != 0 || design.selections != 0 {
		t.Fatal("issuing mail granted rewards")
	}
	if err := s.IssueAttachmentsOnce("daily:2", "签到奖励", "请领取", reward, now); err != nil {
		t.Fatal(err)
	}
	if s.issued["attendance:daily:2"] != 102 {
		t.Fatal("mail ID did not increase")
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 101)
	request = wire.AppendVarint(request, 2, 102)
	_, response, _, err := s.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 20 || design.selections != 2 || len(items.All()) != 4 || len(s.history) != 2 {
		t.Fatalf("claim missing domains/history: gold=%d selections=%d items=%d", wallet.Snapshot().Gold, design.selections, len(items.All()))
	}
	for _, item := range items.All() {
		if item.Type == 9 && item.ID != 200 {
			t.Fatal("OPEN wrapper stored instead of DIRECT child")
		}
	}
	reopened, items, wallet := attendanceMailFixture(t, store, seed, design)
	reopened.now = s.now
	_, replay, _, err := reopened.Handle("/MailOpen", request)
	if err != nil || !bytes.Equal(response, replay) || design.selections != 2 || wallet.Snapshot().Gold != 20 || len(items.All()) != 4 {
		t.Fatal("restart replay changed or repeated grants", err)
	}
	if err := reopened.IssueAttachmentsOnce("expired", "签到奖励", "请领取", reward, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired := wire.AppendVarint(nil, 1, 2)
	expired = wire.AppendVarint(expired, 2, 103)
	if _, _, _, err := reopened.Handle("/MailOpen", expired); err == nil || design.selections != 2 || len(reopened.history) != 2 {
		t.Fatal("expired mail granted")
	}
}

func TestAttendanceEconomyDoesNotRegrantExistingOpenedMail(t *testing.T) {
	store := stateio.NewMemory()
	seed := &Starter{Version: "2.35.10", MailCount: 1, MaxMailID: 100}
	design := &attendanceDesign{}
	s, _, wallet := attendanceMailFixture(t, store, seed, design)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.EnqueueCompensation("existing:grant", "奖励", "请领取", []gamedata.Reward{{Type: 4, Count: 7}}, now); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 101)
	if _, _, _, err := s.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	reopened, _, wallet := attendanceMailFixture(t, store, seed, design)
	reopened.now = s.now
	if _, _, _, err := reopened.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 7 || design.selections != 0 {
		t.Fatal("existing opened mail routed to new attendance economy")
	}
}
