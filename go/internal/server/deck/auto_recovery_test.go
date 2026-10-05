package deck

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"bytes"
	"testing"
)

func recoveryFixture(t *testing.T) *presetFixture {
	f := newPresetFixture(t)
	f.deck.state.FieldCharControlDeckType = 0
	if e := f.deck.AttachFieldSettings(&gamedata.FieldSettingsDesign{TalentSlots: 1, CharacterTalentClass: map[uint64]uint64{350: 10, 360: 14}}); e != nil {
		t.Fatal(e)
	}
	f.characters.AttachMaxHealth(func(player.Character) (uint64, error) { return 100, nil })
	f.characters.SetCurrentHealth(100, 100)
	f.characters.SetCurrentHealth(200, 0)
	if _, _, _, e := f.deck.Handle("/DeckSave", req(1, triple(200, 1, 1))); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(2, wire.AppendVarint(nil, 2, 1), wire.AppendVarint(nil, 3, 100))); e != nil {
		t.Fatal(e)
	}
	return f
}

func TestAutoRecoveryPersistsResponseAndDisabledSettingNeverExecutes(t *testing.T) {
	f := recoveryFixture(t)
	calls := 0
	f.deck.AttachAutoRecovery(func(seq, caster uint64, targets []uint64) (player.AutoRecoveryResult, error) {
		calls++
		if caster != 100 || len(targets) != 1 || targets[0] != 200 {
			t.Fatal("wrong actual fatigue targets")
		}
		f.characters.SetCurrentHealth(200, 25)
		c, _ := f.characters.Find(200)
		c.HP = 25
		return player.AutoRecoveryResult{Caster: 100, Characters: []player.Character{c}, Experience: 2, Catalyst: 10}, nil
	})
	b := req(3, wire.AppendVarint(nil, 2, 100))
	code, out, _, e := f.deck.Handle("/DeckCharAutoRevive", b)
	if e != nil || code != 373 {
		t.Fatal(e)
	}
	mode, _, _ := wire.Varint(out, 4)
	caster, _, _ := wire.Varint(out, 9)
	if mode != 1 || caster != 100 {
		t.Fatalf("response%x", out)
	}
	reopened, e := OpenStore(f.storage, f.seed, testPresetDesign)
	if e != nil {
		t.Fatal(e)
	}
	reopened.BeginSession("preset-test")
	_, again, _, e := reopened.Handle("/DeckCharAutoRevive", b)
	if e != nil || !bytes.Equal(out, again) || calls != 1 {
		t.Fatal("automatic recovery receipt replay")
	}
	if _, _, _, e = f.deck.Handle("/CharAutoReviveSet", req(4, wire.AppendVarint(nil, 3, 100))); e != nil {
		t.Fatal(e)
	}
	f.characters.SetCurrentHealth(200, 0)
	_, out, _, e = f.deck.Handle("/DeckCharAutoRevive", req(5, wire.AppendVarint(nil, 2, 100)))
	if e != nil || calls != 1 {
		t.Fatal("disabled automatic recovery executed", e)
	}
	if got := f.deck.CurrentDeck()[0].CharacterInvenIndex; got != 100 {
		t.Fatalf("fatigued member not replaced: %d", got)
	}
	if _, _, _, e = f.deck.Handle("/CharAutoReviveSet", req(6, wire.AppendVarint(nil, 3, 200))); e == nil {
		t.Fatal("disabled setting accepted Immortal class14 caster")
	}
}

func TestAutoRecoveryFailureReportsExhaustionAndStoryCannotRecover(t *testing.T) {
	f := recoveryFixture(t)
	if _, _, _, e := f.deck.Handle("/DeckSave", req(3, triple(100, 1, 1), triple(200, 2, 2))); e != nil {
		t.Fatal(e)
	}
	f.deck.AttachAutoRecovery(func(uint64, uint64, []uint64) (player.AutoRecoveryResult, error) {
		return player.AutoRecoveryResult{Caster: 100, Catalyst: 0, Disabled: 2}, nil
	})
	_, out, _, e := f.deck.Handle("/DeckCharAutoRevive", req(4, wire.AppendVarint(nil, 2, 100)))
	mode, _, _ := wire.Varint(out, 4)
	disabled, _, _ := wire.Varint(out, 8)
	if e != nil || mode != 3 || disabled != 2 {
		t.Fatalf("failure mode%d disabled%d %v", mode, disabled, e)
	}
	f.characters.SetCurrentHealth(100, 0)
	f.deck.state.FieldCharControlDeckType = 2
	f.deck.AttachAutoRecovery(func(uint64, uint64, []uint64) (player.AutoRecoveryResult, error) {
		t.Fatal("story mode must not recover temporary party")
		return player.AutoRecoveryResult{}, nil
	})
	_, out, _, e = f.deck.Handle("/DeckCharAutoRevive", req(5, wire.AppendVarint(nil, 2, 100)))
	mode, _, _ = wire.Varint(out, 4)
	if e != nil || mode != 3 {
		t.Fatalf("story fatigue mode%d %v", mode, e)
	}
}
