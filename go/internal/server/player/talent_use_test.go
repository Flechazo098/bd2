package player

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type talentEconomyTest struct {
	inventory *Inventory
	calls     int
}

func (e *talentEconomyTest) Apply(identity string, _ []gamedata.Reward, r []gamedata.Reward) ([]byte, error) {
	e.calls++
	var rewards []gamedata.BattleReward
	for _, v := range r {
		rewards = append(rewards, gamedata.BattleReward{Type: v.Type, ID: v.ID, Count: v.Count}) //nolint:staticcheck // S1016
	}
	items, err := e.inventory.GrantOnce(identity, rewards)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, v := range items {
		out = wire.AppendBytes(out, 1, ItemWire(v))
	}
	return out, nil
}
func talentTest(t *testing.T, store stateio.Store, class uint64) (*TalentUseService, *CharacterStore, *Wallet, *talentEconomyTest) {
	t.Helper()
	inventory, err := OpenInventory(store, &Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	chars, err := OpenCharacterStore(store, []Character{{InvenIndex: 77, ID: 350, Level: 1, HP: 100, TalentLevel: 1}}, inventory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := chars.AttachMaxHealth(func(Character) (uint64, error) { return 100, nil }); err != nil {
		t.Fatal(err)
	}
	if err = chars.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	wallet, err := OpenWallet(store, Currency{Catalyst: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	d := &gamedata.TalentUseDesign{Characters: map[uint64]gamedata.TalentUseCharacter{350: {Group: 42, MaxLevel: 5}}, Rules: map[[2]uint64]gamedata.TalentUseRule{{42, 1}: {Group: 42, Level: 1, Class: class, Catalyst: 5, Experience: 3, Values: []float64{100, 10, 0}}}, Rewards: map[uint64][]gamedata.Reward{9: {{Type: 8, ID: 987, Count: 2}}}, Growth: &gamedata.TalentGrowthDesign{Characters: map[uint64]gamedata.CharacterTalent{350: {GrowthGroup: 6, MaxLevel: 5}}, Levels: map[[2]uint64]gamedata.TalentGrowthLevel{{6, 1}: {Level: 1, NeedExp: 10}}}, NPCs: func(int) (map[uint64]gamedata.TalentNPC, error) {
		return map[uint64]gamedata.TalentNPC{11: {ID: 11, MapID: 211, Groups: []uint64{42}, Rewards: []uint64{9}}}, nil
	}}
	e := &talentEconomyTest{inventory: inventory}
	s, err := NewTalentUseService(d, store, chars, inventory, wallet, e)
	if err != nil {
		t.Fatal(err)
	}
	s.BeginSession("test")
	s.now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	s.AttachContext(func() (int, uint64, bool, error) { return 21, 211, false, nil })
	return s, chars, wallet, e
}
func talentRequest(seq uint64, targets ...uint64) []byte {
	b := wire.AppendVarint(wire.AppendVarint(nil, 1, seq), 2, 77)
	for _, id := range targets {
		b = wire.AppendVarint(b, 3, id)
	}
	return b
}

func TestTalentNPCRewardCostExperienceAndRestartReplay(t *testing.T) {
	store := stateio.NewMemory()
	s, chars, wallet, e := talentTest(t, store, 1)
	b := talentRequest(1, 11)
	code, body, handled, err := s.Handle("/TalentSkillUse", b)
	if err != nil || code != 43 || !handled {
		t.Fatal(err)
	}
	c, _ := chars.Find(77)
	if wallet.Snapshot().Catalyst != 45 || c.TalentExp != 3 || e.calls != 1 {
		t.Fatal("talent did not charge/grant experience/reward")
	}
	if reward, ok, _ := wire.Bytes(body, 3); !ok || len(reward) == 0 {
		t.Fatal("talent omitted actual reward")
	}
	next, _, _, _ := talentTest(t, store, 1)
	_, again, _, err := next.Handle("/TalentSkillUse", b)
	if err != nil || !bytes.Equal(body, again) {
		t.Fatal("restart lost exact talent replay")
	}
	if _, _, _, err = next.Handle("/TalentSkillUse", talentRequest(2, 11)); err == nil {
		t.Fatal("NPC cooldown permitted repeat theft")
	}
	if _, _, _, err = next.Handle("/TalentSkillUse", talentRequest(1, 12)); err == nil {
		t.Fatal("changed sequence accepted")
	}
}

func TestTalentDurationAndDailyLimitsComeFromDesign(t *testing.T) {
	s, _, _, _ := talentTest(t, stateio.NewMemory(), 13)
	now := s.now()
	s.now = func() time.Time { return now }
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(1)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(2)); err == nil {
		t.Fatal("duration skill reused while active")
	}
	now = now.Add(10 * time.Second)
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(3)); err != nil {
		t.Fatal("duration did not expire")
	}
	s, _, _, _ = talentTest(t, stateio.NewMemory(), 4)
	now = s.now()
	s.now = func() time.Time { return now }
	r := s.design.Rules[[2]uint64{42, 1}]
	r.Values[0] = 1
	s.design.Rules[[2]uint64{42, 1}] = r
	s.AttachEffect(4, func(string, Character, gamedata.TalentUseRule, []uint64) ([]byte, error) { return nil, nil })
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(1, 3)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(2, 3)); err == nil {
		t.Fatal("daily skill limit ignored")
	}
	now = now.Add(24 * time.Hour)
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(3, 3)); err != nil {
		t.Fatal("daily limit did not reset")
	}
}

