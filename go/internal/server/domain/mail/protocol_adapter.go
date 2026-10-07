package mail

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
)

func (s *Service) AfterDispatch(ctx command.Context, _ string, _ []byte, _ []byte) ([]byte, error) {

	// Allocation is monotonic. Without a new ID, no existing row can match
	// the notification window, so read-only packets need not scan the inbox.
	if s.state.NextDynamicMailID == s.beforeMailID {
		return nil, nil
	}
	now := uint64(s.now().UnixMilli())
	for id, entry := range s.dynamic {
		if id >= s.beforeMailID && !containsID(s.state.Opened, id) && entry.ExpiresAt > now {
			return wire.AppendVarint(nil, 1, 1), nil // Notify.IsNewMail
		}
	}
	return nil, nil
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

func (s *Starter) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
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

func (s *Service) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
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

	if path == "/MailInfo" {
		grants, err := readGrantSpool(s.grantSpoolPath, ctx.AccountID)
		if err != nil {
			return 0, nil, true, err
		}
		if err := s.reloadSeedIfChanged(ctx); err != nil {
			return 0, nil, true, err
		}
		if err := s.enqueueCompensations(ctx, grants); err != nil {
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
	response, err := s.open(ctx, request)
	return openPacketCode, response, true, err
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
	slices.Sort(dynamicIDs)
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

func (s *Service) open(ctx command.Context, request []byte) ([]byte, error) {
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
			granted, err := s.cashEconomy.ApplyResolved(ctx, identity+":cash", nil, mailAttachments(entry))
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, granted...)
		} else if s.isAttendanceMail(entry.MailID) {
			granted, err := s.attendanceEconomy.Apply(ctx, identity+":attendance", nil, mailAttachments(entry))
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
					items = append(items, gamedata.BattleReward{Type: reward.Type, ID: reward.ID, Count: reward.Count}) //nolint:staticcheck // S1016
				}
			}
			if _, err := s.wallet.GrantQuestOnce(ctx, identity+":currency", rewards); err != nil {
				return nil, err
			}
			granted, err := s.inventory.GrantOnce(ctx, identity+":items", items)
			if err != nil {
				return nil, err
			}
			if len(granted) == 0 {
				granted = s.inventory.GrantedItems(identity + ":items")
			}
			for _, item := range granted {
				bundle = wire.AppendBytes(bundle, 1, assets.ItemWire(item))
				view := wire.AppendVarint(nil, 2, item.ID)
				view = wire.AppendVarint(view, 3, item.Type)
				view = wire.AppendVarint(view, 4, item.Count)
				bundle = wire.AppendBytes(bundle, 6, view)
			}
			if len(costumeIDs) != 0 {
				grant, err := s.collection.GrantCostumes(ctx, identity+":costumes", costumeIDs, s.costumeDesign)
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
					if _, err := s.wallet.GrantMileageOnce(ctx, identity+":costume-overflow", mileage); err != nil {
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
	slices.Sort(next.Opened)
	if err := s.commitWithEntries(ctx, next, changes); err != nil {
		return nil, err
	}
	for _, entry := range newHistory {
		s.history[entry.MailID] = entry
	}
	return wire.AppendBytes(nil, 1, bundle), nil
}

func appendCollectionRewardBundle(bundle []byte, collection *roster.CollectionStore, grant roster.CollectionGrant) []byte {
	newCharacters := make(map[uint64]roster.Character, len(grant.CharacterIndices))
	for _, index := range grant.CharacterIndices {
		if character, found := collection.CharacterByIndex(index); found {
			newCharacters[character.ConnectPotentialCostume] = character
			bundle = wire.AppendBytes(bundle, 2, roster.CharacterWire(character))
		}
	}
	for _, index := range grant.CostumeIndices {
		if costume, found := collection.CostumeByIndex(index); found {
			bundle = wire.AppendBytes(bundle, 3, roster.CostumeWire(costume))
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

func (s *Service) cashInfo(request []byte) ([]byte, error) {
	start, _, err := wire.Varint(request, 2)
	if err != nil || start > math.MaxInt64 {
		return nil, errors.New("mail: invalid cash cursor")
	}
	count, present, err := wire.Varint(request, 3)
	if err != nil || !present || count == 0 || count > math.MaxInt32 {
		return nil, errors.New("mail: invalid cash select count")
	}
	if count > mailHistoryPageMax {
		count = mailHistoryPageMax
	}
	entries := map[uint64]MailDBInfo{}
	for _, entry := range s.Starter.Mails {
		if entry.IsCash && !containsID(s.state.Opened, entry.MailID) {
			entries[entry.MailID] = entry
		}
	}
	for id, entry := range s.dynamic {
		if entry.IsCash && !containsID(s.state.Opened, id) {
			entries[id] = entry
		}
	}
	ids := make([]uint64, 0, len(entries))
	var max uint64
	for id := range entries {
		ids = append(ids, id)
		if id > max {
			max = id
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	var result []byte
	var selected uint64
	for _, id := range ids {
		if start != 0 && id >= start {
			continue
		}
		if selected >= count {
			break
		}
		result = wire.AppendBytes(result, 1, entries[id].encode())
		selected++
	}
	result = wire.AppendVarint(result, 2, uint64(len(entries)))
	return wire.AppendVarint(result, 3, max), nil
}
