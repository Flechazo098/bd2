package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestGameProxyEnvironmentPreservesExplicitAndIsIdempotent(t *testing.T) {
	input := []string{"PATH=kept", "UNITY_PROXYSERVER=http://explicit:8080", "UNITY_NOPROXY=example.org;localhost"}
	got := gameProxyEnvironment(input, "http://system:8888")
	if !reflect.DeepEqual(got, gameProxyEnvironment(got, "http://other:9999")) {
		t.Fatal("environment is not idempotent")
	}
	if got[1] != input[1] || input[2] != "UNITY_NOPROXY=example.org;localhost" {
		t.Fatal("explicit environment changed or input mutated")
	}
	if !strings.Contains(got[2], "127.0.0.1") || !strings.Contains(got[2], "::1") {
		t.Fatal("loopback bypass missing")
	}
}

func TestSharedWindowsProxy(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"127.0.0.1:12451", "http://127.0.0.1:12451"},
		{"http=proxy:8080;https=proxy:8080", "http://proxy:8080"},
		{"http=proxy:8080;https=other:8080", ""},
		{"https=proxy:8080", ""}, {"user:password@proxy:8080", ""}, {"proxy:99999", ""},
	} {
		if got := sharedWindowsProxy(test.input); got != test.want {
			t.Errorf("proxy configuration result mismatch")
		}
	}
}

func TestGameProxyEnvironmentIgnoresInvalidSystemProxy(t *testing.T) {
	for _, proxy := range []string{"", "https://proxy:443", "http://user:password@proxy:8080", "http://proxy:8080/path"} {
		for _, entry := range gameProxyEnvironment(nil, proxy) {
			if strings.HasPrefix(entry, "UNITY_PROXYSERVER=") {
				t.Fatal("invalid system proxy accepted")
			}
		}
	}
}
