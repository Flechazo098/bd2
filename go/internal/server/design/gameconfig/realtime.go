package gameconfig

import (
	"errors"
	"net"
	"strings"
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

func (c RealtimeConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.PublicHost == "" || strings.ContainsAny(c.PublicHost, "/\\@?# \t\r\n") || c.TCPPort < 1 || c.TCPPort > 65535 || c.UDPPort < 1 || c.UDPPort > 65535 {
		return errors.New("realtime requires a public host and valid TCP/UDP ports")
	}
	for _, address := range []string{c.TCPListen, c.UDPListen} {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port == "" {
			return errors.New("realtime listener must be host:port")
		}
		if c.DevelopmentPlainLoopback && !net.ParseIP(host).IsLoopback() {
			return errors.New("plaintext realtime listeners must bind loopback")
		}
	}
	if c.DevelopmentPlainLoopback {
		if !net.ParseIP(c.PublicHost).IsLoopback() {
			return errors.New("plaintext realtime public host must be loopback")
		}
	} else if c.TLSCertFile == "" || c.TLSKeyFile == "" {
		return errors.New("realtime TLS certificate and private key paths are required")
	}
	return nil
}
