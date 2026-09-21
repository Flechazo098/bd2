// Package fixture reads captured BD2 protocol messages without duplicating the
// large capture in the source tree. It is intended for local-server bootstrap
// fixtures and protocol tests, not as a persistence format.
package fixture

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"bd2server/internal/cryptox"
)

const CaptureVersion = "2.34.13"

// NamedFixture names the small, stable bootstrap response set. Files remain
// in capture/<version>/<run>/bodies and are loaded on demand.
type NamedFixture struct {
	Name string
	Path string
	File string
	Key  KeyKind
}

type KeyKind uint8

const (
	Plain KeyKind = iota
	FixedLogin
	Session
)

var Bootstrap = []NamedFixture{
	{Name: "maintenance", Path: "/MaintenanceInfo", File: "00010_resp_MaintenanceInfo.bin", Key: Plain},
	{Name: "server_info", Path: "/ServerInfo", File: "00012_resp_ServerInfo.bin", Key: Plain},
	{Name: "login", Path: "/LoginUser", File: "00016_resp_LoginUser.bin", Key: FixedLogin},
	{Name: "notice", Path: "/NoticeInfo", File: "00020_resp_NoticeInfo.bin", Key: Plain},
}

var (
	ErrNotFound       = errors.New("bd2 fixture: response not found")
	ErrUnsafePath     = errors.New("bd2 fixture: unsafe capture path")
	ErrNoPayload      = errors.New("bd2 fixture: response has no data payload")
	ErrLengthMismatch = errors.New("bd2 fixture: response length does not match decoded payload")
)

// Capture points at a single timestamped capture directory, i.e.
// capture/2.34.13/20260920-003254.
type Capture struct {
	Root string
}

// Set is the local-server integration view of one captured new-player run.
// It intentionally keeps responses on disk and only loads bodies on demand;
// this avoids copying the capture into binaries or maintaining a second large
// fixture tree. Load accepts the timestamped capture directory itself.
type Set struct {
	*Capture
	core      map[string]NamedFixture
	responses map[string][]RecordedResponse
}

// RecordedResponse has an unambiguous selector: Path plus RequestSequence.
// Sequence is the one-based request event number in bd2_dump.jsonl, rather
// than an ordinal response count. This prevents a state-changing endpoint
// from accidentally receiving a response captured for another request.
type RecordedResponse struct {
	Path            string
	Method          string
	File            string
	RequestSequence int
	ClientSequence  uint64
}

// RecordedResponses lists the unambiguous capture selectors for path. The
// returned slice is detached from the fixture index and safe for callers to
// retain as a scripted bootstrap plan.
func (s *Set) RecordedResponses(path string) []RecordedResponse {
	return append([]RecordedResponse(nil), s.responses[path]...)
}

// Load opens an on-disk capture and indexes its stable core endpoints.
func Load(captureDir string) (*Set, error) {
	c, err := Open(captureDir)
	if err != nil {
		return nil, err
	}
	core := make(map[string]NamedFixture, len(Bootstrap))
	for _, spec := range Bootstrap {
		core[spec.Path] = spec
	}
	responses, err := indexResponses(c.Root)
	if err != nil {
		return nil, err
	}
	return &Set{Capture: c, core: core, responses: responses}, nil
}

type dumpRecord struct {
	Event   string   `json:"ev"`
	Game    bool     `json:"game"`
	FlowID  string   `json:"flow_id"`
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	RawFile string   `json:"raw_file"`
	PB      []string `json:"pb"`
}

type indexedRequest struct {
	CaptureSequence int
	ClientSequence  uint64
}

