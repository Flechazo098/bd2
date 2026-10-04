package gameconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, text          string
		wantTrue, wantError bool
	}{
		{"enabled", `{"schema_version":1,"gacha":{"include_collaboration_ur_weapons":true}}`, true, false},
		{"disabled", `{"schema_version":1,"gacha":{"include_collaboration_ur_weapons":false}}`, false, false},
		{"omitted bool", `{"schema_version":1,"gacha":{}}`, false, false},
		{"omitted gacha", `{"schema_version":1}`, false, false},
		{"string bool", `{"schema_version":1,"gacha":{"include_collaboration_ur_weapons":"true"}}`, false, true},
		{"unknown root", `{"schema_version":1,"unknown":true}`, false, true},
		{"unknown rule", `{"schema_version":1,"gacha":{"typo":true}}`, false, true},
		{"trailing object", `{"schema_version":1} {}`, false, true},
		{"trailing garbage", `{"schema_version":1} invalid`, false, true},
		{"null root", `null`, false, true},
		{"null section", `{"schema_version":1,"gacha":null}`, false, true},
		{"null bool", `{"schema_version":1,"gacha":{"include_collaboration_ur_weapons":null}}`, false, true},
		{"null schema", `{"schema_version":null}`, false, true},
		{"wrong version", `{"schema_version":2}`, false, true},
		{"missing version", `{}`, false, true},
		{"malformed", `{`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("Load error = %v, wantError %v", err, tc.wantError)
			}
			if err == nil && (cfg.SchemaVersion != 1 || cfg.Gacha.IncludeCollaborationURWeapons != tc.wantTrue) {
				t.Fatalf("unexpected config: %+v", cfg)
			}
		})
	}
}

func TestStartingChapterConfiguration(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{`{"schema_version":1}`, 21},
		{`{"schema_version":1,"story":{}}`, 21},
		{`{"schema_version":1,"story":{"start_pack_id":1}}`, 1},
		{`{"schema_version":1,"story":{"start_pack_id":21}}`, 21},
		{`{"schema_version":1,"story":{"start_pack_id":0}}`, 0},
		{`{"schema_version":1,"story":{"start_pack_id":22}}`, 0},
		{`{"schema_version":1,"story":{"start_pack_id":"1"}}`, 0},
		{`{"schema_version":1,"story":{"start_pack_id":null}}`, 0},
		{`{"schema_version":1,"story":null}`, 0},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if tc.want == 0 {
			if err == nil {
				t.Fatalf("accepted invalid entry: %s", tc.text)
			}
		} else if err != nil || cfg.Story.StartPackID != tc.want {
			t.Fatalf("configuration %s = %+v, %v", tc.text, cfg, err)
		}
	}
}

func TestMissingFileDefaultsWithoutCreating(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cfg, err := Load(path)
	if err != nil || cfg != Default() {
		t.Fatalf("config = %+v, error = %v", cfg, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Load created missing config: %v", err)
	}
}

func TestReadError(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("directory must not use defaults")
	}
}

func TestBesideExecutable(t *testing.T) {
	path, err := BesideExecutable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(filepath.Dir(executable), FileName) {
		t.Fatalf("unexpected path %s", path)
	}
}
