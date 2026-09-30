//go:build !release

package main

import (
	"path/filepath"
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
		{name: "absent", args: []string{"--no-browser"}},
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

func TestDevelopmentRunOptionsUsesRepositoryFiles(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	args, options, err := developmentRunOptions([]string{"--dev", "run", "--no-browser"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "--no-browser" {
		t.Fatalf("client args = %v", args)
	}
	for name, path := range map[string]string{
		"versions":       options.versionConfigPath,
		"log executable": options.logExecutablePath,
		"local identity": options.localIdentityPlugin,
		"login UI":       options.loginUIPlugin,
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
