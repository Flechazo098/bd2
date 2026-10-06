package deck

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"testing"
	"time"
)

func TestTalentSlotsRestoreOwnershipAndClearSlots(t *testing.T) {
	f := newPresetFixture(t)
	design := &gamedata.FieldSettingsDesign{TalentSlots: 3, CharacterTalentClass: map[uint64]uint64{350: 10, 360: 9}}
	if e := f.deck.AttachFieldSettings(design); e != nil {
		t.Fatal(e)
	}
	save := req(1, wire.AppendVarint(nil, 2, 360), wire.AppendVarint(nil, 2, 0), wire.AppendVarint(nil, 2, 350))
	if _, _, _, e := f.deck.Handle("/TalentSlotSave", save); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]uint64{{350, 350, 0}, {999, 0, 0}, {100, 0, 0}, {350}} {
		var r []byte
		for _, id := range bad {
			r = wire.AppendVarint(r, 2, id)
		}
		if _, _, _, e := f.deck.Handle("/TalentSlotSave", req(2, r)); e == nil {
			t.Fatal("invalid slots accepted", bad)
		}
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(3, wire.AppendVarint(nil, 2, 1), wire.AppendVarint(nil, 3, 200))); e == nil {
		t.Fatal("wrong talent caster accepted")
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(4, wire.AppendVarint(nil, 2, 1), wire.AppendVarint(nil, 3, 100))); e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenStore(f.storage, f.seed, f.deck.presetDesign)
	if e != nil {
		t.Fatal(e)
	}
	reopened.characters = f.characters
	if e = reopened.AttachFieldSettings(design); e != nil {
		t.Fatal(e)
	}
	ids, e := reopened.TalentCharacterIDs()
	if e != nil || ids[0] != 360 || ids[1] != 0 || ids[2] != 350 {
		t.Fatal(ids, e)
	}
	on, caster, e := reopened.AutoReviveSettings()
	if e != nil || !on || caster != 100 {
		t.Fatal(on, caster, e)
	}
	_, b, _, e := reopened.Handle("/DeckInfo", req(5))
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	if err := wire.Walk(b, func(f wire.Field) error {
		if f.Number == 2 {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatal("DeckInfo missing empty quick slot", n)
	}
}

func TestCharmFieldAndTalentSlotsExpireWithoutPermanentOwnership(t *testing.T) {
	f := newPresetFixture(t)
	base := f.characters.RawAll()
	index := player.CharmCharacterIndexBase + 11
	install := func(expiry uint64) {
		m := stateio.NewMemory()
		inv, e := player.OpenInventory(m, &player.Starter{Version: f.seed.Version})
		if e != nil {
			t.Fatal(e)
		}
		chars := append(append([]player.Character(nil), base...), player.Character{InvenIndex: index, ID: 9010, Level: 1, CostumeID: 90101, UseCostume: 0, ExpiryTime: expiry})
		s, e := player.OpenCharacterStore(m, chars, inv, "", "")
		if e != nil {
			t.Fatal(e)
		}
		f.deck.characters = s
	}
	install(uint64(time.Now().Add(time.Hour).UnixMilli()))
	d := &gamedata.FieldSettingsDesign{TalentSlots: 1, CharacterTalentClass: map[uint64]uint64{9010: 10}, CharacterTemporaryPack: map[uint64]int{9010: 99}}
	if err := f.deck.AttachFieldSettingsPack(func() (int, error) { return 4, nil }); err != nil {
		t.Fatal(err)
	}
	if e := f.deck.AttachFieldSettings(d); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/TalentSlotSave", req(1, wire.AppendVarint(nil, 2, 9010))); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/FieldDeckSave", req(2, triple(1, index, 0))); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(3, wire.AppendVarint(nil, 2, 1), wire.AppendVarint(nil, 3, index))); e == nil {
		t.Fatal("charm recovery caster accepted")
	}
	install(uint64(time.Now().Add(-time.Second).UnixMilli()))
	ids, e := f.deck.TalentCharacterIDs()
	if e != nil || ids[0] != 0 {
		t.Fatal("expired charm quick slot leaked", ids, e)
	}
	if len(f.deck.CurrentFieldDeck()) != 0 {
		t.Fatal("expired charm field party leaked")
	}
}

func TestTemporaryFieldAndTalentSlotsFollowPack(t *testing.T) {
	f := newPresetFixture(t)
	c := player.Character{InvenIndex: player.StoryCharacterIndexBase + 99, ID: 9001, Level: 1, CostumeID: 90011, UseCostume: 9999}
	if e := f.characters.EnsureStoryCharacters([]player.Character{c}); e != nil {
		t.Fatal(e)
	}
	pack := 4
	d := &gamedata.FieldSettingsDesign{TalentSlots: 1, CharacterTalentClass: map[uint64]uint64{9001: 9}, CharacterTemporaryPack: map[uint64]int{9001: 4}}
	if err := f.deck.AttachFieldSettingsPack(func() (int, error) { return pack, nil }); err != nil {
		t.Fatal(err)
	}
	if e := f.deck.AttachFieldSettings(d); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/TalentSlotSave", req(1, wire.AppendVarint(nil, 2, c.ID))); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := f.deck.Handle("/FieldDeckSave", req(2, triple(1, c.InvenIndex, c.UseCostume))); e != nil {
		t.Fatal(e)
	}
	pack = 5
	ids, e := f.deck.TalentCharacterIDs()
	if e != nil || ids[0] != 0 {
		t.Fatal("temporary quick slot leaked", ids, e)
	}
	if len(f.deck.CurrentFieldDeck()) != 0 {
		t.Fatal("temporary field party leaked")
	}
	if _, _, _, e := f.deck.Handle("/FieldDeckSave", req(3, triple(1, c.InvenIndex, c.UseCostume))); e == nil {
		t.Fatal("foreign-pack temporary party accepted")
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(4)); e != nil {
		t.Fatal("inactive quick slots blocked unrelated setting", e)
	}
}

func TestMalformedSettingsDoNotPersist(t *testing.T) {
	f := newPresetFixture(t)
	d := &gamedata.FieldSettingsDesign{TalentSlots: 2, CharacterTalentClass: map[uint64]uint64{350: 10, 360: 9}}
	if e := f.deck.AttachFieldSettings(d); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{req(0, wire.AppendVarint(nil, 2, 350)), req(1, wire.AppendBytes(nil, 2, []byte{0x80})), req(2, wire.AppendFixed64(nil, 2, 350)), req(^uint64(0), wire.AppendVarint(nil, 2, 350)), req(3, wire.AppendVarint(nil, 2, ^uint64(0)), wire.AppendVarint(nil, 2, 0))} {
		if _, _, _, e := f.deck.Handle("/TalentSlotSave", bad); e == nil {
			t.Fatal("malformed talent request accepted")
		}
		if _, ok, e := f.storage.LoadEntry("deck", "field_settings", "state"); e != nil || ok {
			t.Fatal("invalid talent request wrote settings", e)
		}
	}
	if _, _, _, e := f.deck.Handle("/CharAutoReviveSet", req(4, wire.AppendVarint(nil, 2, 2))); e == nil {
		t.Fatal("invalid protobuf bool accepted")
	}
	if _, _, _, e := f.deck.Handle("/FieldDeckSave", req(5, triple(2, 100, 1001))); e == nil {
		t.Fatal("field gap accepted")
	}
}
