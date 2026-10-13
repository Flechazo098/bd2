package sourcetools_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"bd2server/build/sourcetools"
)

type entry struct{ Name, Kind, Diff, SourceDiff, Reason string }

func (e *entry) UnmarshalJSON(b []byte) error {
	type wire struct {
		Name, Kind, Diff, Reason string
		SourceDiff               string `json:"source_diff"`
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = entry{w.Name, w.Kind, w.Diff, w.SourceDiff, w.Reason}
	return nil
}

type result struct {
	Path, Status, Error    string
	OldPath                *string `json:"old_path"`
	NewPath                *string `json:"new_path"`
	Members, Noise, Review []entry
}
type report struct {
	Counts       map[string]int
	Files        []result
	Warnings     []string
	RenamedPaths int `json:"renamed_paths"`
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(text), 0644); e != nil {
		t.Fatal(e)
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func loadReport(t *testing.T, output string) report {
	t.Helper()
	var r report
	if e := json.Unmarshal([]byte(read(t, strings.TrimSuffix(output, ".txt")+".json")), &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func compare(t *testing.T, old, new string, extra ...string) result {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "Game.cs"), old)
	write(t, filepath.Join(b, "Game.cs"), new)
	output := filepath.Join(dir, "report.txt")
	args := append([]string{"--a", a, "--b", b, "--out", output, "--context", "50", "--workers", "1"}, extra...)
	if e := sourcetools.RunDiff(args, io.Discard); e != nil {
		t.Fatal(e)
	}
	r := loadReport(t, output)
	if len(r.Files) != 1 {
		t.Fatalf("expected one file: %+v", r)
	}
	return r.Files[0]
}
func entryDiff(entries []entry) string {
	var out strings.Builder
	for _, e := range entries {
		out.WriteString(e.Diff)
		out.WriteString(e.SourceDiff)
	}
	return out.String()
}

func TestLocalRenamesRemainReviewableAndStrictDiffKeepsNames(t *testing.T) {
	old := `class Inventory { int Count(int limit = 3) { int count = Read(); { int subtotal = Read(); Use(subtotal); } return count + 1; } int Price() { int amount = Read(); return amount; } }`
	new := `class Inventory { int Count(int limit = 3) { int value = Read(); { int part = Read(); Use(part); } return value + 1; } int Price() { int sum = Read(); return sum; } }`
	r := compare(t, old, new)
	if r.Status != "noise" || len(r.Noise) != 2 || len(r.Members) != 0 {
		t.Fatalf("renames should retain both method diffs as noise: %+v", r)
	}
	diff := entryDiff(r.Noise)
	if !strings.Contains(diff, "count") || !strings.Contains(diff, "subtotal") || !strings.Contains(diff, "part") || !strings.Contains(diff, "sum") {
		t.Fatal("review diff lost original names: " + diff)
	}
	strict := compare(t, old, new, "--strict")
	if strict.Status != "modified" || len(strict.Members) != 2 || len(strict.Noise) != 0 {
		t.Fatalf("strict report should expose both renames: %+v", strict)
	}
}
func TestLoopBindingExpiresBeforeFollowingFieldRead(t *testing.T) {
	old := `class C { int count; void Run() { for (int count = 0; count < 2; count++) { Use(count); } Use(count); } }`
	equivalent := `class C { int count; void Run() { for (int index = 0; index < 2; index++) { Use(index); } Use(count); } }`
	r := compare(t, old, equivalent)
	if r.Status != "noise" {
		t.Fatalf("loop-local rename should not rewrite the later field read: %+v", r)
	}
	changed := `class C { int count; void Run() { for (int index = 0; index < 2; index++) { Use(index); } Use(index); } }`
	r = compare(t, old, changed)
	if r.Status != "modified" || !strings.Contains(entryDiff(r.Members), "Use(count)") || !strings.Contains(entryDiff(r.Members), "Use(index)") {
		t.Fatalf("out-of-scope references must stay visible: %+v", r)
	}
}
func TestBusinessValuesTargetsAndStatementOrderRemainVisible(t *testing.T) {
	old := `class Checkout { static int first = Next(); static int second = Next(); int Run() { int total = Price(); Charge(total); Audit(total); return (a + b) * c; } }`
	new := `class Checkout { static int second = Next(); static int first = Next(); int Run() { int value = Price(); Log(value); Refund(value); return a + b * c; } }`
	r := compare(t, old, new)
	diff := entryDiff(r.Members)
	if r.Status != "modified" || !strings.Contains(diff, "Charge(local1)") || !strings.Contains(diff, "Refund(local1)") || !strings.Contains(diff, "(a + b) * c") || !strings.Contains(diff, "a + b * c") {
		t.Fatalf("business changes were hidden: %+v", r)
	}
	foundOrder := false
	for _, e := range r.Members {
		foundOrder = foundOrder || e.Kind == "order"
	}
	if !foundOrder {
		t.Fatal("field initialization order must be a main difference")
	}
	// The statements alone still differ when their order changes.
	r = compare(t, `class C { void Run() { Charge(); Audit(); } }`, `class C { void Run() { Audit(); Charge(); } }`)
	if r.Status != "modified" {
		t.Fatalf("statement order was normalized away: %+v", r)
	}
}
func TestGenericArgumentsAndLiteralPayloadSurviveNormalization(t *testing.T) {
	old := `class C { Dictionary<int,string> data; void Run() { var items = new NS.List<int>(); Foo<int>(items); Save(@"//keep ""global::__from_u0041"" /*old*/"); } }`
	new := `class C { Dictionary<int,int> data; void Run() { var list = new NS.List<string>(); Foo<string>(list); Save(@"//keep ""global::__from_u0042"" /*new*/"); } }`
	r := compare(t, old, new)
	diff := entryDiff(r.Members)
	if r.Status != "modified" || !strings.Contains(diff, "Dictionary < int, string >") || !strings.Contains(diff, "new NS.List < int >()") || !strings.Contains(diff, "new NS.List < string >()") || !strings.Contains(diff, "Foo < int >") || !strings.Contains(diff, "Foo < string >") || !strings.Contains(diff, `global::__from_u0041"" /*old*/`) || !strings.Contains(diff, `global::__from_u0042"" /*new*/`) {
		t.Fatalf("type/literal changes should remain in main diff: %+v", r)
	}
	inferred := compare(t, `class C { void Run() { Foo(x); } }`, `class C { void Run() { Foo<int>(x); } }`)
	if inferred.Status != "noise" || !strings.Contains(entryDiff(inferred.Noise), "int") {
		t.Fatalf("inference difference should remain reviewable: %+v", inferred)
	}
}
func TestObservableNamesAndAmbiguousBindingsAreNeverDiscarded(t *testing.T) {
	r := compare(t, `class C { string Run() { int count = 0; return nameof(count); } }`, `class C { string Run() { int value = 0; return nameof(value); } }`)
	if r.Status != "modified" || !strings.Contains(entryDiff(r.Members), "nameof(count)") || !strings.Contains(entryDiff(r.Members), "nameof(value)") {
		t.Fatalf("nameof changes observable strings: %+v", r)
	}
	r = compare(t, `class C { void Run() { int num = 1; double num = 2; Use(num); } }`, `class C { void Run() { int first = 1; double second = 2; Use(second); } }`)
	if r.Status != "review" || len(r.Review) != 1 || r.Review[0].Reason == "" || !strings.Contains(r.Review[0].SourceDiff, "num") || !strings.Contains(r.Review[0].SourceDiff, "second") {
		t.Fatalf("ambiguous binding needs the source diff: %+v", r)
	}
}
func TestAsyncStructureKeepsBusinessBodiesInReview(t *testing.T) {
	old := `class C { Task Run() { Run_d__1 state; state.t__builder = Create(); return state.t__builder.Task; } struct Run_d__1 { AsyncTaskMethodBuilder t__builder; void MoveNext() { ChargeAccount(10); AuditTransaction(10); } } int Price() { return 100; } }`
	new := `class C { async Task Run() { await ChargeAccount(20); AuditTransaction(20); } int Price() { return 200; } }`
	r := compare(t, old, new)
	diff := entryDiff(r.Review)
	if r.Status != "modified" || len(r.Review) == 0 || len(r.Members) != 1 || !strings.Contains(r.Members[0].Name, "Price") || !strings.Contains(diff, "ChargeAccount(10)") || !strings.Contains(diff, "ChargeAccount(20)") || !strings.Contains(diff, "AuditTransaction(10)") || !strings.Contains(diff, "AuditTransaction(20)") {
		t.Fatalf("async body or independent business change lost: %+v", r)
	}
}
func TestCommentsAreIgnoredWhileStringsAndLambdaTargetsArePreserved(t *testing.T) {
	r := compare(t, "class C { global::System.Int32 value__from_u0041; // old\n }", "/* new */ class C\n { System.Int32 value; }")
	if r.Status != "unchanged" {
		t.Fatalf("format/suffix cleanup should be exact: %+v", r)
	}
	r = compare(t, `class C { void Run() { callbacks.b__12_0(); Save("name__from_u0041"); } }`, `class C { void Run() { callbacks.b__13_1(); Save("name__from_u0042"); } }`)
	diff := entryDiff(r.Members)
	if r.Status != "modified" || !strings.Contains(diff, "b__N_0") || !strings.Contains(diff, "b__N_1") || !strings.Contains(diff, `"name__from_u0041"`) || !strings.Contains(diff, `"name__from_u0042"`) {
		t.Fatalf("lambda target/literal changes must remain visible: %+v", r)
	}
}
func TestCollisionPairingPreservesDistinctTypesAndCallTargets(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "Message__from_u0041.cs"), `class Message__from_u0041 { int Count = 1; static void Build() {} }`)
	write(t, filepath.Join(a, "Message__from_u0042.cs"), `class Message__from_u0042 { string Label; static void Build() {} }`)
	write(t, filepath.Join(b, "Message__from_u0043.cs"), `class Message__from_u0043 { string Label; static void Build() {} }`)
	write(t, filepath.Join(b, "Message__from_u0044.cs"), `class Message__from_u0044 { int Count = 2; static void Build() {} }`)
	write(t, filepath.Join(a, "Use.cs"), `class Use { void Run() { Message__from_u0041.Build(); } }`)
	write(t, filepath.Join(b, "Use.cs"), `class Use { void Run() { Message__from_u0044.Build(); } }`)
	output := filepath.Join(dir, "report.txt")
	args := []string{"--a", a, "--b", b, "--out", output, "--workers", "1"}
	if e := sourcetools.RunDiff(args, io.Discard); e != nil {
		t.Fatal(e)
	}
	r := loadReport(t, output)
	pairs := map[string]string{}
	for _, f := range r.Files {
		if f.OldPath == nil || f.NewPath == nil {
			t.Fatalf("unique types should pair: %+v", f)
		}
		pairs[*f.OldPath] = *f.NewPath
		if *f.OldPath == "Use.cs" && f.Status != "unchanged" {
			t.Fatalf("reference to the same type should be unchanged: %+v", f)
		}
	}
	if pairs["Message__from_u0041.cs"] != "Message__from_u0044.cs" || pairs["Message__from_u0042.cs"] != "Message__from_u0043.cs" || r.Counts["modified"] != 1 {
		t.Fatalf("pairing changed type identity: %+v", r)
	}
	write(t, filepath.Join(b, "Use.cs"), `class Use { void Run() { Message__from_u0043.Build(); } }`)
	if e := sourcetools.RunDiff(args, io.Discard); e != nil {
		t.Fatal(e)
	}
	r = loadReport(t, output)
	if r.Counts["modified"] != 2 {
		t.Fatalf("change to a distinct same-named type was hidden: %+v", r)
	}
}
func TestAmbiguousCollisionReportsEveryFileSeparately(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "Message__from_u0041.cs"), `class Message__from_u0041 { int value; }`)
	write(t, filepath.Join(a, "Message__from_u0042.cs"), `class Message__from_u0042 { int value; }`)
	write(t, filepath.Join(b, "Message__from_u0043.cs"), `class Message__from_u0043 { int value; }`)
	write(t, filepath.Join(b, "Message__from_u0044.cs"), `class Message__from_u0044 { int value; }`)
	output := filepath.Join(dir, "report.txt")
	if e := sourcetools.RunDiff([]string{"--a", a, "--b", b, "--out", output}, io.Discard); e != nil {
		t.Fatal(e)
	}
	r := loadReport(t, output)
	if len(r.Files) != 4 || r.Counts["added"] != 2 || r.Counts["removed"] != 2 || len(r.Warnings) == 0 {
		t.Fatalf("ambiguous collision discarded files: %+v", r)
	}
	seen := map[string]bool{}
	for _, f := range r.Files {
		if seen[f.Path] || f.OldPath != nil && f.NewPath != nil {
			t.Fatalf("files must retain distinct unpaired identities: %+v", f)
		}
		seen[f.Path] = true
	}
}
func TestWorkersReportsFilteringErrorsAndHTMLPayload(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "Proto/Net/Changed.cs"), `class C { string x = "</script><script>alert(1)</script>"; }`)
	write(t, filepath.Join(b, "Proto/Net/Changed.cs"), `class C { string x = "safe"; }`)
	write(t, filepath.Join(a, "Proto/Net/Bad.cs"), string([]byte{0xff}))
	write(t, filepath.Join(b, "Proto/Net/Bad.cs"), string([]byte{0xff}))
	write(t, filepath.Join(a, "Skip.cs"), `class A {}`)
	write(t, filepath.Join(b, "Skip.cs"), `class B {}`)
	write(t, filepath.Join(b, "Proto/Net/Empty.cs"), "")
	output := filepath.Join(dir, "report.txt")
	args := []string{"--a", a, "--b", b, "--out", output, "--include", "Proto/Net/*", "--exclude", "*Ignore*", "--workers", "1"}
	if e := sourcetools.RunDiff(args, io.Discard); e == nil {
		t.Fatal("invalid UTF-8 must fail even when bytes match")
	}
	serial := loadReport(t, output)
	if serial.Counts["modified"] != 1 || serial.Counts["error"] != 1 || serial.Counts["added"] != 1 || len(serial.Files) != 3 {
		t.Fatalf("filter/error contract failed: %+v", serial)
	}
	args[len(args)-1] = "2"
	if e := sourcetools.RunDiff(args, io.Discard); e == nil {
		t.Fatal("parallel run must report the same decoding failure")
	}
	parallel := loadReport(t, output)
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("worker count changed report content: serial=%+v parallel=%+v", serial, parallel)
	}
	stem := strings.TrimSuffix(output, ".txt")
	for _, suffix := range []string{".txt", ".json", ".html", ".diff", ".noise.diff", ".review.diff"} {
		if _, e := os.Stat(stem + suffix); e != nil {
			t.Fatal(e)
		}
	}
	html := read(t, stem+".html")
	if strings.Contains(html, "</script><script>alert(1)</script>") || !strings.Contains(html, `\u003c/script\u003e`) || !strings.Contains(html, "source_diff") {
		t.Fatal("HTML must safely retain the actual source diffs")
	}
}
func TestReportOutputRefusesSourceAndUnownedFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, filepath.Join(a, "C.cs"), `class C {}`)
	write(t, filepath.Join(b, "C.cs"), `class C {}`)
	protected := filepath.Join(dir, "report.diff")
	write(t, protected, "do not overwrite")
	if e := sourcetools.RunDiff([]string{"--a", a, "--b", b, "--out", filepath.Join(dir, "report.txt")}, io.Discard); e == nil {
		t.Fatal("non-managed companion report must not be overwritten")
	}
	if read(t, protected) != "do not overwrite" {
		t.Fatal("protected file was modified")
	}
	if e := sourcetools.RunDiff([]string{"--a", a, "--b", b, "--out", filepath.Join(a, "report.txt")}, io.Discard); e == nil {
		t.Fatal("report inside source must be rejected")
	}
	var help bytes.Buffer
	if e := sourcetools.RunDiff([]string{"--help"}, &help); e != nil || !strings.Contains(help.String(), "include") {
		t.Fatalf("--help should work without input/config: %v %s", e, help.String())
	}
}
func TestDeobfuscationPreservesPayloadEncodingSourcesAndMappingAmbiguity(t *testing.T) {
	dir := t.TempDir()
	source, output, mapping := filepath.Join(dir, "source"), filepath.Join(dir, "mirror"), filepath.Join(dir, "map.obfuscate")
	original := `class Ω { Ω Ω2; Π Π3; Ж unresolved; string value = "Ω Π Ж"; string raw = """Ω // Π"""; // Ω Π Ж
/* Ω Π Ж */ }`
	write(t, filepath.Join(source, "Ω.cs"), original)
	write(t, filepath.Join(source, "Π.cs"), `class Π {}`)
	write(t, filepath.Join(source, "Player.cs"), `class Existing {}`)
	write(t, filepath.Join(source, "bytes.bin"), string([]byte{1, 2, 3}))
	write(t, mapping, "\ufeff#ReverseOrder\nΩ⇨Net.Player\nΠ⇨Player\nЖ⇨One\nЖ⇨Two\ninvalid line\n")
	// A UTF-16 input retains its byte order marker after translating tokens.
	words := utf16.Encode([]rune(`class Ψ { Ω value; }`))
	encoded := make([]byte, 2+len(words)*2)
	encoded[0], encoded[1] = 0xfe, 0xff
	for i, w := range words {
		binary.BigEndian.PutUint16(encoded[2+i*2:], w)
	}
	if e := os.WriteFile(filepath.Join(source, "Utf16.cs"), encoded, 0644); e != nil {
		t.Fatal(e)
	}
	args := []string{"--source", source, "--mapping", mapping, "--output", output}
	if e := sourcetools.RunDeobfuscate(args, io.Discard); e != nil {
		t.Fatal(e)
	}
	translated := read(t, filepath.Join(output, "Player__file_2.cs"))
	if !strings.Contains(translated, "class Player") || !strings.Contains(translated, "Player Player2") || !strings.Contains(translated, "Player__from_u03A0 Player__from_u03A03") || !strings.Contains(translated, "Ж unresolved") || !strings.Contains(translated, `"Ω Π Ж"`) || !strings.Contains(translated, `"""Ω // Π"""`) || !strings.Contains(translated, "// Ω Π Ж") {
		t.Fatal("mapping or literal semantics lost: " + translated)
	}
	if read(t, filepath.Join(source, "Ω.cs")) != original {
		t.Fatal("source modified")
	}
	if read(t, filepath.Join(output, "bytes.bin")) != string([]byte{1, 2, 3}) {
		t.Fatal("non-code copy changed")
	}
	actual, e := os.ReadFile(filepath.Join(output, "Utf16.cs"))
	if e != nil {
		t.Fatal(e)
	}
	if len(actual) < 2 || actual[0] != 0xfe || actual[1] != 0xff {
		t.Fatal("UTF-16 BOM lost")
	}
	var manifest struct {
		Warnings   []string
		Files      []struct{ Source, Output string }
		Statistics map[string]int
	}
	if e = json.Unmarshal([]byte(read(t, filepath.Join(output, ".bd2-deobfuscate-manifest.json"))), &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 5 || len(manifest.Warnings) < 2 || manifest.Statistics["derived_identifier_replacements"] != 2 {
		t.Fatalf("manifest must expose ambiguity and derived token replacements: %+v", manifest)
	}
	write(t, filepath.Join(source, "Ω.cs"), `class Ω { int newField; }`)
	if e = sourcetools.RunDeobfuscate(args, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(read(t, filepath.Join(output, "Player__file_2.cs")), "newField") {
		t.Fatal("managed mirror was not republished")
	}
}
func TestDeobfuscationRefusesOverlappingAndUnownedOutput(t *testing.T) {
	dir := t.TempDir()
	source, mapping := filepath.Join(dir, "source"), filepath.Join(dir, "map.obfuscate")
	write(t, filepath.Join(source, "Ω.cs"), `class Ω {}`)
	write(t, mapping, "Ω⇨Player\n")
	unowned := filepath.Join(dir, "output")
	write(t, filepath.Join(unowned, "keep.txt"), "owned by user")
	args := []string{"--source", source, "--mapping", mapping, "--output", unowned}
	if e := sourcetools.RunDeobfuscate(args, io.Discard); e == nil {
		t.Fatal("unowned directory must be protected")
	}
	if read(t, filepath.Join(unowned, "keep.txt")) != "owned by user" {
		t.Fatal("unowned output changed")
	}
	args[len(args)-1] = filepath.Join(source, "mirror")
	if e := sourcetools.RunDeobfuscate(args, io.Discard); e == nil {
		t.Fatal("output nested in input must be rejected")
	}
	args[len(args)-1] = dir
	if e := sourcetools.RunDeobfuscate(args, io.Discard); e == nil {
		t.Fatal("output containing source must be rejected")
	}
	var help bytes.Buffer
	if e := sourcetools.RunDeobfuscate([]string{"--help"}, &help); e != nil || !strings.Contains(help.String(), "mapping") {
		t.Fatalf("standalone help failed: %v %s", e, help.String())
	}
}
