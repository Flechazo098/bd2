package util

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const responseReasonLimit = 300

// ResponseError describes an unexpected HTTP response together with the
// server's own explanation from its body, so the failure names its cause
// rather than only a status code. It reads at most a few KiB of the body.
func ResponseError(response *http.Response) error {
	status := response.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if reason := responseReason(raw); reason != "" {
		return fmt.Errorf("HTTP %s: %s", status, reason)
	}
	return fmt.Errorf("HTTP %s (the server gave no reason)", status)
}

func responseReason(raw []byte) string {
	var document struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	text := string(raw)
	if json.Unmarshal(raw, &document) == nil {
		text = document.Error
		if text == "" {
			text = document.Message
		}
	}
	if !utf8.ValidString(text) || strings.Contains(strings.ToLower(text), "<html") {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > responseReasonLimit {
		text = string([]rune(text)[:responseReasonLimit]) + "…"
	}
	return text
}
