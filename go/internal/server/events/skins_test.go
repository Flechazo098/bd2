package events

import (
	"bytes"
	"fmt"
	"testing"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func skinSetRequest(seq, costume, design uint64, set bool) []byte {
	info := wire.AppendVarint(nil, 1, costume)
	info = wire.AppendVarint(info, 2, design)
	if set {
		info = wire.AppendVarint(info, 3, 1)
	}
	return wire.AppendBytes(wire.AppendVarint(nil, 1, seq), 2, info)
}

func TestPrestigeSkinSelectionPersistsAndReplaysWithoutUndoingLaterChanges(t *testing.T) {
	store := stateio.NewMemory()
	eco, _, _ := economyFixture(t, store, &economyGraph{})
	design := map[uint64]uint64{9901: 60601, 9902: 60601, 9903: 60602}
	eco.AttachPrestigeSkins(design)
	if _, err := eco.Apply("skins", nil, []gamedata.Reward{{Type: 45, ID: 9901, Count: 1}, {Type: 45, ID: 9902, Count: 1}, {Type: 45, ID: 9903, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	portrait := uint64(60601)
	eco.AttachPrestigePortrait(func() uint64 { return portrait })
	handler := SkinHandler{Economy: eco}
	first := skinSetRequest(1, 60601, 9901, true)
	code, original, handled, err := handler.HandleSession("/PrestigeSkinSet", first, "s1")
	if err != nil || code != 426 || !handled {
		t.Fatalf("set: %d %v %v", code, handled, err)
	}
	for _, req := range [][]byte{skinSetRequest(2, 60601, 9902, true), skinSetRequest(3, 60602, 9903, true), skinSetRequest(4, 60601, 9901, false)} {
		_, out, _, callErr := handler.HandleSession("/PrestigeSkinSet", req, "s1")
		if callErr != nil {
			t.Fatal(callErr)
		}
		id, _, _ := wire.Varint(out, 1)
		design, _, _ := wire.Varint(out, 2)
		if id != 60601 || design != 9902 {
			t.Fatalf("set changed unrelated portrait: %x", out)
		}
	}
	// Disabling an older skin cannot clear the newer choice on the same costume.
	sets, err := eco.PrestigeSkinSelections()
	if err != nil || sets[60601] != 9902 || sets[60602] != 9903 {
		t.Fatalf("selections: %v %v", sets, err)
	}
	portraitID, _, _ := wire.Varint(original, 1)
	portraitDesign, _, _ := wire.Varint(original, 2)
	if portraitID != 60601 || portraitDesign != 9901 {
		t.Fatalf("portrait: %x", original)
	}
	_, _, _, err = handler.HandleSession("/PrestigeSkinSet", skinSetRequest(1, 60601, 9902, true), "s1")
	if err == nil {
		t.Fatal("sequence conflict accepted")
	}
	reopened, _, _ := economyFixture(t, store, &economyGraph{})
	reopened.AttachPrestigeSkins(design)
	_, replay, _, err := reopened.HandleSession("/PrestigeSkinSet", first, "s1")
	if err != nil || !bytes.Equal(original, replay) {
		t.Fatalf("restart replay %x %v", replay, err)
	}
	_, info, _, err := reopened.PrestigeSkinInfo("/PrestigeSkinInfo", wire.AppendVarint(nil, 1, 9))
	if err != nil {
		t.Fatal(err)
	}
	selected := map[uint64]bool{}
	if err = wire.Walk(info, func(f wire.Field) error {
		id, _, _ := wire.Varint(f.Value, 2)
		set, _, _ := wire.Varint(f.Value, 3)
		selected[id] = set == 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if selected[9901] || !selected[9902] || !selected[9903] {
		t.Fatalf("info selection: %v", selected)
	}
	if _, _, _, err = reopened.HandleSession("/PrestigeSkinSet", skinSetRequest(1, 60601, 9902, false), "s2"); err != nil {
		t.Fatal(err)
	}
	sets, err = reopened.PrestigeSkinSelections()
	if err != nil || sets[60601] != 0 || sets[60602] != 9903 {
		t.Fatalf("unset disturbed another costume: %v %v", sets, err)
	}
}

type failedSkinStore struct {
	*stateio.Memory
	fail bool
}

func (s *failedSkinStore) Save(name string, payload []byte) error {
	if s.fail && name == "prestige_skin_sets" {
		return fmt.Errorf("injected skin save failure")
	}
	return s.Memory.Save(name, payload)
}

func TestPrestigeSkinRejectsUnownedAndFailedSaveLeavesSelectionUnchanged(t *testing.T) {
	store := &failedSkinStore{Memory: stateio.NewMemory()}
	eco, _, _ := economyFixture(t, store, &economyGraph{})
	eco.AttachPrestigeSkins(map[uint64]uint64{9901: 60601, 9902: 60601})
	if _, err := eco.Apply("skin", nil, []gamedata.Reward{{Type: 45, ID: 9901, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	for _, req := range [][]byte{skinSetRequest(1, 60601, 9902, true), skinSetRequest(1, 60602, 9901, true), wire.AppendBytes(wire.AppendVarint(nil, 1, 1), 2, wire.AppendBytes(wire.AppendVarint(wire.AppendVarint(nil, 1, 60601), 2, 9901), 3, nil))} {
		if _, _, _, err := eco.HandleSession("/PrestigeSkinSet", req, "session"); err == nil {
			t.Fatalf("invalid request accepted: %x", req)
		}
	}
	req := skinSetRequest(1, 60601, 9901, true)
	store.fail = true
	if _, _, _, err := eco.HandleSession("/PrestigeSkinSet", req, "session"); err == nil {
		t.Fatal("save failure succeeded")
	}
	sets, err := eco.PrestigeSkinSelections()
	if err != nil || len(sets) != 0 {
		t.Fatalf("failed selection leaked %v %v", sets, err)
	}
	store.fail = false
	if _, _, _, err = eco.HandleSession("/PrestigeSkinSet", req, "session"); err != nil {
		t.Fatal(err)
	}
	sets, err = eco.PrestigeSkinSelections()
	if err != nil || sets[60601] != 9901 {
		t.Fatalf("retry not committed: %v %v", sets, err)
	}
}
