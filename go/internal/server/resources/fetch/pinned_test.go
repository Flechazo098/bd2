package resourcefetch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/common/release"
)

func TestPinnedDownloadPreservesInstalledFileWhenSameSizeContentDiffers(t *testing.T) {
	response := "wrong"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) }))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "resource.bundle")
	if err := os.WriteFile(target, []byte("prior"), 0600); err != nil {
		t.Fatal(err)
	}
	pin := release.ResourceFile{Size: 5, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("valid")))}
	if _, err := downloadFile(context.Background(), server.Client(), server.URL, target, pin); err == nil {
		t.Fatal("accepted wrong bytes with the expected length")
	}
	if actual, err := os.ReadFile(target); err != nil || string(actual) != "prior" {
		t.Fatalf("failed verification changed installed file: %q, %v", actual, err)
	}
	response = "valid"
	if _, err := downloadFile(context.Background(), server.Client(), server.URL, target, pin); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.ReadFile(target); err != nil || string(actual) != "valid" {
		t.Fatalf("verified replacement was not installed: %q, %v", actual, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "resource.bundle" {
		t.Fatalf("download left temporary files: %v", entries)
	}
}
