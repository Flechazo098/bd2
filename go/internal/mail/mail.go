// Package mail owns the local new-player mailbox.  Its seed is source data,
// not a captured protobuf response: responses are encoded field-by-field here.
package mail

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

const packetCode = 131
const openPacketCode = 132

// MailDBInfo is the persisted shape used by the client.  A mail either has a
// literal title/body (ordinary system mail), or a TemplateID and Sender (the
// localized/template-driven mail form).  Reward fields are parallel arrays:
// type, table ID, and amount respectively.
type MailDBInfo struct {
	MailID       uint64   `json:"mail_id"`
	MailType     uint64   `json:"mail_type,omitempty"`
	TemplateID   uint64   `json:"template_id,omitempty"`
	Sender       string   `json:"sender,omitempty"`
	Title        string   `json:"title,omitempty"`
	Body         string   `json:"body,omitempty"`
	ExpiresAt    uint64   `json:"expires_at"`
	RewardTypes  []uint64 `json:"reward_types,omitempty"`
	RewardIDs    []uint64 `json:"reward_ids,omitempty"`
	RewardCounts []uint64 `json:"reward_counts,omitempty"`
	SentAt       uint64   `json:"sent_at"`
}

type Starter struct {
	Version   string       `json:"version"`
	Mails     []MailDBInfo `json:"mails"`
	MailCount uint64       `json:"mail_count"`
	MaxMailID uint64       `json:"max_mail_id"`
}

func Load(path string) (*Starter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mail: read seed: %w", err)
	}
	var seed Starter
	if err := json.Unmarshal(data, &seed); err != nil {
		return nil, fmt.Errorf("mail: decode seed: %w", err)
	}
	if err := seed.Validate(); err != nil {
		return nil, err
	}
	return &seed, nil
}

func (s *Starter) Validate() error {
	if s == nil || s.Version != "2.34.13" {
		return errors.New("mail: wrong starter version")
	}
	if s.MailCount != uint64(len(s.Mails))+1 {
		return errors.New("mail: mail_count must include the server sentinel")
	}
	for _, m := range s.Mails {
		if m.MailID == 0 || m.ExpiresAt == 0 || m.SentAt == 0 {
			return errors.New("mail: invalid mail identity or time")
		}
		if m.MailType == 0 && m.TemplateID == 0 {
			return errors.New("mail: each mail needs mail_type or template_id")
		}
		if len(m.RewardTypes) != len(m.RewardIDs) || len(m.RewardTypes) != len(m.RewardCounts) {
			return errors.New("mail: reward arrays differ in length")
		}
	}
	return nil
}

func packed(values []uint64) []byte {
	var result []byte
	for _, value := range values {
		result = binary.AppendUvarint(result, value)
	}
	return result
}

func (m MailDBInfo) encode() []byte {
	result := wire.AppendVarint(nil, 1, m.MailID)
	if m.MailType != 0 {
		result = wire.AppendVarint(result, 2, m.MailType)
	}
	if m.TemplateID != 0 {
		result = wire.AppendVarint(result, 3, m.TemplateID)
	}
	if m.Sender != "" {
		result = wire.AppendString(result, 4, m.Sender)
	}
	if m.Title != "" {
		result = wire.AppendString(result, 5, m.Title)
	}
	if m.Body != "" {
		result = wire.AppendString(result, 6, m.Body)
	}
	result = wire.AppendVarint(result, 7, m.ExpiresAt)
	if len(m.RewardTypes) != 0 {
		result = wire.AppendBytes(result, 8, packed(m.RewardTypes))
	}
	if len(m.RewardIDs) != 0 {
		result = wire.AppendBytes(result, 9, packed(m.RewardIDs))
	}
	if len(m.RewardCounts) != 0 {
		result = wire.AppendBytes(result, 10, packed(m.RewardCounts))
	}
	return wire.AppendVarint(result, 13, m.SentAt)
}

func (s *Starter) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/MailInfo" {
		return 0, nil, false, nil
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("mail: %s invalid sequence", path)
	}
	var result []byte
	for _, entry := range s.Mails {
		result = wire.AppendBytes(result, 1, entry.encode())
	}
	result = wire.AppendVarint(result, 2, s.MailCount)
	result = wire.AppendVarint(result, 3, s.MaxMailID)
	return packetCode, result, true, nil
}

