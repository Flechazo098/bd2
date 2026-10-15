package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testManifest() Manifest {
	return Manifest{
		Schema: SchemaVersion, Channel: "stable", PublishedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC),
		Commit: strings.Repeat("a", 40),
		Server: &Component{Version: "2.36.7+server.0.2.0", Migration: &Migration{Kind: MigrationNone},
			Files: []File{{Name: "bd2server.zip", Platform: "linux-amd64", Size: 3, SHA256: strings.Repeat("0", 64), URL: "../files/x/bd2server.zip"}}},
	}
}

func TestVerifyAcceptsOnlyTheTrustedSignerAndExactBytes(t *testing.T) {
	seed, key, err := testSigner()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signManifest(testManifest(), seed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(signed, []TrustedKey{key})
	if err != nil {
		t.Fatalf("Verify(signed) = %v", err)
	}
	if got.Server.Version != "2.36.7+server.0.2.0" {
		t.Fatalf("verified manifest server version = %q", got.Server.Version)
	}

	_, other, err := testSigner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(signed, []TrustedKey{other}); err == nil {
		t.Fatal("a manifest signed by a key the build does not trust was accepted")
	}

	// Raising the version inside the signed bytes must break the signature.
	tampered := bytes.Replace(signed, []byte("server.0.2.0"), []byte("server.9.2.0"), 1)
	if bytes.Equal(tampered, signed) {
		t.Fatal("test setup: version not found in envelope")
	}
	if _, err := Verify(tampered, []TrustedKey{key}); err == nil {
		t.Fatal("a manifest edited after signing was accepted")
	}
}

func TestNewerRanksTheGameVersionAboveTheComponentNumber(t *testing.T) {
	for _, c := range []struct {
		current, candidate string
		want               bool
	}{
		{"2.36.7+server.0.2.0", "2.36.7+server.0.2.1", true},
		{"2.36.7+server.0.10.0", "2.36.7+server.0.9.9", false},
		{"2.36.7+server.3.0.0", "2.37.0+server.0.1.0", true},
		{"2.36.7+server.0.2.0", "2.36.7+server.0.2.0", false},
	} {
		got, err := Newer(c.current, c.candidate)
		if err != nil || got != c.want {
			t.Errorf("Newer(%q, %q) = %v, %v; want %v", c.current, c.candidate, got, err, c.want)
		}
	}
	if _, err := Newer("2.36.7+server.0.2.0", "2.36.7+client.0.3.0"); err == nil {
		t.Error("comparing a server release with a client release did not fail")
	}
}

const sampleChangelog = "# Changelog\n\nIntroduction text belongs to no release.\n\n```markdown\n## server 9.9.9+server.9.9.9 · 2026-01-01\n```\n\n" +
	"## server 2.36.7+server.0.3.0 · 2026-10-20\n\n" +
	"> type: emergency\n> pin: yes\n> days: 7\n> migration: irreversible\n\n" +
	"### en_us: Save layout change\n\n#### Changed\n\n- **Guild** data moves to a new table\n- See [notes](https://example.com/a)\n\n![chart](https://example.com/a.png)\n\n" +
	"### zh_cn: 存档结构调整\n\n#### 调整\n\n- **公会**数据迁移到新表\n\n" +
	"## client 2.36.7+client.0.2.0 · 2026-10-15\n\n" +
	"### en_us:\n\n- Fixed the login screen\n"

func parseSample(t *testing.T) (server, client Entry) {
	t.Helper()
	entries, err := ParseChangelog([]byte(sampleChangelog))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2 (a heading inside a code block is not a release)", len(entries))
	}
	return entries[0], entries[1]
}

func TestChangelogMetadataSetsNoticeFieldsAndMigration(t *testing.T) {
	server, _ := parseSample(t)
	if server.Type != NoticeEmergency || !server.Pinned || server.Days != 7 {
		t.Errorf("server metadata = %+v", server)
	}
	if server.Migration != MigrationIrreversible || !server.Backup {
		t.Errorf("an irreversible migration must require a backup: %+v", server)
	}
	if want := time.Date(2026, 10, 20, 0, 0, 0, 0, ChangelogZone); !server.StartsAt().Equal(want) || !server.EndsAt().Equal(want.AddDate(0, 0, 7)) {
		t.Errorf("notice window = %v..%v", server.StartsAt(), server.EndsAt())
	}
	if server.NoticeID() <= 0 {
		t.Errorf("notice ID %d must be positive", server.NoticeID())
	}
}

func TestChangelogKeepsEachLanguageSeparately(t *testing.T) {
	server, client := parseSample(t)
	titles := server.Titles()
	if titles["en_us"] != "Save layout change" || titles["zh_cn"] != "存档结构调整" {
		t.Errorf("titles = %v", titles)
	}
	if notes := server.Notes(); !strings.Contains(notes["zh_cn"], "公会") || strings.Contains(notes["en_us"], "公会") {
		t.Errorf("notes mixed languages: %v", notes)
	}
	if _, ok := client.Text["zh_cn"]; ok || client.Titles()["en_us"] == "" {
		t.Errorf("an untitled English-only section must get a default English title and no other language: %+v", client.Text)
	}
	if server.NoticeID() == client.NoticeID() {
		t.Error("two releases share a notice ID")
	}
}

