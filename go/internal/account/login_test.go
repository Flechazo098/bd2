package account

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/cryptox"
	"bd2server/internal/wire"
)

func TestEncodeUsesFreshLocalKey(t *testing.T) {
	user := wire.AppendVarint(nil, 1, 42)
	user = wire.AppendString(user, 2, "Guest_42")
	user = wire.AppendVarint(user, 5, 100)
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 11, UserInfo: user}
	const local = "0123456789abcdef0123456789abcdef"
	body, err := seed.Encode(local, time.UnixMilli(1234))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		PacketCode    int    `json:"packetCode"`
		Length        int    `json:"length"`
		Data          string `json:"data"`
		ServerNowTime int64  `json:"serverNowTime"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.PacketCode != 11 || envelope.ServerNowTime != 1234 {
		t.Fatalf("envelope=%+v", envelope)
	}
	proto, err := cryptox.DecryptBase64Payload(envelope.Data, cryptox.Key())
	if err != nil {
		t.Fatal(err)
	}
	responseUser, found, err := wire.Bytes(proto, 1)
	if err != nil || !found {
		t.Fatalf("response UserInfo: found=%v err=%v", found, err)
	}
	gotKey, found, err := wire.Bytes(responseUser, 3)
	if err != nil || !found || string(gotKey) != local {
		t.Fatalf("local key=%q found=%v err=%v", gotKey, found, err)
	}
}

func TestLoginValidatesEncryptedRequest(t *testing.T) {
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 1)}
	if _, err := seed.Login([]byte("not protobuf"), []byte("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatal("Login accepted invalid protobuf request")
	}
}

func TestLoadRejectsSeedWithUserKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 11, UserInfo: wire.AppendString(nil, 3, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}
	if err := seed.Write(path); err == nil {
		t.Fatal("Write accepted user_key")
	}
	if err := os.WriteFile(path, []byte(`{"version":"2.34.13","packet_code":11,"user_info_base64":"GgF4"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted user_key")
	}
}

func TestCheckedInSeedBuildsLoginWithoutCapture(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "seed", "v2_34_13", "login_user.json"))
	if err != nil {
		t.Fatal(err)
	}
	proto, err := seed.Login(wire.AppendVarint(nil, 1, 4), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	user, found, err := wire.Bytes(proto, 1)
	if err != nil || !found {
		t.Fatalf("missing UserInfo: found=%v err=%v", found, err)
	}
	key, found, err := wire.Bytes(user, 3)
	if err != nil || !found || string(key) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected generated key: %q found=%v err=%v", key, found, err)
	}
}

type loginCurrencyFixture struct{}

func (loginCurrencyFixture) Currencies() (uint64, uint64, uint64, uint64) {
	return 1, 2, 3, 4
}
func (loginCurrencyFixture) EquipmentMileageBalances() (uint64, uint64) { return 17, 845 }

func TestLoginRestoresEquipmentMileageFromCurrencyProvider(t *testing.T) {
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 1)}
	if err := seed.AttachCurrencies(loginCurrencyFixture{}); err != nil {
		t.Fatal(err)
	}
	response, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	user, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing login user: %v", err)
	}
	if mileage, found, err := wire.Varint(user, 67); err != nil || !found || mileage != 17 {
		t.Fatalf("equipment mileage=%d found=%v err=%v", mileage, found, err)
	}
	if gauge, found, err := wire.Varint(user, 68); err != nil || !found || gauge != 845 {
		t.Fatalf("equipment mileage gauge=%d found=%v err=%v", gauge, found, err)
	}
}

type loginPurchaseCountFixture struct {
	infos [][]byte
}

func (f *loginPurchaseCountFixture) PurchaseCountDBInfos() [][]byte { return f.infos }

func TestLoginReplacesSeedPurchaseCountsFromProvider(t *testing.T) {
	stale := wire.AppendVarint(nil, 1, 999)
	userTemplate := wire.AppendVarint(nil, 1, 1)
	userTemplate = wire.AppendBytes(userTemplate, 26, stale)
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: userTemplate}

	current := wire.AppendVarint(nil, 1, 1100001)
	current = wire.AppendVarint(current, 2, 9100033)
	current = wire.AppendVarint(current, 4, 1)
	provider := &loginPurchaseCountFixture{infos: [][]byte{current}}
	if err := seed.AttachPurchaseCounts(provider); err != nil {
		t.Fatal(err)
	}

	login := func() []byte {
		response, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, found, err := wire.Bytes(response, 1)
		if err != nil || !found {
			t.Fatalf("missing login user: found=%v err=%v", found, err)
		}
		return user
	}

	counts := byteFields(login(), 26)
	if len(counts) != 1 || string(counts[0]) != string(current) {
		t.Fatalf("purchase counts=%x want=%x", counts, current)
	}

	// The provider is consulted on every LoginUser response. An empty current
	// state must also remove any stale count captured in the seed template.
	provider.infos = nil
	if counts = byteFields(login(), 26); len(counts) != 0 {
		t.Fatalf("empty current state retained purchase counts: %x", counts)
	}
}