func indexResponses(root string) (map[string][]RecordedResponse, error) {
	data, err := os.ReadFile(filepath.Join(root, "bd2_dump.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("read capture index: %w", err)
	}
	requests := make(map[string]indexedRequest)
	responses := make(map[string][]RecordedResponse)
	sequence := 0
	for lineNo, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record dumpRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode capture index line %d: %w", lineNo+1, err)
		}
		if !record.Game || record.FlowID == "" {
			continue
		}
		if record.Event == "req" {
			sequence++
			requests[record.FlowID] = indexedRequest{CaptureSequence: sequence, ClientSequence: protobufSequence(record.PB)}
			continue
		}
		if record.Event != "resp" || record.RawFile == "" {
			continue
		}
		request, found := requests[record.FlowID]
		if !found {
			continue // capture can begin after an already-open HTTP flow
		}
		if err := safeName(filepath.Base(record.RawFile)); err != nil {
			return nil, fmt.Errorf("capture index line %d: %w", lineNo+1, err)
		}
		responses[record.Path] = append(responses[record.Path], RecordedResponse{
			Path: record.Path, Method: record.Method, File: filepath.Base(record.RawFile), RequestSequence: request.CaptureSequence, ClientSequence: request.ClientSequence,
		})
	}
	return responses, nil
}

func protobufSequence(fields []string) uint64 {
	const prefix = "#1 varint "
	for _, field := range fields {
		text := strings.TrimSpace(field)
		if !strings.HasPrefix(text, prefix) {
			continue
		}
		value, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(text, prefix)), 10, 64)
		if err == nil {
			return value
		}
	}
	return 0
}

// RecordedResponseForClientSequence resolves the captured response using the
// endpoint and the request message's protobuf seq field. A missing/zero seq
// never falls back to a path-only match.
func (s *Set) RecordedResponseForClientSequence(path string, clientSequence uint64) (RecordedResponse, error) {
	if clientSequence == 0 {
		return RecordedResponse{}, fmt.Errorf("%w: response %s has no client sequence", ErrNotFound, path)
	}
	for _, response := range s.responses[path] {
		if response.ClientSequence == clientSequence {
			return response, nil
		}
	}
	return RecordedResponse{}, fmt.Errorf("%w: response %s at client sequence %d", ErrNotFound, path, clientSequence)
}

func Open(root string) (*Capture, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("open capture: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("open capture: %s is not a directory", root)
	}
	if _, err := os.Stat(filepath.Join(root, "bodies")); err != nil {
		return nil, fmt.Errorf("open capture bodies: %w", err)
	}
	return &Capture{Root: root}, nil
}

// Response is a raw captured HTTP body along with its recorded endpoint.
type Response struct {
	Path string
	File string
	Body []byte
}

// BootstrapResponse loads one of the fixed bootstrap fixtures by symbolic name.
func (c *Capture) BootstrapResponse(name string) (Response, error) {
	for _, spec := range Bootstrap {
		if spec.Name == name {
			return c.readResponse(spec.Path, spec.File)
		}
	}
	return Response{}, fmt.Errorf("%w: bootstrap %q", ErrNotFound, name)
}

func (c *Capture) Maintenance() (Response, error) { return c.BootstrapResponse("maintenance") }
func (c *Capture) ServerInfo() (Response, error)  { return c.BootstrapResponse("server_info") }
func (c *Capture) Login() (Response, error)       { return c.BootstrapResponse("login") }
func (c *Capture) Notice() (Response, error)      { return c.BootstrapResponse("notice") }

// CoreResponse returns the original JSON HTTP response for a bootstrap path.
// It is useful where a handler deliberately wants an exact captured response.
func (s *Set) CoreResponse(path string) ([]byte, error) {
	spec, ok := s.core[path]
	if !ok {
		return nil, fmt.Errorf("%w: core path %q", ErrNotFound, path)
	}
	response, err := s.readResponse(spec.Path, spec.File)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), response.Body...), nil
}

// RecordedResponseAt retrieves one captured HTTP response only when both its
// endpoint path and original request sequence match. Passing an arbitrary
// ordinal is deliberately not supported; callers should record the sequence
// that their route/session policy maps to a fixture.
func (s *Set) RecordedResponseAt(path string, requestSequence int) ([]byte, RecordedResponse, error) {
	for _, response := range s.responses[path] {
		if response.RequestSequence != requestSequence {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.Root, "bodies", response.File))
		if err != nil {
			return nil, RecordedResponse{}, fmt.Errorf("read recorded response %s: %w", response.File, err)
		}
		return body, response, nil
	}
	return nil, RecordedResponse{}, fmt.Errorf("%w: response %s at request sequence %d", ErrNotFound, path, requestSequence)
}

