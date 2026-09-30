package gamedata

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestValidate(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "123", "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(release, ArchiveName)
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, err := w.Create("common-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("verified payload")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, InfoName), []byte(strconv.FormatInt(stat.Size(), 10)), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Validate(root, "123")
	if err != nil {
		t.Fatal(err)
	}
	if result.EntryCount != 1 || result.UncompressedSize != int64(len("verified payload")) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidateRejectsSizeMismatch(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "123", "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, ArchiveName), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, InfoName), []byte("999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(root, "123"); err == nil {
		t.Fatal("expected validation failure")
	}
}

func TestEnsureDownloadsAndPreservesBrokenArchive(t *testing.T) {
	served := t.TempDir()
	writeTestArchive(t, served, "123", []byte("healthy archive payload"))
	origin := httptest.NewServer(http.FileServer(http.Dir(served)))
	defer origin.Close()

	root := t.TempDir()
	release := filepath.Join(root, "123", "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, ArchiveName), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, InfoName), []byte("6"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, downloaded, err := Ensure(context.Background(), origin.Client(), root, "123", origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !downloaded || result.EntryCount != 1 {
		t.Fatalf("unexpected ensure result: downloaded=%v result=%+v", downloaded, result)
	}
	broken, err := filepath.Glob(filepath.Join(release, ArchiveName+".broken.*"))
	if err != nil || len(broken) != 1 {
		t.Fatalf("broken archive was not preserved: %v %v", broken, err)
	}
}

func writeTestArchive(t *testing.T, root, version string, payload []byte) {
	t.Helper()
	release := filepath.Join(root, version, "release")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(release, ArchiveName)
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, err := w.Create("common-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, InfoName), []byte(strconv.FormatInt(stat.Size(), 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}
