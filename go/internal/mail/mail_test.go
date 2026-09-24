package mail

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/fixture"
	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/wire"
)

func TestStarterAnswersMailInfoWithoutCapture(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "mail.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, proto, ok, err := seed.Handle("/MailInfo", wire.AppendVarint(nil, 1, 249))
	if err != nil || !ok || code != packetCode || len(proto) == 0 {
		t.Fatalf("MailInfo: code=%d bytes=%d ok=%t err=%v", code, len(proto), ok, err)
	}
	if _, _, ok, err := seed.Handle("/MailOpen", wire.AppendVarint(nil, 1, 1)); ok || err != nil {
		t.Fatal("seed claimed an endpoint it does not own")
	}
	if _, _, ok, err := seed.Handle("/MailInfo", nil); !ok || err == nil {
		t.Fatal("missing sequence was accepted")
	}
}

func TestDynamicCompensationMailPersistsAndIsIdempotent(t *testing.T) {
	seed := &Starter{Version: "2.34.13", MailCount: 1, MaxMailID: 100}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	rewards := []gamedata.Reward{{Type: 4, Count: 123}}
	if err := service.EnqueueCompensation("daily:2026-09-23:1", "日常任务到期补发", "任务已完成但未领取。", rewards, now); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueCompensation("daily:2026-09-23:1", "日常任务到期补发", "任务已完成但未领取。", rewards, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.dynamic) != 1 || reopened.issued["daily:2026-09-23:1"] != 101 {
		t.Fatalf("dynamic=%+v issued=%+v", reopened.dynamic, reopened.issued)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 101)
	if _, _, _, err := reopened.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 123 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
}

func TestMailOpenGrantsItemsAndCurrencyAndPersists(t *testing.T) {
	seed := &Starter{Version: "2.34.13", MailCount: 3, MaxMailID: 12, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{3, 4}, RewardIDs: []uint64{0, 0}, RewardCounts: []uint64{70, 123456789}},
		{MailID: 12, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{8}, RewardIDs: []uint64{9}, RewardCounts: []uint64{10}},
	}}
	starter := &player.Starter{Version: "2.34.13"}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Gold: 321})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{11, 12}))
	code, response, ok, err := service.Handle("/MailOpen", request)
	if err != nil || !ok || code != 132 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if wallet.Snapshot().FreeJewelry != 70 {
		t.Fatalf("wallet=%+v", wallet.Snapshot())
	}
	if wallet.Snapshot().Gold != 123457110 {
		t.Fatalf("gold did not stack into wallet: %+v", wallet.Snapshot())
	}
	if len(inv.All()) != 1 || inv.All()[0].ID != 9 || inv.All()[0].Count != 10 {
		t.Fatalf("items=%+v", inv.All())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found || len(bundle) == 0 {
		t.Fatal("reward bundle missing")
	}
	infoReq := wire.AppendVarint(nil, 1, 2)
	_, info, _, err := service.Handle("/MailInfo", infoReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(info, 1); found {
		t.Fatal("opened mail remained visible")
	}
	count, found, _ := wire.Varint(info, 2)
	if !found || count != 1 {
		t.Fatalf("mail count=%d found=%v", count, found)
	}
	service, err = OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	_, info, _, _ = service.Handle("/MailInfo", infoReq)
	if _, found, _ := wire.Bytes(info, 1); found {
		t.Fatal("opened mail was not persisted")
	}
}

func TestMailOpenGrantsNonResourceItemDBInfoType(t *testing.T) {
	seed := &Starter{Version: "2.34.13", MailCount: 2, MaxMailID: 13, Mails: []MailDBInfo{{
		MailID: 13, MailType: 2, ExpiresAt: 100, SentAt: 10,
		RewardTypes: []uint64{14}, RewardIDs: []uint64{1}, RewardCounts: []uint64{2},
	}}}
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
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{13}))
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	items := inv.All()
	if len(items) != 1 || items[0].ID != 1 || items[0].Type != 14 || items[0].Count != 2 {
		t.Fatalf("items=%+v", items)
	}
}

