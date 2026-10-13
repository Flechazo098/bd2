package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/common/config"
)

func TestDecodePoliciesAndDefaults(t *testing.T) {
	type settings struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	for _, test := range []struct {
		name    string
		raw     string
		options config.DecodeOptions
		wantErr bool
	}{
		{name: "omission retains default", raw: `{"name":"saved"}`},
		{name: "unknown accepted by default", raw: `{"name":"saved","future":1}`},
		{name: "unknown rejected", raw: `{"name":"saved","future":1}`, options: config.DecodeOptions{DisallowUnknownFields: true}, wantErr: true},
		{name: "trailing accepted", raw: `{"name":"saved"} not JSON`},
		{name: "trailing JSON rejected", raw: `{"name":"saved"} {}`, options: config.DecodeOptions{RejectTrailing: true}, wantErr: true},
		{name: "trailing junk rejected", raw: `{"name":"saved"} broken`, options: config.DecodeOptions{RejectTrailing: true}, wantErr: true},
		{name: "BOM accepted explicitly", raw: "\xef\xbb\xbf" + `{"name":"saved"}`, options: config.DecodeOptions{AllowBOM: true}},
		{name: "BOM rejected by default", raw: "\xef\xbb\xbf" + `{"name":"saved"}`, wantErr: true},
		{name: "null cannot select a default", raw: `{"name":"saved","limit":null}`, options: config.DecodeOptions{RejectNull: true}, wantErr: true},
		{name: "null policy preserves trailing policy", raw: `{"name":"saved"} broken`, options: config.DecodeOptions{RejectNull: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := config.Decode([]byte(test.raw), settings{Name: "default", Limit: 7}, test.options)
			if test.wantErr {
				if err == nil {
					t.Fatal("invalid configuration was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != (settings{Name: "saved", Limit: 7}) {
				t.Fatalf("omitted setting lost its default: %+v", got)
			}
		})
	}
}

func TestEnsureKeepsUserFileAndSaveReplacesCompleteDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	type settings struct {
		Name string `json:"name"`
	}
	if err := config.Ensure(path, settings{Name: "default"}, config.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(path, settings{Name: "operator"}, config.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(path, settings{Name: "new default"}, config.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("default publication replaced operator configuration")
	}
	if err := config.Save(path, make(chan int), config.WriteOptions{}); err == nil {
		t.Fatal("unsupported JSON value was saved")
	}
	got, err := config.Load(path, settings{}, config.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true})
	if err != nil || got.Name != "operator" {
		t.Fatalf("failed save damaged configuration: %+v, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestFailedInstallRemovesStagedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "directory")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(target, struct{}{}, config.WriteOptions{}); err == nil {
		t.Fatal("directory was replaced with a configuration file")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed install left temporary files: %v", entries)
	}
}