// RecordedPayloadAt decodes a selected captured response with the captured
// session key. LoginUser is always decoded with the fixed pre-login key.
func (s *Set) RecordedPayloadAt(path string, requestSequence int) (Payload, RecordedResponse, error) {
	body, response, err := s.RecordedResponseAt(path, requestSequence)
	if err != nil {
		return Payload{}, RecordedResponse{}, err
	}
	key := []byte(nil)
	if path == "/LoginUser" || path == "/JoinUser" {
		key = cryptox.Key()
	} else {
		capturedKey, err := s.SessionKey()
		if err != nil {
			return Payload{}, RecordedResponse{}, err
		}
		key = []byte(capturedKey)
	}
	env, proto, encrypted, err := decodeEnvelope(body, key)
	if err != nil {
		return Payload{}, RecordedResponse{}, err
	}
	return Payload{Path: path, PacketCode: env.PacketCode, Proto: proto, Envelope: env, Encrypted: encrypted}, response, nil
}

// ReencryptRecordedAt is the safe replay helper for a selected response. It
// preserves unencrypted protocol responses, but re-encrypts every encrypted
// response payload with the supplied local-session key and fixes length.
func (s *Set) ReencryptRecordedAt(path string, requestSequence int, localSessionKey []byte) ([]byte, RecordedResponse, error) {
	payload, response, err := s.RecordedPayloadAt(path, requestSequence)
	if err != nil {
		return nil, RecordedResponse{}, err
	}
	if !payload.Encrypted {
		body, _, err := s.RecordedResponseAt(path, requestSequence)
		return body, response, err
	}
	body, err := EncodePayload(payload, localSessionKey, true)
	return body, response, err
}

// Payload is the decoded portion of a captured endpoint response. PacketCode
// and Proto are sufficient for a server to modify state and re-encrypt it.
type Payload struct {
	Path       string
	PacketCode int
	Proto      []byte
	Envelope   Envelope
	Encrypted  bool
}

// CorePayload decodes a core fixture. LoginUser uses the fixed pre-login key;
// the remaining initial bootstrap bodies are direct base64 protobuf payloads.
func (s *Set) CorePayload(path string) (Payload, error) {
	spec, ok := s.core[path]
	if !ok {
		return Payload{}, fmt.Errorf("%w: core path %q", ErrNotFound, path)
	}
	body, err := s.CoreResponse(path)
	if err != nil {
		return Payload{}, err
	}
	key := []byte(nil)
	if spec.Key == FixedLogin {
		key = cryptox.Key()
	}
	env, proto, encrypted, err := decodeEnvelope(body, key)
	if err != nil {
		return Payload{}, err
	}
	return Payload{Path: path, PacketCode: env.PacketCode, Proto: proto, Envelope: env, Encrypted: encrypted}, nil
}

// SessionKey returns the captured LoginUser user_key. It is intended only for
// offline fixture decoding; callers should replace it with a local test key
// once their account/session layer exists. This package never logs it.
func (s *Set) SessionKey() (string, error) {
	payload, err := s.CorePayload("/LoginUser")
	if err != nil {
		return "", err
	}
	match := sessionKeyPattern.Find(payload.Proto)
	if match == nil {
		return "", errors.New("bd2 fixture: LoginUser payload does not contain user_key")
	}
	return string(match), nil
}

var sessionKeyPattern = regexp.MustCompile(`[0-9a-f]{32}`)

