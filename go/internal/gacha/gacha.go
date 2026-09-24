// Package gacha implements the local, non-payment version of the official
// infinite reroll product. Static pools come from verified GameData;
// only the latest preview and confirmed ownership are persisted locally.
package gacha

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"bd2server/internal/gamedata"
	"bd2server/internal/player"
	"bd2server/internal/wire"
)

const infiniteGrant = "cash-product:1100001:9100033"

const (
	moonriseProductGroupID = 1500001
	moonriseProductID      = 9100037
	moonriseTicketType     = 19
	moonriseTicketID       = 450030
	moonriseProductGrant   = "cash-product:1500001:9100037"
	moonriseTicketGrant    = "cash-product-reward:1500001:9100037:0"
	moonriseDrawGrant      = "special-gacha:30011:9100037"
)

const (
	infiniteScheduleGroupID = 30010
	twelvePickGroupID       = 10001
	paidTwelvePickGroupID   = 30011
)

type Service struct {
	design             *gamedata.InfiniteGachaDesign
	regular            *gamedata.RegularGachaCatalog
	collection         *player.CollectionStore
	wallet             *player.Wallet
	inventory          *player.Inventory
	equipmentCatalog   *gamedata.EquipmentGachaCatalog
	equipmentInventory *player.EquipmentInventory
	onPreview          func() error
	schedule           *ScheduleSeed
	previewEventIndex  uint64
	sessionMu          sync.RWMutex
	sessionID          string
	now                func() time.Time
}

func (s *Service) AttachPreviewMission(callback func() error)  { s.onPreview = callback }
func (s *Service) AttachInventory(inventory *player.Inventory) { s.inventory = inventory }
func (s *Service) AttachPreviewEventIndex(eventIndex uint64) error {
	if eventIndex == 0 {
		return errors.New("gacha: invalid preview event index")
	}
	s.previewEventIndex = eventIndex
	return nil
}
func (s *Service) AttachSchedule(seed *ScheduleSeed) error {
	if err := seed.Validate(seedVersion(seed)); err != nil {
		return err
	}
	copy := *seed
	copy.Schedules = append([]ScheduleWindow(nil), seed.Schedules...)
	copy.StepUps = append([]ScheduleWindow(nil), seed.StepUps...)
	s.schedule = &copy
	return nil
}
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
	return &Service{design: design, regular: regular, collection: collection, wallet: wallet, now: time.Now}, nil
}

func (s *Service) Handle(path string, request []byte) (int, []byte, bool, error) {
	if path != "/GachaInfo" && path != "/GachaBuy" && path != "/GachaPointExchange" && path != "/GachaPointManualExchange" && path != "/GachaBuyPreview" && path != "/GachaBuyPreviewLock" && path != "/GachaSelectionSave" && path != "/CashShopPurchaseCountInfo" && path != "/CashShopBuy" {
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
	case "/GachaPointExchange":
		return s.pointExchange(request, seq)
	case "/GachaPointManualExchange":
		return s.manualPointExchange(request, seq)
	case "/GachaSelectionSave":
		return s.saveSelection(request)
	case "/CashShopPurchaseCountInfo":
		var response []byte
		for _, count := range s.PurchaseCountDBInfos() {
			response = wire.AppendBytes(response, 1, count)
		}
		return 432, response, true, nil
	case "/CashShopBuy":
		return s.confirm(request)
	default:
		return 0, nil, false, nil
	}
}