func (s *Starter) Write(path string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func unpack(data []byte) ([]uint64, error) {
	var result []uint64
	for len(data) != 0 {
		value, count := binary.Uvarint(data)
		if count <= 0 {
			return nil, errors.New("mail: malformed packed values")
		}
		result, data = append(result, value), data[count:]
	}
	return result, nil
}

type stateSnapshot struct {
	Version string   `json:"version"`
	Opened  []uint64 `json:"opened,omitempty"`
}

// Service owns mailbox visibility and idempotent reward delivery. Starter is
// immutable source data; only opened IDs are persisted in the player state.
type Service struct {
	mu        sync.Mutex
	Starter   *Starter
	path      string
	inventory *player.Inventory
	wallet    *player.Wallet
	state     stateSnapshot
}

func OpenService(path string, starter *Starter, inventory *player.Inventory, wallet *player.Wallet) (*Service, error) {
	if path == "" || starter == nil || inventory == nil || wallet == nil {
		return nil, errors.New("mail: invalid service configuration")
	}
	if err := starter.Validate(); err != nil {
		return nil, err
	}
	s := &Service{Starter: starter, path: filepath.Clean(path), inventory: inventory, wallet: wallet,
		state: stateSnapshot{Version: "2.34.13"}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mail: read state: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != "2.34.13" {
		return nil, errors.New("mail: malformed state")
	}
	sort.Slice(s.state.Opened, func(i, j int) bool { return s.state.Opened[i] < s.state.Opened[j] })
	for i := 1; i < len(s.state.Opened); i++ {
		if s.state.Opened[i] == s.state.Opened[i-1] {
			return nil, errors.New("mail: duplicate opened ID")
		}
	}
	return s, nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/MailInfo" && path != "/MailOpen" {
		return 0, nil, false, nil
	}
	if s == nil || s.Starter == nil || s.inventory == nil || s.wallet == nil {
		return 0, nil, true, errors.New("mail: unavailable service")
	}
	seq, present, err := wire.Varint(request, 1)
	if err != nil || !present || seq == 0 {
		return 0, nil, true, fmt.Errorf("mail: %s invalid sequence", path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "/MailInfo" {
		return packetCode, s.info(), true, nil
	}
	response, err := s.open(request)
	return openPacketCode, response, true, err
}

func (s *Service) info() []byte {
	var result []byte
	remaining := uint64(0)
	for _, entry := range s.Starter.Mails {
		if containsID(s.state.Opened, entry.MailID) {
			continue
		}
		result = wire.AppendBytes(result, 1, entry.encode())
		remaining++
	}
	// The official total includes one server-side sentinel row.
	result = wire.AppendVarint(result, 2, remaining+1)
	result = wire.AppendVarint(result, 3, s.Starter.MaxMailID)
	return result
}

func (s *Service) open(request []byte) ([]byte, error) {
	ids, err := requestMailIDs(request)
	if err != nil || len(ids) == 0 {
		return nil, fmt.Errorf("mail: invalid open request: %w", err)
	}
	selected := make([]MailDBInfo, 0, len(ids))
	seen := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			return nil, errors.New("mail: duplicate or zero mail ID")
		}
		seen[id] = true
		found := false
		for _, entry := range s.Starter.Mails {
			if entry.MailID == id {
				selected = append(selected, entry)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("mail: unknown mail %d", id)
		}
	}

	var bundle []byte
	next := stateSnapshot{Version: s.state.Version, Opened: append([]uint64(nil), s.state.Opened...)}
	for _, entry := range selected {
		identity := fmt.Sprintf("mail:%d", entry.MailID)
		rewards := make([]gamedata.Reward, len(entry.RewardTypes))
		var items []gamedata.BattleReward
		for i := range entry.RewardTypes {
			reward := gamedata.Reward{Type: entry.RewardTypes[i], ID: entry.RewardIDs[i], Count: entry.RewardCounts[i]}
			rewards[i] = reward
			switch reward.Type {
			case 3, 4:
				currency := wire.AppendVarint(nil, 3, reward.Type)
				currency = wire.AppendVarint(currency, 4, reward.Count)
				bundle = wire.AppendBytes(bundle, 1, currency)
			case 8:
				if reward.ID == 0 || reward.Count == 0 {
					return nil, errors.New("mail: invalid item reward")
				}
				items = append(items, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
			default:
				return nil, fmt.Errorf("mail: unsupported reward type %d", reward.Type)
			}
		}
		if _, err := s.wallet.GrantQuestOnce(identity+":currency", rewards); err != nil {
			return nil, err
		}
		granted, err := s.inventory.GrantOnce(identity+":items", items)
		if err != nil {
			return nil, err
		}
		if len(granted) == 0 {
			granted = s.inventory.GrantedItems(identity + ":items")
		}
		for _, item := range granted {
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
			view := wire.AppendVarint(nil, 2, item.ID)
			view = wire.AppendVarint(view, 3, item.Type)
			view = wire.AppendVarint(view, 4, item.Count)
			bundle = wire.AppendBytes(bundle, 6, view)
		}
		if !containsID(next.Opened, entry.MailID) {
			next.Opened = append(next.Opened, entry.MailID)
		}
	}
	sort.Slice(next.Opened, func(i, j int) bool { return next.Opened[i] < next.Opened[j] })
	if err := s.commit(next); err != nil {
		return nil, err
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}

func requestMailIDs(request []byte) ([]uint64, error) {
	var result []uint64
	err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		switch field.Type {
		case 0:
			value, _ := binary.Uvarint(field.Value)
			result = append(result, value)
		case 2:
			values, err := unpack(field.Value)
			if err != nil {
				return err
			}
			result = append(result, values...)
		default:
			return errors.New("mail: invalid InvenIndex wire type")
		}
		return nil
	})
	return result, err
}

func (s *Service) commit(next stateSnapshot) error {
	if len(next.Opened) == len(s.state.Opened) {
		equal := true
		for i := range next.Opened {
			if next.Opened[i] != s.state.Opened[i] {
				equal = false
				break
			}
		}
		if equal {
			return nil
		}
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".mail-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("mail: persist state: %w", err)
	}
	s.state = next
	return nil
}

func containsID(values []uint64, want uint64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
