// Package protocol implements the game's authenticated HTTP wire framing.
// It has no dependency on captured traffic or account state.
package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bd2server/internal/cryptox"
)

type Envelope struct {
	ErrorType     int    `json:"errorType"`
	PacketCode    int    `json:"packetCode"`
	ErrorMessage  string `json:"errorMessage"`
	Length        int    `json:"length"`
	Data          string `json:"data"`
	ServerNowTime int64  `json:"serverNowTime"`
	Notify        string `json:"notify,omitempty"`
}

// Encode encrypts protobuf bytes using the active key and wraps them in the
// JSON envelope expected by game API responses.
func Encode(code int, proto, key []byte, now int64) ([]byte, error) {
	data, err := cryptox.EncryptBase64Payload(proto, key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		PacketCode: code, Length: base64.StdEncoding.EncodedLen(len(proto)),
		Data: data, ServerNowTime: now,
	})
}

type BatchRequest struct {
	Path        string `json:"path"`
	RequestData string `json:"requestData"`
}

type BatchResponse struct {
	Path         string   `json:"path"`
	ResponseData Envelope `json:"responseData"`
}

// DecodeBatchRequest decrypts the JSON array, preserving request order.
// Each member's requestData is base64(protobuf), not encrypted a second time.
func DecodeBatchRequest(body, key []byte) ([]BatchRequest, [][]byte, error) {
	plain, err := cryptox.DecryptBase64(strings.TrimSpace(string(body)), key)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt batch: %w", err)
	}
	var requests []BatchRequest
	if err := json.Unmarshal(plain, &requests); err != nil {
		return nil, nil, fmt.Errorf("decode batch JSON: %w", err)
	}
	if len(requests) == 0 || len(requests) > 256 {
		return nil, nil, errors.New("batch request has invalid item count")
	}
	decoded := make([][]byte, 0, len(requests))
	seen := make(map[string]bool, len(requests))
	for _, req := range requests {
		if len(req.Path) < 2 || req.Path[0] != '/' || strings.Contains(req.Path[1:], "/") || seen[req.Path] || req.RequestData == "" {
			return nil, nil, fmt.Errorf("invalid or duplicate batch path %q", req.Path)
		}
		seen[req.Path] = true
		proto, err := base64.StdEncoding.DecodeString(req.RequestData)
		if err != nil {
			return nil, nil, fmt.Errorf("decode %s request: %w", req.Path, err)
		}
		decoded = append(decoded, proto)
	}
	return requests, decoded, nil
}