type talentFailStore struct{ stateio.Store }

func (s talentFailStore) Save(name string, b []byte) error {
	if name == "talentuse" {
		return errors.New("receipt write failed")
	}
	return s.Store.Save(name, b)
}
func TestTalentReceiptFailureRollsBackRewardCostAndExperience(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := talentTest(t, repo, 1)
	s.store = talentFailStore{repo}
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/TalentSkillUse", talentRequest(1, 11)); err == nil {
		t.Fatal("receipt failure ignored")
	}
	if err = op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	next, chars, wallet, e := talentTest(t, repo, 1)
	c, _ := chars.Find(77)
	if c.TalentExp != 0 || wallet.Snapshot().Catalyst != 50 || len(e.inventory.All()) != 0 {
		t.Fatal("partial talent mutation survived rollback")
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = next.Handle("/TalentSkillUse", talentRequest(1, 11)); err != nil {
		t.Fatal(err)
	}
	if err = op.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestTalentSkillFoodConsumesOneWithoutChargingCasterResources(t *testing.T) {
	s, _, wallet, _ := talentTest(t, stateio.NewMemory(), 13)
	s.design.Foods = map[uint64]uint64{22: 42}
	items, err := s.inventory.GrantOnce("test-food", []gamedata.BattleReward{{Type: 5, ID: 22, Count: 2}})
	if err != nil {
		t.Fatal(err)
	}
	b := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 4, items[0].InvenIndex)
	_, reply, _, err := s.Handle("/TalentSkillUse", b)
	if err != nil {
		t.Fatal(err)
	}
	if s.inventory.All()[0].Count != 1 || wallet.Snapshot().Catalyst != 50 {
		t.Fatal("skill food duplicated catalyst charge or did not consume one")
	}
	_, again, _, err := s.Handle("/TalentSkillUse", b)
	if err != nil || !bytes.Equal(reply, again) || s.inventory.All()[0].Count != 1 {
		t.Fatal("skill food retry consumed twice")
	}
}