func TestChangelogRequiresTheEnglishFallback(t *testing.T) {
	raw := "## server 2.36.7+server.0.3.0 · 2026-10-20\n\n### zh_cn: 仅中文\n\n- 内容\n"
	if _, err := ParseChangelog([]byte(raw)); err == nil || !strings.Contains(err.Error(), "en_us") {
		t.Fatalf("a section without en_us = %v; want an error naming en_us", err)
	}
	raw = "## server 2.36.7+server.0.3.0 · 2026-10-20\n\n### en_us: Update\n\n- Valid fallback\n\n### de_de: Nur Deutsch\n\n- Inhalt\n"
	if _, err := ParseChangelog([]byte(raw)); err == nil || !strings.Contains(err.Error(), "de_de") {
		t.Fatal("a language the game does not support was accepted")
	}
}

func TestChangelogBodyBecomesRichTextAndImageBlocks(t *testing.T) {
	server, _ := parseSample(t)
	var text, images []string
	for _, block := range server.Blocks("en_us") {
		if block.Image {
			images = append(images, block.Value)
		} else {
			text = append(text, block.Value)
		}
	}
	if len(images) != 1 || images[0] != "https://example.com/a.png" {
		t.Errorf("image blocks = %q; want the standalone image URL", images)
	}
	joined := strings.Join(text, "\n")
	for _, raw := range []string{"**", "](", "###", "![", "\n- "} {
		if strings.Contains(joined, raw) {
			t.Errorf("text blocks still contain markdown %q: %q", raw, joined)
		}
	}
	for _, want := range []string{"<b>Changed</b>", "<b>Guild</b>", "notes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("text blocks lack %q: %q", want, joined)
		}
	}
}

func TestChangelogRejectsAnUnfilledTemplate(t *testing.T) {
	template := ChangelogTemplate(ComponentServer, "2.36.7+server.0.3.0", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	_, err := ParseChangelog([]byte(template))
	if err == nil || !strings.Contains(err.Error(), "empty template item") {
		t.Fatalf("unfilled template error = %v; want the empty template item error", err)
	}
	placeholder := regexp.MustCompile(`(?m)^- $`)
	if !placeholder.MatchString(template) {
		t.Fatal("test setup: the template has no placeholder items")
	}
	if _, err := ParseChangelog([]byte(placeholder.ReplaceAllString(template, "- filled"))); err != nil {
		t.Fatalf("filled template: %v", err)
	}
}

// Official notices are shown only when the release key signed exactly the
// changelog served, so a forged or edited changelog is not published.
func TestChangelogSignatureBindsTheExactContent(t *testing.T) {
	seed, key, err := testSigner()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signChangelog([]byte(sampleChangelog), seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChangelog([]byte(sampleChangelog), signature, []TrustedKey{key}); err != nil {
		t.Fatalf("genuine changelog rejected: %v", err)
	}
	forged := strings.Replace(sampleChangelog, "Save layout change", "Free diamonds", 1)
	if err := VerifyChangelog([]byte(forged), signature, []TrustedKey{key}); err == nil {
		t.Fatal("an edited changelog was accepted with the original signature")
	}
	otherSeed, _, err := testSigner()
	if err != nil {
		t.Fatal(err)
	}
	selfSigned, err := signChangelog([]byte(forged), otherSeed)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChangelog([]byte(forged), selfSigned, []TrustedKey{key}); err == nil {
		t.Fatal("a changelog signed by an untrusted key was accepted")
	}

	manifest, err := signManifest(testManifest(), seed)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(manifest, &envelope); err != nil {
		t.Fatal(err)
	}
	reused, _ := json.Marshal(Detached{KeyID: envelope.KeyID, Signature: envelope.Signature})
	if err := VerifyChangelog(envelope.Manifest, reused, []TrustedKey{key}); err == nil {
		t.Fatal("a manifest signature was accepted as a changelog signature")
	}
}

// The repository's CHANGELOG.md is served to every server as notices, so it
// must always parse.
func TestRepositoryChangelogParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseChangelog(raw); err != nil {
		t.Fatal(err)
	}
}

func testSigner() (testSigningFunc, TrustedKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, TrustedKey{}, err
	}
	key := TrustedKey{ID: KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}
	return func(message []byte) (Detached, error) {
		return Detached{KeyID: key.ID, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, message))}, nil
	}, key, nil
}

func TestSignedChangelogFallsBackWhenTheFirstMirrorWasTamperedWith(t *testing.T) {
	signer, key, err := testSigner()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signChangelog([]byte(sampleChangelog), signer)
	if err != nil {
		t.Fatal(err)
	}
	var fallbackReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad.md":
			_, _ = w.Write([]byte(strings.Replace(sampleChangelog, "Save layout change", "Forged notice", 1)))
		case "/good.md":
			fallbackReads++
			_, _ = w.Write([]byte(sampleChangelog))
		case "/bad.md.sig", "/good.md.sig":
			_, _ = w.Write(signature)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	raw, _, source, err := FetchSigned(context.Background(), server.Client(), []string{server.URL + "/bad.md", server.URL + "/good.md"}, []TrustedKey{key})
	if err != nil || source != server.URL+"/good.md" || string(raw) != sampleChangelog || fallbackReads != 1 {
		t.Fatalf("a forged first mirror prevented the authentic fallback: source=%q fallback_reads=%d error=%v", source, fallbackReads, err)
	}
}

type testSigningFunc func([]byte) (Detached, error)

func signManifest(manifest Manifest, signer testSigningFunc) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(manifest, "  ", "  ")
	if err != nil {
		return nil, err
	}
	signature, err := signer(append([]byte(signingDomain), payload...))
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(Envelope{KeyID: signature.KeyID, Signature: signature.Signature, Manifest: payload}, "", "  ")
}

func signChangelog(raw []byte, signer testSigningFunc) ([]byte, error) {
	signature, err := signer(append([]byte(changelogDomain), raw...))
	if err != nil {
		return nil, err
	}
	return json.Marshal(signature)
}
