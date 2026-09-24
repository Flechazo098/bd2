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
	"strconv"
	"sync"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/stateio"
	"bd2server/internal/versionconfig"
	"bd2server/internal/wire"
)

const packetCode = 131
const openPacketCode = 132

// itemDBInfoTypes are ElementType values whose successful mail claim is
// represented by ItemDBInfo in RewardDBInfoBundle.  They are deliberately
// separate from character (6), equipment (10), and costume (11): the client
// requires CharDBInfo, EquipDBInfo, and CostumeDBInfo for those rewards, and
// this mailbox owns no such domain stores.  The values are from 2.34.13
// DataManager.GetItemInfo and ItemDBInfo, not inferred from table names.
var itemDBInfoTypes = map[uint64]bool{
	5:  true, // food
	7:  true, // cooking recipe/result
	8:  true, // resource
	9:  true, // random box
	13: true, // quest item
	14: true, // use item
	17: true, // collection item
	27: true, // my-room item
	29: true, // instant-use item
}

var currencyRewardTypes = map[uint64]bool{
	3:  true, // free jewelry
	4:  true, // gold
	12: true, // catalyst / talent elixir
}

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
	if s == nil || s.Version != versionconfig.Protocol() {
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
	Version           string   `json:"version"`
	Opened            []uint64 `json:"opened"`
	NextDynamicMailID uint64   `json:"next_dynamic_mail_id"`
}

// Service owns mailbox visibility and idempotent reward delivery. Starter is
// immutable source data; only opened IDs are persisted in the player state.
type Service struct {
	mu        sync.Mutex
	Starter   *Starter
	seedPath  string
	seedStamp fileStamp
	storage   stateio.AtomicEntryStore
	inventory *player.Inventory
	wallet    *player.Wallet
	state     stateSnapshot
	dynamic   map[uint64]MailDBInfo
	issued    map[string]uint64
}

type fileStamp struct {
	size    int64
	modTime int64
}

func OpenService(storage stateio.Store, starter *Starter, inventory *player.Inventory, wallet *player.Wallet) (*Service, error) {
	entries, ok := storage.(stateio.AtomicEntryStore)
	if !ok || starter == nil || inventory == nil || wallet == nil {
		return nil, errors.New("mail: invalid service configuration")
	}
	if err := starter.Validate(); err != nil {
		return nil, err
	}
	if starter.MaxMailID == ^uint64(0) {
		return nil, errors.New("mail: starter mail ID exhausted")
	}
	s := &Service{Starter: starter, storage: entries, inventory: inventory, wallet: wallet,
		state:   stateSnapshot{Version: versionconfig.Protocol(), NextDynamicMailID: starter.MaxMailID + 1},
		dynamic: map[uint64]MailDBInfo{}, issued: map[string]uint64{}}
	b, err := storage.Load("mail")
	if err != nil {
		return nil, fmt.Errorf("mail: load state: %w", err)
	}
	if b == nil {
		if err := stateio.RequireNoEntries(entries, "mail", "dynamic", "issued"); err != nil {
			return nil, fmt.Errorf("mail: invalid storage: %w", err)
		}
		return s, nil
	}
	if err := stateio.RequireExactJSONObject(b, "version", "opened", "next_dynamic_mail_id"); err != nil {
		return nil, fmt.Errorf("mail: incompatible state layout: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != versionconfig.Protocol() || s.state.NextDynamicMailID == 0 {
		return nil, errors.New("mail: malformed state")
	}
	rawDynamic, err := entries.ListEntries("mail", "dynamic")
	if err != nil {
		return nil, err
	}
	for key, payload := range rawDynamic {
		id, parseErr := strconv.ParseUint(key, 10, 64)
		var entry MailDBInfo
		if parseErr != nil || id == 0 || json.Unmarshal(payload, &entry) != nil || entry.MailID != id || entry.MailID >= s.state.NextDynamicMailID {
			return nil, fmt.Errorf("mail: invalid dynamic entry %q", key)
		}
		s.dynamic[id] = entry
	}
	rawIssued, err := entries.ListEntries("mail", "issued")
	if err != nil {
		return nil, err
	}
	for identity, payload := range rawIssued {
		var id uint64
		if identity == "" || json.Unmarshal(payload, &id) != nil || id == 0 {
			return nil, fmt.Errorf("mail: invalid issued entry %q", identity)
		}
		if _, found := s.dynamic[id]; !found {
			return nil, fmt.Errorf("mail: issued entry %q references missing mail %d", identity, id)
		}
		s.issued[identity] = id
	}
	// A watched development seed is allowed to append immutable static mails
	// between runs. Move the dynamic allocator above that range as long as none
	// of the already-persisted dynamic IDs collide with the expanded starter.
	if starter.MaxMailID >= s.state.NextDynamicMailID {
		for id := range s.dynamic {
			if id <= starter.MaxMailID {
				return nil, fmt.Errorf("mail: starter mail range collides with dynamic mail %d", id)
			}
		}
		if starter.MaxMailID == ^uint64(0) {
			return nil, errors.New("mail: starter mail ID exhausted")
		}
		s.state.NextDynamicMailID = starter.MaxMailID + 1
	}
	sort.Slice(s.state.Opened, func(i, j int) bool { return s.state.Opened[i] < s.state.Opened[j] })
	for i := 1; i < len(s.state.Opened); i++ {
		if s.state.Opened[i] == s.state.Opened[i-1] {
			return nil, errors.New("mail: duplicate opened ID")
		}
	}
	return s, nil
}

func (s *Service) EnsurePersisted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.storage.Load("mail")
	if err != nil {
		return err
	}
	if b != nil {
		return nil
	}
	return s.persist(s.state)
}

