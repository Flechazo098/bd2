// Package gacha implements server-authoritative draws from verified GameData,
// including the starter reroll and scheduled daily-free batches.
package gacha

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/roster"
	"errors"
	"fmt"

	"time"
)

func (s *Service) infiniteGrant() string {
	return fmt.Sprintf("cash-product:%d:%d", s.design.ProductGroupID, s.design.ProductID)
}

type Service struct {
	design             *gamedata.InfiniteGachaDesign
	first              *gamedata.FirstGachaDesign
	regular            *gamedata.RegularGachaCatalog
	collection         *roster.CollectionStore
	wallet             *assets.Wallet
	inventory          *assets.Inventory
	equipmentCatalog   *gamedata.EquipmentGachaCatalog
	equipmentInventory *assets.EquipmentInventory
	onPreview          func(ctx command.Context) error
	onDraw             func(ctx command.Context, _ uint64) error
	schedule           *ScheduleSeed
	previewEventIndex  uint64

	firstPreviews    map[string]firstGachaPreview
	transientVersion uint64
	now              func() time.Time
}

func (s *Service) TransientVersion() uint64 {

	return s.transientVersion
}

func (s *Service) AttachDrawMission(callback func(ctx command.Context, _ uint64) error) {
	s.onDraw = callback
}

func (s *Service) AttachPreviewMission(callback func(ctx command.Context) error) {
	s.onPreview = callback
}
func (s *Service) AttachInventory(inventory *assets.Inventory) { s.inventory = inventory }
func (s *Service) AttachPreviewEventIndex(ctx command.Context, eventIndex uint64) error {
	if eventIndex == 0 {
		return errors.New("gacha: invalid preview event index")
	}
	s.previewEventIndex = eventIndex
	return nil
}
func (s *Service) AttachSchedule(ctx command.Context, seed *ScheduleSeed) error {
	if err := seed.Validate(seedVersion(seed)); err != nil {
		return err
	}
	copy := *seed
	copy.Schedules = append([]ScheduleWindow(nil), seed.Schedules...)
	copy.StepUps = append([]ScheduleWindow(nil), seed.StepUps...)
	s.schedule = &copy
	return nil
}
func (s *Service) AttachEquipmentGacha(catalog *gamedata.EquipmentGachaCatalog, inventory *assets.EquipmentInventory) {
	s.equipmentCatalog, s.equipmentInventory = catalog, inventory
}

func (s *Service) AttachFirstGacha(ctx command.Context, design *gamedata.FirstGachaDesign) error {
	if design == nil || design.GachaID == 0 || design.Count == 0 || design.Group.GachaSubType != 3 {
		return errors.New("gacha: invalid first gacha design")
	}
	s.first = design

	s.firstPreviews = make(map[string]firstGachaPreview)

	return nil
}

// FirstGachaCompleted reflects UserDBInfo.IsFirstGacha. The official symbol
// map names the client-side property IsDoneFirstGachaPick, and the client sets
// it after a GachaSubType=3 purchase succeeds.
func (s *Service) FirstGachaCompleted() bool {
	return s.collection.FirstGachaCompleted()
}

func NewService(design *gamedata.InfiniteGachaDesign, regular *gamedata.RegularGachaCatalog, collection *roster.CollectionStore, wallet *assets.Wallet) (*Service, error) {
	if design == nil || regular == nil || collection == nil || wallet == nil {
		return nil, errors.New("gacha: invalid service configuration")
	}
	return &Service{design: design, regular: regular, collection: collection, wallet: wallet, now: time.Now}, nil
}

func (s *Service) dailyFreeAllowance(groupID, base uint64) (uint64, bool, error) {
	if s.schedule == nil {
		return 0, false, nil
	}
	now := s.now().UnixMilli()
	for _, entry := range s.schedule.Schedules {
		if entry.GroupID != groupID || now < 0 || uint64(now) < entry.StartTime || uint64(now) > entry.EndTime {
			continue
		}
		if entry.FreeCountBonus {
			bonus := s.regular.GachaEventAddFreeCount()
			if ^uint64(0)-base < bonus {
				return 0, false, errors.New("gacha: daily free allowance overflows")
			}
			base += bonus
		}
		return base, true, nil
	}
	return 0, false, nil
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

func costumeFixedStates(fixed gamedata.GachaFixedDesign, result gamedata.GachaFixedRoll) []roster.GachaFixedState {
	states := make([]roster.GachaFixedState, 0, 2)
	if fixed.CostumeGrade4Count != 0 {
		states = append(states, roster.GachaFixedState{
			FixedID: fixed.ID, Type: 0, Count: result.CostumeGrade4Count, ApplySort: result.CostumeGrade4Sort,
		})
	}
	if fixed.CostumeGrade5Count != 0 {
		states = append(states, roster.GachaFixedState{
			FixedID: fixed.ID, Type: 1, Count: result.CostumeGrade5Count, ApplySort: result.CostumeGrade5Sort,
		})
	}
	return states
}

func (s *Service) completeSharedFixedStates(fixedID uint64, updates []roster.GachaFixedState) []roster.GachaFixedState {
	if fixedID == 0 || s.equipmentCatalog == nil {
		return updates
	}
	known := false
	for _, fixed := range s.equipmentCatalog.FixedDesigns() {
		if fixed.ID == fixedID {
			known = true
			break
		}
	}
	if !known {
		return updates
	}
	states := make([]roster.GachaFixedState, 4)
	for fixedType := range states {
		states[fixedType] = roster.GachaFixedState{
			FixedID: fixedID, Type: uint64(fixedType),
			Count: s.collection.GachaFixedCount(fixedID, uint64(fixedType)), ApplySort: -1,
		}
	}
	for _, update := range updates {
		if update.FixedID != fixedID || update.Type >= uint64(len(states)) {
			return updates
		}
		states[update.Type] = update
	}
	return states
}

func (s *Service) loginIdentity(ctx command.Context) string {
	return ctx.SessionID
}

func (s *Service) requestIdentity(ctx command.Context, gachaID, seq uint64) string {
	return fmt.Sprintf("regular-gacha:%d:session:%s:seq:%d", gachaID, ctx.SessionID, seq)
}

func (s *Service) creditOverflow(ctx command.Context, identity string, grant roster.CollectionGrant) error {
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
	_, err := s.wallet.GrantMileageOnce(ctx, identity, mileage)
	return err
}

// The client converts Unix time to UTC+9 and applies GameData's
// 09:00:00 DailyResetTime. That boundary is exactly 00:00 UTC. Keeping the
// key independent of the host OS timezone prevents an early reset on a
// Chinese development machine.
func dailyResetKey(now time.Time) string { return now.UTC().Format("2006-01-02") }
