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

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/versionconfig"
	"bd2server/internal/server/wire"
)

const packetCode = 131
const openPacketCode = 132
const historyPacketCode = 138

const (
	mailHistoryPeriod             = 30 * 24 * time.Hour
	mailHistoryPageMax            = 100
	starterLimitedCostumeIdentity = "starter:limited-costumes:not-current-pickup:v1"
)

// itemDBInfoTypes are ElementType values whose successful mail claim is
// represented by ItemDBInfo in RewardDBInfoBundle.  They are deliberately
// separate from character (6), equipment (10), and costume (11): the client
// requires CharDBInfo, EquipDBInfo, and CostumeDBInfo for those rewards, and
// this mailbox owns no such domain stores.  The values are from the client
// DataManager.GetItemInfo and ItemDBInfo, not inferred from table names.
var itemDBInfoTypes = map[uint64]bool{
	5:  true, // food
	7:  true, // cooking recipe/result
	8:  true, // resource
	9:  true, // random box
	13: true, // quest item
	14: true, // use item
	17: true, // collection item
	19: true, // audited one-use content ticket; restricted below
	27: true, // my-room item
	29: true, // instant-use item
}

// Content tickets are one-use dictionary entries; only Gacha semantics are
// supported by this mailbox, with IDs read from the current table.
func supportedItemDBInfoReward(reward gamedata.Reward) bool {
	return itemDBInfoTypes[reward.Type] && reward.ID != 0 && (reward.Type != 19 || reward.Count == 1)
}

func (s *Service) supportedItemDBInfoReward(reward gamedata.Reward) bool {
	return itemDBInfoTypes[reward.Type] && reward.ID != 0 && (reward.Type != 19 || reward.Count == 1 && s.contentTickets != nil && s.contentTickets.IDs[reward.ID])
}

var currencyRewardTypes = map[uint64]bool{
	2:  true, // paid jewelry
	3:  true, // free jewelry
	4:  true, // gold
	12: true, // catalyst / talent elixir
	20: true, // mileage / golden thread
}