func TestMailOpenGrantsCatalystCurrencyAndPersists(t *testing.T) {
	seed := &Starter{Version: "2.34.13", MailCount: 2, MaxMailID: 14, Mails: []MailDBInfo{{
		MailID: 14, MailType: 2, ExpiresAt: 100, SentAt: 10,
		RewardTypes: []uint64{12}, RewardIDs: []uint64{0}, RewardCounts: []uint64{250},
	}}}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Catalyst: 10})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{14}))
	_, response, _, err := service.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Catalyst != 260 || len(inv.All()) != 0 {
		t.Fatalf("wallet=%+v items=%+v", wallet.Snapshot(), inv.All())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found {
		t.Fatal("reward bundle missing")
	}
	item, found, _ := wire.Bytes(bundle, 1)
	if !found {
		t.Fatal("currency ItemDBInfo missing")
	}
	if typ, _, _ := wire.Varint(item, 3); typ != 12 {
		t.Fatalf("currency type=%d", typ)
	}
	if count, _, _ := wire.Varint(item, 4); count != 250 {
		t.Fatalf("currency count=%d", count)
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Catalyst != 260 {
		t.Fatalf("replay catalyst=%d", wallet.Snapshot().Catalyst)
	}
	reopened, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Catalyst != 260 {
		t.Fatalf("persisted catalyst=%d", reopened.Snapshot().Catalyst)
	}
}

func TestWatchedSeedReloadsOnlyValidAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.json")
	first := &Starter{Version: "2.34.13", MailCount: 2, MaxMailID: 11, Mails: []MailDBInfo{{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := first.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	starter, err := Load(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, starter, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachSeedPath(seedPath); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, response, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	} else if id, _, _ := wire.Varint(mustFirstMail(t, response), 1); id != 11 {
		t.Fatalf("initial mail ID=%d", id)
	}
	second := &Starter{Version: "2.34.13", MailCount: 2, MaxMailID: 12, Mails: []MailDBInfo{{MailID: 12, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := second.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	if _, response, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	} else if id, _, _ := wire.Varint(mustFirstMail(t, response), 1); id != 12 {
		t.Fatalf("reloaded mail ID=%d", id)
	}
	if err := os.WriteFile(seedPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", request); err == nil {
		t.Fatal("accepted malformed watched seed")
	}
	// The malformed file did not replace the last known-good mailbox.
	if service.Starter.Mails[0].MailID != 12 {
		t.Fatalf("last known-good seed lost: %+v", service.Starter.Mails)
	}
}

func TestExpandedWatchedSeedAdvancesDynamicMailAllocator(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.json")
	first := &Starter{Version: "2.34.13", MailCount: 2, MaxMailID: 11, Mails: []MailDBInfo{{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := first.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.34.13"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, first, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := service.AttachSeedPath(seedPath); err != nil {
		t.Fatal(err)
	}
	second := &Starter{Version: "2.34.13", MailCount: 3, MaxMailID: 101, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10},
		{MailID: 101, MailType: 2, ExpiresAt: 100, SentAt: 10},
	}}
	if err := second.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if service.state.NextDynamicMailID != 102 {
		t.Fatalf("next dynamic mail ID=%d", service.state.NextDynamicMailID)
	}
	reopened, err := OpenService(storage, second, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.NextDynamicMailID != 102 {
		t.Fatalf("reopened next dynamic mail ID=%d", reopened.state.NextDynamicMailID)
	}
}

func mustFirstMail(t *testing.T, response []byte) []byte {
	t.Helper()
	entry, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing first mail: found=%v err=%v", found, err)
	}
	return entry
}

func TestMailOpenAcceptsOfficialPackedRequest(t *testing.T) {
	request := []byte{0x08, 0xef, 0x01, 0x12, 0x0f, 0x85, 0xc1, 0xe1, 0xce, 0x30, 0x88, 0xc1, 0xe1, 0xce, 0x30, 0x8a, 0xc1, 0xe1, 0xce, 0x30}
	ids, err := requestMailIDs(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{13050077317, 13050077320, 13050077322}
	if len(ids) != len(want) {
		t.Fatalf("ids=%v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids=%v", ids)
		}
	}
}

func TestStarterMatchesInitialAccountSample(t *testing.T) {
	set, err := fixture.Load(filepath.Join("..", "..", "..", "data", "capture", "2.34.13", "20260920-003254"))
	if err != nil {
		t.Skipf("optional capture unavailable: %v", err)
	}
	recorded, err := set.RecordedResponseForClientSequence("/MailInfo", 249)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := set.RecordedPayloadAt("/MailInfo", recorded.RequestSequence)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "mail.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, actual, ok, err := seed.Handle("/MailInfo", wire.AppendVarint(nil, 1, 249))
	if err != nil || !ok || code != payload.PacketCode || !bytes.Equal(actual, payload.Proto) {
		t.Fatalf("MailInfo differs: code=%d want=%d ok=%v err=%v\\ngot =%x\\nwant=%x", code, payload.PacketCode, ok, err, actual, payload.Proto)
	}
}

func TestValidateRejectsRewardLengthMismatch(t *testing.T) {
	seed := &Starter{Version: "2.34.13", Mails: []MailDBInfo{{MailID: 1, MailType: 2, ExpiresAt: 1, SentAt: 1, RewardTypes: []uint64{8}, RewardIDs: []uint64{1}}}, MailCount: 2, MaxMailID: 1}
	if seed.Validate() == nil {
		t.Fatal("accepted bad rewards")
	}
}
