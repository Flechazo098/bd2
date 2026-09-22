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
	seed := &LoginSeed{Version: Version23413, PacketCode: 11, UserInfo: user}
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
	seed := &LoginSeed{Version: Version23413, PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 1)}
	if _, err := seed.Login([]byte("not protobuf"), []byte("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatal("Login accepted invalid protobuf request")
	}
}

func TestLoadRejectsSeedWithUserKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	seed := &LoginSeed{Version: Version23413, PacketCode: 11, UserInfo: wire.AppendString(nil, 3, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}
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
	seed := &LoginSeed{Version: Version23413, PacketCode: 3, UserInfo: wire.AppendVarint(nil, 1, 1)}
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
