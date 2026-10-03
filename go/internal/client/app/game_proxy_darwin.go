//go:build darwin

package app

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

func systemGameProxy() string {
	output, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return ""
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " : ")
		if found {
			values[key] = strings.TrimSpace(value)
		}
	}
	if values["ProxyAutoConfigEnable"] == "1" || values["ProxyAutoDiscoveryEnable"] == "1" ||
		values["HTTPEnable"] != "1" || values["HTTPSEnable"] != "1" ||
		values["HTTPProxy"] != values["HTTPSProxy"] || values["HTTPPort"] != values["HTTPSPort"] {
		return ""
	}
	port, err := strconv.Atoi(values["HTTPPort"])
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	proxy := "http://" + net.JoinHostPort(values["HTTPProxy"], strconv.Itoa(port))
	if validUnityProxy(proxy) {
		return proxy
	}
	return ""
}
