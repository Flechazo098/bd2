// Package gacha implements the local, non-payment version of the official
// 2.34.13 infinite reroll product. Static pools come from verified GameData;
// only the latest preview and confirmed ownership are persisted locally.
package gacha

import (
	"errors"
	"fmt"
	"sync"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

const infiniteGrant = "cash-product:1100001:9100033"

const (
	twelvePickGroupID     = 10001
	paidTwelvePickGroupID = 30011
)

const stepUpGroupID = 29

var stepUpGachaIDs = [...]uint64{8100118, 8100119, 8100120, 8100121}

func StepUpMigrationFact() (uint64, []uint64) {
	return stepUpGroupID, append([]uint64(nil), stepUpGachaIDs[:]...)
}

type Service struct {
	design             *gamedata.InfiniteGachaDesign
	regular            *gamedata.RegularGachaCatalog
	collection         *player.CollectionStore
	wallet             *player.Wallet
	inventory          *player.Inventory
	equipmentCatalog   *gamedata.EquipmentGachaCatalog
	equipmentInventory *player.EquipmentInventory
	onPreview          func() error
	sessionMu          sync.RWMutex
	sessionID          string
}

func (s *Service) AttachPreviewMission(callback func() error)  { s.onPreview = callback }
func (s *Service) AttachInventory(inventory *player.Inventory) { s.inventory = inventory }
func (s *Service) AttachEquipmentGacha(catalog *gamedata.EquipmentGachaCatalog, inventory *player.EquipmentInventory) {
	s.equipmentCatalog, s.equipmentInventory = catalog, inventory
}

// FirstGachaCompleted reflects UserDBInfo.IsFirstGacha. The official symbol
// map names the client-side property IsDoneFirstGachaPick, and the client sets
// it after a GachaSubType=3 purchase succeeds.
func (s *Service) FirstGachaCompleted() bool {
	return s.collection.FirstGachaCompleted()
}

// BeginSession is called by the transport session after each successful
// login. Request sequence numbers restart with the client, so the login ID is
// part of the durable idempotency key: retries in one login remain idempotent,
// while a new login may legitimately reuse the same protobuf sequence.
func (s *Service) BeginSession(id string) {
	s.sessionMu.Lock()
	s.sessionID = id
	s.sessionMu.Unlock()
}

func NewService(design *gamedata.InfiniteGachaDesign, regular *gamedata.RegularGachaCatalog, collection *player.CollectionStore, wallet *player.Wallet) (*Service, error) {
	if design == nil || regular == nil || collection == nil || wallet == nil {
		return nil, errors.New("gacha: invalid service configuration")
	}
	return &Service{design: design, regular: regular, collection: collection, wallet: wallet}, nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/GachaInfo" && path != "/GachaBuy" && path != "/GachaPointManualExchange" && path != "/GachaBuyPreview" && path != "/GachaBuyPreviewLock" && path != "/GachaSelectionSave" && path != "/CashShopPurchaseCountInfo" && path != "/CashShopBuy" {
		return 0, nil, false, nil
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("gacha: %s invalid sequence", path)
	}
	switch path {
	case "/GachaInfo":
		return 145, s.gachaInfo(), true, nil
	case "/GachaBuyPreview":
		return s.preview(request)
	case "/GachaBuyPreviewLock":
		return s.lockPreview(request)
	case "/GachaBuy":
		return s.buy(request, seq)
	case "/GachaPointManualExchange":
		return s.manualPointExchange(request, seq)
	case "/GachaSelectionSave":
		return s.saveSelection(request)
	case "/CashShopPurchaseCountInfo":
		if _, bought := s.collection.Grant(infiniteGrant); bought {
			count := wire.AppendVarint(nil, 1, gamedata.InfiniteProductGroupID)
			count = wire.AppendVarint(count, 2, gamedata.InfiniteProductID)
			count = wire.AppendVarint(count, 4, 1)
			return 432, wire.AppendBytes(nil, 1, count), true, nil
		}
		return 432, nil, true, nil
	case "/CashShopBuy":
		return s.confirm(request)
	default:
		return 0, nil, false, nil
	}
}

