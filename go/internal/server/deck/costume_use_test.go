package deck

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/player"
	"bd2server/internal/server/wire"
	"path/filepath"
	"testing"
)

func TestBatchCostumeUseRestoresCharacterSelectionsFromSQLite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.db")
	r, e := accountstate.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	seed, e := LoadSeed("../../../seed/v2_35_10/decks.json")
	if e != nil {
		t.Fatal(e)
	}
	initial := []player.Character{{InvenIndex: 100, ID: 350, HP: 80, Level: 1, UseCostume: 1001, CostumeID: 3501, ConnectPotentialCostume: 3501}, {InvenIndex: 200, ID: 360, HP: 60, Level: 1, UseCostume: 2001, CostumeID: 3601, ConnectPotentialCostume: 3601}}
	inv, e := player.OpenInventory(r, &player.Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	chars, e := player.OpenCharacterStore(r, initial, inv, "", "")
	if e != nil {
		t.Fatal(e)
	}
	coll, e := player.OpenCollectionStore(r, []player.Costume{{InvenIndex: 1001, ID: 3501, UseChar: 100}, {InvenIndex: 1002, ID: 3502, UseChar: 100}, {InvenIndex: 2001, ID: 3601, UseChar: 200}, {InvenIndex: 2002, ID: 3602, UseChar: 200}})
	if e != nil {
		t.Fatal(e)
	}
	if e = chars.AttachCollection(coll); e != nil {
		t.Fatal(e)
	}
	if e = coll.EnsurePersisted(); e != nil {
		t.Fatal(e)
	}
	if e = chars.EnsurePersisted(); e != nil {
		t.Fatal(e)
	}
	d, e := OpenStore(r, seed)
	if e != nil {
		t.Fatal(e)
	}
	d.characters = chars
	d.collection = coll
	invalid := req(1, wire.AppendBytes(nil, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 1002), 2, 100)), wire.AppendBytes(nil, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 1001), 2, 200)))
	if _, _, _, err := d.Handle("/CostumeUse", invalid); err == nil {
		t.Fatal("accepted another character's costume")
	}
	if c, _ := chars.Find(100); c.UseCostume != 1001 {
		t.Fatal("invalid batch partially changed first character")
	}
	request := req(1, wire.AppendBytes(nil, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 1002), 2, 100)), wire.AppendBytes(nil, 2, wire.AppendVarint(wire.AppendVarint(nil, 1, 2002), 2, 200)))
	if _, body, _, e := d.Handle("/CostumeUse", request); e != nil || len(body) != 0 {
		t.Fatal("empty response protocol", e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	r, e = accountstate.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	inv, e = player.OpenInventory(r, &player.Starter{Version: "2.35.10"})
	if e != nil {
		t.Fatal(e)
	}
	coll, e = player.OpenCollectionStore(r, nil)
	if e != nil {
		t.Fatal(e)
	}
	chars, e = player.OpenCharacterStore(r, initial, inv, "", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = chars.AttachCollection(coll); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct{ index, cost, id, hp, connect uint64 }{{100, 1002, 3502, 80, 3501}, {200, 2002, 3602, 60, 3601}} {
		c, ok := chars.Find(v.index)
		if !ok || c.UseCostume != v.cost || c.CostumeID != v.id || c.HP != v.hp || c.ConnectPotentialCostume != v.connect {
			t.Fatalf("reconnected character%+v", c)
		}
	}
}
