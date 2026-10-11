package textmoderation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"bd2server/internal/server/design/gameconfig"
)

const maxResponseBytes = 64 << 10

type remoteProvider struct {
	client                                        *http.Client
	endpoint, kind, key, keyHeader, failurePolicy string
	scopes                                        map[string]bool
	threshold                                     int
	lastFailureLog                                atomic.Int64
}

func (p *remoteProvider) logFailure(scope string, started time.Time) {
	now := time.Now().UnixNano()
	previous := p.lastFailureLog.Load()
	if previous != 0 && now-previous < int64(10*time.Second) || !p.lastFailureLog.CompareAndSwap(previous, now) {
		return
	}
	slog.Warn("text moderation provider unavailable", "provider", p.kind, "scope", scope, "duration_ms", time.Since(started).Milliseconds(), "failure_policy", p.failurePolicy)
}

func newRemote(c gameconfig.TextModerationProviderConfig) (*remoteProvider, error) {
	if c.Type == "off" {
		return nil, nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("text moderation: invalid provider endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("text moderation: provider requires HTTPS or loopback HTTP")
	}
	if c.Type == "azure_content_safety" {
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			return nil, fmt.Errorf("text moderation: Azure endpoint must be a resource base URL")
		}
		u.Path = "/contentsafety/text:analyze"
		u.RawQuery = "api-version=2024-09-01"
	}
	p := &remoteProvider{kind: c.Type, endpoint: u.String(), failurePolicy: c.FailurePolicy, scopes: map[string]bool{}, threshold: c.SeverityThreshold}
	for _, scope := range c.Scopes {
		p.scopes[scope] = true
	}
	if c.APIKeyEnv != "" {
		p.key = os.Getenv(c.APIKeyEnv)
		if p.key == "" || strings.ContainsAny(p.key, "\r\n") {
			return nil, fmt.Errorf("text moderation: provider credential environment variable is missing or invalid")
		}
	}
	p.keyHeader = c.APIKeyHeader
	if p.kind == "azure_content_safety" {
		p.keyHeader = "Ocp-Apim-Subscription-Key"
	}
	if p.key != "" {
		if !validHeaderName(p.keyHeader) {
			return nil, fmt.Errorf("text moderation: invalid credential header")
		}
		if strings.EqualFold(p.keyHeader, "Authorization") {
			p.key = "Bearer " + p.key
		}
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: time.Duration(c.TimeoutMS) * time.Millisecond, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: time.Duration(c.TimeoutMS) * time.Millisecond, MaxResponseHeaderBytes: 16 << 10}
	p.client = &http.Client{Transport: transport, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return p, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)
		if !allowed {
			return false
		}
	}
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	return canonical != "Host" && canonical != "Content-Length" && canonical != "Content-Type" && canonical != "Connection" && canonical != "Transfer-Encoding"
}

func (p *remoteProvider) check(ctx context.Context, scope, accountID, text string) error {
	var payload []byte
	var err error
	if p.kind == "azure_content_safety" {
		payload, err = azureRequest(text)
	} else {
		payload, err = json.Marshal(struct {
			Scope     string `json:"scope"`
			AccountID string `json:"account_id"`
			Text      string `json:"text"`
		}{scope, accountID, text})
	}
	if err != nil {
		return ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if p.key != "" {
		request.Header.Set(p.keyHeader, p.key)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return ErrUnavailable
	}
	if p.kind == "azure_content_safety" {
		return azureResult(data, p.threshold)
	}
	var result struct {
		Decision string `json:"decision"`
	}
	if !decodeResponse(data, &result, true) {
		return ErrUnavailable
	}
	switch result.Decision {
	case "allow":
		return nil
	case "reject":
		return ErrRejected
	default:
		return ErrUnavailable
	}
}

func decodeResponse(data []byte, result any, strict bool) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if decoder.Decode(result) != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