func (s *Service) saveSelection(request []byte) (int, []byte, bool, error) {
	var selections []player.GachaSelection
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 2 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("gacha: invalid selection entry")
		}
		groupID, groupFound, err := wire.Varint(field.Value, 1)
		if err != nil || !groupFound {
			return errors.New("gacha: selection group missing")
		}
		slot, _, err := wire.Varint(field.Value, 2) // slot zero is omitted by protobuf
		if err != nil {
			return err
		}
		itemID, itemFound, err := wire.Varint(field.Value, 3)
		if err != nil || !itemFound {
			return errors.New("gacha: selection item missing")
		}
		selections = append(selections, player.GachaSelection{GroupID: groupID, Slot: slot, ItemID: itemID})
		return nil
	}); err != nil {
		return 198, nil, true, err
	}
	if len(selections) == 0 {
		return 198, nil, true, errors.New("gacha: empty selection")
	}
	groupID := selections[0].GroupID
	expected := 0
	if group, ok := s.regular.Group(groupID); ok && group.SelectCount != 0 {
		expected = int(group.SelectCount)
	} else if groupID == paidTwelvePickGroupID {
		expected = 12
	}
	if expected == 0 || len(selections) != expected {
		return 198, nil, true, fmt.Errorf("gacha: group %d requires %d selections, got %d", groupID, expected, len(selections))
	}
	validItems := make(map[uint64]bool, len(s.design.FiveStarIDs))
	for _, id := range s.design.FiveStarIDs {
		validItems[id] = true
	}
	seenSlots := make(map[uint64]bool, len(selections))
	seenItems := make(map[uint64]bool, len(selections))
	for _, selection := range selections {
		if selection.GroupID != groupID || selection.Slot >= uint64(expected) || seenSlots[selection.Slot] || seenItems[selection.ItemID] || !validItems[selection.ItemID] {
			return 198, nil, true, fmt.Errorf("gacha: invalid 12PICK selection group=%d slot=%d item=%d", selection.GroupID, selection.Slot, selection.ItemID)
		}
		seenSlots[selection.Slot] = true
		seenItems[selection.ItemID] = true
	}
	if err := s.collection.SaveGachaSelections(groupID, selections); err != nil {
		return 198, nil, true, err
	}
	return 198, nil, true, nil
}

func (s *Service) lockPreview(request []byte) (int, []byte, bool, error) {
	eventIndex, found, err := wire.Varint(request, 2)
	if err != nil || !found || eventIndex == 0 {
		return 175, nil, true, errors.New("gacha: missing preview event index")
	}
	if err := s.collection.LockPreview(eventIndex); err != nil {
		return 175, nil, true, err
	}
	// 2.34.13 has no separate PacketCodeTypeProto member for the lock RPC.
	// It is a sub-operation of GachaBuyPreview and the client callback only
	// parses the empty response, so it uses the parent packet code.
	return 175, nil, true, nil
}