// AttachSeedPath enables development-time hot reloading of an atomically
// replaced mail seed. It is deliberately a mailbox concern, not an HTTP debug
// endpoint: the client continues to call only the normal /MailInfo API.
// A changed file is validated before replacing the in-memory starter. A bad
// replacement leaves the last known-good starter intact and fails that request.
func (s *Service) AttachSeedPath(path string) error {
	if s == nil || path == "" {
		return errors.New("mail: invalid seed watch path")
	}
	stamp, err := seedFileStamp(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seedPath, s.seedStamp = filepath.Clean(path), stamp
	return nil
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
		if err := s.reloadSeedIfChanged(); err != nil {
			return 0, nil, true, err
		}
		return packetCode, s.info(), true, nil
	}
	response, err := s.open(request)
	return openPacketCode, response, true, err
}

func seedFileStamp(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, fmt.Errorf("mail: stat watched seed: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fileStamp{}, errors.New("mail: watched seed is not a regular file")
	}
	return fileStamp{size: info.Size(), modTime: info.ModTime().UnixNano()}, nil
}

// reloadSeedIfChanged is called with s.mu held.
func (s *Service) reloadSeedIfChanged() error {
	if s.seedPath == "" {
		return nil
	}
	stamp, err := seedFileStamp(s.seedPath)
	if err != nil {
		return err
	}
	if stamp == s.seedStamp {
		return nil
	}
	next, err := Load(s.seedPath)
	if err != nil {
		return fmt.Errorf("mail: reject changed seed and retain last known-good mailbox: %w", err)
	}
	if next.MaxMailID >= s.state.NextDynamicMailID {
		for id := range s.dynamic {
			if id <= next.MaxMailID {
				return fmt.Errorf("mail: reject changed seed because mail range collides with dynamic mail %d", id)
			}
		}
		if next.MaxMailID == ^uint64(0) {
			return errors.New("mail: reject changed seed because mail ID range is exhausted")
		}
		updated := s.state
		updated.Opened = append([]uint64(nil), s.state.Opened...)
		updated.NextDynamicMailID = next.MaxMailID + 1
		if err := s.persist(updated); err != nil {
			return fmt.Errorf("mail: persist watched seed mail range: %w", err)
		}
	}
	s.Starter, s.seedStamp = next, stamp
	return nil
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
	dynamicIDs := make([]uint64, 0, len(s.dynamic))
	for id := range s.dynamic {
		dynamicIDs = append(dynamicIDs, id)
	}
	sort.Slice(dynamicIDs, func(i, j int) bool { return dynamicIDs[i] < dynamicIDs[j] })
	for _, id := range dynamicIDs {
		if containsID(s.state.Opened, id) {
			continue
		}
		result = wire.AppendBytes(result, 1, s.dynamic[id].encode())
		remaining++
	}
	// The official total includes one server-side sentinel row.
	result = wire.AppendVarint(result, 2, remaining+1)
	maxMailID := s.Starter.MaxMailID
	if s.state.NextDynamicMailID > maxMailID+1 {
		maxMailID = s.state.NextDynamicMailID - 1
	}
	result = wire.AppendVarint(result, 3, maxMailID)
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
			if entry, exists := s.dynamic[id]; exists {
				selected = append(selected, entry)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("mail: unknown mail %d", id)
		}
	}

	var bundle []byte
	next := stateSnapshot{Version: s.state.Version, Opened: append([]uint64(nil), s.state.Opened...), NextDynamicMailID: s.state.NextDynamicMailID}
	for _, entry := range selected {
		identity := fmt.Sprintf("mail:%d", entry.MailID)
		rewards := make([]gamedata.Reward, len(entry.RewardTypes))
		var items []gamedata.BattleReward
		for i := range entry.RewardTypes {
			reward := gamedata.Reward{Type: entry.RewardTypes[i], ID: entry.RewardIDs[i], Count: entry.RewardCounts[i]}
			rewards[i] = reward
			switch {
			case currencyRewardTypes[reward.Type]:
				currency := wire.AppendVarint(nil, 3, reward.Type)
				currency = wire.AppendVarint(currency, 4, reward.Count)
				bundle = wire.AppendBytes(bundle, 1, currency)
			case reward.Type == 28:
				// DataManager recognizes this type, but RewardDBInfoBundle
				// carries MyRoomTrophyDBInfo in a separate field.  Encoding it as
				// ItemDBInfo would make the local state and client model disagree.
				return nil, errors.New("mail: my-room trophy rewards require MyRoomTrophyDBInfo")
			default:
				if !itemDBInfoTypes[reward.Type] {
					return nil, fmt.Errorf("mail: unsupported reward type %d", reward.Type)
				}
				if reward.ID == 0 || reward.Count == 0 {
					return nil, errors.New("mail: invalid item reward")
				}
				items = append(items, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count})
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
	if next.NextDynamicMailID == s.state.NextDynamicMailID && len(next.Opened) == len(s.state.Opened) {
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
	return s.persist(next)
}

func (s *Service) persist(next stateSnapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.SaveWithEntries("mail", b, nil); err != nil {
		return fmt.Errorf("mail: persist state: %w", err)
	}
	s.state = next
	return nil
}

// EnqueueCompensation creates one durable system mail for an expired,
// completed reward. Identity is period-scoped and makes repeated rollover
// checks idempotent.
func (s *Service) EnqueueCompensation(identity, title, body string, rewards []gamedata.Reward, sentAt time.Time) error {
	if identity == "" || title == "" || body == "" || sentAt.IsZero() || len(rewards) == 0 {
		return errors.New("mail: invalid compensation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.issued[identity]; exists {
		return nil
	}
	if s.state.NextDynamicMailID == 0 || s.state.NextDynamicMailID == ^uint64(0) {
		return errors.New("mail: dynamic mail ID exhausted")
	}
	entry := MailDBInfo{
		MailID: s.state.NextDynamicMailID, MailType: 2, Title: title, Body: body,
		SentAt: uint64(sentAt.UTC().UnixMilli()), ExpiresAt: uint64(sentAt.UTC().Add(30 * 24 * time.Hour).UnixMilli()),
	}
	for _, reward := range rewards {
		if reward.Type == 0 || reward.Count == 0 || (!currencyRewardTypes[reward.Type] && (!itemDBInfoTypes[reward.Type] || reward.ID == 0)) {
			return fmt.Errorf("mail: compensation %q has unsupported reward type=%d id=%d count=%d", identity, reward.Type, reward.ID, reward.Count)
		}
		entry.RewardTypes = append(entry.RewardTypes, reward.Type)
		entry.RewardIDs = append(entry.RewardIDs, reward.ID)
		entry.RewardCounts = append(entry.RewardCounts, reward.Count)
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	issuedPayload, err := json.Marshal(entry.MailID)
	if err != nil {
		return err
	}
	next := s.state
	next.Opened = append([]uint64(nil), s.state.Opened...)
	next.NextDynamicMailID++
	core, err := json.Marshal(next)
	if err != nil {
		return err
	}
	changes := []stateio.EntryMutation{
		{Bucket: "dynamic", Key: strconv.FormatUint(entry.MailID, 10), Payload: payload},
		{Bucket: "issued", Key: identity, Payload: issuedPayload},
	}
	if err := s.storage.SaveWithEntries("mail", core, changes); err != nil {
		return err
	}
	s.state = next
	s.dynamic[entry.MailID] = entry
	s.issued[identity] = entry.MailID
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
