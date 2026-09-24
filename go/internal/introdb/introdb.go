// Package introdb patches Brown Dust II's embedded Intro database without
// changing the size or layout of Unity's resources.assets file.
package introdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bd2server/internal/dbcrypt"
)

const (
	oldURL = "https://mt.bd2.pmang.cloud/"
)

var salt = dbcrypt.Header

// Result describes a completed in-place client patch. BackupPath is the
// immutable pre-patch copy and is never overwritten by a later invocation.
type Result struct {
	AssetsPath string
	BackupPath string
	ObjectPath int64
	ObjectSize uint32
	OldURL     string
	NewURL     string
}

// VerifyResult is useful to patch-client's --verify mode and to diagnostics.
type VerifyResult struct {
	AssetsPath string
	ObjectPath int64
	ObjectSize uint32
	URL        string
}

// PatchClient resolves resources.assets below gameDir, verifies the embedded
// TextAsset named Intro, makes a one-time .bak copy, then atomically replaces
// resources.assets. newURL must be exactly as long as the original URL.
func PatchClient(gameDir, newURL string) (Result, error) {
	if len(newURL) != len(oldURL) {
		return Result{}, fmt.Errorf("introdb: local URL must be exactly %d bytes (got %d): %q", len(oldURL), len(newURL), newURL)
	}
	if !isASCII(newURL) {
		return Result{}, errors.New("introdb: local URL must be ASCII")
	}
	assets, err := ResourcesPath(gameDir)
	if err != nil {
		return Result{}, err
	}
	b, err := os.ReadFile(assets)
	if err != nil {
		return Result{}, fmt.Errorf("introdb: read assets: %w", err)
	}
	entry, script, err := findIntro(b)
	if err != nil {
		return Result{}, err
	}
	plain, err := DecryptPages(script)
	if err != nil {
		return Result{}, fmt.Errorf("introdb: decrypt Intro TextAsset: %w", err)
	}
	// A repeated command is intentionally a no-op. This makes automation safe
	// while refusing to overwrite an Intro database that was patched to a
	// different endpoint by another tool.
	if bytes.Count(plain, []byte(oldURL)) == 0 {
		current, findErr := urlInDB(plain)
		if findErr != nil {
			return Result{}, findErr
		}
		if current == newURL {
			return Result{assets, assets + ".bak", entry.pathID, entry.size, current, newURL}, nil
		}
		return Result{}, fmt.Errorf("introdb: LIVE_URL is already %q, not the expected official URL", current)
	}
	if err := validateIntroDB(plain, oldURL); err != nil {
		return Result{}, err
	}
	updated := bytes.Replace(plain, []byte(oldURL), []byte(newURL), 1)
	ciphertext, err := EncryptPages(updated)
	if err != nil {
		return Result{}, err
	}
	copy(b[entry.scriptStart:entry.scriptStart+int64(len(ciphertext))], ciphertext)
	// Verify the staged bytes before touching the installed asset. This catches
	// a parser/cipher regression even though the payload length never changes.
	_, stagedScript, err := findIntro(b)
	if err != nil {
		return Result{}, fmt.Errorf("introdb: re-read staged Intro: %w", err)
	}
	stagedPlain, err := DecryptPages(stagedScript)
	if err != nil {
		return Result{}, fmt.Errorf("introdb: decrypt staged Intro: %w", err)
	}
	if err := validateIntroDB(stagedPlain, newURL); err != nil {
		return Result{}, fmt.Errorf("introdb: staged verification: %w", err)
	}

	backup := assets + ".bak"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := copyFile(assets, backup); err != nil {
			return Result{}, fmt.Errorf("introdb: create backup: %w", err)
		}
	} else if err != nil {
		return Result{}, fmt.Errorf("introdb: inspect backup: %w", err)
	}
	if err := atomicWrite(assets, b); err != nil {
		return Result{}, err
	}
	return Result{assets, backup, entry.pathID, entry.size, oldURL, newURL}, nil
}

// VerifyClient reads and decrypts the embedded Intro TextAsset. It verifies
// the SQLite header plus the expected table/key markers before returning URL.
func VerifyClient(gameDir string) (VerifyResult, error) {
	assets, err := ResourcesPath(gameDir)
	if err != nil {
		return VerifyResult{}, err
	}
	b, err := os.ReadFile(assets)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("introdb: read assets: %w", err)
	}
	e, script, err := findIntro(b)
	if err != nil {
		return VerifyResult{}, err
	}
	p, err := DecryptPages(script)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("introdb: decrypt Intro TextAsset: %w", err)
	}
	if !bytes.HasPrefix(p, salt) {
		return VerifyResult{}, errors.New("introdb: decrypted Intro is not a SQLite database")
	}
	if !bytes.Contains(p, []byte("ServerURLTable")) || !bytes.Contains(p, []byte("LIVE_URL")) {
		return VerifyResult{}, errors.New("introdb: Intro database lacks ServerURLTable/LIVE_URL markers")
	}
	url, err := urlInDB(p)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{assets, e.pathID, e.size, url}, nil
}

