package app

import "strings"

func isGameProxyEnvironmentKey(key string) bool {
	switch strings.ToLower(key) {
	case "unity_proxyserver", "unity_noproxy", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "bd2_client_proxy_url":
		return true
	default:
		return false
	}
}

// Player settings are authoritative; stale process and OS proxy values cannot win.
func gameProxyEnvironment(environment []string, proxyURL string) []string {
	result := make([]string, 0, len(environment)+12)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !isGameProxyEnvironmentKey(key) {
			result = append(result, entry)
		}
	}
	for _, key := range []string{"UNITY_PROXYSERVER", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "BD2_CLIENT_PROXY_URL"} {
		result = append(result, key+"="+proxyURL)
	}
	for _, key := range []string{"UNITY_NOPROXY", "NO_PROXY", "no_proxy"} {
		result = append(result, key+"=localhost,127.0.0.1,::1")
	}
	return result
}

// LaunchServices needs explicit empty values when the player selects direct.
func gameOpenArguments(target string, environment []string, proxyURL string) []string {
	args := []string{target}
	for _, entry := range gameProxyEnvironment(environment, proxyURL) {
		key, _, found := strings.Cut(entry, "=")
		if found && isGameProxyEnvironmentKey(key) {
			args = append(args, "--env", entry)
		}
	}
	args = append(args, "--args")
	return append(args, gameLaunchArguments()...)
}
