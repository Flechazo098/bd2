//go:build !release

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClientDevelopmentGameDirectory(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "separate", args: []string{"--game-dir", filepath.Join("some", "game")}, want: filepath.Join("some", "game")},
		{name: "equals", args: []string{"--game-dir=" + filepath.Join("other", "game")}, want: filepath.Join("other", "game")},
		{name: "absent", args: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := clientDevelopmentGameDirectory(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("game directory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClientDevelopmentGameDirectoryRequiresValue(t *testing.T) {
	for _, args := range [][]string{{"--game-dir"}, {"--game-dir="}} {
		if _, err := clientDevelopmentGameDirectory(args); err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
	}
}

func TestLoadClientDevelopmentConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	gameDir := filepath.Join(dir, "BrownDust II")
	quoted, err := json.Marshal(gameDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"game_directory":`+string(quoted)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadClientDevelopmentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(gameDir)
	if config.SchemaVersion != 1 || config.GameDirectory != want {
		t.Fatalf("config=%+v want directory %q", config, want)
	}
	if _, err := loadClientDevelopmentConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing development config accepted")
	}
	for name, body := range map[string]string{
		"unknown":  `{"schema_version":1,"game_directory":"x","extra":true}`,
		"version":  `{"schema_version":2,"game_directory":"x"}`,
		"empty":    `{"schema_version":1,"game_directory":""}`,
		"trailing": `{"schema_version":1,"game_directory":"x"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(dir, name+".json")
			if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadClientDevelopmentConfig(bad); err == nil {
				t.Fatal("invalid development config accepted")
			}
		})
	}
}

func TestDevelopmentRunOptionsUsesRepositoryFiles(t *testing.T) {
	gameDir := t.TempDir()
	args, options, err := developmentRunOptions([]string{"--dev", "run", "--game-dir", gameDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "--game-dir" || args[1] != gameDir {
		t.Fatalf("client args = %v", args)
	}
	for name, path := range map[string]string{
		"versions":       options.versionConfigPath,
		"log executable": options.logExecutablePath,
		"local identity": options.localIdentityPlugin,
		"login UI":       options.loginUIPlugin,
		"cash shop":      options.cashShopPlugin,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s path is not absolute: %q", name, path)
		}
	}
}

func TestDevelopmentRunOptionsRequiresRun(t *testing.T) {
	if _, _, err := developmentRunOptions([]string{"--dev"}); err == nil {
		t.Fatal("development command without run unexpectedly succeeded")
	}
}

func TestDevelopmentRunRelaunchesWithWailsProductionHost(t *testing.T) {
	if wailsDevelopmentBuild {
		t.Skip("test exercises the untagged bootstrap process")
	}
	previous := runDevelopmentChild
	t.Cleanup(func() { runDevelopmentChild = previous })
	var gotRoot string
	var gotArgs []string
	runDevelopmentChild = func(root string, args []string) error {
		gotRoot = root
		gotArgs = append([]string(nil), args...)
		return nil
	}
	relaunched, err := relaunchDevelopmentIfNeeded([]string{"--dev", "run", "--game-dir", "example"})
	if err != nil || !relaunched {
		t.Fatalf("relaunched=%v err=%v", relaunched, err)
	}
	if !filepath.IsAbs(gotRoot) {
		t.Fatalf("root is not absolute: %q", gotRoot)
	}
	want := []string{"run", "-tags", "production", "./cmd/bd2client", "--dev", "run", "--game-dir", "example"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("child args=%v want=%v", gotArgs, want)
	}
}

func TestDevelopmentRunDoesNotRelaunchOtherCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"--game-dir", "example"}, {"--dev"}, {"--dev", "other"}} {
		relaunched, err := relaunchDevelopmentIfNeeded(args)
		if err != nil || relaunched {
			t.Fatalf("args=%v relaunched=%v err=%v", args, relaunched, err)
		}
	}
}