// ResourcesPath returns the conventional standalone Windows resource location.
func ResourcesPath(gameDir string) (string, error) {
	if gameDir == "" {
		return "", errors.New("introdb: empty game directory")
	}
	p := filepath.Join(gameDir, "BrownDust II_Data", "resources.assets")
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("introdb: resources.assets not found at %q: %w", p, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("introdb: resources.assets path is a directory: %q", p)
	}
	return p, nil
}

// DecryptPages decrypts the game's independent 4096-byte AES-CBC pages.
func DecryptPages(in []byte) ([]byte, error) { return dbcrypt.DecryptPages(in) }

// EncryptPages encrypts the game's independent 4096-byte AES-CBC pages.
func EncryptPages(in []byte) ([]byte, error) { return dbcrypt.EncryptPages(in) }

func validateIntroDB(p []byte, expected string) error {
	if !bytes.HasPrefix(p, salt) {
		return errors.New("introdb: decrypted Intro is not a SQLite database (wrong cipher parameters or asset)")
	}
	if !bytes.Contains(p, []byte("ServerURLTable")) || !bytes.Contains(p, []byte("LIVE_URL")) {
		return errors.New("introdb: database does not contain ServerURLTable/LIVE_URL")
	}
	if bytes.Count(p, []byte(expected)) != 1 {
		return fmt.Errorf("introdb: expected LIVE_URL value %q exactly once, found %d", expected, bytes.Count(p, []byte(expected)))
	}
	return nil
}
func urlInDB(p []byte) (string, error) {
	if bytes.Count(p, []byte(oldURL)) == 1 {
		return oldURL, nil
	}
	// Patching stays strictly exact-length, so an already-patched value can be
	// diagnosed without needing a SQLite C dependency. Search only the record
	// containing LIVE_URL; unrelated configuration URLs are also present.
	marker := []byte("LIVE_URL")
	for start := 0; ; {
		i := bytes.Index(p[start:], marker)
		if i < 0 {
			break
		}
		i += start + len(marker)
		limit := i + 128
		if limit > len(p) {
			limit = len(p)
		}
		if j := bytes.Index(p[i:limit], []byte("http")); j >= 0 {
			at := i + j
			if at+len(oldURL) <= len(p) {
				u := p[at : at+len(oldURL)]
				if bytes.HasSuffix(u, []byte("/")) && isASCII(string(u)) {
					return string(u), nil
				}
			}
		}
		start = i
	}
	return "", errors.New("introdb: cannot locate LIVE_URL value")
}
func isASCII(s string) bool {
	for _, c := range []byte(s) {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, e := io.Copy(out, in)
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}
func atomicWrite(path string, data []byte) error {
	tmp := path + ".bd2server.tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("introdb: stage patched assets: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("introdb: stage patched assets: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("introdb: sync staged assets: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("introdb: close staged assets: %w", err)
	}
	// Go's Windows implementation replaces an existing destination (covered by
	// TestPatchClientTransaction). Prefer that one-step replacement. A rollback
	// path remains for Windows filesystems that reject replacement by rename.
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	rollback := path + ".bd2server.rollback"
	if _, err := os.Stat(rollback); err == nil {
		return fmt.Errorf("introdb: cannot safely replace assets; rollback file exists: %q", rollback)
	}
	if err := os.Rename(path, rollback); err != nil {
		return fmt.Errorf("introdb: replace assets: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		if restoreErr := os.Rename(rollback, path); restoreErr != nil {
			return fmt.Errorf("introdb: replacement failed (%v) and rollback restore failed (%v); original is %q", err, restoreErr, rollback)
		}
		return fmt.Errorf("introdb: replace assets (original restored): %w", err)
	}
	if err := os.Remove(rollback); err != nil {
		return fmt.Errorf("introdb: patched successfully but could not remove rollback copy %q: %w", rollback, err)
	}
	return nil
}

// Kept here so the object parser and patch transaction stay together.
type textAsset struct {
	pathID      int64
	size        uint32
	scriptStart int64
	scriptSize  int
}
type reader struct {
	b      []byte
	off    int
	little bool
}

func (r *reader) need(n int) error {
	if n < 0 || r.off+n > len(r.b) {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (r *reader) u8() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}
func (r *reader) u16() (uint16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	if !r.little {
		v = binary.BigEndian.Uint16(r.b[r.off:])
	}
	r.off += 2
	return v, nil
}
func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	if !r.little {
		v = binary.BigEndian.Uint32(r.b[r.off:])
	}
	r.off += 4
	return v, nil
}
func (r *reader) i32() (int32, error) { v, e := r.u32(); return int32(v), e }
func (r *reader) i64() (int64, error) {
	if e := r.need(8); e != nil {
		return 0, e
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	if !r.little {
		v = binary.BigEndian.Uint64(r.b[r.off:])
	}
	r.off += 8
	return int64(v), nil
}
func (r *reader) u64() (uint64, error) { v, e := r.i64(); return uint64(v), e }
func (r *reader) skip(n int) error {
	if e := r.need(n); e != nil {
		return e
	}
	r.off += n
	return nil
}
func (r *reader) align4() { r.off = (r.off + 3) &^ 3 }
func (r *reader) str() (string, error) {
	n, e := r.u32()
	if e != nil {
		return "", e
	}
	if n > uint32(len(r.b)-r.off) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(r.b[r.off : r.off+int(n)])
	r.off += int(n)
	r.align4()
	return s, nil
}
func (r *reader) cstr() (string, error) {
	start := r.off
	for r.off < len(r.b) && r.b[r.off] != 0 {
		r.off++
	}
	if r.off == len(r.b) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(r.b[start:r.off])
	r.off++
	return s, nil
}

func findIntro(file []byte) (textAsset, []byte, error) {
	if len(file) < 48 {
		return textAsset{}, nil, errors.New("introdb: truncated Unity serialized file")
	}
	version := binary.BigEndian.Uint32(file[8:12])
	if version < 14 {
		return textAsset{}, nil, fmt.Errorf("introdb: Unity serialized version %d is unsupported", version)
	}
	dataOff := binary.BigEndian.Uint64(file[32:40])
	if dataOff >= uint64(len(file)) {
		return textAsset{}, nil, errors.New("introdb: invalid Unity data offset")
	}
	// Unity records endian at byte 16: 0 means little endian for metadata.
	r := reader{b: file[48:int(dataOff)], little: file[16] == 0}
	if _, e := r.cstr(); e != nil {
		return textAsset{}, nil, fmt.Errorf("introdb: user version: %w", e)
	}
	if _, e := r.i32(); e != nil {
		return textAsset{}, nil, e
	}
	if _, e := r.u8(); e != nil {
		return textAsset{}, nil, e
	}
	typeCount, e := r.i32()
	if e != nil || typeCount < 0 || typeCount > 100000 {
		return textAsset{}, nil, fmt.Errorf("introdb: invalid type count %d", typeCount)
	}
	classes := make([]int32, typeCount)
	for i := range classes {
		c, e := r.i32()
		if e != nil {
			return textAsset{}, nil, e
		}
		classes[i] = c
		if _, e = r.u8(); e != nil {
			return textAsset{}, nil, e
		}
		if _, e = r.u16(); e != nil {
			return textAsset{}, nil, e
		}
		if c == 114 {
			if e = r.skip(16); e != nil {
				return textAsset{}, nil, e
			}
		}
		if e = r.skip(16); e != nil {
			return textAsset{}, nil, e
		}
	}
	count, e := r.i32()
	if e != nil || count < 0 || count > 10000000 {
		return textAsset{}, nil, fmt.Errorf("introdb: invalid object count %d", count)
	}
	var candidates []textAsset
	for i := int32(0); i < count; i++ {
		// Since serialized version 14, Unity aligns object records to four bytes
		// before their 64-bit path ID (not to an eight-byte boundary).
		r.off = (r.off + 48 + 3) &^ 3
		r.off -= 48
		pid, e := r.i64()
		if e != nil {
			return textAsset{}, nil, e
		}
		start, e := r.u64()
		if e != nil {
			return textAsset{}, nil, e
		}
		size, e := r.u32()
		if e != nil {
			return textAsset{}, nil, e
		}
		typ, e := r.i32()
		if e != nil {
			return textAsset{}, nil, e
		}
		if typ < 0 || int(typ) >= len(classes) {
			return textAsset{}, nil, fmt.Errorf("introdb: object %d has invalid type ID %d", i, typ)
		}
		if classes[typ] != 49 {
			continue
		}
		abs := int64(dataOff) + int64(start)
		if abs < 0 || abs+int64(size) > int64(len(file)) {
			return textAsset{}, nil, fmt.Errorf("introdb: TextAsset object %d range outside file (start=%d size=%d)", i, start, size)
		}
		candidates = append(candidates, textAsset{pathID: pid, size: size, scriptStart: abs})
	}
	for _, c := range candidates {
		object := reader{b: file[c.scriptStart : c.scriptStart+int64(c.size)], little: r.little}
		name, e := object.str()
		if e != nil {
			continue
		}
		n, e := object.u32()
		if e != nil || n > uint32(len(object.b)-object.off) {
			continue
		}
		if name == "Intro" {
			c.scriptStart += int64(object.off)
			c.scriptSize = int(n)
			return c, file[c.scriptStart : c.scriptStart+int64(n)], nil
		}
	}
	return textAsset{}, nil, errors.New("introdb: TextAsset named Intro was not found")
}
