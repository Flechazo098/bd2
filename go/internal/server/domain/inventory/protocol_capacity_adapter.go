package inventory

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func (s *InventorySlots) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	var code int
	var current *uint64
	var rule gamedata.InventorySlotRule
	switch path {
	case "/InvenAddSlot":
		code, current, rule = 24, &s.state.Items, s.design.Items
	case "/StorageAddSlot":
		code, current, rule = 25, &s.state.Storage, s.design.Storage
	case "/EquipAddSlot":
		code, current, rule = 39, &s.state.Equipment, s.design.Equipment
	case "/EquipStorageAddSlot":
		code, current, rule = 82, &s.state.EquipmentStorage, s.design.EquipmentStorage
	default:
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, errors.New("player: inventory slot request missing sequence")
	}
	count, found, err := wire.Varint(request, 2)
	if err != nil || !found || count == 0 {
		return 0, nil, true, errors.New("player: inventory slot request invalid count")
	}

	key := fmt.Sprintf("%s:%d", path, seq)
	if prior, ok := s.replies[key]; ok {
		return prior.code, append([]byte(nil), prior.body...), true, nil
	}
	if *current > rule.Maximum || count > rule.Maximum-*current {
		return 0, nil, true, errors.New("player: inventory slot expansion exceeds GameData maximum")
	}
	cost, err := inventorySlotPrice(rule, *current, count)
	if err != nil {
		return 0, nil, true, err
	}
	identity := fmt.Sprintf("inventory-slot:%s:%s:%d", path, ctx.SessionID, seq)
	var chargeErr error
	switch rule.PriceType {
	case 2:
		_, chargeErr = s.wallet.SpendJewelryOnce(ctx, identity, cost)
	case 3:
		_, chargeErr = s.wallet.SpendFreeJewelryOnce(ctx, identity, cost)
	case 4:
		_, chargeErr = s.wallet.SpendGoldOnce(ctx, identity, cost)
	default:
		chargeErr = errors.New("unsupported slot currency")
	}
	if err := chargeErr; err != nil {
		return 0, nil, true, fmt.Errorf("player: inventory slot price: %w", err)
	}
	next := s.state
	switch path {
	case "/InvenAddSlot":
		next.Items += count
	case "/StorageAddSlot":
		next.Storage += count
	case "/EquipAddSlot":
		next.Equipment += count
	case "/EquipStorageAddSlot":
		next.EquipmentStorage += count
	}
	if err := s.persistLocked(ctx, next); err != nil {
		return 0, nil, true, err
	}
	s.replies[key] = inventorySlotReply{code: code}
	return code, nil, true, nil
}

func (s *ContentOpenService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	if path != "/ContentOpen" {
		return 0, nil, false, nil
	}
	var seq, kind uint64
	var haveSeq, haveKind bool
	err := wire.Walk(request, func(field wire.Field) error {
		switch field.Number {
		case 1, 2:
			if field.Type != 0 {
				return errors.New("player: ContentOpen field must be varint")
			}
			value, _ := binary.Uvarint(field.Value)
			if field.Number == 1 {
				if haveSeq {
					return errors.New("player: duplicate ContentOpen sequence")
				}
				seq, haveSeq = value, true
			} else {
				if haveKind {
					return errors.New("player: duplicate ContentOpen type")
				}
				kind, haveKind = value, true
			}
		}
		return nil
	})
	if err != nil || !haveSeq || seq == 0 || seq > math.MaxInt32 || !haveKind || kind != 1 {
		if err == nil {
			err = errors.New("player: invalid ContentOpen sequence or unsupported type")
		}
		return 0, nil, true, err
	}

	if ctx.SessionID == "" {
		return 0, nil, true, errors.New("player: ContentOpen requires authenticated session")
	}
	sessionHash := sha256.Sum256([]byte(ctx.SessionID))
	key := hex.EncodeToString(sessionHash[:]) + ":" + strconv.FormatUint(seq, 10)
	requestHash := sha256.Sum256(request)
	digest := hex.EncodeToString(requestHash[:])
	state, err := s.loadState(ctx)
	if err != nil {
		return 0, nil, true, err
	}
	if receipt, ok := state.Receipts[key]; ok {
		if receipt.Digest != digest {
			return 0, nil, true, errors.New("player: ContentOpen sequence reused with different request")
		}
		return contentOpenPacketCode, append([]byte(nil), receipt.Body...), true, nil
	}
	var prerequisite, complete bool
	var bundle []byte
	for _, item := range s.inventory.All(ctx) {
		if item.Type != 19 || item.Count == 0 {
			continue
		}
		prerequisite = prerequisite || item.ID == s.design.Prerequisite.TicketID
		if item.ID == s.design.Completion.TicketID && !complete {
			if item.Count != 1 || item.InvenIndex == 0 || item.InvenIndex > math.MaxInt64 {
				return 0, nil, true, errors.New("player: invalid owned ContentOpen completion ticket")
			}
			complete = true
			bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
		}
	}
	if !complete {
		if !prerequisite {
			return 0, nil, true, errors.New("player: ContentOpen prerequisite ticket is not owned")
		}
		level, err := s.squadLevel()
		if err != nil {
			return 0, nil, true, fmt.Errorf("player: ContentOpen squad level: %w", err)
		}
		if level < max(s.design.Prerequisite.SquadLevel, s.design.Completion.SquadLevel) {
			return 0, nil, true, errors.New("player: ContentOpen squad level is too low")
		}
		items, err := s.inventory.GrantOnce(ctx, "content-open:"+strconv.FormatUint(s.design.Completion.TicketID, 10), []gamedata.BattleReward{{Type: 19, ID: s.design.Completion.TicketID, Count: 1}})
		if err != nil {
			return 0, nil, true, fmt.Errorf("player: grant ContentOpen ticket: %w", err)
		}
		if len(items) != 1 {
			return 0, nil, true, errors.New("player: ContentOpen grant marker has no completion ticket")
		}
		for _, item := range items {
			bundle = wire.AppendBytes(bundle, 1, ItemWire(item))
		}
	}
	body := wire.AppendBytes(nil, 1, bundle)
	state.Receipts[key] = contentOpenReceipt{Digest: digest, Body: body}
	data, err := json.Marshal(state)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.store.Save(ctx.State, "content_open", data); err != nil {
		return 0, nil, true, fmt.Errorf("player: persist ContentOpen receipt: %w", err)
	}
	return contentOpenPacketCode, append([]byte(nil), body...), true, nil
}

