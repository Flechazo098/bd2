// Package mail owns the local new-player mailbox.  Its seed is source data,
// not a captured protobuf response: responses are encoded field-by-field here.
package mail

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/storage/stateio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"time"
)

const packetCode = 131
const openPacketCode = 132
const historyPacketCode = 138

const (
	mailHistoryPeriod             = 30 * 24 * time.Hour
	mailHistoryPageMax            = 100
	starterLimitedCostumeIdentity = "starter:limited-costumes:not-current-pickup:v1"
	starterPrestigeSkinIdentity   = "starter:prestige-skins:not-current-purchase:v1"
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
	45: true, // prestige skin ownership, one copy per mail attachment
}

// Content tickets are one-use dictionary entries; only Gacha semantics are
// supported by this mailbox, with IDs read from the current table.
func supportedItemDBInfoReward(reward gamedata.Reward) bool {
	return itemDBInfoTypes[reward.Type] && reward.ID != 0 && ((reward.Type != 19 && reward.Type != 45) || reward.Count == 1)
}

func (s *Service) supportedItemDBInfoReward(reward gamedata.Reward) bool {
	return supportedItemDBInfoReward(reward) && (reward.Type != 19 || s.contentTickets != nil && s.contentTickets.IDs[reward.ID])
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

	Starter        *Starter
	seedPath       string
	seedStamp      fileStamp
	grantSpoolPath string
	storage        stateio.ScopedEntryStore
	inventory      *assets.Inventory
	wallet         *assets.Wallet
	collection     *roster.CollectionStore
	costumeDesign  roster.CostumeDesignSource
	state          stateSnapshot
	dynamic        map[uint64]MailDBInfo
	issued         map[string]uint64
	history        map[uint64]MailDBInfo
	now            func() time.Time
}

func (s *Service) AttachContentTickets(ctx command.Context, design *gamedata.GachaContentTicketDesign) error {
	if design == nil {
		return errors.New("mail: nil content tickets")
	}
	s.contentTickets = design
	return nil
}

// AttachCostumeRewards enables ElementType 11 mail attachments. The catalog is
// GameData-backed and the collection ledger makes repeated MailOpen calls safe.
func (s *Service) AttachCostumeRewards(ctx command.Context, collection *roster.CollectionStore, design roster.CostumeDesignSource) error {
	if s == nil || collection == nil || design == nil {
		return errors.New("mail: invalid costume reward configuration")
	}

	s.collection, s.costumeDesign = collection, design
	return nil
}

type fileStamp struct {
	size    int64
	modTime int64
}