// PurchaseCountDBInfos returns the same persisted cash-product purchase state
// used by /CashShopPurchaseCountInfo. LoginUser consumes this method too, so
// both protocol surfaces remain consistent after a restart.
func (s *Service) PurchaseCountDBInfos() [][]byte {
	products := []struct {
		group, id uint64
		bought    bool
	}{}
	_, infiniteBought := s.collection.Grant(infiniteGrant)
	products = append(products, struct {
		group, id uint64
		bought    bool
	}{gamedata.InfiniteProductGroupID, gamedata.InfiniteProductID, infiniteBought})
	_, moonriseBought := s.collection.Grant(moonriseProductGrant)
	products = append(products, struct {
		group, id uint64
		bought    bool
	}{moonriseProductGroupID, moonriseProductID, moonriseBought})
	var result [][]byte
	for _, product := range products {
		if !product.bought {
			continue
		}
		count := wire.AppendVarint(nil, 1, product.group)
		count = wire.AppendVarint(count, 2, product.id)
		count = wire.AppendVarint(count, 4, 1)
		result = append(result, count)
	}
	return result
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
	changeLimit := uint64(0)
	if group, ok := s.regular.Group(groupID); ok && group.SelectCount != 0 {
		expected = int(group.SelectCount)
		changeLimit = group.SelectionChangeCount
	} else if groupID == paidTwelvePickGroupID {
		expected = 12
	}
	if expected == 0 || len(selections) != expected {
		return 198, nil, true, fmt.Errorf("gacha: group %d requires %d selections, got %d", groupID, expected, len(selections))
	}
	eligible := s.design.FiveStarIDs
	if group, ok := s.regular.Group(groupID); ok && group.GachaSubType == 1 && group.TenTimeGachaID != 0 {
		if special := s.regular.SpecialSelectionIDs(group.TenTimeGachaID); len(special) != 0 {
			eligible = special
		} else if fiveStars := s.regular.FiveStarIDs(group.TenTimeGachaID); group.UseSelectionOnlyFixedApply && len(fiveStars) != 0 {
			eligible = fiveStars
		}
	}
	validItems := make(map[uint64]bool, len(eligible))
	for _, id := range eligible {
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
	if err := s.collection.SaveGachaSelections(groupID, selections, changeLimit); err != nil {
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
	if err != nil || buyType > 3 {
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
	if !ok {
		return 146, nil, true, fmt.Errorf("gacha: unsupported regular gacha id=%d", id)
	}
	group, knownGroup := s.regular.GroupForGacha(id)
	if !knownGroup {
		group = gamedata.GachaGroupDesign{ID: id, PointCount: 1}
	}
	isDailyFree := buyType == 0 && design.Count == 1 && design.FreeCountDay != 0 && knownGroup
	isDailyPaid := buyType == 2 && design.Count == 1 && design.DailyPayGachaCount != 0 && design.DailyPayGachaPriceCount != 0 && knownGroup
	isMoonrise := buyType == 3 && design.PriceType == moonriseTicketType && design.PriceID == moonriseTicketID && id == moonriseProductID && group.ID == paidTwelvePickGroupID
	validOrdinary := buyType == 1 && (design.PriceType == 2 || design.PriceType == 3)
	if !isDailyFree && !isDailyPaid && !isMoonrise && !validOrdinary {
		priceType := uint64(0)
		priceType = design.PriceType
		return 146, nil, true, fmt.Errorf("gacha: unsupported regular gacha id=%d buyType=%d priceType=%d (server never substitutes diamonds for another currency)", id, buyType, priceType)
	}
	if buyType != 1 && len(tickets) != 0 {
		return 146, nil, true, errors.New("gacha: discounted or content-ticket draw cannot use ordinary gacha tickets")
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
	identity := s.requestIdentity(id, seq)
	grant, already := s.collection.Grant(identity)
	if isMoonrise && !already {
		if _, completed := s.collection.Grant(moonriseDrawGrant); completed {
			return 146, nil, true, errors.New("gacha: Moonrise product already drawn")
		}
	}
	var moonriseSelected []uint64
	if isMoonrise && !already {
		selections := s.collection.GachaSelections(group.ID)
		if len(selections) != int(group.SelectCount) {
			return 146, nil, true, fmt.Errorf("gacha: special selection group %d has %d choices, want %d", group.ID, len(selections), group.SelectCount)
		}
		moonriseSelected = make([]uint64, len(selections))
		for i, selection := range selections {
			moonriseSelected[i] = selection.ItemID
		}
	}
	stepUpGroupID, stepDesign, isStepUp := s.stepForGacha(id)
	if isStepUp && !already {
		completed := s.collection.StepUpProgress(stepUpGroupID)
		if completed+1 != stepDesign.Step {
			return 146, nil, true, fmt.Errorf("gacha: step-up group %d completed=%d cannot buy step=%d", stepUpGroupID, completed, stepDesign.Step)
		}
	}
	if !already {
		if group.BuyLimitCount != 0 && s.collection.GachaUser(group.ID).TotalBuyCount+uint64(design.Count) > group.BuyLimitCount {
			return 146, nil, true, fmt.Errorf("gacha: group %d buy limit %d exhausted", group.ID, group.BuyLimitCount)
		}
		day := dailyResetKey(s.now())
		var dailyLimit uint64
		if isDailyFree {
			dailyLimit = design.FreeCountDay
		} else if isDailyPaid {
			dailyLimit = design.DailyPayGachaCount
		}
		if dailyLimit != 0 && s.collection.GachaDailyCount(day, group.ID, buyType) >= dailyLimit {
			return 146, nil, true, fmt.Errorf("gacha: daily draw exhausted group=%d buyType=%d", group.ID, buyType)
		}
		var ticketCount uint64
		for _, ticket := range tickets {
			ticketCount += ticket.Count
		}
		if ticketCount > uint64(design.Count) || (buyType != 1 && ticketCount != 0) || (design.PriceType != 3 && ticketCount != 0) {
			return 146, nil, true, errors.New("gacha: invalid ticket count")
		}
		var roll []uint64
		var fixedStates []player.GachaFixedState
		var selectionApplySortIDs []uint64
		if isMoonrise {
			roll, err = design.RollSpecialSelection(moonriseSelected)
			for i := uint64(0); i < s.regular.SpecialSelectionCount(id); i++ {
				selectionApplySortIDs = append(selectionApplySortIDs, i)
			}
		} else if fixed, found := s.regular.Fixed(group.FixedID); found && design.FixedCostumeID == 0 {
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
			if group.IsSelectedFromPity || group.UseSelectionOnlyFixedApply {
				pitySelected = selected
			}
			roll, fixedResult, err = design.RollWithCostumeFixedSelection(s.collection.GachaFixedCount(group.FixedID, 0), s.collection.GachaFixedCount(group.FixedID, 1), fixed, normalSelected, pitySelected)
			if err == nil {
				fixedStates = costumeFixedStates(fixed, fixedResult)
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
		// Generate and validate the complete reward before mutating any cost
		// domain. The enclosing session transaction remains the final atomic
		// boundary, but deterministic business errors can no longer consume a
		// ticket or currency first.
		if len(tickets) != 0 {
			if s.inventory == nil {
				return 146, nil, true, errors.New("gacha: inventory not attached")
			}
			if err := s.inventory.CanConsume(tickets); err != nil {
				return 146, nil, true, err
			}
		}
		var moonriseCost []player.Item
		if isMoonrise {
			if s.inventory == nil {
				return 146, nil, true, errors.New("gacha: inventory not attached")
			}
			moonriseCost, err = s.inventory.SelectMutable(moonriseTicketType, moonriseTicketID, design.Price)
			if err != nil {
				return 146, nil, true, err
			}
		}
		var charge uint64
		var chargePaid bool
		switch {
		case isDailyPaid:
			charge, chargePaid = design.DailyPayGachaPriceCount, true
		case !isDailyFree && !isMoonrise:
			remaining := uint64(design.Count) - ticketCount
			charge = design.Price / uint64(design.Count) * remaining
			chargePaid = design.PriceType == 2
		}
		if charge != 0 {
			currency := s.wallet.Snapshot()
			if chargePaid && currency.Jewelry < charge {
				return 146, nil, true, errors.New("gacha: insufficient paid jewelry")
			}
			if !chargePaid && currency.FreeJewelry < charge {
				return 146, nil, true, errors.New("gacha: insufficient free jewelry")
			}
		}
		if len(tickets) != 0 {
			if err := s.inventory.Consume(tickets); err != nil {
				return 146, nil, true, err
			}
		}
		switch {
		case isDailyFree:
			// The daily allowance is the complete cost.
		case isDailyPaid:
			if _, err := s.wallet.SpendJewelryOnce(identity, charge); err != nil {
				return 146, nil, true, err
			}
		case isMoonrise:
			if err := s.inventory.Consume(moonriseCost); err != nil {
				return 146, nil, true, err
			}
		default:
			if charge != 0 {
				if chargePaid {
					if _, err := s.wallet.SpendJewelryOnce(identity, charge); err != nil {
						return 146, nil, true, err
					}
				} else {
					if _, err := s.wallet.SpendFreeJewelryOnce(identity, charge); err != nil {
						return 146, nil, true, err
					}
				}
			}
		}
		purchase := player.GachaPurchase{Group: group, BuyType: buyType, Fixed: fixedStates, SelectionApplySortIDs: selectionApplySortIDs}
		if isMoonrise {
			purchase.CompletionGrant = moonriseDrawGrant
		}
		if dailyLimit != 0 {
			purchase.DailyKey = day
			purchase.DailyLimit = dailyLimit
		}
		if isStepUp {
			purchase.StepUpGroupID = stepUpGroupID
			purchase.StepUpStep = stepDesign.Step
		}
		grant, err = s.collection.GrantRegularPurchase(identity, roll, s.regular, purchase)
		if err != nil {
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

func (s *Service) stepForGacha(gachaID uint64) (uint64, gamedata.GachaStepDesign, bool) {
	step, ok := s.regular.StepForGacha(gachaID)
	if !ok {
		return 0, gamedata.GachaStepDesign{}, false
	}
	for _, group := range s.regular.StepUps() {
		for _, candidate := range group.Steps {
			if candidate.GachaID == gachaID && candidate == step {
				return group.ID, step, true
			}
		}
	}
	return 0, gamedata.GachaStepDesign{}, false
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

func costumeFixedStates(fixed gamedata.GachaFixedDesign, result gamedata.GachaFixedRoll) []player.GachaFixedState {
	states := make([]player.GachaFixedState, 0, 2)
	if fixed.CostumeGrade4Count != 0 {
		states = append(states, player.GachaFixedState{
			FixedID: fixed.ID, Type: 0, Count: result.CostumeGrade4Count, ApplySort: result.CostumeGrade4Sort,
		})
	}
	if fixed.CostumeGrade5Count != 0 {
		states = append(states, player.GachaFixedState{
			FixedID: fixed.ID, Type: 1, Count: result.CostumeGrade5Count, ApplySort: result.CostumeGrade5Sort,
		})
	}
	return states
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

func (s *Service) pointExchange(request []byte, seq uint64) (int, []byte, bool, error) {
	groupID, found, err := wire.Varint(request, 2)
	if err != nil || !found || groupID == 0 {
		return 147, nil, true, errors.New("gacha: missing point exchange group")
	}
	selectedItemID, _, err := wire.Varint(request, 3)
	if err != nil {
		return 147, nil, true, errors.New("gacha: invalid point exchange selection")
	}
	group, ok := s.regular.Group(groupID)
	if !ok || group.GachaType != 1 || group.PickUpExchangeCost == 0 {
		return 147, nil, true, fmt.Errorf("gacha: group %d does not support costume point exchange", groupID)
	}
	costumeID := group.PickUpCostumeID
	if selectedItemID != 0 {
		selections := s.collection.GachaSelections(groupID)
		if len(selections) != 1 || selections[0].ItemID != selectedItemID {
			return 147, nil, true, fmt.Errorf("gacha: selection %d is not the saved pickup target for group %d", selectedItemID, groupID)
		}
		costumeID = selectedItemID
	}
	if costumeID == 0 {
		return 147, nil, true, fmt.Errorf("gacha: group %d has no pickup costume", groupID)
	}
	if _, ok := s.regular.Character(costumeID); !ok {
		return 147, nil, true, fmt.Errorf("gacha: pickup costume %d has no character design", costumeID)
	}
	identity := fmt.Sprintf("gacha-point-costume:%s:%d:%d:seq:%d", s.loginIdentity(), groupID, costumeID, seq)
	grant, err := s.collection.GrantGachaPointCostume(identity, group, costumeID, s.regular)
	if err != nil {
		return 147, nil, true, err
	}
	if err := s.creditOverflow(identity, grant); err != nil {
		return 147, nil, true, err
	}
	return 147, wire.AppendBytes(nil, 1, s.rewardBundle(grant)), true, nil
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
	if s.previewEventIndex == 0 {
		return 0, nil, true, errors.New("gacha: infinite preview event is not configured")
	}
	roll, err := s.design.Roll()
	if err != nil {
		return 0, nil, true, err
	}
	if err := s.collection.SetPreview(s.previewEventIndex, roll); err != nil {
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
	if err != nil || !found {
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
	if saleGroup != 0 || buyCount != 1 {
		return 0, nil, true, fmt.Errorf("gacha: unsupported product %d sale %d count %d", productID, saleGroup, buyCount)
	}
	if group == moonriseProductGroupID && productID == moonriseProductID {
		if s.inventory == nil {
			return 61, nil, true, errors.New("gacha: inventory not attached")
		}
		items, err := s.inventory.GrantOnce(moonriseTicketGrant, []gamedata.BattleReward{{Type: moonriseTicketType, ID: moonriseTicketID, Count: 1}})
		if err != nil {
			return 61, nil, true, err
		}
		if err := s.collection.RecordGrantMarker(moonriseProductGrant); err != nil {
			return 61, nil, true, err
		}
		if len(items) == 0 {
			items = s.inventory.GrantedItems(moonriseTicketGrant)
		}
		var bundle []byte
		for _, item := range items {
			bundle = wire.AppendBytes(bundle, 1, player.ItemWire(item))
		}
		return 61, wire.AppendBytes(nil, 1, bundle), true, nil
	}
	if group != gamedata.InfiniteProductGroupID || productID != gamedata.InfiniteProductID {
		return 0, nil, true, fmt.Errorf("gacha: unsupported cash product group=%d product=%d", group, productID)
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

func (s *Service) gachaInfo() []byte {
	var out []byte
	_, infinitePurchased := s.collection.Grant(infiniteGrant)
	_, moonriseDrawn := s.collection.Grant(moonriseDrawGrant)
	if s.schedule != nil {
		for _, entry := range s.schedule.Schedules {
			// The versioned seed is the public schedule, while availability of
			// the one-time infinite product is account state. Once the product
			// grant exists, publishing this group makes the client reopen the
			// preview flow even though the authoritative preview endpoint must
			// reject a second purchase.
			if infinitePurchased && entry.GroupID == infiniteScheduleGroupID {
				continue
			}
			if moonriseDrawn && entry.GroupID == paidTwelvePickGroupID {
				continue
			}
			schedule := wire.AppendVarint(nil, 1, entry.GroupID)
			schedule = wire.AppendVarint(schedule, 2, entry.StartTime)
			schedule = wire.AppendVarint(schedule, 3, entry.EndTime)
			if entry.FreeCountBonus {
				schedule = wire.AppendVarint(schedule, 4, 1)
			}
			if entry.CashCountBonus {
				schedule = wire.AppendVarint(schedule, 5, 1)
			}
			out = wire.AppendBytes(out, 1, schedule)
		}
		for _, entry := range s.schedule.StepUps {
			step := wire.AppendVarint(nil, 1, entry.GroupID)
			step = wire.AppendVarint(step, 2, entry.StartTime)
			step = wire.AppendVarint(step, 3, entry.EndTime)
			out = wire.AppendBytes(out, 7, step)
			if completed := s.collection.StepUpProgress(entry.GroupID); completed != 0 {
				user := wire.AppendVarint(nil, 1, entry.GroupID)
				user = wire.AppendVarint(user, 2, completed)
				out = wire.AppendBytes(out, 8, user)
			}
		}
	}
	for _, selection := range s.collection.AllGachaSelections() {
		info := wire.AppendVarint(nil, 1, selection.GroupID)
		if selection.Slot != 0 {
			info = wire.AppendVarint(info, 2, selection.Slot)
		}
		info = wire.AppendVarint(info, 3, selection.ItemID)
		out = wire.AppendBytes(out, 5, info)
	}
	for _, change := range s.collection.GachaSelectionChangeCounts() {
		info := wire.AppendVarint(nil, 1, change.GroupID)
		info = wire.AppendVarint(info, 2, change.Count)
		out = wire.AppendBytes(out, 6, info)
	}
	if !infinitePurchased {
		preview := s.collection.LatestPreview()
		eventIndex, locked := s.collection.PreviewLock()
		if len(preview) == s.design.Count && eventIndex != 0 {
			var bundle []byte
			for sortID, costumeID := range preview {
				costume := wire.AppendVarint(nil, 2, costumeID)
				if sortID != 0 {
					costume = wire.AppendVarint(costume, 6, uint64(sortID))
				}
				bundle = wire.AppendBytes(bundle, 3, costume)
			}
			persisted := wire.AppendVarint(nil, 1, eventIndex)
			persisted = wire.AppendBytes(persisted, 8, bundle)
			if locked {
				persisted = wire.AppendVarint(persisted, 3, 1)
			}
			out = wire.AppendBytes(out, 9, persisted)
		}
	}
	for _, user := range s.collection.GachaUsersForDay(dailyResetKey(s.now())) {
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

// The 2.35.10 client converts Unix time to UTC+9 and applies GameData's
// 09:00:00 DailyResetTime. That boundary is exactly 00:00 UTC. Keeping the
// key independent of the host OS timezone prevents an early reset on a
// Chinese development machine.
func dailyResetKey(now time.Time) string { return now.UTC().Format("2006-01-02") }
