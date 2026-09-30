package app

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	h := &handler{token: "test-session", origin: "http://local.invalid", logger: logger}
	body := `{"game_directory":"Z:\\missing","server_origin":"http://127.0.0.1:8080","cdn_mode":"official","local_resource_directory":"","ui_language":"zh-CN"}`
	request := httptest.NewRequest(http.MethodPost, "/api/inspect", strings.NewReader(body))
	request.Header.Set("X-BD2-Session", "test-session")
	request.Header.Set("Origin", "http://local.invalid")
	response := httptest.NewRecorder()
	h.routes().ServeHTTP(response, request)
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

func TestAuthorizationLogDoesNotIncludeSessionValues(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	h := &handler{token: "expected-session-secret", origin: "http://local.invalid", logger: logger}
	request := httptest.NewRequest(http.MethodPost, "/api/inspect", strings.NewReader("{}"))
	request.Header.Set("X-BD2-Session", "provided-session-secret")
	request.Header.Set("Origin", "http://local.invalid")
	response := httptest.NewRecorder()
	h.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	logged := output.String()
	for _, secret := range []string{"expected-session-secret", "provided-session-secret"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log contains session value %q: %s", secret, logged)
		}
	}
}
