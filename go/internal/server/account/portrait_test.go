package account

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/deck"
	"bd2server/internal/server/wire"
	"path/filepath"
	"testing"
)

func TestLoginPortraitUsesSQLiteSelectionAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	r, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := deck.LoadSeed("../../../seed/v2_35_10/decks.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := deck.OpenStore(r, seed)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 3601)
	if _, _, _, err = d.Handle("/UserPortraitChange", request); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, err = deck.OpenStore(r, seed)
	if err != nil {
		t.Fatal(err)
	}
	s := &LoginSeed{Version: StateVersion(), PacketCode: 3, UserInfo: wire.AppendVarint(wire.AppendVarint(nil, 1, 42), 14, 3501)}
	if err = s.AttachPortrait(d); err != nil {
		t.Fatal(err)
	}
	body, err := s.Login(wire.AppendVarint(nil, 1, 1), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	user, _, _ := wire.Bytes(body, 1)
	portrait, _, _ := wire.Varint(user, 14)
	if portrait != 3601 {
		t.Fatalf("reconnected portrait%d, want saved3601", portrait)
	}
}
