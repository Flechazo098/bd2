package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bd2server/internal/common/config"
)

type sample struct {
	Mode    string            `json:"mode"`
	Limit   int               `json:"limit"`
	Tags    []string          `json:"tags"`
	Targets map[string]target `json:"targets"`
}

type target struct {
	Enabled *bool `json:"enabled,omitempty"`
}

var sampleMin, sampleMax = config.Range(1, 10)

var sampleDocument = config.Document[sample]{
	Name:     "sample",
	FileName: "sample.json",
	Defaults: func() sample { return sample{Mode: "a", Limit: 1, Tags: []string{}, Targets: map[string]target{}} },
	Decode:   config.DecodeOptions{DisallowUnknownFields: true, RejectTrailing: true, RejectNull: true},
	Schema: config.Schema{Sections: []config.Section{{ID: "main", Fields: []config.Field{
		{Path: "mode", Kind: config.KindString, Options: []config.Option{{Value: "a"}, {Value: "b"}}},
		{Path: "limit", Kind: config.KindInteger, Min: sampleMin, Max: sampleMax, When: &config.Condition{Path: "mode", In: []any{"b"}}},
		{Path: "tags", Kind: config.KindList, Unique: true},
		{Path: "targets", Kind: config.KindMap, Keys: []config.Option{{Value: "x"}}},
		{Path: "targets.*.enabled", Kind: config.KindBool, Required: true},
	}}}},
	Rules: func(value sample) config.Problems {
		var problems config.Problems
		if value.Mode == "b" && len(value.Tags) == 0 {
			problems.Add("tags", config.RuleRequired, "is required in mode b")
		}
		return problems
	},
}

func problemPaths(t *testing.T, err error) map[string]string {
	t.Helper()
	paths := map[string]string{}
	for _, problem := range config.AsProblems(err) {
		paths[problem.Path] = problem.Rule
	}
	return paths
}

func TestDocumentLocatesSchemaAndRuleProblems(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      map[string]string
	}{
		{name: "valid defaults", raw: `{"mode":"a","limit":99,"tags":[],"targets":{}}`, want: map[string]string{}},
		{name: "enum", raw: `{"mode":"c","tags":[],"targets":{}}`, want: map[string]string{"mode": config.RuleEnum}},
		{name: "range only when condition holds", raw: `{"mode":"b","limit":99,"tags":["t"],"targets":{}}`, want: map[string]string{"limit": config.RuleRange}},
		{name: "unique list item", raw: `{"mode":"a","tags":["t","t"],"targets":{}}`, want: map[string]string{"tags[1]": config.RuleUnique}},
		{name: "unsupported map key", raw: `{"mode":"a","tags":[],"targets":{"y":{"enabled":true}}}`, want: map[string]string{"targets.y": config.RuleEnum}},
		{name: "required field absent from file", raw: `{"mode":"a","tags":[],"targets":{"x":{}}}`, want: map[string]string{"targets.x.enabled": config.RuleRequired}},
		{name: "cross-field rule", raw: `{"mode":"b","limit":2,"tags":[],"targets":{}}`, want: map[string]string{"tags": config.RuleRequired}},
		{name: "decode failure", raw: `{"mode":"a","unknown":1}`, want: map[string]string{"": config.RuleDecode}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := sampleDocument.Parse([]byte(test.raw))
			got := problemPaths(t, err)
			if len(got) != len(test.want) {
				t.Fatalf("problems %v, want %v", got, test.want)
			}
			for path, rule := range test.want {
				if got[path] != rule {
					t.Fatalf("problems %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestFileReplaceRequiresCurrentRevisionAndHonoursLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "sample.json")
	file, loaded, err := sampleDocument.Open(path)
	if err != nil || loaded.Mode != "a" {
		t.Fatalf("open published defaults: %+v, %v", loaded, err)
	}
	first, err := file.Read()
	if err != nil || first.Pending() || !first.Writable {
		t.Fatalf("fresh file: %+v, %v", first, err)
	}

	next := first.Value
	next.Limit = 5
	saved, err := file.Replace(next, first.Revision)
	if err != nil || saved.Value.Limit != 5 || !saved.Pending() {
		t.Fatalf("replace: %+v, %v", saved, err)
	}
	if _, err := file.Replace(first.Value, first.Revision); !errors.Is(err, config.ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}

	file.Lock(config.Lock{Path: "mode", Value: "a", Reason: "fixed"})
	locked := saved.Value
	locked.Mode = "b"
	locked.Tags = []string{"t"}
	if _, err := file.Replace(locked, saved.Revision); problemPaths(t, err)["mode"] != config.RuleConflict {
		t.Fatalf("locked value changed: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"mode":"zz"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := file.Read()
	if err != nil || problemPaths(t, broken.Problems)["mode"] != config.RuleEnum {
		t.Fatalf("externally broken file was not reported: %+v, %v", broken, err)
	}
	if _, err := file.Replace(loaded, saved.Revision); !errors.Is(err, config.ErrConflict) {
		t.Fatalf("external edit was overwritten: %v", err)
	}
}