func (s *Service) buy(request []byte, seq uint64) (int, []byte, bool, error) {
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id == 0 {
		return 146, nil, true, errors.New("gacha: missing gacha id")
	}
	buyType, _, err := wire.Varint(request, 3)
	if err != nil || (buyType != 1 && buyType != 2) {
		return 146, nil, true, fmt.Errorf("gacha: unsupported buy type %d", buyType)
	}
	var tickets []player.Item
	if err := wire.Walk(request, func(field wire.Field) error {
		if field.Number != 4 {
			return nil
		}
		if field.Type != 2 {
			return errors.New("gacha: invalid ticket info")
		}
		var item player.Item
		var found bool
		var parseErr error
		if item.InvenIndex, found, parseErr = wire.Varint(field.Value, 1); parseErr != nil || !found {
			return errors.New("gacha: ticket inventory index missing")
		}
		item.ID, _, parseErr = wire.Varint(field.Value, 2)
		if parseErr != nil {
			return parseErr
		}
		item.Type, _, parseErr = wire.Varint(field.Value, 3)
		if parseErr != nil {
			return parseErr
		}
		item.Count, _, parseErr = wire.Varint(field.Value, 4)
		if parseErr != nil {
			return parseErr
		}
		if item.Type != 8 || item.Count == 0 {
			return errors.New("gacha: unsupported ticket")
		}
		tickets = append(tickets, item)
		return nil
	}); err != nil {
		return 146, nil, true, err
	}
	design, ok := s.regular.Gacha(id)
	if !ok && s.equipmentCatalog != nil {
		if equipment, found := s.equipmentCatalog.Gacha(id); found {
			return s.buyEquipment(seq, buyType, tickets, equipment)
		}
	}
	if !ok || (design.PriceType != 2 && design.PriceType != 3) || (buyType == 2 && design.PriceType != 2) {
		priceType := uint64(0)
		if ok {
			priceType = design.PriceType
		}
		return 146, nil, true, fmt.Errorf("gacha: unsupported regular gacha id=%d buyType=%d priceType=%d", id, buyType, priceType)
	}
	for _, ticket := range tickets {
		allowed := false
		for _, ticketID := range design.TicketIDs {
			if ticket.ID == ticketID {
				allowed = true
				break
			}
		}
		if !allowed {
			return 146, nil, true, fmt.Errorf("gacha: ticket %d is not valid for gacha %d", ticket.ID, id)
		}
	}
	group, knownGroup := s.regular.GroupForGacha(id)
	if !knownGroup {
		group = gamedata.GachaGroupDesign{ID: id, PointCount: 1}
	}
	identity := s.requestIdentity(id, seq)
	grant, already := s.collection.Grant(identity)
	step, isStepUp := stepUpStep(id)
	if isStepUp && !already {
		completed := s.collection.StepUpProgress(stepUpGroupID)
		if completed+1 != step {
			return 146, nil, true, fmt.Errorf("gacha: step-up group %d completed=%d cannot buy step=%d", stepUpGroupID, completed, step)
		}
	}
	if !already {
		var ticketCount uint64
		for _, ticket := range tickets {
			ticketCount += ticket.Count
		}
		if ticketCount > uint64(design.Count) || ((buyType == 2 || design.PriceType != 3) && ticketCount != 0) {
			return 146, nil, true, errors.New("gacha: invalid ticket count")
		}
		remaining := uint64(design.Count) - ticketCount
		unitPrice := design.Price / uint64(design.Count)
		if len(tickets) != 0 {
			if s.inventory == nil {
				return 146, nil, true, errors.New("gacha: inventory not attached")
			}
			if err := s.inventory.Consume(tickets); err != nil {
				return 146, nil, true, err
			}
		}
		if remaining != 0 {
			price := unitPrice * remaining
			if buyType == 2 || design.PriceType == 2 {
				if _, err := s.wallet.SpendJewelryOnce(identity, price); err != nil {
					return 146, nil, true, err
				}
			} else {
				if _, err := s.wallet.SpendFreeJewelryOnce(identity, price); err != nil {
					return 146, nil, true, err
				}
			}
		}
		var roll []uint64
		var fixedStates []player.GachaFixedState
		var selectionApplySortIDs []uint64
		if fixed, found := s.regular.Fixed(group.FixedID); found && design.FixedCostumeID == 0 {
			var fixedResult gamedata.GachaFixedRoll
			var selected, normalSelected, pitySelected []uint64
			if group.GachaSubType == 1 {
				for _, choice := range s.collection.GachaSelections(group.ID) {
					selected = append(selected, choice.ItemID)
				}
			}
			if !group.UseSelectionOnlyFixedApply {
				normalSelected = selected
			}
			if group.IsSelectedFromPity {
				pitySelected = selected
			}
			roll, fixedResult, err = design.RollWithCostumeFixedSelection(s.collection.GachaFixedCount(group.FixedID, 0), s.collection.GachaFixedCount(group.FixedID, 1), fixed, normalSelected, pitySelected)
			if err == nil {
				fixedStates = []player.GachaFixedState{
					{FixedID: group.FixedID, Type: 0, Count: fixedResult.CostumeGrade4Count, ApplySort: fixedResult.CostumeGrade4Sort},
					{FixedID: group.FixedID, Type: 1, Count: fixedResult.CostumeGrade5Count, ApplySort: fixedResult.CostumeGrade5Sort},
				}
				for _, sortID := range fixedResult.SelectionSorts {
					selectionApplySortIDs = append(selectionApplySortIDs, uint64(sortID))
				}
			}
		} else {
			roll, err = design.Roll()
		}
		if err != nil {
			return 146, nil, true, err
		}
		grant, err = s.collection.GrantRegularPurchase(identity, roll, s.regular, player.GachaPurchase{Group: group, BuyType: buyType, Fixed: fixedStates, SelectionApplySortIDs: selectionApplySortIDs})
		if err != nil {
			return 146, nil, true, err
		}
	}
	if isStepUp {
		if err := s.collection.CompleteStepUp(stepUpGroupID, step); err != nil {
			return 146, nil, true, err
		}
	}
	if err := s.creditOverflow(identity, grant); err != nil {
		return 146, nil, true, err
	}
	response := wire.AppendBytes(nil, 1, s.rewardBundle(grant))
	response = wire.AppendVarint(response, 2, grant.GachaPoint)
	for _, fixed := range grant.GachaFixed {
		response = wire.AppendBytes(response, 3, gachaFixedWire(fixed))
	}
	for _, sortID := range grant.SelectionApplySortIDs {
		response = wire.AppendVarint(response, 4, sortID)
	}
	if s.onPreview != nil {
		if err := s.onPreview(); err != nil {
			return 146, nil, true, fmt.Errorf("gacha: update buy mission: %w", err)
		}
	}
	return 146, response, true, nil
}

