package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestGameProxyEnvironmentAuthoritative(t *testing.T) {
	input := []string{"PATH=kept", "UNITY_PROXYSERVER=http://stale:8080", "uNiTy_NoPrOxY=*", "http_proxy=http://stale:8080", "HTTPS_PROXY=http://stale:8080", "All_Proxy=http://stale:8080", "NO_PROXY=*", "bd2_client_proxy_url=http://stale:8080"}
	original := append([]string(nil), input...)
	for _, proxy := range []string{"", "http://127.0.0.1:12451"} {
		got := gameProxyEnvironment(input, proxy)
		if !reflect.DeepEqual(got, gameProxyEnvironment(got, proxy)) {
			t.Fatal("environment is not idempotent")
		}
		if got[0] != "PATH=kept" || !reflect.DeepEqual(input, original) {
			t.Fatal("unrelated environment or input changed")
		}
		seen := map[string]string{}
		for _, entry := range got[1:] {
			key, value, _ := strings.Cut(entry, "=")
			if !isGameProxyEnvironmentKey(key) || strings.Contains(value, "stale") {
				t.Fatal("stale proxy survived")
			}
			seen[key] = value
		}
		for _, key := range []string{"UNITY_PROXYSERVER", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "BD2_CLIENT_PROXY_URL"} {
			if value, ok := seen[key]; !ok || value != proxy {
				t.Errorf("missing authoritative %s override", key)
			}
		}
		for _, key := range []string{"UNITY_NOPROXY", "NO_PROXY", "no_proxy"} {
			if seen[key] != "localhost,127.0.0.1,::1" {
				t.Errorf("loopback bypass missing for %s", key)
			}
		}
	}
}

func TestGameProxyEnvironmentKeys(t *testing.T) {
	for _, key := range []string{"UNITY_PROXYSERVER", "unity_noproxy", "http_proxy", "HTTPS_PROXY", "All_Proxy", "NO_PROXY", "bd2_client_proxy_url"} {
		if !isGameProxyEnvironmentKey(key) {
			t.Errorf("proxy key %q not recognized", key)
		}
	}
	for _, key := range []string{"PATH", "SECRET", "HTTP_PROXY_PASSWORD", "NO_PROXY_EXTRA"} {
		if isGameProxyEnvironmentKey(key) {
			t.Errorf("unrelated key %q recognized", key)
		}
	}
}

func TestGameOpenArgumentsOverridesLaunchServicesProxy(t *testing.T) {
	args := gameOpenArguments("/Applications/BrownDust II.app", []string{"HTTP_PROXY=http://stale:80", "PATH=private"}, "")
	overrides := map[string]bool{}
	for index := 1; index < len(args) && args[index] != "--args"; index += 2 {
		if args[index] != "--env" {
			t.Fatal("missing explicit LaunchServices environment flag")
		}
		overrides[args[index+1]] = true
	}
	for _, entry := range []string{"HTTP_PROXY=", "http_proxy=", "HTTPS_PROXY=", "ALL_PROXY=", "UNITY_PROXYSERVER=", "BD2_CLIENT_PROXY_URL=", "UNITY_NOPROXY=localhost,127.0.0.1,::1"} {
		if !overrides[entry] {
			t.Errorf("LaunchServices override missing: %s", entry)
		}
	}
	if overrides["PATH=private"] || overrides["HTTP_PROXY=http://stale:80"] {
		t.Fatal("unrelated or stale environment forwarded")
	}
}