func OpenService(ctx command.Context, storage stateio.Store, starter *Starter, inventory *assets.Inventory, wallet *assets.Wallet) (*Service, error) {
	entries, ok := storage.(stateio.ScopedEntryStore)
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
	b, err := storage.Load(ctx.State, "mail")
	if err != nil {
		return nil, fmt.Errorf("mail: load state: %w", err)
	}
	if b == nil {
		if err := stateio.RequireNoEntries(entries, ctx.State, "mail", "dynamic", "issued", "history"); err != nil {
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
	rawDynamic, err := entries.ListEntries(ctx.State, "mail", "dynamic")
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
	rawIssued, err := entries.ListEntries(ctx.State, "mail", "issued")
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
	if id, exists := s.issued[starterPrestigeSkinIdentity]; exists {
		if err := validateStarterPrestigeSkinMail(s.dynamic[id]); err != nil {
			return nil, fmt.Errorf("mail: invalid persisted starter prestige-skin gift: %w", err)
		}
	}
	rawHistory, err := entries.ListEntries(ctx.State, "mail", "history")
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
	slices.Sort(s.state.Opened)
	for i := 1; i < len(s.state.Opened); i++ {
		if s.state.Opened[i] == s.state.Opened[i-1] {
			return nil, errors.New("mail: duplicate opened ID")
		}
	}
	return s, nil
}

func (s *Service) EnsurePersisted(ctx command.Context) error {

	b, err := s.storage.Load(ctx.State, "mail")
	if err != nil {
		return err
	}
	if b != nil {
		return nil
	}
	return s.persist(ctx, s.state)
}

// AttachSeedPath enables development-time hot reloading of an atomically
// replaced mail seed. It is deliberately a mailbox concern, not an HTTP debug
// endpoint: the client continues to call only the normal /MailInfo API.
// A changed file is validated before replacing the in-memory starter. A bad
// replacement leaves the last known-good starter intact and fails that request.
func (s *Service) AttachSeedPath(ctx command.Context, path string) error {
	if s == nil || path == "" {
		return errors.New("mail: invalid seed watch path")
	}
	stamp, err := seedFileStamp(path)
	if err != nil {
		return err
	}

	s.seedPath, s.seedStamp = filepath.Clean(path), stamp
	return nil
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
func (s *Service) reloadSeedIfChanged(ctx command.Context) error {
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
		if err := s.persist(ctx, updated); err != nil {
			return fmt.Errorf("mail: persist watched seed mail range: %w", err)
		}
	}
	s.Starter, s.seedStamp = next, stamp
	return nil
}

func (s *Service) commit(ctx command.Context, next stateSnapshot) error {
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
	return s.persist(ctx, next)
}

func (s *Service) commitWithEntries(ctx command.Context, next stateSnapshot, changes []stateio.EntryMutation) error {
	if len(changes) == 0 {
		return s.commit(ctx, next)
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.SaveWithEntries(ctx.State, "mail", b, changes); err != nil {
		return fmt.Errorf("mail: persist state and history: %w", err)
	}
	s.state = next
	return nil
}

func (s *Service) persist(ctx command.Context, next stateSnapshot) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.storage.SaveWithEntries(ctx.State, "mail", b, nil); err != nil {
		return fmt.Errorf("mail: persist state: %w", err)
	}
	s.state = next
	return nil
}

// EnqueueCompensation creates one durable system mail for an expired,
// completed reward. Identity is period-scoped and makes repeated rollover
// checks idempotent.
func (s *Service) EnqueueCompensation(ctx command.Context, identity, title, body string, rewards []gamedata.Reward, sentAt time.Time) error {
	grant := compensation{identity: identity, title: title, body: body, rewards: rewards, sentAt: sentAt}
	if err := grant.validate(); err != nil {
		return err
	}

	return s.enqueueCompensations(ctx, []compensation{grant})
}

// EnsureStarterLimitedCostumes issues the new-player limited-costume gift at
// most once for this account. The durable issued-identity row is written in the
// same transaction as the mail, so retries and restarts return without
// allocating another mail ID. Six copies mean one acquisition plus five
// duplicate upgrades, producing enhancement +5 when claimed.
func (s *Service) EnsureStarterLimitedCostumes(ctx command.Context, costumeIDs []uint64, sentAt time.Time) error {
	if s == nil {
		return errors.New("mail: unavailable starter limited-costume service")
	}

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
	return s.enqueueCompensations(ctx, []compensation{grant})
}

func validateStarterLimitedCostumeMail(entry MailDBInfo, design roster.CostumeDesignSource) error {
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

// EnsureStarterPrestigeSkins gives an account its prestige-skin gift if it has
// never been issued. Existing and new accounts use the same durable identity;
// unclaimed or claimed mail never causes a second gift on restart.
func (s *Service) EnsureStarterPrestigeSkins(ctx command.Context, designIDs []uint64, sentAt time.Time) error {
	if s == nil {
		return errors.New("mail: unavailable starter prestige-skin service")
	}

	if id, issued := s.issued[starterPrestigeSkinIdentity]; issued {
		return validateStarterPrestigeSkinMail(s.dynamic[id])
	}
	if len(designIDs) == 0 {
		return errors.New("mail: starter prestige-skin gift is empty")
	}
	seen := make(map[uint64]bool, len(designIDs))
	rewards := make([]gamedata.Reward, 0, len(designIDs))
	for _, designID := range designIDs {
		if designID == 0 || seen[designID] {
			return fmt.Errorf("mail: invalid starter prestige skin %d", designID)
		}
		seen[designID] = true
		rewards = append(rewards, gamedata.Reward{Type: 45, ID: designID, Count: 1})
	}
	grant := compensation{identity: starterPrestigeSkinIdentity, title: "Prestige Skin Gift", body: "Prestige skins not currently available on the cash purchase page.", rewards: rewards, sentAt: sentAt}
	if err := grant.validate(); err != nil {
		return err
	}
	return s.enqueueCompensations(ctx, []compensation{grant})
}

func validateStarterPrestigeSkinMail(entry MailDBInfo) error {
	if entry.MailID == 0 || len(entry.RewardTypes) == 0 || len(entry.RewardTypes) != len(entry.RewardIDs) || len(entry.RewardTypes) != len(entry.RewardCounts) {
		return errors.New("invalid starter prestige-skin reward arrays")
	}
	seen := make(map[uint64]bool, len(entry.RewardTypes))
	for i, typ := range entry.RewardTypes {
		id := entry.RewardIDs[i]
		if typ != 45 || id == 0 || entry.RewardCounts[i] != 1 || seen[id] {
			return errors.New("starter prestige-skin gift must contain unique Type45 designs")
		}
		seen[id] = true
	}
	return nil
}

// enqueueCompensations shares the EnqueueCompensation allocator and durable
// identity ledger. The entire batch is prepared before a single atomic write;
// neither storage nor memory can retain a partially imported grant spool.
// Caller holds s.mu and has validated every grant.
func (s *Service) enqueueCompensations(ctx command.Context, grants []compensation) error {
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
	if err := s.storage.SaveWithEntries(ctx.State, "mail", core, changes); err != nil {
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
	return slices.Contains(values, want)
}
