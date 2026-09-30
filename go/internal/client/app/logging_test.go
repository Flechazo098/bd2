package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestOpenPersistentLoggerUsesExecutableLogDirectory(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bd2client.exe")
	logger, closer, logPath, err := OpenPersistentLogger(executable)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("test entry")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "logs", clientLogName)
	if logPath != wantPath {
		t.Fatalf("log path=%q want=%q", logPath, wantPath)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("test entry")) {
		t.Fatalf("log does not contain test entry: %s", data)
	}
}

func TestPersistentOperationalLogsUseEnglish(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	studio := NewStudio(Options{Logger: logger}, NativeHost{})
	studio.Startup(context.Background())
	_, _ = studio.Inspect(Request{GameDirectory: `Z:\missing`, UILanguage: "zh-CN"})
	for len(output.Bytes()) > 0 {
		r, size := utf8.DecodeRune(output.Bytes())
		if unicode.Is(unicode.Han, r) {
			t.Fatalf("operational log contains Han character %q: %s", r, output.String())
		}
		output.Next(size)
	}
}

func TestRollingLogRetainsOneBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, clientLogName)
	backup := filepath.Join(root, clientLogBackupName)
	writer, err := openRollingLog(path, backup, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != "second" || string(previous) != "first" {
		t.Fatalf("active=%q backup=%q", active, previous)
	}
}
