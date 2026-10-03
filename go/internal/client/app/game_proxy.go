package app

import (
	"net"
	"net/url"
	"strings"
)

// Unity reads these variables before managed plugins can run.
func gameProxyEnvironment(environment []string, systemProxy string) []string {
	result := append([]string(nil), environment...)
	lookup := func(name string) (int, string) {
		for index, entry := range result {
			key, value, found := strings.Cut(entry, "=")
			if found && strings.EqualFold(key, name) {
				return index, value
			}
		}
		return -1, ""
	}
	if index, _ := lookup("UNITY_PROXYSERVER"); index < 0 && validUnityProxy(systemProxy) {
		result = append(result, "UNITY_PROXYSERVER="+systemProxy)
	}
	index, bypass := lookup("UNITY_NOPROXY")
	parts := strings.FieldsFunc(bypass, func(character rune) bool { return character == ',' || character == ';' })
	for _, local := range []string{"localhost", "127.0.0.1", "::1"} {
		present := false
		for _, part := range parts {
			if strings.EqualFold(strings.TrimSpace(part), local) {
				present = true
			}
		}
		if !present {
			parts = append(parts, local)
		}
	}
	entry := "UNITY_NOPROXY=" + strings.Join(parts, ",")
	if index >= 0 {
		result[index] = entry
	} else {
		result = append(result, entry)
	}
	return result
}

func validUnityProxy(proxy string) bool {
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" || port == "" {
		return false
	}
	_, err = net.LookupPort("tcp", port)
	return err == nil
}

// Unity accepts one proxy, so per-scheme configurations must agree.
func sharedWindowsProxy(raw string) string {
	if !strings.Contains(raw, "=") {
		proxy := "http://" + strings.TrimSpace(raw)
		if validUnityProxy(proxy) {
			return proxy
		}
		return ""
	}
	var httpProxy, httpsProxy string
	for _, entry := range strings.Split(raw, ";") {
		key, value, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found {
			return ""
		}
		switch strings.ToLower(key) {
		case "http":
			httpProxy = value
		case "https":
			httpsProxy = value
		}
	}
	if httpProxy == "" || !strings.EqualFold(httpProxy, httpsProxy) {
		return ""
	}
	proxy := "http://" + httpProxy
	if validUnityProxy(proxy) {
		return proxy
	}
	return ""
}
