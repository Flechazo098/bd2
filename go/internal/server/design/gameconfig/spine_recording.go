package gameconfig

import (
	"net"
	"net/url"
	"strings"

	commonconfig "bd2server/internal/common/config"
)

type SpineRecordingConfig struct {
	DownloadBaseURL string `json:"download_base_url"`
}

func spineRecordingRules(c SpineRecordingConfig, problems *commonconfig.Problems) {
	if c.DownloadBaseURL == "" {
		return
	}
	u, err := url.Parse(c.DownloadBaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(c.DownloadBaseURL, " \t\r\n") {
		problems.Add("spine_recording.download_base_url", commonconfig.RuleFormat, "must be an absolute HTTP(S) URL without credentials, query, or fragment")
		return
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		problems.Add("spine_recording.download_base_url", commonconfig.RuleFormat, "must use HTTPS outside loopback development")
	}
}
