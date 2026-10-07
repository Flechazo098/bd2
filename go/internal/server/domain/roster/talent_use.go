package roster

import (
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"time"
)

type TalentUseContext func(command.Context) (pack int, mapID uint64, battle bool, err error)
type TalentUseEffect func(ctx command.Context, identity string, character Character, rule gamedata.TalentUseRule, targets []uint64) ([]byte, error)
type TalentUseEconomy interface {
	Apply(ctx command.Context, _ string, _ []gamedata.Reward, _ []gamedata.Reward) ([]byte, error)
}
type talentUseState struct {
	End     int64
	Count   uint64
	Day     string
	Level   uint64
	Settled bool
}
type talentUseReceipt struct {
	Digest string
	Body   []byte
}
type talentUseSnapshot struct {
	Skills    map[uint64]talentUseState
	NPCs      map[string]int64
	Receipts  map[string]talentUseReceipt
	Discounts map[string]uint64
}
type TalentUseService struct {
	design     *gamedata.TalentUseDesign
	store      stateio.Store
	characters *CharacterStore
	inventory  *assets.Inventory
	wallet     *assets.Wallet
	economy    TalentUseEconomy
	context    TalentUseContext
	effects    map[uint64]TalentUseEffect

	now func() time.Time
}

func NewTalentUseService(d *gamedata.TalentUseDesign, store stateio.Store, characters *CharacterStore, inventory *assets.Inventory, wallet *assets.Wallet, economy TalentUseEconomy) (*TalentUseService, error) {
	if d == nil || store == nil || characters == nil || inventory == nil || wallet == nil || economy == nil {
		return nil, fmt.Errorf("player: missing talent use dependencies")
	}
	return &TalentUseService{design: d, store: store, characters: characters, inventory: inventory, wallet: wallet, economy: economy, effects: map[uint64]TalentUseEffect{}, now: time.Now}, nil
}
func (s *TalentUseService) AttachContext(ctx command.Context, f TalentUseContext) { s.context = f }
func (s *TalentUseService) AttachEffect(class uint64, f TalentUseEffect)          { s.effects[class] = f }

func (s *TalentUseService) load(ctx command.Context) (talentUseSnapshot, error) {
	v := talentUseSnapshot{Skills: map[uint64]talentUseState{}, NPCs: map[string]int64{}, Receipts: map[string]talentUseReceipt{}, Discounts: map[string]uint64{}}
	b, e := s.store.Load(ctx.State, "talentuse")
	if e != nil {
		return v, e
	}
	if b != nil {
		if e = stateio.RequireExactJSONObject(b, "Skills", "NPCs", "Receipts", "Discounts"); e != nil {
			return v, e
		}
		if e = json.Unmarshal(b, &v); e != nil || v.Skills == nil || v.NPCs == nil || v.Receipts == nil || v.Discounts == nil {
			return v, fmt.Errorf("player: invalid talent use state")
		}
	}
	return v, nil
}

func sha256Sum(b []byte) []byte        { v := sha256.Sum256(b); return v[:] }
func talentNPCClass(class uint64) bool { return class == 1 || class == 5 || class == 16 || class == 19 }
func talentHasCooldown(class uint64) bool {
	return class == 2 || class == 6 || class == 12 || class == 13 || class == 15 || class == 17
}

func (s *TalentUseService) experienceMaximum(c Character) (uint64, error) {
	if s.design.Growth == nil {
		return 0, fmt.Errorf("player: talent growth design missing")
	}
	meta, ok := s.design.Growth.Characters[c.ID]
	if !ok {
		return 0, fmt.Errorf("player: missing character growth")
	}
	var total uint64
	for level := uint64(1); level <= c.TalentLevel; level++ {
		v, ok := s.design.Growth.Levels[[2]uint64{meta.GrowthGroup, level}]
		if !ok {
			return 0, fmt.Errorf("player: missing talent growth level")
		}
		total += v.NeedExp
	}
	return total, nil
}

func (s *TalentUseService) nextNPCReset(reset uint64) time.Time {
	shift := 9*time.Hour - s.design.ResetSchedule.DailyReset
	clock := s.now().UTC().Add(shift)
	next := time.Date(clock.Year(), clock.Month(), clock.Day()+1, 0, 0, 0, 0, time.UTC)
	if reset == 2 {
		for next.Weekday() != s.design.ResetSchedule.WeeklyDay {
			next = next.AddDate(0, 0, 1)
		}
	}
	return next.Add(-shift)
}

func (s *TalentUseService) ShopDiscount(ctx command.Context, pack, npc uint64) (uint64, error) {

	v, e := s.load(ctx)
	if e != nil {
		return 0, e
	}
	return v.Discounts[ctx.SessionID+":"+fmt.Sprintf("%d/%d", pack, npc)], nil
}

// ConsumeOverwhelm authorizes the follow-up result for one successful cast.
// The one-minute handoff window is this server's request policy, not a claimed
// official skill duration; the field domain validates every actual monster.
func (s *TalentUseService) ConsumeOverwhelm(ctx command.Context, identity string, count uint64) error {

	if identity == "" || count == 0 {
		return fmt.Errorf("player: invalid overwhelm settlement")
	}
	v, err := s.load(ctx)
	if err != nil {
		return err
	}
	receiptKey := "overwhelm:" + identity
	digest := fmt.Sprint(count)
	if prior, ok := v.Receipts[receiptKey]; ok {
		if prior.Digest != digest {
			return fmt.Errorf("player: changed overwhelm settlement")
		}
		return nil
	}
	var chosen uint64
	var latest int64
	for group, state := range v.Skills {
		rule, ok := s.design.Rules[[2]uint64{group, state.Level}]
		if !ok || rule.Class != 3 || state.Settled || state.Count == 0 {
			continue
		}
		if state.End <= s.now().UnixMilli() && s.now().UnixMilli()-state.End <= 60000 && state.End > latest {
			chosen = group
			latest = state.End
		}
	}
	if chosen == 0 {
		return fmt.Errorf("player: overwhelm result has no pending successful cast")
	}
	state := v.Skills[chosen]
	state.Settled = true
	v.Skills[chosen] = state
	v.Receipts[receiptKey] = talentUseReceipt{Digest: digest}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.store.Save(ctx.State, "talentuse", b)
}