// Scheduled equipment draws update GachaUser/GachaFixed accounting. Standalone
// ticket-only draws have no schedule group and persist only their idempotency
// marker plus the generated equipment instances.
func (s *Service) buyEquipment(seq, buyType uint64, tickets []player.Item, design gamedata.EquipmentGacha) (int, []byte, bool, error) {
	if s.equipmentInventory == nil {
		return 146, nil, true, errors.New("gacha: equipment inventory not attached")
	}
	if buyType != 1 || (!design.TicketOnly && design.PriceType != 3) {
		return 146, nil, true, fmt.Errorf("gacha: unsupported equipment buy type %d", buyType)
	}
	if design.TicketOnly && len(tickets) == 0 {
		return 146, nil, true, errors.New("gacha: ticket-only equipment draw requires a ticket")
	}
	for _, ticket := range tickets {
		allowed := false
		for _, ticketID := range design.TicketIDs {
			if ticket.ID == ticketID {
				allowed = true
				break
			}
		}
		if !allowed {
			return 146, nil, true, fmt.Errorf("gacha: ticket %d is not valid for equipment gacha %d", ticket.ID, design.ID)
		}
	}
	group, hasGroup := s.equipmentCatalog.GroupForGacha(design.ID)
	if !hasGroup && !design.TicketOnly {
		return 146, nil, true, fmt.Errorf("gacha: equipment gacha %d has no group", design.ID)
	}
	identity := s.requestIdentity(design.ID, seq)
	grant, already := s.collection.Grant(identity)
	var entries []player.Equipment
	if already {
		for sort := 0; sort < design.Count; sort++ {
			entry, found := s.equipmentInventory.Granted(fmt.Sprintf("%s:equip:%d", identity, sort))
			if !found {
				return 146, nil, true, fmt.Errorf("gacha: equipment retry %s missing slot %d", identity, sort)
			}
			entries = append(entries, entry)
		}
	} else {
		var ticketCount uint64
		for _, ticket := range tickets {
			ticketCount += ticket.Count
		}
		if ticketCount > uint64(design.Count) || (design.TicketOnly && ticketCount != uint64(design.Count)) {
			return 146, nil, true, errors.New("gacha: invalid equipment ticket count")
		}
		if ticketCount != 0 {
			if s.inventory == nil {
				return 146, nil, true, errors.New("gacha: inventory not attached")
			}
			if err := s.inventory.Consume(tickets); err != nil {
				return 146, nil, true, err
			}
		}
		if remain := uint64(design.Count) - ticketCount; remain != 0 {
			if design.TicketOnly {
				return 146, nil, true, errors.New("gacha: ticket-only equipment draw cannot use diamonds")
			}
			if _, err := s.wallet.SpendFreeJewelryOnce(identity, design.Price/uint64(design.Count)*remain); err != nil {
				return 146, nil, true, err
			}
		}
		fixed := s.equipmentCatalog.Fixed()
		var priorSR, priorUR uint64
		if !design.TicketOnly {
			priorSR = s.collection.GachaFixedCount(fixed.ID, 2)
			priorUR = s.collection.GachaFixedCount(fixed.ID, 3)
		}
		roll, state, err := design.Roll(priorSR, priorUR, fixed)
		if err != nil {
			return 146, nil, true, err
		}
		var fixedStates []player.GachaFixedState
		if !design.TicketOnly {
			fixedStates = []player.GachaFixedState{{FixedID: fixed.ID, Type: 2, Count: state.SRCount, ApplySort: state.SRSort}, {FixedID: fixed.ID, Type: 3, Count: state.URCount, ApplySort: state.URSort}}
		}
		for sort, equipmentID := range roll {
			main, sub, private, err := s.equipmentCatalog.RollOptions(equipmentID)
			if err != nil {
				return 146, nil, true, err
			}
			entry := player.Equipment{ID: equipmentID, SortID: uint64(sort), Rank: []uint64{0, 0, 0}}
			for _, o := range main {
				entry.MainOption = append(entry.MainOption, player.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
			}
			for _, o := range sub {
				entry.SubOption = append(entry.SubOption, player.EquipmentOption{GroupID: o.GroupID, ID: o.ID})
			}
			if private != nil {
				entry.PrivateOption = &player.EquipmentOption{GroupID: private.GroupID, ID: private.ID}
			}
			saved, err := s.equipmentInventory.GrantGeneratedOnce(fmt.Sprintf("%s:equip:%d", identity, sort), entry)
			if err != nil {
				return 146, nil, true, err
			}
			entries = append(entries, saved)
		}
		if design.TicketOnly {
			grant, err = s.collection.GrantEquipmentDraw(identity)
		} else {
			grant, err = s.collection.GrantEquipmentPurchase(identity, uint64(len(entries)), player.GachaPurchase{Group: gamedata.GachaGroupDesign{ID: group.ID, PointCount: group.PointCount}, BuyType: buyType, Fixed: fixedStates})
		}
		if err != nil {
			return 146, nil, true, err
		}
	}
	bundle := []byte{}
	for _, entry := range entries {
		bundle = wire.AppendBytes(bundle, 4, player.EquipmentWire(entry))
	}
	response := wire.AppendBytes(nil, 1, bundle)
	response = wire.AppendVarint(response, 2, grant.GachaPoint)
	for _, fixed := range grant.GachaFixed {
		response = wire.AppendBytes(response, 3, gachaFixedWire(fixed))
	}
	return 146, response, true, nil
}

func gachaFixedWire(fixed player.GachaFixedState) []byte {
	info := wire.AppendVarint(nil, 1, fixed.FixedID)
	info = wire.AppendVarint(info, 2, fixed.Type)
	info = wire.AppendVarint(info, 3, fixed.Count)
	// ApplySortId is signed int32: -1 must be encoded explicitly, otherwise
	// proto3 defaults to slot 0 and falsely labels the first draw guaranteed.
	return wire.AppendVarint(info, 4, uint64(int64(fixed.ApplySort)))
}

func (s *Service) manualPointExchange(request []byte, seq uint64) (int, []byte, bool, error) {
	groupID, found, err := wire.Varint(request, 2)
	if err != nil || !found || groupID == 0 {
		return 188, nil, true, errors.New("gacha: missing point exchange group")
	}
	count, found, err := wire.Varint(request, 3)
	if err != nil || !found || count == 0 {
		return 188, nil, true, errors.New("gacha: invalid point exchange count")
	}
	group, ok := s.regular.Group(groupID)
	if !ok || group.PointCount == 0 {
		return 188, nil, true, fmt.Errorf("gacha: group %d does not support point exchange", groupID)
	}
	identity := fmt.Sprintf("gacha-point-manual:%s:%d:seq:%d", s.loginIdentity(), groupID, seq)
	exchange, err := s.collection.ExchangeGachaPoint(identity, groupID, count)
	if err != nil {
		return 188, nil, true, err
	}
	if _, err := s.wallet.GrantHopePowderOnce(identity, exchange.Count); err != nil {
		return 188, nil, true, err
	}
	// Manual conversion grants account currency type 22, not a stackable
	// ItemDBInfo. The client's common reward handler applies RepaidCurrency.
	reward := wire.AppendVarint(nil, 2, 22)
	reward = wire.AppendVarint(reward, 3, exchange.Count)
	bundle := wire.AppendBytes(nil, 10, reward)
	return 188, wire.AppendBytes(nil, 1, bundle), true, nil
}

func (s *Service) loginIdentity() string {
	s.sessionMu.RLock()
	defer s.sessionMu.RUnlock()
	if s.sessionID == "" {
		return "local"
	}
	return s.sessionID
}

func (s *Service) requestIdentity(gachaID, seq uint64) string {
	s.sessionMu.RLock()
	sessionID := s.sessionID
	s.sessionMu.RUnlock()
	if sessionID == "" {
		// Unit tests and embedders without a session coordinator retain the
		// original stable key. Production always supplies BeginSession.
		return fmt.Sprintf("regular-gacha:%d:seq:%d", gachaID, seq)
	}
	return fmt.Sprintf("regular-gacha:%d:session:%s:seq:%d", gachaID, sessionID, seq)
}

func stepUpStep(gachaID uint64) (uint64, bool) {
	for i, id := range stepUpGachaIDs {
		if id == gachaID {
			return uint64(i + 1), true
		}
	}
	return 0, false
}

func (s *Service) preview(request []byte) (int, []byte, bool, error) {
	if _, bought := s.collection.Grant(infiniteGrant); bought {
		return 175, nil, true, errors.New("gacha: infinite product already purchased")
	}
	id, found, err := wire.Varint(request, 2)
	if err != nil || !found || id != gamedata.InfiniteGachaID {
		return 0, nil, true, fmt.Errorf("gacha: unsupported preview id %d", id)
	}
	group, found, err := wire.Varint(request, 3)
	if err != nil || !found || group != gamedata.InfiniteProductGroupID {
		return 0, nil, true, fmt.Errorf("gacha: unsupported preview product group %d", group)
	}
	product, found, err := wire.Varint(request, 4)
	if err != nil || !found || product != gamedata.InfiniteProductID {
		return 0, nil, true, fmt.Errorf("gacha: unsupported preview product %d", product)
	}
	roll, err := s.design.Roll()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.collection.SetPreview(roll); err != nil {
		return 0, nil, true, err
	}
	if s.onPreview != nil {
		if err := s.onPreview(); err != nil {
			return 0, nil, true, fmt.Errorf("gacha: update preview mission: %w", err)
		}
	}
	var bundle []byte
	for i, costumeID := range roll {
		costume := wire.AppendVarint(nil, 2, costumeID)
		if i != 0 {
			costume = wire.AppendVarint(costume, 6, uint64(i))
		}
		bundle = wire.AppendBytes(bundle, 3, costume)
	}
	return 175, wire.AppendBytes(nil, 1, bundle), true, nil
}

func (s *Service) confirm(request []byte) (int, []byte, bool, error) {
	group, found, err := wire.Varint(request, 3)
	if err != nil || !found || group != gamedata.InfiniteProductGroupID {
		return 0, nil, true, fmt.Errorf("gacha: unsupported cash product group %d", group)
	}
	var productID, saleGroup, buyCount uint64
	err = wire.Walk(request, func(field wire.Field) error {
		if field.Number != 4 {
			return nil
		}
		if field.Type != 2 || productID != 0 {
			return errors.New("gacha: invalid product list")
		}
		var parseErr error
		productID, found, parseErr = wire.Varint(field.Value, 1)
		if parseErr != nil || !found {
			return errors.New("gacha: product id missing")
		}
		saleGroup, _, parseErr = wire.Varint(field.Value, 2)
		if parseErr != nil {
			return parseErr
		}
		buyCount, found, parseErr = wire.Varint(field.Value, 3)
		if parseErr != nil || !found {
			return errors.New("gacha: buy count missing")
		}
		return nil
	})
	if err != nil {
		return 0, nil, true, err
	}
	if productID != gamedata.InfiniteProductID || saleGroup != 0 || buyCount != 1 {
		return 0, nil, true, fmt.Errorf("gacha: unsupported product %d sale %d count %d", productID, saleGroup, buyCount)
	}
	grant, err := s.collection.ConfirmInfinite(infiniteGrant, s.design)
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.creditOverflow(infiniteGrant, grant); err != nil {
		return 61, nil, true, err
	}
	return 61, wire.AppendBytes(nil, 1, s.rewardBundle(grant)), true, nil
}

func (s *Service) creditOverflow(identity string, grant player.CollectionGrant) error {
	var mileage uint64
	for _, exchange := range grant.Exchanges {
		if exchange.ExchangeItemType != 20 {
			return fmt.Errorf("gacha: unsupported costume overflow type %d", exchange.ExchangeItemType)
		}
		if ^uint64(0)-mileage < exchange.ExchangeCount {
			return errors.New("gacha: costume overflow mileage overflow")
		}
		mileage += exchange.ExchangeCount
	}
	if mileage == 0 {
		return nil
	}
	_, err := s.wallet.GrantMileageOnce(identity, mileage)
	return err
}

func (s *Service) rewardBundle(grant player.CollectionGrant) []byte {
	var bundle []byte
	newCharacters := make(map[uint64]player.Character, len(grant.CharacterIndices))
	for _, index := range grant.CharacterIndices {
		if character, found := s.collection.CharacterByIndex(index); found {
			newCharacters[character.ConnectPotentialCostume] = character
			bundle = wire.AppendBytes(bundle, 2, player.CharacterWire(character))
		}
	}
	for _, index := range grant.CostumeIndices {
		if costume, found := s.collection.CostumeByIndex(index); found {
			bundle = wire.AppendBytes(bundle, 3, player.CostumeWire(costume))
		}
	}
	for sortID, costumeID := range grant.ViewCostumeIDs {
		view := wire.AppendVarint(nil, 2, costumeID)
		view = wire.AppendVarint(view, 3, 11)
		view = wire.AppendVarint(view, 4, 1)
		bundle = wire.AppendBytes(bundle, 6, view)
		if character, found := newCharacters[costumeID]; found {
			charView := wire.AppendVarint(nil, 2, character.ID)
			charView = wire.AppendVarint(charView, 3, 6)
			charView = wire.AppendVarint(charView, 4, 1)
			if sortID != 0 {
				charView = wire.AppendVarint(charView, 6, uint64(sortID))
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
	var repaidMileage uint64
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
		if exchange.ExchangeItemType == 20 {
			repaidMileage += exchange.ExchangeCount
		}
	}
	if repaidMileage != 0 {
		repaid := wire.AppendVarint(nil, 2, 20)
		repaid = wire.AppendVarint(repaid, 3, repaidMileage)
		bundle = wire.AppendBytes(bundle, 10, repaid)
	}
	return bundle
}

// The dynamic schedule is taken from the verified 2026-09-20 official
// GachaInfo response. Static banner definitions remain in GameData. The paid
// infinite product is confirmed through CashShopBuy and is not one of these
// ordinary schedule groups.
func (s *Service) gachaInfo() []byte {
	entries := []struct {
		group      uint64
		start, end uint64
	}{
		{135, 1787788800000, 1790121599000},
		{205, 1788998400000, 1791417599000},
		{121, 1788998400000, 1790121599000},
	}
	if s.equipmentCatalog != nil {
		// Permanent equipment draw: direct client evidence is GachaBuy id=201,
		// which GameData maps to group 10002. It has no SelectCount fields.
		entries = append([]struct{ group, start, end uint64 }{{10002, 0, 4102444800000}}, entries...)
	}
	if _, bought := s.collection.Grant(infiniteGrant); !bought {
		entries = append(entries, struct{ group, start, end uint64 }{30010, 1788998400000, 1791417599000})
	}
	var out []byte
	for _, entry := range entries {
		schedule := wire.AppendVarint(nil, 1, entry.group)
		schedule = wire.AppendVarint(schedule, 2, entry.start)
		schedule = wire.AppendVarint(schedule, 3, entry.end)
		out = wire.AppendBytes(out, 1, schedule)
	}
	step := wire.AppendVarint(nil, 1, 29)
	step = wire.AppendVarint(step, 2, 1788998400000)
	step = wire.AppendVarint(step, 3, 1791417599000)
	out = wire.AppendBytes(out, 7, step)
	if completed := s.collection.StepUpProgress(stepUpGroupID); completed != 0 {
		user := wire.AppendVarint(nil, 1, stepUpGroupID)
		user = wire.AppendVarint(user, 2, completed)
		out = wire.AppendBytes(out, 8, user)
	}
	for _, groupID := range []uint64{twelvePickGroupID, paidTwelvePickGroupID} {
		for _, selection := range s.collection.GachaSelections(groupID) {
			info := wire.AppendVarint(nil, 1, selection.GroupID)
			if selection.Slot != 0 {
				info = wire.AppendVarint(info, 2, selection.Slot)
			}
			info = wire.AppendVarint(info, 3, selection.ItemID)
			out = wire.AppendBytes(out, 5, info)
		}
	}
	for _, user := range s.collection.GachaUsers() {
		info := wire.AppendVarint(nil, 1, user.GroupID)
		info = wire.AppendVarint(info, 2, user.Point)
		info = wire.AppendVarint(info, 3, user.TotalBuyCount)
		info = wire.AppendVarint(info, 4, user.OneFreePickCount)
		info = wire.AppendVarint(info, 5, user.OneCashPickCount)
		info = wire.AppendVarint(info, 6, user.TenFreePickCount)
		info = wire.AppendVarint(info, 7, user.TenCashPickCount)
		info = wire.AppendVarint(info, 8, user.ExchangeItemCount)
		info = wire.AppendVarint(info, 9, user.ExchangeMileageCount)
		out = wire.AppendBytes(out, 2, info)
	}
	for _, fixed := range s.collection.GachaFixedStates() {
		out = wire.AppendBytes(out, 4, gachaFixedWire(fixed))
	}
	return out
}
