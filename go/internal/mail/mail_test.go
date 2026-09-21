package mail

import (
	"bytes"
	"path/filepath"
	"testing"

	"bd2server/internal/fixture"
	"bd2server/internal/player"
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

func TestMailOpenGrantsItemsAndCurrencyAndPersists(t *testing.T) {
	dir := t.TempDir()
	seed := &Starter{Version: "2.34.13", MailCount: 3, MaxMailID: 12, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{3}, RewardIDs: []uint64{0}, RewardCounts: []uint64{70}},
		{MailID: 12, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{8}, RewardIDs: []uint64{9}, RewardCounts: []uint64{10}},
	}}
	starter := &player.Starter{Version: "2.34.13"}
	inv, err := player.OpenInventory(filepath.Join(dir, "items.json"), starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(filepath.Join(dir, "wallet.json"), player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(filepath.Join(dir, "mail.json"), seed, inv, wallet)
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
	service, err = OpenService(filepath.Join(dir, "mail.json"), seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	_, info, _, _ = service.Handle("/MailInfo", infoReq)
	if _, found, _ := wire.Bytes(info, 1); found {
		t.Fatal("opened mail was not persisted")
	}
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
