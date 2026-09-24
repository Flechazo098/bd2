package bootstrap

import (
	"testing"
	"time"

	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
)

func TestMaintenance(t *testing.T) {
	req := wire.AppendVarint(nil, 1, 2)
	req = wire.AppendVarint(req, 2, 8)
	response, err := Maintenance(versionconfig.Client(), versionconfig.Bundle(), req)
	if err != nil {
		t.Fatal(err)
	}
	market, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("market info missing: %v", err)
	}
	typ, _, _ := wire.Varint(market, 1)
	if typ != 4 {
		t.Fatalf("market type does not match response contract: %d", typ)
	}
	connect, _, _ := wire.Varint(response, 3)
	user, _, _ := wire.Varint(response, 4)
	if connect != 1 || user == 0 {
		t.Fatalf("client would enter update branch: connect=%d user=%d", connect, user)
	}
}

func TestServerInfoNoOfficialEndpoints(t *testing.T) {
	c := Config{BaseURL: "http://127.0.0.1:8080/game/", CDNURL: "http://127.0.0.1:8080/assets/ServerData", Version: versionconfig.Client(), BundleVer: versionconfig.Bundle()}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	response := ServerInfo(c)
	info, _, _ := wire.Bytes(response, 1)
	address, _, _ := wire.Bytes(info, 2)
	if string(address) != c.BaseURL {
		t.Fatalf("game URL: %q", address)
	}
	if _, exists, _ := wire.Bytes(info, 8); exists {
		t.Fatal("unexpected GameData URL")
	}
	if v, _, _ := wire.Varint(ServerNowTime(time.UnixMilli(1234567)), 1); v != 1234567 {
		t.Fatalf("time: %d", v)
	}
}

func TestServerInfoIncludesLocalGameData(t *testing.T) {
	c := Config{
		BaseURL: "http://127.0.0.1:8080/game/",
		CDNURL:  "http://127.0.0.1:8080/assets/ServerData",
		Version: versionconfig.Client(), BundleVer: versionconfig.Bundle(),
		GameDataURL: "http://127.0.0.1:8080/assets/GameData",
		GameDataVer: "20260921140855",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	info, found, err := wire.Bytes(ServerInfo(c), 1)
	if err != nil || !found {
		t.Fatalf("server info missing: %v", err)
	}
	url, _, _ := wire.Bytes(info, 8)
	version, _, _ := wire.Bytes(info, 9)
	if string(url) != c.GameDataURL || string(version) != c.GameDataVer {
		t.Fatalf("GameData mismatch: url=%q version=%q", url, version)
	}
}