func NewContentOpenService(ctx command.Context, design *gamedata.ContentOpeningDesign, inventory *Inventory, store stateio.Store, squadLevel func() (uint64, error)) (*ContentOpenService, error) {
	if design == nil || inventory == nil || store == nil || squadLevel == nil || design.Prerequisite.TicketID == 0 || design.Completion.TicketID == 0 || design.Completion.TicketID == design.Prerequisite.TicketID {
		return nil, errors.New("player: incomplete ContentOpen service")
	}
	s := &ContentOpenService{design: design, inventory: inventory, store: store, squadLevel: squadLevel}
	if _, err := s.loadState(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ContentOpenService) loadState(ctx command.Context) (contentOpenState, error) {
	state := contentOpenState{Receipts: make(map[string]contentOpenReceipt)}
	data, err := s.store.Load(ctx.State, "content_open")
	if err != nil || data == nil {
		return state, err
	}
	if err := stateio.RequireExactJSONObject(data, "receipts"); err != nil {
		return state, fmt.Errorf("player: invalid ContentOpen state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Receipts == nil {
		return state, errors.New("player: invalid ContentOpen receipts")
	}
	validHash := func(value string) bool {
		decoded, err := hex.DecodeString(value)
		return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
	}
	for key, receipt := range state.Receipts {
		hash, sequence, found := strings.Cut(key, ":")
		seq, err := strconv.ParseUint(sequence, 10, 32)
		if !found || !validHash(hash) || err != nil || seq == 0 || seq > math.MaxInt32 || strconv.FormatUint(seq, 10) != sequence || !validHash(receipt.Digest) {
			return state, errors.New("player: invalid ContentOpen receipt identity")
		}
		if err := validateContentOpenBody(receipt.Body, s.design.Completion.TicketID); err != nil {
			return state, err
		}
	}
	return state, nil
}

func validateContentOpenBody(body []byte, ticketID uint64) error {
	unwrap := func(data []byte) ([]byte, error) {
		var value []byte
		var found bool
		err := wire.Walk(data, func(field wire.Field) error {
			if field.Number != 1 || field.Type != 2 || found {
				return errors.New("player: invalid ContentOpen receipt bundle")
			}
			value, found = field.Value, true
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("player: missing ContentOpen receipt bundle")
		}
		return value, nil
	}
	bundle, err := unwrap(body)
	if err != nil {
		return err
	}
	item, err := unwrap(bundle)
	if err != nil {
		return err
	}
	for field, want := range map[int]uint64{2: ticketID, 3: 19, 4: 1} {
		value, present, err := wire.Varint(item, field)
		if err != nil || !present || value != want {
			return errors.New("player: invalid ContentOpen receipt ticket")
		}
	}
	index, present, err := wire.Varint(item, 1)
	if err != nil || !present || index == 0 || index > math.MaxInt64 {
		return errors.New("player: invalid ContentOpen receipt ticket index")
	}
	return nil
}
