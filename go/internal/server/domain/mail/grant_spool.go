package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
)

// GrantSpool is a local operator input, independent of the versioned starter
// mailbox and player state. Writers atomically replace the whole JSON file.
// Version 2 requires an account recipient and uses Unix milliseconds; a stable identity is never issued twice,
// including after the player opens the mail or the server restarts.
type GrantSpool struct {
	Version uint64  `json:"version"`
	Grants  []Grant `json:"grants"`
}

type Grant struct {
	AccountID string        `json:"account_id"`
	Identity  string        `json:"identity"`
	Title     string        `json:"title"`
	Body      string        `json:"body"`
	SentAt    int64         `json:"sent_at"`
	Rewards   []GrantReward `json:"rewards"`
}

type GrantReward struct {
	Type  uint64 `json:"type"`
	ID    uint64 `json:"id"`
	Count uint64 `json:"count"`
}

type compensation struct {
	identity, title, body string
	sentAt                time.Time
	rewards               []gamedata.Reward
	templateID            uint64
	isCash                bool
}

func (g compensation) validate() error {
	if strings.TrimSpace(g.identity) == "" || strings.TrimSpace(g.identity) != g.identity || utf8.RuneCountInString(g.identity) > 500 ||
		strings.TrimSpace(g.title) == "" || utf8.RuneCountInString(g.title) > 500 ||
		strings.TrimSpace(g.body) == "" || utf8.RuneCountInString(g.body) > 5000 ||
		g.sentAt.IsZero() || g.sentAt.UnixMilli() <= 0 || g.sentAt.UnixMilli() > math.MaxInt64-int64(30*24*time.Hour/time.Millisecond) || len(g.rewards) == 0 {
		return errors.New("mail: invalid compensation identity, content, time or rewards")
	}
	for _, reward := range g.rewards {
		costumeReward := reward.Type == 11 && reward.ID != 0 && reward.Count <= 6
		if reward.Count == 0 || reward.Count > math.MaxInt32 || reward.ID > math.MaxInt32 || (!currencyRewardTypes[reward.Type] && !supportedItemDBInfoReward(reward) && !costumeReward) ||
			(currencyRewardTypes[reward.Type] && reward.ID != 0) {
			return fmt.Errorf("mail: compensation %q has unsupported reward type=%d id=%d count=%d", g.identity, reward.Type, reward.ID, reward.Count)
		}
	}
	return nil
}

// AttachGrantSpoolPath enables reads before each /MailInfo. An absent file is
// an empty queue, allowing an operator to create it after server startup.
// Existing malformed input is rejected immediately without issuing mail.
func (s *Service) AttachGrantSpoolPath(ctx command.Context, path string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return errors.New("mail: invalid grant spool path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("mail: resolve grant spool path: %w", err)
	}
	if _, err := readGrantSpool(abs, ctx.AccountID); err != nil {
		return err
	}

	s.grantSpoolPath = abs
	return nil
}

func readGrantSpool(path, accountID string) ([]compensation, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("mail: grant spool requires command account identity")
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mail: read grant spool: %w", err)
	}
	grants, err := decodeGrantSpool(data, accountID)
	if err != nil {
		return nil, fmt.Errorf("mail: reject grant spool: %w", err)
	}
	return grants, nil
}

// Decode the entire file before returning any grants. The exact object reader
// rejects missing, unknown, duplicate and null fields at every schema level.
func decodeGrantSpool(data []byte, accountID string) ([]compensation, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8")
	}
	root, err := exactSpoolObject(data, "version", "grants")
	if err != nil {
		return nil, err
	}
	var version uint64
	if err := json.Unmarshal(root["version"], &version); err != nil || version != 2 {
		return nil, errors.New("unsupported grant spool version")
	}
	var rawGrants []json.RawMessage
	if err := json.Unmarshal(root["grants"], &rawGrants); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rawGrants))
	grants := make([]compensation, 0, len(rawGrants))
	for i, raw := range rawGrants {
		fields, err := exactSpoolObject(raw, "account_id", "identity", "title", "body", "sent_at", "rewards")
		if err != nil {
			return nil, fmt.Errorf("grant %d: %w", i, err)
		}
		var entry Grant
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("grant %d: %w", i, err)
		}
		if strings.TrimSpace(entry.AccountID) == "" || strings.TrimSpace(entry.AccountID) != entry.AccountID || utf8.RuneCountInString(entry.AccountID) > 500 {
			return nil, fmt.Errorf("grant %d: invalid recipient account", i)
		}
		recipientIdentity := entry.AccountID + "\x00" + entry.Identity
		if seen[recipientIdentity] {
			return nil, fmt.Errorf("duplicate grant identity %q", entry.Identity)
		}
		seen[recipientIdentity] = true
		var rawRewards []json.RawMessage
		if err := json.Unmarshal(fields["rewards"], &rawRewards); err != nil {
			return nil, err
		}
		grant := compensation{identity: entry.Identity, title: entry.Title, body: entry.Body, sentAt: time.UnixMilli(entry.SentAt)}
		for j, reward := range rawRewards {
			if _, err := exactSpoolObject(reward, "type", "id", "count"); err != nil {
				return nil, fmt.Errorf("grant %d reward %d: %w", i, j, err)
			}
			grant.rewards = append(grant.rewards, gamedata.Reward{Type: entry.Rewards[j].Type, ID: entry.Rewards[j].ID, Count: entry.Rewards[j].Count})
		}
		if err := grant.validate(); err != nil {
			return nil, fmt.Errorf("grant %d: %w", i, err)
		}
		if entry.AccountID == accountID {
			grants = append(grants, grant)
		}
	}
	return grants, nil
}

func exactSpoolObject(data []byte, names ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("expected JSON object")
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || !wanted[name] {
			return nil, fmt.Errorf("unknown field %q", token)
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("null field %q", name)
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	for _, name := range names {
		if _, exists := fields[name]; !exists {
			return nil, fmt.Errorf("missing field %q", name)
		}
	}
	return fields, nil
}