// MailDBInfo is the persisted shape used by the client.  A mail either has a
// literal title/body (ordinary system mail), or a TemplateID and Sender (the
// localized/template-driven mail form).  Reward fields are parallel arrays:
// type, table ID, and amount respectively.
type MailDBInfo struct {
	MailID            uint64   `json:"mail_id"`
	MailType          uint64   `json:"mail_type,omitempty"`
	TemplateID        uint64   `json:"template_id,omitempty"`
	Sender            string   `json:"sender,omitempty"`
	Title             string   `json:"title,omitempty"`
	Body              string   `json:"body,omitempty"`
	ExpiresAt         uint64   `json:"expires_at"`
	RewardTypes       []uint64 `json:"reward_types,omitempty"`
	RewardIDs         []uint64 `json:"reward_ids,omitempty"`
	RewardCounts      []uint64 `json:"reward_counts,omitempty"`
	IsOpen            bool     `json:"is_open,omitempty"`
	OpenTime          uint64   `json:"open_time,omitempty"`
	SentAt            uint64   `json:"sent_at"`
	HistoryDeleteTime uint64   `json:"history_delete_time,omitempty"`
	IsCash            bool     `json:"is_cash,omitempty"`
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
	if s == nil || s.Version != versionconfig.State() {
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
		for i, typ := range m.RewardTypes {
			if typ == 19 && (m.RewardIDs[i] == 0 || m.RewardCounts[i] != 1) {
				return errors.New("mail: content ticket reward requires an ID and count 1")
			}
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
	if m.IsOpen {
		result = wire.AppendVarint(result, 11, 1)
	}
	if m.OpenTime != 0 {
		result = wire.AppendVarint(result, 12, m.OpenTime)
	}
	result = wire.AppendVarint(result, 13, m.SentAt)
	if m.HistoryDeleteTime != 0 {
		result = wire.AppendVarint(result, 14, m.HistoryDeleteTime)
	}
	if m.IsCash {
		result = wire.AppendVarint(result, 15, 1)
	}
	return result
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

// Service owns mailbox visibility, claim history, and idempotent reward
// delivery. Starter is immutable source data; mutable rows live in the player
// state alongside the bounded core snapshot.
type Service struct {
	contentTickets    *gamedata.GachaContentTicketDesign
	attendanceEconomy AttendanceRewardEconomy
	cashEconomy       CashRewardEconomy
	cashTemplates     map[uint64]bool
	beforeMailID      uint64
	mu                sync.Mutex
	Starter           *Starter
	seedPath          string
	seedStamp         fileStamp
	grantSpoolPath    string
	storage           stateio.AtomicEntryStore
	inventory         *player.Inventory
	wallet            *player.Wallet
	collection        *player.CollectionStore
	costumeDesign     player.CostumeDesignSource
	state             stateSnapshot
	dynamic           map[uint64]MailDBInfo
	issued            map[string]uint64
	history           map[uint64]MailDBInfo
	now               func() time.Time
}

func (s *Service) AttachContentTickets(design *gamedata.GachaContentTicketDesign) error {
	if design == nil {
		return errors.New("mail: nil content tickets")
	}
	s.contentTickets = design
	return nil
}

// AttachCostumeRewards enables ElementType 11 mail attachments. The catalog is
// GameData-backed and the collection ledger makes repeated MailOpen calls safe.
func (s *Service) AttachCostumeRewards(collection *player.CollectionStore, design player.CostumeDesignSource) error {
	if s == nil || collection == nil || design == nil {
		return errors.New("mail: invalid costume reward configuration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collection, s.costumeDesign = collection, design
	return nil
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
		state:   stateSnapshot{Version: versionconfig.State(), NextDynamicMailID: starter.MaxMailID + 1},
		dynamic: map[uint64]MailDBInfo{}, issued: map[string]uint64{}, history: map[uint64]MailDBInfo{}, now: time.Now}
	b, err := storage.Load("mail")
	if err != nil {
		return nil, fmt.Errorf("mail: load state: %w", err)
	}
	if b == nil {
		if err := stateio.RequireNoEntries(entries, "mail", "dynamic", "issued", "history"); err != nil {
			return nil, fmt.Errorf("mail: invalid storage: %w", err)
		}
		return s, nil
	}
	if err := stateio.RequireExactJSONObject(b, "version", "opened", "next_dynamic_mail_id"); err != nil {
		return nil, fmt.Errorf("mail: incompatible state layout: %w", err)
	}
	if err := json.Unmarshal(b, &s.state); err != nil || s.state.Version != versionconfig.State() || s.state.NextDynamicMailID == 0 {
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
	if id, exists := s.issued[starterLimitedCostumeIdentity]; exists {
		if err := validateStarterLimitedCostumeMail(s.dynamic[id], nil); err != nil {
			return nil, fmt.Errorf("mail: invalid persisted starter limited-costume gift: %w", err)
		}
	}
	rawHistory, err := entries.ListEntries("mail", "history")
	if err != nil {
		return nil, err
	}
	for key, payload := range rawHistory {
		id, parseErr := strconv.ParseUint(key, 10, 64)
		var entry MailDBInfo
		if parseErr != nil || id == 0 || json.Unmarshal(payload, &entry) != nil || entry.MailID != id ||
			!entry.IsOpen || entry.OpenTime == 0 || entry.HistoryDeleteTime <= entry.OpenTime ||
			len(entry.RewardTypes) != len(entry.RewardIDs) || len(entry.RewardTypes) != len(entry.RewardCounts) {
			return nil, fmt.Errorf("mail: invalid history entry %q", key)
		}
		s.history[id] = entry
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
	if path != "/MailInfo" && path != "/MailOpen" && path != "/MailHistoryInfo" && path != "/CashMailInfo" {
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
		grants, err := readGrantSpool(s.grantSpoolPath)
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.reloadSeedIfChanged(); err != nil {
			return 0, nil, true, err
		}
		if err := s.enqueueCompensations(grants); err != nil {
			return 0, nil, true, err
		}
		return packetCode, s.info(), true, nil
	}
	if path == "/CashMailInfo" {
		response, err := s.cashInfo(request)
		return 140, response, true, err
	}
	if path == "/MailHistoryInfo" {
		response, err := s.historyInfo(request)
		return historyPacketCode, response, true, err
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
		if entry.IsCash || containsID(s.state.Opened, entry.MailID) {
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
		if s.dynamic[id].IsCash || containsID(s.state.Opened, id) {
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
	// Validate the entire attendance/cash batch before any reward domain writes.
	for _, entry := range selected {
		if entry.IsCash {
			if s.cashEconomy == nil {
				return nil, errors.New("mail: cash reward economy unavailable")
			}
			if err := validateCashAttachments(mailAttachments(entry)); err != nil {
				return nil, err
			}
		}
		if s.isAttendanceMail(entry.MailID) {
			if s.attendanceEconomy == nil {
				return nil, errors.New("mail: attendance reward economy unavailable")
			}
			if !containsID(s.state.Opened, entry.MailID) && uint64(s.now().UnixMilli()) >= entry.ExpiresAt {
				return nil, errors.New("mail: attendance mail expired")
			}
			if err := s.validateAttendanceAttachments(mailAttachments(entry)); err != nil {
				return nil, err
			}
		}
	}

	var bundle []byte
	next := stateSnapshot{Version: s.state.Version, Opened: append([]uint64(nil), s.state.Opened...), NextDynamicMailID: s.state.NextDynamicMailID}
	openedAt := s.now().UTC()
	changes := make([]stateio.EntryMutation, 0, len(selected))
	newHistory := make([]MailDBInfo, 0, len(selected))
	for _, entry := range selected {
		identity := fmt.Sprintf("mail:%d", entry.MailID)
		if entry.IsCash {
			granted, err := s.cashEconomy.ApplyResolved(identity+":cash", nil, mailAttachments(entry))
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, granted...)
		} else if s.isAttendanceMail(entry.MailID) {
			granted, err := s.attendanceEconomy.Apply(identity+":attendance", nil, mailAttachments(entry))
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, granted...)
		} else {
			rewards := make([]gamedata.Reward, len(entry.RewardTypes))
			var items []gamedata.BattleReward
			var costumeIDs []uint64
			for i := range entry.RewardTypes {
				reward := gamedata.Reward{Type: entry.RewardTypes[i], ID: entry.RewardIDs[i], Count: entry.RewardCounts[i]}
				rewards[i] = reward
				switch {
				case currencyRewardTypes[reward.Type]:
					currency := wire.AppendVarint(nil, 3, reward.Type)
					currency = wire.AppendVarint(currency, 4, reward.Count)
					bundle = wire.AppendBytes(bundle, 1, currency)
				case reward.Type == 11:
					if s.collection == nil || s.costumeDesign == nil || reward.ID == 0 || reward.Count == 0 || reward.Count > 6 {
						return nil, errors.New("mail: costume reward service unavailable or reward invalid")
					}
					for copy := uint64(0); copy < reward.Count; copy++ {
						costumeIDs = append(costumeIDs, reward.ID)
					}
				case reward.Type == 28:
					// DataManager recognizes this type, but RewardDBInfoBundle
					// carries MyRoomTrophyDBInfo in a separate field.  Encoding it as
					// ItemDBInfo would make the local state and client model disagree.
					return nil, errors.New("mail: my-room trophy rewards require MyRoomTrophyDBInfo")
				default:
					if !s.supportedItemDBInfoReward(reward) {
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
			if len(costumeIDs) != 0 {
				grant, err := s.collection.GrantCostumes(identity+":costumes", costumeIDs, s.costumeDesign)
				if err != nil {
					return nil, err
				}
				var mileage uint64
				for _, exchange := range grant.Exchanges {
					if exchange.ExchangeItemType != 20 || ^uint64(0)-mileage < exchange.ExchangeCount {
						return nil, errors.New("mail: unsupported costume overflow exchange")
					}
					mileage += exchange.ExchangeCount
				}
				if mileage != 0 {
					if _, err := s.wallet.GrantMileageOnce(identity+":costume-overflow", mileage); err != nil {
						return nil, err
					}
				}
				bundle = appendCollectionRewardBundle(bundle, s.collection, grant)
			}
		}
		if !containsID(next.Opened, entry.MailID) {
			next.Opened = append(next.Opened, entry.MailID)
		}
		if _, exists := s.history[entry.MailID]; !exists {
			history := entry
			history.IsOpen = true
			history.OpenTime = uint64(openedAt.UnixMilli())
			history.HistoryDeleteTime = uint64(openedAt.Add(mailHistoryPeriod).UnixMilli())
			payload, err := json.Marshal(history)
			if err != nil {
				return nil, err
			}
			changes = append(changes, stateio.EntryMutation{Bucket: "history", Key: strconv.FormatUint(entry.MailID, 10), Payload: payload})
			newHistory = append(newHistory, history)
		}
	}
	sort.Slice(next.Opened, func(i, j int) bool { return next.Opened[i] < next.Opened[j] })
	if err := s.commitWithEntries(next, changes); err != nil {
		return nil, err
	}
	for _, entry := range newHistory {
		s.history[entry.MailID] = entry
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}

func appendCollectionRewardBundle(bundle []byte, collection *player.CollectionStore, grant player.CollectionGrant) []byte {
	newCharacters := make(map[uint64]player.Character, len(grant.CharacterIndices))
	for _, index := range grant.CharacterIndices {
		if character, found := collection.CharacterByIndex(index); found {
			newCharacters[character.ConnectPotentialCostume] = character
			bundle = wire.AppendBytes(bundle, 2, player.CharacterWire(character))
		}
	}
	for _, index := range grant.CostumeIndices {
		if costume, found := collection.CostumeByIndex(index); found {
			bundle = wire.AppendBytes(bundle, 3, player.CostumeWire(costume))
		}
	}
	for position, costumeID := range grant.ViewCostumeIDs {
		view := wire.AppendVarint(nil, 2, costumeID)
		view = wire.AppendVarint(view, 3, 11)
		view = wire.AppendVarint(view, 4, 1)
		bundle = wire.AppendBytes(bundle, 6, view)
		if character, found := newCharacters[costumeID]; found {
			charView := wire.AppendVarint(nil, 2, character.ID)
			charView = wire.AppendVarint(charView, 3, 6)
			charView = wire.AppendVarint(charView, 4, 1)
			if position != 0 {
				charView = wire.AppendVarint(charView, 6, uint64(position))
			}
			bundle = wire.AppendBytes(bundle, 6, charView)
		}
	}
	for _, upgrade := range grant.Upgrades {
		info := wire.AppendVarint(nil, 1, upgrade.InvenIndex)
		info = wire.AppendVarint(info, 2, 11)
		info = wire.AppendVarint(info, 3, upgrade.CostumeID)
		if upgrade.Before != 0 {
			info = wire.AppendVarint(info, 4, upgrade.Before)
		}
		info = wire.AppendVarint(info, 5, upgrade.After)
		if upgrade.SortID != 0 {
			info = wire.AppendVarint(info, 6, upgrade.SortID)
		}
		bundle = wire.AppendBytes(bundle, 9, info)
	}
	var mileage uint64
	for _, exchange := range grant.Exchanges {
		info := wire.AppendVarint(nil, 1, exchange.OriginalItemType)
		info = wire.AppendVarint(info, 2, exchange.OriginalItemID)
		info = wire.AppendVarint(info, 3, exchange.OriginalCount)
		info = wire.AppendVarint(info, 4, exchange.ExchangeItemType)
		if exchange.ExchangeItemID != 0 {
			info = wire.AppendVarint(info, 5, exchange.ExchangeItemID)
		}
		info = wire.AppendVarint(info, 6, exchange.ExchangeCount)
		if exchange.SortID != 0 {
			info = wire.AppendVarint(info, 7, exchange.SortID)
		}
		bundle = wire.AppendBytes(bundle, 8, info)
		mileage += exchange.ExchangeCount
	}
	if mileage != 0 {
		repaid := wire.AppendVarint(nil, 2, 20)
		repaid = wire.AppendVarint(repaid, 3, mileage)
		bundle = wire.AppendBytes(bundle, 10, repaid)
	}
	return bundle
}

func (s *Service) historyInfo(request []byte) ([]byte, error) {
	start, _, err := wire.Varint(request, 2)
	if err != nil {
		return nil, errors.New("mail: invalid history start index")
	}
	count, present, err := wire.Varint(request, 3)
	if err != nil || !present || count == 0 {
		return nil, errors.New("mail: invalid history select count")
	}
	if count > mailHistoryPageMax {
		count = mailHistoryPageMax
	}
	now := uint64(s.now().UTC().UnixMilli())
	ids := make([]uint64, 0, len(s.history))
	for id, entry := range s.history {
		if entry.HistoryDeleteTime > now {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	total := len(ids)
	var result []byte
	selected := uint64(0)
	for _, id := range ids {
		if start != 0 && id >= start {
			continue
		}
		if selected >= count {
			break
		}
		result = wire.AppendBytes(result, 1, s.history[id].encode())
		selected++
	}
	return wire.AppendVarint(result, 2, uint64(total)), nil
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

func (s *Service) commitWithEntries(next stateSnapshot, changes []stateio.EntryMutation) error {
	if len(changes) == 0 {
		return s.commit(next)
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.SaveWithEntries("mail", b, changes); err != nil {
		return fmt.Errorf("mail: persist state and history: %w", err)
	}
	s.state = next
	return nil
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
	grant := compensation{identity: identity, title: title, body: body, rewards: rewards, sentAt: sentAt}
	if err := grant.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueCompensations([]compensation{grant})
}

// EnsureStarterLimitedCostumes issues the new-player limited-costume gift at
// most once for this account. The durable issued-identity row is written in the
// same transaction as the mail, so retries and restarts return without
// allocating another mail ID. Six copies mean one acquisition plus five
// duplicate upgrades, producing enhancement +5 when claimed.
func (s *Service) EnsureStarterLimitedCostumes(costumeIDs []uint64, sentAt time.Time) error {
	if s == nil {
		return errors.New("mail: unavailable starter limited-costume service")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, issued := s.issued[starterLimitedCostumeIdentity]; issued {
		return validateStarterLimitedCostumeMail(s.dynamic[id], s.costumeDesign)
	}
	if len(costumeIDs) == 0 || s.costumeDesign == nil {
		return errors.New("mail: starter limited-costume gift is empty or unavailable")
	}
	seen := make(map[uint64]bool, len(costumeIDs))
	rewards := make([]gamedata.Reward, 0, len(costumeIDs))
	for _, costumeID := range costumeIDs {
		design, ok := s.costumeDesign.Character(costumeID)
		if costumeID == 0 || seen[costumeID] || !ok || design.CostumeMaxLevel != 5 {
			return fmt.Errorf("mail: invalid starter limited costume %d", costumeID)
		}
		seen[costumeID] = true
		rewards = append(rewards, gamedata.Reward{Type: 11, ID: costumeID, Count: 6})
	}
	grant := compensation{
		identity: starterLimitedCostumeIdentity,
		title:    "New Player Limited Costumes",
		body:     "Limited costumes not featured in the current pickup banners. Each costume is enhanced to +5 when claimed.",
		rewards:  rewards,
		sentAt:   sentAt,
	}
	if err := grant.validate(); err != nil {
		return err
	}
	return s.enqueueCompensations([]compensation{grant})
}

func validateStarterLimitedCostumeMail(entry MailDBInfo, design player.CostumeDesignSource) error {
	if entry.MailID == 0 || len(entry.RewardTypes) == 0 || len(entry.RewardTypes) != len(entry.RewardIDs) || len(entry.RewardTypes) != len(entry.RewardCounts) {
		return errors.New("invalid starter gift reward arrays")
	}
	seen := make(map[uint64]bool, len(entry.RewardTypes))
	for i, typ := range entry.RewardTypes {
		id := entry.RewardIDs[i]
		if typ != 11 || id == 0 || entry.RewardCounts[i] != 6 || seen[id] {
			return errors.New("starter gift must contain unique six-copy costumes")
		}
		seen[id] = true
		if design != nil {
			costume, ok := design.Character(id)
			if !ok || costume.CostumeMaxLevel != 5 {
				return fmt.Errorf("starter gift costume %d is unavailable or not enhancement +5", id)
			}
		}
	}
	return nil
}

// enqueueCompensations shares the EnqueueCompensation allocator and durable
// identity ledger. The entire batch is prepared before a single atomic write;
// neither storage nor memory can retain a partially imported grant spool.
// Caller holds s.mu and has validated every grant.
func (s *Service) enqueueCompensations(grants []compensation) error {
	next := s.state
	next.Opened = append([]uint64(nil), s.state.Opened...)
	var changes []stateio.EntryMutation
	var entries []MailDBInfo
	var identities []string
	for _, grant := range grants {
		if _, exists := s.issued[grant.identity]; exists {
			continue
		}
		if next.NextDynamicMailID == 0 || next.NextDynamicMailID == ^uint64(0) {
			return errors.New("mail: dynamic mail ID exhausted")
		}
		entry := MailDBInfo{
			MailID: next.NextDynamicMailID, MailType: 2, Title: grant.title, Body: grant.body,
			SentAt: uint64(grant.sentAt.UTC().UnixMilli()), ExpiresAt: uint64(grant.sentAt.UTC().Add(30 * 24 * time.Hour).UnixMilli()),
		}
		if grant.isCash {
			entry.IsCash = true
			entry.TemplateID = grant.templateID
			entry.MailType = 0
			entry.ExpiresAt = 253402300799000 // cash mail has no claim expiry in the client
		}
		for _, reward := range grant.rewards {
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
		changes = append(changes,
			stateio.EntryMutation{Bucket: "dynamic", Key: strconv.FormatUint(entry.MailID, 10), Payload: payload},
			stateio.EntryMutation{Bucket: "issued", Key: grant.identity, Payload: issuedPayload},
		)
		entries = append(entries, entry)
		identities = append(identities, grant.identity)
		next.NextDynamicMailID++
	}
	if len(entries) == 0 {
		return nil
	}
	core, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.SaveWithEntries("mail", core, changes); err != nil {
		return err
	}
	s.state = next
	for i, entry := range entries {
		s.dynamic[entry.MailID] = entry
		s.issued[identities[i]] = entry.MailID
	}
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
