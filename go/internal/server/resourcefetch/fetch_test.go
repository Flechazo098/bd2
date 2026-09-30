package resourcefetch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCatalogPathsAreUniqueSortedAndSafe(t *testing.T) {
	data := []byte(`{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/1/z.bundle","ignored","{BDNetwork.CdnInfo.Info}\\StandaloneWindows64\\HD\\1\\a.bundle","{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/1/z.bundle"]}`)
	got, err := catalogPaths(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.bundle", "z.bundle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths=%v want=%v", got, want)
	}
	if _, err := catalogPaths([]byte(`{"m_InternalIds":["{BDNetwork.CdnInfo.Info}/StandaloneWindows64/HD/1/../../escape"]}`)); err == nil {
		t.Fatal("unsafe catalog path accepted")
	}
}

func TestValidUnityBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle")
	if err := os.WriteFile(path, []byte("UnityFSpayload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if size, ok := validUnityBundle(path); !ok || size != 14 {
		t.Fatalf("size=%d ok=%v", size, ok)
	}
}

func TestFetchRejectsUntrustedVersionPathsBeforeNetwork(t *testing.T) {
	_, err := Fetch(t.Context(), Options{OutputRoot: t.TempDir(), BundleVersion: "../../escape", GameDataVersion: "20260923193640"})
	if err == nil {
		t.Fatal("unsafe resource version accepted")
	}
}
