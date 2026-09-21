package introdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/dbcrypt"
)

func referenceClientDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("BD2_TEST_CLIENT_DIR")
	if dir == "" {
		t.Skip("set BD2_TEST_CLIENT_DIR to enable read-only client integration tests")
	}
	return dir
}

func TestPagesRoundTrip(t *testing.T) {
	p := make([]byte, dbcrypt.PageSize*2)
	copy(p, salt)
	for i := 16; i < len(p); i++ {
		p[i] = byte(i * 31)
	}
	c, err := EncryptPages(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptPages(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, p) {
		t.Fatal("page cipher did not round-trip")
	}
}
func TestPagesRejectPartialPage(t *testing.T) {
	if _, err := DecryptPages(make([]byte, dbcrypt.PageSize-1)); err == nil {
		t.Fatal("accepted partial page")
	}
}
func TestValidateRequiresUniqueURL(t *testing.T) {
	p := append([]byte{}, salt...)
	p = append(p, []byte(" ServerURLTable LIVE_URL "+oldURL)...)
	if err := validateIntroDB(p, oldURL); err != nil {
		t.Fatal(err)
	}
	if err := validateIntroDB(append(p, []byte(oldURL)...), oldURL); err == nil {
		t.Fatal("accepted duplicated URL")
	}
}

// This is deliberately read-only. When the reference client is present, it
// proves the Unity metadata parser and crypto parameters against the real file.
func TestKnownClientVerify(t *testing.T) {
	knownClientDir := referenceClientDir(t)
	if _, err := os.Stat(knownClientDir); os.IsNotExist(err) {
		t.Skip("reference client is not available")
	}
	v, err := VerifyClient(knownClientDir)
	if err != nil {
		t.Fatal(err)
	}
	if v.URL != oldURL && v.URL != "http://127.0.0.1:8080/game/" {
		t.Fatalf("unexpected LIVE_URL=%q", v.URL)
	}
}

// The transaction test copies the reference asset into a test directory, never
// mutating the installed client. It covers Unity object lookup, backup,
// encryption, atomic replacement, and the post-patch diagnostic together.
func TestPatchClientTransaction(t *testing.T) {
	knownClientDir := referenceClientDir(t)
	if _, err := os.Stat(knownClientDir); os.IsNotExist(err) {
		t.Skip("reference client is not available")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "BrownDust II_Data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := ResourcesPath(knownClientDir)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := VerifyClient(knownClientDir); err == nil && current.URL != oldURL {
		// The installed research client is normally patched. Exercise the
		// transaction against its immutable pre-patch backup in that case.
		if _, err := os.Stat(src + ".bak"); err != nil {
			t.Skipf("official pre-patch asset is unavailable: %v", err)
		}
		src += ".bak"
	}
	dst := filepath.Join(dir, "resources.assets")
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, in, 0o600); err != nil {
		t.Fatal(err)
	}
	const local = "http://127.0.0.1:8080/bd2x/"
	r, err := PatchClient(tmp, local)
	if err != nil {
		t.Fatal(err)
	}
	if r.OldURL != oldURL || r.NewURL != local || r.BackupPath != dst+".bak" {
		t.Fatalf("unexpected patch result: %#v", r)
	}
	if _, err := os.Stat(r.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	v, err := VerifyClient(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if v.URL != local {
		t.Fatalf("LIVE_URL=%q, want %q", v.URL, local)
	}
	if _, err := PatchClient(tmp, local); err != nil {
		t.Fatalf("idempotent patch: %v", err)
	}
}
