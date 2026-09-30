package stateio

import "testing"

func TestRequireExactJSONObject(t *testing.T) {
	if err := RequireExactJSONObject([]byte(`{"version":1,"items":[]}`), "version", "items"); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"version":1}`,
		`{"version":1,"items":[],"legacy":true}`,
		`[]`,
	} {
		if err := RequireExactJSONObject([]byte(payload), "version", "items"); err == nil {
			t.Fatalf("accepted non-current layout %s", payload)
		}
	}
}
