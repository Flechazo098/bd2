package authconfig

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"mode":"oauth","public_url":"https://example.com","master_key_env":"MASTER","providers":{"discord":{"client_id":"d","client_secret_env":"DS"},"google":{"client_id":"g","client_secret_env":"GS"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "oauth" || len(config.Providers) != 2 || config.Providers["discord"].ClientID != "d" || config.Providers["google"].ClientID != "g" {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestValidateRejectsUnsafePolicies(t *testing.T) {
	for name, config := range map[string]Config{
		"unknown mode":     {Mode: "disabled"},
		"local providers":  {Mode: "local", Providers: map[string]ProviderConfig{"discord": {ClientID: "d", ClientSecretEnv: "DS"}}},
		"empty oauth":      {Mode: "oauth"},
		"unknown provider": {Mode: "oauth", PublicURL: "https://example.com", MasterKeyEnv: "MASTER", Providers: map[string]ProviderConfig{"github": {ClientID: "g", ClientSecretEnv: "GS"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := config.Validate(); err == nil {
				t.Fatal("accepted invalid authentication policy")
			}
		})
	}
}

func TestResolveEnvironmentKeepsSecretsOutOfPublicView(t *testing.T) {
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1)
	}
	t.Setenv("AUTH_MASTER", base64.StdEncoding.EncodeToString(master))
	t.Setenv("DISCORD_SECRET", "private-discord-secret")
	config := Config{
		Mode:         "oauth",
		PublicURL:    "https://example.com",
		MasterKeyEnv: "AUTH_MASTER",
		Providers: map[string]ProviderConfig{
			"discord": {ClientID: "public-client-id", ClientSecretEnv: "DISCORD_SECRET"},
		},
	}
	runtime, err := config.ResolveEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.MasterKey) != 32 || runtime.ProviderSecrets["discord"] != "private-discord-secret" {
		t.Fatal("runtime did not resolve authentication secrets")
	}
	publicJSON, err := json.Marshal(config.Public())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"AUTH_MASTER", "DISCORD_SECRET", "private-discord-secret", "public-client-id"} {
		if strings.Contains(string(publicJSON), forbidden) {
			t.Fatalf("public authentication view leaked %q: %s", forbidden, publicJSON)
		}
	}
}

func TestValidateRejectsUnsafePublicURLsAndTTLs(t *testing.T) {
	base := Config{
		Mode:         "oauth",
		PublicURL:    "https://example.com",
		MasterKeyEnv: "MASTER",
		Providers: map[string]ProviderConfig{
			"discord": {ClientID: "d", ClientSecretEnv: "DS"},
		},
	}
	for name, mutate := range map[string]func(*Config){
		"http public":    func(c *Config) { c.PublicURL = "http://example.com" },
		"path":           func(c *Config) { c.PublicURL = "https://example.com/auth" },
		"query":          func(c *Config) { c.PublicURL = "https://example.com?x=y" },
		"missing secret": func(c *Config) { c.Providers["discord"] = ProviderConfig{ClientID: "d"} },
		"invalid ttl":    func(c *Config) { c.Session.AccessTTL = "0s" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Providers = map[string]ProviderConfig{"discord": base.Providers["discord"]}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("accepted unsafe authentication configuration")
			}
		})
	}
}