func TestCharmCompanionsExpireWithoutPermanentCollectionGrant(t *testing.T) {
	s, chars, _, _ := talentTest(t, stateio.NewMemory(), 19)
	s.now = func() time.Time { return time.Now() }
	s.design.NPCs = func(int) (map[uint64]gamedata.TalentNPC, error) {
		return map[uint64]gamedata.TalentNPC{11: {ID: 11, MapID: 211, Groups: []uint64{42}, Rewards: []uint64{0}, CharmCharacters: []uint64{999}}}, nil
	}
	s.design.CharmCharacter = func(uint64) (gamedata.StoryCharacterDesign, error) {
		return gamedata.StoryCharacterDesign{CharacterID: 999, Level: 1, CostumeID: 111, HP: 100}, nil
	}
	_, reply, _, err := s.Handle("/TalentSkillUse", talentRequest(1, 11))
	if err != nil {
		t.Fatal(err)
	}
	w, ok, _ := wire.Bytes(reply, 5)
	if !ok {
		t.Fatal("charm did not return companion")
	}
	index, _, _ := wire.Varint(w, 1)
	c, found := chars.Find(index)
	if !found || !IsCharmCharacter(c) || c.ExpiryTime == 0 || len(chars.All()) != 2 {
		t.Fatal("charm was not a temporary visible character")
	}
	c.ExpiryTime = uint64(time.Now().Add(-time.Second).UnixMilli())
	if err = chars.ensureCharmCharacters([]Character{c}); err != nil {
		t.Fatal(err)
	}
	if _, found = chars.Find(index); found || len(chars.All()) != 1 {
		t.Fatal("expired charm companion remained playable")
	}
	if chars.collection != nil {
		t.Fatal("charm created a collection grant")
	}
}

func TestCharHealingRevivesFatigueChargesPerTargetAndReplays(t *testing.T) {
	s, chars, wallet, _ := talentTest(t, stateio.NewMemory(), 10)
	r := s.design.Rules[[2]uint64{42, 1}]
	r.Values[0] = 0
	s.design.Rules[[2]uint64{42, 1}] = r
	if err := chars.SetCurrentHealth(77, 0); err != nil {
		t.Fatal(err)
	}
	b := wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 77), 3, 77)
	code, response, _, err := s.Handle("/CharHealing", b)
	if err != nil || code != 62 {
		t.Fatal(err)
	}
	hp, _ := chars.CurrentHealth(77)
	if hp != 1 || wallet.Snapshot().Catalyst != 45 {
		t.Fatal("fatigue revival did not restore1HP with exact catalyst cost")
	}
	_, again, _, err := s.Handle("/CharHealing", b)
	if err != nil || !bytes.Equal(response, again) || wallet.Snapshot().Catalyst != 45 {
		t.Fatal("revival replay charged twice")
	}
	if _, _, _, err = s.Handle("/CharHealing", wire.AppendVarint(wire.AppendVarint(wire.AppendVarint(nil, 1, 2), 2, 77), 3, 77)); err == nil {
		t.Fatal("living character revived again")
	}
}

func TestBargainPercentIsSessionDiscountAndNotCooldownDuration(t *testing.T) {
	s, _, _, _ := talentTest(t, stateio.NewMemory(), 16)
	r := s.design.Rules[[2]uint64{42, 1}]
	r.Values[1] = 35
	s.design.Rules[[2]uint64{42, 1}] = r
	s.design.NPCs = func(int) (map[uint64]gamedata.TalentNPC, error) {
		return map[uint64]gamedata.TalentNPC{11: {ID: 11, MapID: 211, Groups: []uint64{42}, Rewards: []uint64{0}}}, nil
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(1, 11)); err != nil {
		t.Fatal(err)
	}
	discount, err := s.ShopDiscount(21, 11)
	if err != nil || discount != 35 {
		t.Fatal("bargain did not grant35 percent")
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(2, 11)); err != nil {
		t.Fatal("bargain percent incorrectly treated as35 second cooldown")
	}
	s.BeginSession("other")
	discount, err = s.ShopDiscount(21, 11)
	if err != nil || discount != 0 {
		t.Fatal("bargain leaked to new login session")
	}
}

