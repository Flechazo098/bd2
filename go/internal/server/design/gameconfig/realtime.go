package gameconfig

import (
	"net"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

type RealtimeConfig struct {
	Enabled                  bool   `json:"enabled"`
	TCPListen                string `json:"tcp_listen"`
	UDPListen                string `json:"udp_listen"`
	PublicHost               string `json:"public_host"`
	TCPPort                  int    `json:"tcp_port"`
	UDPPort                  int    `json:"udp_port"`
	TLSCertFile              string `json:"tls_cert_file"`
	TLSKeyFile               string `json:"tls_key_file"`
	DevelopmentPlainLoopback bool   `json:"development_plain_loopback"`
}

// Port ranges are schema constraints; these rules cover dependent fields.
func realtimeRules(c RealtimeConfig, problems *commonconfig.Problems) {
	if !c.Enabled {
		return
	}
	if c.PublicHost == "" {
		problems.Add("realtime.public_host", commonconfig.RuleRequired, "is required")
	} else if strings.ContainsAny(c.PublicHost, "/\\@?# \t\r\n") {
		problems.Add("realtime.public_host", commonconfig.RuleFormat, "must be a bare host name without scheme, path or port")
	}
	for _, listener := range [...]struct{ path, address string }{{"realtime.tcp_listen", c.TCPListen}, {"realtime.udp_listen", c.UDPListen}} {
		path := listener.path
		host, port, err := net.SplitHostPort(listener.address)
		if err != nil || port == "" {
			problems.Add(path, commonconfig.RuleFormat, "must be host:port")
		} else if c.DevelopmentPlainLoopback && !net.ParseIP(host).IsLoopback() {
			problems.Add(path, commonconfig.RuleConflict, "plaintext realtime listeners must bind loopback")
		}
	}
	if c.DevelopmentPlainLoopback {
		if !net.ParseIP(c.PublicHost).IsLoopback() {
			problems.Add("realtime.public_host", commonconfig.RuleConflict, "plaintext realtime public host must be loopback")
		}
		return
	}
	if c.TLSCertFile == "" {
		problems.Add("realtime.tls_cert_file", commonconfig.RuleRequired, "is required unless plaintext loopback development is enabled")
	}
	if c.TLSKeyFile == "" {
		problems.Add("realtime.tls_key_file", commonconfig.RuleRequired, "is required unless plaintext loopback development is enabled")
	}
}
