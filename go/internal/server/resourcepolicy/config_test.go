package resourcepolicy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfficialDefaultsAreCapturedOrigins(t *testing.T) {
	cfg := Config{Mode: ModeOfficial}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	public := cfg.Public("bundle", "game-data")
	if public.ServerDataURL != OfficialServerDataURL || public.GameDataURL != OfficialGameDataURL {
		t.Fatalf("official policy = %+v", public)
	}
}

func TestServerModeRequiresSafeCompleteOrigins(t *testing.T) {
	cfg := Config{Mode: ModeServer, ServerDataURL: "https://cdn.example.com/ServerData", GameDataURL: "https://cdn.example.com/GameData"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("server mode rejected: %v", err)
	}
	bad := []Config{
		{Mode: ModeServer, ServerDataURL: "https://cdn.example.com/ServerData"},
		{Mode: ModeServer, ServerDataURL: "http://cdn.example.com/ServerData", GameDataURL: "https://cdn.example.com/GameData"},
		{Mode: ModeServer, ServerDataURL: "https://user:password@cdn.example.com/ServerData", GameDataURL: "https://cdn.example.com/GameData"},
		{Mode: ModeServer, ServerDataURL: "https://cdn.example.com/ServerData/", GameDataURL: "https://cdn.example.com/GameData"},
		{Mode: "self_hosted", ServerDataURL: "https://cdn.example.com/ServerData", GameDataURL: "https://cdn.example.com/GameData"},
		{Mode: "reverse_proxy", ServerDataURL: "https://cdn.example.com/ServerData", GameDataURL: "https://cdn.example.com/GameData"},
		{Mode: "local", ServerDataURL: "C:\\bd2-resources\\ServerData", GameDataURL: "C:\\bd2-resources\\GameData"},
	}
	for i, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("unsafe policy %d accepted: %+v", i, cfg)
		}
	}
}

func TestLoopbackHTTPIsDevelopmentOnly(t *testing.T) {
	cfg := Config{Mode: ModeServer, ServerDataURL: "http://127.0.0.1:9000/ServerData", GameDataURL: "http://localhost:9000/GameData"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsUnknownAndTrailingJSON(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"mode":"official","cdn_url":"https://example.com"}`,
		"trailing": `{"mode":"official"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}
