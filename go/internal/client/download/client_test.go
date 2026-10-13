package download

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonconfig "bd2server/internal/common/config"
)

func TestClientProbeAndDownloadOnlyDoNotRequirePreparationPackage(t *testing.T) {
	const bundleVersion = "20261007175947"
	const dataVersion = "20261008155037"
	makeArchive := func(names []string) []byte {
		t.Helper()
		var raw bytes.Buffer
		archive := zip.NewWriter(&raw)
		for _, name := range names {
			entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write(bytes.Repeat([]byte("archive-payload"), 32<<10)); err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		return raw.Bytes()
	}
	game := makeArchive(requiredGameFiles)
	data := makeArchive([]string{"common.db"})
	catalog := []byte(`{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/` + bundleVersion + `/scene.bundle"]}` + strings.Repeat(" ", 256<<10))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/Build/"):
			_, _ = w.Write(game)
		case strings.HasSuffix(r.URL.Path, "/common-dbdata.bin"):
			_, _ = w.Write(data)
		case strings.HasSuffix(r.URL.Path, "/common-dbdata.info"):
			_, _ = fmt.Fprint(w, len(data))
		case strings.HasSuffix(r.URL.Path, "/design.version"):
			_, _ = fmt.Fprintf(w, `{"version":"%s"}`, dataVersion)
		case strings.HasSuffix(r.URL.Path, "/catalog_alpha.json"):
			_, _ = w.Write(catalog)
		case strings.HasSuffix(r.URL.Path, "/catalog_alpha.hash"):
			_, _ = io.WriteString(w, "0f90a94601be05737f86a4ec7a1c889f")
		case strings.HasSuffix(r.URL.Path, "/scene.bundle"):
			_, _ = w.Write([]byte{'U', 'n', 'i', 't', 'y', 'F', 'S', 0, 0, 0, 0, 6, 'v', 0, 'r', 0, 0, 0, 0, 0, 0, 0, 0, 24})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	m := manifest{
		GameVersion: "2.36.7", GameDataVersion: dataVersion, BundleVersion: bundleVersion, Platform: "StandaloneWindows64",
		BuildOrigins: []string{server.URL}, ResourceOrigins: []string{server.URL}, GamePath: "/Build/game.zip",
		GameSHA256: fmt.Sprintf("%x", sha256.Sum256(game)), GameBytes: int64(len(game)),
		DataSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), DataBytes: int64(len(data)),
		CatalogSHA256: fmt.Sprintf("%x", sha256.Sum256(catalog)), CatalogBytes: int64(len(catalog)),
	}
	manifestPath := filepath.Join(root, "client-download.json")
	if err := commonconfig.Save(manifestPath, m, commonconfig.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	installed, err := Client(context.Background(), Options{PackageDir: root, Workers: 2, ProbeOnly: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "client-download.json" {
		t.Fatalf("probe created installation or checkpoint files: %v", entries)
	}
	if installed.GameDir != filepath.Join(root, "game") || installed.ResourceDir != filepath.Join(root, "resources") {
		t.Fatalf("download returned incorrect preparation paths: %+v", installed)
	}
	installed, err = Client(context.Background(), Options{PackageDir: root, Workers: 2}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredGameFiles {
		actual, err := os.ReadFile(filepath.Join(installed.GameDir, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(actual, bytes.Repeat([]byte("archive-payload"), 32<<10)) {
			t.Fatalf("game file was not downloaded intact: %s, %v", name, err)
		}
	}
	for _, name := range []string{"BepInEx", "winhttp.dll"} {
		if _, err := os.Stat(filepath.Join(installed.GameDir, name)); !os.IsNotExist(err) {
			t.Fatalf("download-only installed preparation files: %s, %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(installed.ResourceDir, "resource-fetch-manifest.json")); err != nil {
		t.Fatal("resource installation did not complete", err)
	}
}