// ReencryptCore returns a core response whose payload has been encrypted with
// key. It is primarily for LoginUser replay after a handler substitutes a
// local user key in its protobuf. Callers who mutate protobuf should prefer
// EncodePayload below.
func (s *Set) ReencryptCore(path string, key []byte) ([]byte, error) {
	payload, err := s.CorePayload(path)
	if err != nil {
		return nil, err
	}
	return EncodePayload(payload, key, true)
}

// EncodePayload serializes a server-owned payload with an accurate length.
func EncodePayload(payload Payload, key []byte, encrypted bool) ([]byte, error) {
	return EncodeEnvelope(payload.Envelope, payload.Proto, key, encrypted)
}

// Batch loads one of the two recorded initial BatchRequest response bodies.
func (c *Capture) Batch(number int) (Response, error) {
	switch number {
	case 1:
		return c.readResponse("/BatchRequest", "00041_resp_BatchRequest.bin")
	case 2:
		return c.readResponse("/BatchRequest", "00077_resp_BatchRequest.bin")
	default:
		return Response{}, fmt.Errorf("%w: batch %d", ErrNotFound, number)
	}
}

// BatchResponse returns the captured raw BatchRequest response. For live
// replay prefer BatchForPaths, because capture response order is arbitrary.
func (s *Set) BatchResponse(index int) ([]byte, error) {
	response, err := s.Batch(index)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), response.Body...), nil
}

// BatchForPaths returns the captured responses in exactly the request path
// order supplied by the client. It verifies both the captured request and
// response before selecting items, preventing accidental path drift.
func (s *Set) BatchForPaths(index int, paths []string) ([]BatchItem, error) {
	key, err := s.SessionKey()
	if err != nil {
		return nil, err
	}
	return s.batchForPathsWithKey(index, paths, []byte(key))
}

// HandleBatch decodes an encrypted client BatchRequest body then produces a
// JSON BatchRequest response arranged to match its request order. The result
// is not AES-wrapped: captured BatchRequest responses are JSON arrays too.
func (s *Set) HandleBatch(index int, requestBody []byte, sessionKey []byte) ([]byte, error) {
	requests, _, err := DecodeBatchRequest(requestBody, sessionKey)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(requests))
	for i, request := range requests {
		paths[i] = request.Path
	}
	items, err := s.batchForPathsCaptured(index, paths)
	if err != nil {
		return nil, err
	}
	if err := s.reencryptBatchItems(items, sessionKey); err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

func (s *Set) batchForPathsWithKey(index int, paths []string, key []byte) ([]BatchItem, error) {
	items, err := s.batchForPathsCaptured(index, paths)
	if err != nil {
		return nil, err
	}
	if err := s.reencryptBatchItems(items, key); err != nil {
		return nil, err
	}
	return items, nil
}

// batchForPathsCaptured selects data using only the captured session key. The
// captured request itself is validated before its response map is trusted.
func (s *Set) batchForPathsCaptured(index int, paths []string) ([]BatchItem, error) {
	capturedKey, err := s.SessionKey()
	if err != nil {
		return nil, err
	}
	key := []byte(capturedKey)
	if err := s.validateBatchRequest(index, key); err != nil {
		return nil, err
	}
	raw, err := s.BatchResponse(index)
	if err != nil {
		return nil, err
	}
	items, _, err := DecodeBatch(raw, key)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]BatchItem, len(items))
	for _, item := range items {
		byPath[item.Path] = item
	}
	ordered := make([]BatchItem, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("bd2 fixture: duplicate requested batch path %q", path)
		}
		seen[path] = struct{}{}
		item, ok := byPath[path]
		if !ok {
			return nil, fmt.Errorf("%w: batch %d path %q", ErrNotFound, index, path)
		}
		ordered = append(ordered, item)
	}
	return ordered, nil
}

