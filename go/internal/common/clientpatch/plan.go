package clientpatch

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"bd2server/internal/common/release"
)

const (
	SignatureDomain = "bd2-client-patch-v1\n"
	MaxPlaintext    = 16 << 20
	Database        = "ClientLocal_v1"
	MaxPatches      = 256
)

type Plan struct {
	Schema      int     `json:"schema"`
	Origin      string  `json:"origin"`
	GameVersion string  `json:"game_version"`
	Database    string  `json:"database"`
	Revision    uint64  `json:"revision"`
	Patches     []Patch `json:"patches"`
}

type Patch struct {
	ID         string      `json:"id"`
	OwnerUID   int64       `json:"owner_uid"`
	Requires   []string    `json:"requires"`
	Statements []Statement `json:"statements"`
	Before     []Assertion `json:"before"`
	After      []Assertion `json:"after"`
}

type Statement struct {
	SQL  string         `json:"sql"`
	Args map[string]any `json:"args"`
}

type Assertion struct {
	SQL    string         `json:"sql"`
	Args   map[string]any `json:"args"`
	Equals int64          `json:"equals"`
}

type Envelope struct {
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
	Plan      json.RawMessage `json:"plan"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var argument = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var gameVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func ValidateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || len(origin) > 2048 || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != origin {
		return errors.New("clientpatch: invalid origin")
	}
	host := u.Hostname()
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "%\\ \t\r\n") || strings.HasSuffix(u.Host, ":") {
		return errors.New("clientpatch: origin host is not canonical")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || u.Scheme == "https" && n == 443 || u.Scheme == "http" && n == 80 {
			return errors.New("clientpatch: origin port is not canonical")
		}
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.String() != host {
		return errors.New("clientpatch: origin IP is not canonical")
	}
	loopbackHTTP := u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !loopbackHTTP {
		return errors.New("clientpatch: origin requires HTTPS")
	}
	return nil
}

func (p Plan) Validate() error {
	if p.Schema != 1 || p.Database != Database || p.Revision == 0 || (len(p.GameVersion) > 32 || !gameVersion.MatchString(p.GameVersion)) {
		return errors.New("clientpatch: unsupported plan schema, database, revision or game version")
	}
	if err := ValidateOrigin(p.Origin); err != nil {
		return err
	}
	if p.Patches == nil || len(p.Patches) > MaxPatches {
		return errors.New("clientpatch: patches must be an array of at most 256 entries")
	}
	ids := make(map[string]int, len(p.Patches))
	total := 0
	for i, patch := range p.Patches {
		if !identifier.MatchString(patch.ID) || patch.OwnerUID < 0 {
			return fmt.Errorf("clientpatch: invalid patch identity at index %d", i)
		}
		if _, exists := ids[patch.ID]; exists {
			return fmt.Errorf("clientpatch: duplicate patch %q", patch.ID)
		}
		ids[patch.ID] = i
		if len(patch.Requires) > MaxPatches || len(patch.Statements) == 0 || len(patch.Statements) > 256 || len(patch.Before) == 0 || len(patch.Before) > 64 || len(patch.After) == 0 || len(patch.After) > 64 {
			return fmt.Errorf("clientpatch: patch %q exceeds operation limits or lacks statements/preconditions/postconditions", patch.ID)
		}
		total += len(patch.Statements) + len(patch.Before) + len(patch.After)
		if total > 4096 {
			return errors.New("clientpatch: plan exceeds operation limit")
		}
		for _, statement := range patch.Statements {
			if err := validateQuery(statement.SQL, statement.Args, false); err != nil {
				return fmt.Errorf("clientpatch: patch %q statement: %w", patch.ID, err)
			}
		}
		for _, assertions := range [][]Assertion{patch.Before, patch.After} {
			for _, assertion := range assertions {
				if err := validateQuery(assertion.SQL, assertion.Args, true); err != nil {
					return fmt.Errorf("clientpatch: patch %q assertion: %w", patch.ID, err)
				}
			}
		}
	}
	state := make([]uint8, len(p.Patches))
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 1 {
			return errors.New("clientpatch: cyclic patch dependencies")
		}
		if state[i] == 2 {
			return nil
		}
		state[i] = 1
		seen := map[string]bool{}
		patch := p.Patches[i]
		for _, required := range patch.Requires {
			j, found := ids[required]
			if !found || required == patch.ID || seen[required] || p.Patches[j].OwnerUID != 0 && p.Patches[j].OwnerUID != patch.OwnerUID {
				return fmt.Errorf("clientpatch: patch %q has invalid dependency %q", patch.ID, required)
			}
			seen[required] = true
			if err := visit(j); err != nil {
				return err
			}
		}
		state[i] = 2
		return nil
	}
	for i := range p.Patches {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

func validateQuery(sql string, args map[string]any, assertion bool) error {
	if len(sql) == 0 || len(sql) > 64<<10 || strings.ContainsRune(sql, '\x00') || len(args) > 64 {
		return errors.New("invalid SQL or argument size")
	}
	words := strings.Fields(sql)
	if len(words) == 0 {
		return errors.New("empty SQL")
	}
	verb := strings.ToUpper(words[0])
	if assertion && verb != "SELECT" || !assertion && verb != "INSERT" && verb != "UPDATE" && verb != "DELETE" {
		return errors.New("only INSERT, UPDATE, DELETE data migrations and SELECT assertions are allowed")
	}
	for name, value := range args {
		if !argument.MatchString(name) || strings.EqualFold(name, "owner") {
			return fmt.Errorf("invalid or reserved argument %q", name)
		}
		switch v := value.(type) {
		case nil, bool, int64, int:
		case string:
			if len(v) > 64<<10 {
				return errors.New("argument string too large")
			}
		case json.Number:
			if _, err := v.Int64(); err != nil {
				if !strings.ContainsAny(v.String(), ".eE") {
					return errors.New("integer argument exceeds SQLite signed 64-bit range")
				}
				if f, err := v.Float64(); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
					return errors.New("invalid numeric argument")
				}
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return errors.New("invalid numeric argument")
			}
		default:
			return fmt.Errorf("argument %q is not a SQLite scalar", name)
		}
	}
	return nil
}

func decode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("clientpatch: trailing JSON data")
	}
	return nil
}

func Verify(raw []byte, keys []release.TrustedKey) (Plan, error) {
	if len(raw) == 0 || len(raw) > MaxPlaintext {
		return Plan{}, errors.New("clientpatch: signed plan size is invalid")
	}
	var envelope Envelope
	if err := decode(raw, &envelope); err != nil {
		return Plan{}, fmt.Errorf("clientpatch: envelope: %w", err)
	}
	var public []byte
	for _, key := range keys {
		if key.ID != envelope.KeyID {
			continue
		}
		var err error
		public, err = base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize || release.KeyID(public) != key.ID {
			return Plan{}, errors.New("clientpatch: invalid trusted key")
		}
		break
	}
	if len(public) == 0 {
		return Plan{}, errors.New("clientpatch: untrusted signing key")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(public, append([]byte(SignatureDomain), envelope.Plan...), signature) {
		return Plan{}, errors.New("clientpatch: invalid signature")
	}
	var plan Plan
	if err := decode(envelope.Plan, &plan); err != nil {
		return Plan{}, fmt.Errorf("clientpatch: plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}
