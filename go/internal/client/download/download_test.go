package download

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The resource paths follow the native Addressables catalog and GameData URL layout.
func TestResourceDownloadRejectsCorruptionAndResumesCompleteInstallation(t *testing.T) {
	const bundleVersion = "20261007175947"
	const dataVersion = "20261008155037"
	header := bytes.NewBufferString("UnityFS\x00")
	if err := binary.Write(header, binary.BigEndian, uint32(6)); err != nil {
		t.Fatal(err)
	}
	header.WriteString("2022.3\x002022.3.62f1\x00")
	length := header.Len() + 8 + len("bundle-payload")
	if err := binary.Write(header, binary.BigEndian, uint64(length)); err != nil {
		t.Fatal(err)
	}
	header.WriteString("bundle-payload")
	valid := header.Bytes()
	var data bytes.Buffer
	dataArchive := zip.NewWriter(&data)
	entry, err := dataArchive.Create("common.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "resource-database"); err != nil {
		t.Fatal(err)
	}
	if err := dataArchive.Close(); err != nil {
		t.Fatal(err)
	}
	var requests, bundleAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") != "" {
			t.Error("resource transfer used a partial GET")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/catalog_alpha.json"):
			_, _ = fmt.Fprintf(w, `{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/%s/nested/scene.bundle"]}`, bundleVersion)
		case strings.HasSuffix(r.URL.Path, "/catalog_alpha.hash"):
			_, _ = io.WriteString(w, "0f90a94601be05737f86a4ec7a1c889f")
		case strings.HasSuffix(r.URL.Path, "/design.version"):
			_, _ = fmt.Fprintf(w, `{"version":"%s"}`, dataVersion)
		case strings.HasSuffix(r.URL.Path, "/common-dbdata.info"):
			_, _ = fmt.Fprint(w, data.Len())
		case strings.HasSuffix(r.URL.Path, "/common-dbdata.bin"):
			_, _ = w.Write(data.Bytes())
		case strings.HasSuffix(r.URL.Path, "/nested/scene.bundle"):
			if bundleAttempts.Add(1) == 1 {
				_, _ = w.Write(valid[:len(valid)-1])
				return
			}
			_, _ = w.Write(valid)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	args := []string{"--output", root, "--bundle-version", bundleVersion, "--game-data-version", dataVersion, "--server-data-url", server.URL + "/ServerData", "--game-data-url", server.URL + "/GameData"}
	if err := RunResources(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	if bundleAttempts.Load() != 2 {
		t.Fatalf("corrupt bundle did not require a full retry: %d", bundleAttempts.Load())
	}
	bundlePath := filepath.Join(root, "ServerData", "StandaloneWindows64", "HD", bundleVersion, "nested", "scene.bundle")
	installed, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, valid) {
		t.Fatal("corrupt or incomplete bundle was published")
	}
	if _, err := os.Stat(filepath.Join(root, "GameData", dataVersion, "release", "common-dbdata.bin")); err != nil {
		t.Fatal("native GameData layout missing", err)
	}
	before := requests.Load()
	if err := RunResources(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Fatal("reopened downloader fetched already verified installation")
	}
	if err := os.WriteFile(bundlePath, []byte("corrupt-local-file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunResources(context.Background(), args, io.Discard); err != nil {
		t.Fatal(err)
	}
	if bundleAttempts.Load() != 3 {
		t.Fatal("damaged cached bundle was trusted")
	}
	repaired, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repaired, valid) {
		t.Fatal("download retry did not repair the installed bundle")
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name(), ".part") {
			return fmt.Errorf("temporary file left: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeArchiveDoesNotAlterAnExistingInstallation(t *testing.T) {
	root := t.TempDir()
	game := filepath.Join(root, "game")
	if err := os.Mkdir(game, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(game, "BrownDust II.exe")
	if err := os.WriteFile(sentinel, []byte("existing-game"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "game.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(out)
	for _, name := range []string{"BrownDust II.exe", "UnityPlayer.dll", "BrownDust II_Data/globalgamemanagers", "BrownDust II_Data/Managed/Assembly-CSharp.dll", "../escape.dll"} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, "replacement"); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(path, game, true, "digest"); err == nil {
		t.Fatal("unsafe archive was extracted")
	}
	value, err := os.ReadFile(sentinel)
	if err != nil || string(value) != "existing-game" {
		t.Fatal("rejected archive changed the existing game")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.dll")); !os.IsNotExist(err) {
		t.Fatal("archive escaped game directory")
	}
}