func (s *Set) reencryptBatchItems(items []BatchItem, localSessionKey []byte) error {
	if len(localSessionKey) != cryptox.AESKeySize {
		return cryptox.ErrInvalidKey
	}
	capturedKey, err := s.SessionKey()
	if err != nil {
		return err
	}
	for i := range items {
		body, err := json.Marshal(items[i].Response)
		if err != nil {
			return err
		}
		env, proto, _, err := decodeEnvelope(body, []byte(capturedKey))
		if err != nil {
			return fmt.Errorf("decode batch response %s: %w", items[i].Path, err)
		}
		encoded, err := EncodeEnvelope(env, proto, localSessionKey, true)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(encoded, &items[i].Response); err != nil {
			return err
		}
	}
	return nil
}

func (s *Set) validateBatchRequest(index int, key []byte) error {
	var file string
	switch index {
	case 1:
		file = "00040_req_BatchRequest.bin"
	case 2:
		file = "00076_req_BatchRequest.bin"
	default:
		return fmt.Errorf("%w: batch %d", ErrNotFound, index)
	}
	body, err := os.ReadFile(filepath.Join(s.Root, "bodies", file))
	if err != nil {
		return err
	}
	_, _, err = DecodeBatchRequest(body, key)
	return err
}

func (c *Capture) readResponse(path, file string) (Response, error) {
	if err := safeName(file); err != nil {
		return Response{}, err
	}
	body, err := os.ReadFile(filepath.Join(c.Root, "bodies", file))
	if err != nil {
		return Response{}, fmt.Errorf("read fixture %s: %w", file, err)
	}
	return Response{Path: path, File: file, Body: body}, nil
}

func safeName(name string) error {
	if filepath.Base(name) != name || strings.Contains(name, "..") || name == "" {
		return fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return nil
}

// Envelope is the common game response envelope. Unknown fields are retained
// in Extra, which keeps a replay fixture forward-compatible.
type Envelope struct {
	ErrorType     int             `json:"errorType"`
	PacketCode    int             `json:"packetCode,omitempty"`
	ErrorMessage  string          `json:"errorMessage,omitempty"`
	Length        int             `json:"length"`
	Data          string          `json:"data"`
	ServerNowTime int64           `json:"serverNowTime,omitempty"`
	Notify        string          `json:"notify,omitempty"`
	Extra         json.RawMessage `json:"-"`
}

// DecodeEnvelope accepts either base64(protobuf) or
// base64(AES-CBC(base64(protobuf))). The latter uses key. Captures legitimately
// contain both forms, including after login, so the fallback is intentional.
func DecodeEnvelope(body, key []byte) (Envelope, []byte, error) {
	env, proto, _, err := decodeEnvelope(body, key)
	return env, proto, err
}

func decodeEnvelope(body, key []byte) (Envelope, []byte, bool, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Envelope{}, nil, false, fmt.Errorf("decode envelope JSON: %w", err)
	}
	if env.Data == "" {
		if env.Length != 0 {
			return Envelope{}, nil, false, fmt.Errorf("%w: advertised %d bytes", ErrNoPayload, env.Length)
		}
		return env, nil, false, nil
	}
	plain, encrypted, err := decodeData(env.Data, key)
	if err != nil {
		return Envelope{}, nil, false, err
	}
	// In an encrypted envelope length is the character count of the inner
	// base64(protobuf), not the protobuf byte count. Direct-base64 bootstrap
	// envelopes commonly leave it at zero (MaintenanceInfo is an example).
	if encrypted && env.Length != base64.StdEncoding.EncodedLen(len(plain)) {
		return Envelope{}, nil, false, fmt.Errorf("%w: advertised=%d wire=%d decoded=%d encrypted=%t", ErrLengthMismatch, env.Length, base64.StdEncoding.EncodedLen(len(plain)), len(plain), encrypted)
	}
	return env, plain, encrypted, nil
}

func decodeData(data string, key []byte) ([]byte, bool, error) {
	if len(key) == cryptox.AESKeySize {
		if inner, err := cryptox.DecryptBase64(data, key); err == nil {
			if proto, err := base64.StdEncoding.DecodeString(string(inner)); err == nil {
				return proto, true, nil
			}
		}
	}
	proto, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, false, fmt.Errorf("decode fixture data: %w", err)
	}
	return proto, false, nil
}