func TestPackTalentProjectionUsesActualLevelAndResetsUsageOnNextDay(t *testing.T) {
	s, _, _, _ := talentTest(t, stateio.NewMemory(), 13)
	s.design.Rules[[2]uint64{42, 2}] = gamedata.TalentUseRule{Group: 42, Level: 2, Class: 13, Values: []float64{0.75, 99}}
	b := []byte(`{"Skills":{"42":{"End":1,"Count":1,"Day":"2026-10-04","Level":2}},"NPCs":{},"Receipts":{},"Discounts":{}}`)
	if err := s.store.Save("talentuse", b); err != nil {
		t.Fatal(err)
	}
	projection, err := s.PackInfo(21)
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ := wire.Bytes(projection, 13)
	cool, _, _ := wire.Varint(row, 3)
	if cool != 99 {
		t.Fatal("projection downgraded actual talent duration to level1")
	}
	r := s.design.Rules[[2]uint64{42, 2}]
	r.Class = 4
	s.design.Rules[[2]uint64{42, 2}] = r
	projection, err = s.PackInfo(21)
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ = wire.Bytes(projection, 13)
	count, _, _ := wire.Varint(row, 4)
	if count != 0 {
		t.Fatal("next-day client projection kept exhausted daily count")
	}
}

func TestCharmSQLiteRestartAndNewCharmRefreshDamagedHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repo, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, chars, _, _ := talentTest(t, repo, 19)
	s.now = time.Now
	s.design.NPCs = func(int) (map[uint64]gamedata.TalentNPC, error) {
		return map[uint64]gamedata.TalentNPC{11: {ID: 11, MapID: 211, Groups: []uint64{42}, Rewards: []uint64{0}, CharmCharacters: []uint64{999}}}, nil
	}
	s.design.CharmCharacter = func(uint64) (gamedata.StoryCharacterDesign, error) {
		return gamedata.StoryCharacterDesign{CharacterID: 999, Level: 1, CostumeID: 111, HP: 100}, nil
	}
	_, response, _, err := s.Handle("/TalentSkillUse", talentRequest(1, 11))
	if err != nil {
		t.Fatal(err)
	}
	row, _, _ := wire.Bytes(response, 5)
	index, _, _ := wire.Varint(row, 1)
	if err = chars.SetCurrentHealth(index, 5); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()
	next, chars, _, _ := talentTest(t, repo, 19)
	next.design = s.design
	next.now = time.Now
	c, found := chars.Find(index)
	if !found || c.ExpiryTime == 0 || c.HP != 5 {
		t.Fatal("restart lost temporary companion expiration or currentHP")
	}
	c.ExpiryTime = uint64(time.Now().Add(-time.Second).UnixMilli())
	if err = chars.ensureCharmCharacters([]Character{c}); err != nil {
		t.Fatal(err)
	}
	state, err := next.load()
	if err != nil {
		t.Fatal(err)
	}
	state.NPCs = map[string]int64{}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Save("talentuse", b); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = next.Handle("/TalentSkillUse", talentRequest(2, 11)); err != nil {
		t.Fatal(err)
	}
	c, found = chars.Find(index)
	if !found || c.HP != 100 {
		t.Fatal("new charm reused old companion damage")
	}
}

func TestOverwhelmRequiresPendingCastAndConsumesAuthorizationOnce(t *testing.T) {
	s, _, _, _ := talentTest(t, stateio.NewMemory(), 3)
	if err := s.ConsumeOverwhelm("first", 2); err == nil {
		t.Fatal("unearned overwhelm result accepted")
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOverwhelm("first", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeOverwhelm("first", 2); err != nil {
		t.Fatal("exact overwhelm replay rejected")
	}
	if err := s.ConsumeOverwhelm("first", 3); err == nil {
		t.Fatal("changed monster count accepted")
	}
	if err := s.ConsumeOverwhelm("second", 2); err == nil {
		t.Fatal("one cast authorized two independent settlements")
	}
	if _, _, _, err := s.Handle("/TalentSkillUse", talentRequest(2)); err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(61 * time.Second)
	s.now = func() time.Time { return now }
	if err := s.ConsumeOverwhelm("expired", 2); err == nil {
		t.Fatal("expired handoff authorization accepted")
	}
}
