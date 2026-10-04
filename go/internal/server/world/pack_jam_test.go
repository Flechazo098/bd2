package world

import (
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"errors"
	"testing"
)

type jamFailStore struct {
	*stateio.Memory
	fail bool
}

func (s *jamFailStore) SaveWithEntries(domain string, core []byte, changes []stateio.EntryMutation) error {
	if s.fail {
		return errors.New("write failed")
	}
	return s.Memory.SaveWithEntries(domain, core, changes)
}

func TestPackJamClaimPersistsAndRepeatedDockingSucceeds(t *testing.T) {
	storage := &jamFailStore{Memory: stateio.NewMemory()}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	s := testService()
	s.wallet = wallet
	s.packJamDesign = &gamedata.PackJamDesign{InsertMin: 5, InsertMax: 5, Reward: gamedata.Reward{Type: 3, Count: 150}}
	s.fieldPacks = map[int]gamedata.FieldPack{3001: {ID: 3001, Type: 3, MapIDs: map[int]bool{30011: true}}}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 3001)
	_, preview, _, err := s.Handle("/PackPreviewInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(preview, 1); found {
		t.Fatal("arena fabricated story quest")
	}
	if claimed, _, _ := wire.Varint(preview, 3); claimed != 0 {
		t.Fatal("unclaimed reward marked claimed")
	}
	storage.fail = true
	if _, body, _, err := s.Handle("/PackJamEvent", request); err == nil || body != nil {
		t.Fatal("failed write returned success reward")
	}
	if wallet.WasGranted(packJamIdentity(3001)) || wallet.Snapshot().FreeJewelry != 0 {
		t.Fatal("failed write changed wallet")
	}
	_, preview, _, err = s.Handle("/PackPreviewInfo", request)
	if claimed, _, _ := wire.Varint(preview, 3); err != nil || claimed != 0 {
		t.Fatal("failed grant preview marked claimed")
	}
	storage.fail = false
	code, body, handled, err := s.Handle("/PackJamEvent", request)
	if err != nil || !handled || code != 72 {
		t.Fatalf("claim: %d %v %v", code, handled, err)
	}
	item, found, _ := wire.Bytes(body, 1)
	typ, _, _ := wire.Varint(item, 3)
	count, _, _ := wire.Varint(item, 4)
	if !found || typ != 3 || count != 150 || wallet.Snapshot().FreeJewelry != 150 {
		t.Fatal("wrong currency reward")
	}
	s.wallet, err = player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	_, preview, _, err = s.Handle("/PackPreviewInfo", request)
	claimed, _, _ := wire.Varint(preview, 3)
	if err != nil || claimed != 1 {
		t.Fatal("reopened preview lost claim")
	}
	code, body, _, err = s.Handle("/PackJamEvent", request)
	if err != nil || code != 72 || len(body) != 0 || s.wallet.Snapshot().FreeJewelry != 150 {
		t.Fatal("repeated docking replayed reward")
	}
	story := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 21)
	_, preview, _, err = s.Handle("/PackPreviewInfo", story)
	quest, found, _ := wire.Bytes(preview, 1)
	id, _, _ := wire.Varint(quest, 1)
	pack, _, _ := wire.Varint(quest, 6)
	if err != nil || !found || id != 1 || pack != 21 {
		t.Fatal("preview lost real active quest")
	}
	for _, invalid := range [][]byte{nil, wire.AppendVarint(nil, 2, 3001), wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 9999), wire.AppendVarint(wire.AppendVarint(nil, 1, 0), 2, 3001)} {
		for _, path := range []string{"/PackPreviewInfo", "/PackJamEvent"} {
			if _, _, handled, err := s.Handle(path, invalid); !handled || !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("invalid %s: %v", path, err)
			}
		}
	}
	if s.wallet.Snapshot().FreeJewelry != 150 || s.wallet.WasGranted(packJamIdentity(9999)) {
		t.Fatal("invalid pack changed wallet")
	}
	s.packJamDesign.Reward.Type = 8
	if _, _, _, err := s.Handle("/PackJamEvent", story); err == nil {
		t.Fatal("unsupported reward accepted")
	}
	if s.wallet.WasGranted(packJamIdentity(21)) {
		t.Fatal("unsupported reward marked claimed")
	}
	s.packJamDesign.Reward.Type = 4
	s.packJamDesign.Reward.Count = 73
	code, body, _, err = s.Handle("/PackJamEvent", story)
	item, _, _ = wire.Bytes(body, 1)
	typ, _, _ = wire.Varint(item, 3)
	count, _, _ = wire.Varint(item, 4)
	if err != nil || code != 72 || typ != 4 || count != 73 || s.wallet.Snapshot().Gold != 73 {
		t.Fatal("changed reward currency/count ignored")
	}
}