// EncodeEnvelope creates a client-shaped response. encrypted chooses the
// AES-wrapped variant; plain responses use a direct base64 protobuf payload.
func EncodeEnvelope(env Envelope, proto, key []byte, encrypted bool) ([]byte, error) {
	copyEnv := env
	if encrypted {
		if len(key) != cryptox.AESKeySize {
			return nil, cryptox.ErrInvalidKey
		}
		payload, err := cryptox.EncryptBase64Payload(proto, key)
		if err != nil {
			return nil, err
		}
		copyEnv.Data = payload
		copyEnv.Length = base64.StdEncoding.EncodedLen(len(proto))
	} else {
		copyEnv.Data = base64.StdEncoding.EncodeToString(proto)
	}
	return json.Marshal(copyEnv)
}

// BatchItem is one JSON member in a BatchRequest response.
type BatchItem struct {
	Path     string   `json:"path"`
	Response Envelope `json:"responseData"`
}

// DecodeBatch parses and validates a BatchRequest response. It rejects
// duplicate endpoint paths because a local dispatcher could not replay them
// deterministically by path.
func DecodeBatch(body, key []byte) ([]BatchItem, map[string][]byte, error) {
	var items []BatchItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, nil, fmt.Errorf("decode batch JSON: %w", err)
	}
	if len(items) == 0 {
		return nil, nil, errors.New("bd2 fixture: empty batch response")
	}
	decoded := make(map[string][]byte, len(items))
	for i, item := range items {
		if item.Path == "" || !strings.HasPrefix(item.Path, "/") {
			return nil, nil, fmt.Errorf("bd2 fixture: batch item %d has invalid path %q", i, item.Path)
		}
		if _, exists := decoded[item.Path]; exists {
			return nil, nil, fmt.Errorf("bd2 fixture: duplicate batch path %q", item.Path)
		}
		body, err := json.Marshal(item.Response)
		if err != nil {
			return nil, nil, err
		}
		_, proto, err := DecodeEnvelope(body, key)
		if err != nil {
			return nil, nil, fmt.Errorf("decode batch item %s: %w", item.Path, err)
		}
		decoded[item.Path] = proto
	}
	return items, decoded, nil
}

// Request describes a decoded BatchRequest member. RequestData is a direct
// base64 protobuf payload; the JSON array itself is AES-wrapped on the wire.
type Request struct {
	Path        string `json:"path"`
	RequestData string `json:"requestData"`
}

func DecodeBatchRequest(body, key []byte) ([]Request, map[string][]byte, error) {
	plain, err := cryptox.DecryptBase64(strings.TrimSpace(string(body)), key)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt batch request: %w", err)
	}
	var requests []Request
	if err := json.Unmarshal(plain, &requests); err != nil {
		return nil, nil, fmt.Errorf("decode batch request JSON: %w", err)
	}
	decoded := make(map[string][]byte, len(requests))
	for i, req := range requests {
		if req.Path == "" || !strings.HasPrefix(req.Path, "/") || req.RequestData == "" {
			return nil, nil, fmt.Errorf("bd2 fixture: batch request item %d is invalid", i)
		}
		if _, exists := decoded[req.Path]; exists {
			return nil, nil, fmt.Errorf("bd2 fixture: duplicate batch request path %q", req.Path)
		}
		proto, err := base64.StdEncoding.DecodeString(req.RequestData)
		if err != nil {
			return nil, nil, fmt.Errorf("decode batch request %s: %w", req.Path, err)
		}
		decoded[req.Path] = proto
	}
	return requests, decoded, nil
}

// Paths returns the fixture response paths in lexical order for diagnostics.
func Paths(items []BatchItem) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
	}
	sort.Strings(paths)
	return paths
}

// EqualProto deliberately treats nil and empty payloads equally; protocol
// endpoints with response length zero use both representations in practice.
func EqualProto(a, b []byte) bool { return bytes.Equal(a, b) }

var _ fs.FS // document that fixtures deliberately use an on-disk capture.