func TestAttachPurchaseCountsRejectsNil(t *testing.T) {
	seed := &LoginSeed{}
	if err := seed.AttachPurchaseCounts(nil); err == nil {
		t.Fatal("AttachPurchaseCounts accepted nil provider")
	}
}

type loginPresetSlotFixture struct{ count uint64 }

func (f *loginPresetSlotFixture) PresetSlotCount() uint64 { return f.count }

func TestLoginReplacesSeedPresetSlotFromProvider(t *testing.T) {
	userTemplate := wire.AppendVarint(nil, 1, 1)
	userTemplate = wire.AppendVarint(userTemplate, 28, 6)
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: userTemplate}
	provider := &loginPresetSlotFixture{count: 9}
	if err := seed.AttachPresetSlots(provider); err != nil {
		t.Fatal(err)
	}

	login := func() []byte {
		response, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		user, found, err := wire.Bytes(response, 1)
		if err != nil || !found {
			t.Fatalf("missing login user: found=%v err=%v", found, err)
		}
		return user
	}

	if count, found, err := wire.Varint(login(), 28); err != nil || !found || count != 9 {
		t.Fatalf("preset slots=%d found=%v err=%v", count, found, err)
	}
	provider.count = 12
	if count, found, err := wire.Varint(login(), 28); err != nil || !found || count != 12 {
		t.Fatalf("updated preset slots=%d found=%v err=%v", count, found, err)
	}
}

func TestAttachPresetSlotsRejectsNil(t *testing.T) {
	seed := &LoginSeed{}
	if err := seed.AttachPresetSlots(nil); err == nil {
		t.Fatal("AttachPresetSlots accepted nil provider")
	}
}

type loginInventorySlotFixture struct {
	items, storage, equipment, equipmentStorage uint64
	err                                         error
}

func (f *loginInventorySlotFixture) UserInventorySlots() (uint64, uint64, uint64, uint64, error) {
	return f.items, f.storage, f.equipment, f.equipmentStorage, f.err
}

func TestLoginReplacesAllInventorySlotFieldsFromProvider(t *testing.T) {
	user := wire.AppendVarint(nil, 1, 1)
	for field, value := range map[int]uint64{5: 100, 6: 100, 10: 500, 15: 100} {
		user = wire.AppendVarint(user, field, value)
	}
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: user}
	provider := &loginInventorySlotFixture{items: 500, storage: 100, equipment: 2000, equipmentStorage: 100}
	if err := seed.AttachInventorySlots(provider); err != nil {
		t.Fatal(err)
	}
	response, err := seed.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing UserInfo: %v", err)
	}
	for field, want := range map[int]uint64{5: 500, 6: 100, 10: 2000, 15: 100} {
		if got, found, err := wire.Varint(result, field); err != nil || !found || got != want {
			t.Fatalf("field %d=%d found=%t err=%v want=%d", field, got, found, err, want)
		}
	}
}

func TestSeedInventorySlotsReadsUserInfoFields(t *testing.T) {
	user := wire.AppendVarint(nil, 1, 1)
	for field, value := range map[int]uint64{5: 100, 6: 101, 10: 500, 15: 102} {
		user = wire.AppendVarint(user, field, value)
	}
	seed := &LoginSeed{Version: ProtocolVersion(), PacketCode: 3, UserInfo: user}
	items, storage, equipment, equipmentStorage, err := seed.SeedInventorySlots()
	if err != nil || items != 100 || storage != 101 || equipment != 500 || equipmentStorage != 102 {
		t.Fatalf("slots=%d/%d/%d/%d err=%v", items, storage, equipment, equipmentStorage, err)
	}
}

func byteFields(data []byte, number int) [][]byte {
	var result [][]byte
	_ = wire.Walk(data, func(field wire.Field) error {
		if field.Number == number && field.Type == 2 {
			result = append(result, append([]byte(nil), field.Value...))
		}
		return nil
	})
	return result
}
