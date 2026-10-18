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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	gameNames := append(slices.Clone(requiredGameFiles), "BrownDust II_Data/resources.assets", "BrownDust II_Data/StreamingAssets/aa/catalog.json")
	game := makeArchive(gameNames)
	data := makeArchive([]string{"common.db"})
	catalog := []byte(`{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/` + bundleVersion + `/scene.bundle"]}` + strings.Repeat(" ", 256<<10))
	bundleData := []byte{'U', 'n', 'i', 't', 'y', 'F', 'S', 0, 0, 0, 0, 6, 'v', 0, 'r', 0, 0, 0, 0, 0, 0, 0, 0, 28, 'd', 'a', 't', 'a'}
	var mu sync.Mutex
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
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
			_, _ = w.Write(bundleData)
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
	resetRequests := func() { mu.Lock(); requested = nil; mu.Unlock() }
	requests := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(requested) }
	run := func() {
		t.Helper()
		if _, err := Client(context.Background(), Options{PackageDir: root, Workers: 2}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() map[string]time.Time {
		t.Helper()
		out := map[string]time.Time{}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err == nil {
				out[path] = info.ModTime()
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	assertUnchanged := func(before map[string]time.Time, except ...string) {
		t.Helper()
		after := snapshot()
		if len(before) != len(after) {
			t.Fatal("repeat changed the installation file set")
		}
		for path, stamp := range before {
			if !slices.Contains(except, path) && after[path] != stamp {
				t.Fatalf("repeat rewrote %s", path)
			}
		}
	}
	before := snapshot()
	resetRequests()
	run()
	if got := requests(); len(got) != 0 {
		t.Fatalf("complete installation still contacted download origins: %v", got)
	}
	assertUnchanged(before)
	prepared := []string{"BrownDust II_Data/resources.assets", "BrownDust II_Data/StreamingAssets/aa/catalog.json"}
	patched := []byte("launcher-prepared-content")
	for _, relative := range prepared {
		if err := os.WriteFile(filepath.Join(installed.GameDir, filepath.FromSlash(relative)), patched, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordPreparedGame(installed.GameDir, prepared...); err != nil {
		t.Fatal(err)
	}
	if err := RecordPreparedGame(installed.GameDir, "UnityPlayer.dll"); err == nil {
		t.Fatal("unrecognized game change was accepted as launcher output")
	}
	before = snapshot()
	resetRequests()
	run()
	if got := requests(); len(got) != 0 {
		t.Fatalf("registered launcher output forced an official download: %v", got)
	}
	assertUnchanged(before)
	player := filepath.Join(installed.GameDir, "UnityPlayer.dll")
	bundle := filepath.Join(installed.ResourceDir, "ServerData", "StandaloneWindows64", "HD", bundleVersion, "scene.bundle")
	for _, path := range []string{player, bundle} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	resetRequests()
	run()
	got := requests()
	if len(got) == 0 {
		t.Fatal("missing resource was not downloaded")
	}
	for _, path := range got {
		if !strings.HasSuffix(path, "/scene.bundle") {
			t.Fatalf("repair fetched an already complete file: %s", path)
		}
	}
	if actual, err := os.ReadFile(player); err != nil || !bytes.Equal(actual, bytes.Repeat([]byte("archive-payload"), 32<<10)) {
		t.Fatal("missing game file was not repaired from cached official archive")
	}
	for _, relative := range prepared {
		if actual, err := os.ReadFile(filepath.Join(installed.GameDir, filepath.FromSlash(relative))); err != nil || !bytes.Equal(actual, patched) {
			t.Fatal("single-file repair overwrote registered launcher output")
		}
	}
	for _, path := range []string{player, bundle} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body[len(body)-1] ^= 1
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	resetRequests()
	run()
	got = requests()
	if len(got) == 0 {
		t.Fatal("same-size damaged resource was trusted")
	}
	for _, path := range got {
		if !strings.HasSuffix(path, "/scene.bundle") {
			t.Fatalf("damaged-file repair fetched unaffected content: %s", path)
		}
	}
	if actual, err := os.ReadFile(player); err != nil || !bytes.Equal(actual, bytes.Repeat([]byte("archive-payload"), 32<<10)) {
		t.Fatal("same-size game corruption survived cached installation receipt")
	}
	if actual, err := os.ReadFile(bundle); err != nil || !bytes.Equal(actual, bundleData) {
		t.Fatal("same-size bundle payload corruption survived its valid header")
	}
	unknown := filepath.Join(installed.GameDir, filepath.FromSlash(prepared[0]))
	if err := os.WriteFile(unknown, []byte("unregistered modification"), 0600); err != nil {
		t.Fatal(err)
	}
	resetRequests()
	run()
	if actual, err := os.ReadFile(unknown); err != nil || !bytes.Equal(actual, bytes.Repeat([]byte("archive-payload"), 32<<10)) {
		t.Fatal("unregistered launcher-file change was silently trusted")
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("game-file repair unnecessarily contacted the network: %v", got)
	}
	catalogTarget := filepath.Join(installed.GameDir, filepath.FromSlash(prepared[1]))
	if err := os.WriteFile(catalogTarget, []byte("unregistered catalog change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordPreparedGame(installed.GameDir, prepared[0]); err != nil {
		t.Fatal(err)
	}
	run()
	if actual, err := os.ReadFile(catalogTarget); err != nil || !bytes.Equal(actual, bytes.Repeat([]byte("archive-payload"), 32<<10)) {
		t.Fatal("registering one prepared file also trusted another modified file")
	}
}
