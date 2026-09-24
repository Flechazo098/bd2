package accountstate

import (
	"bytes"
	"context"
	"testing"

	"bd2server/internal/stateio"
)

func TestSaveWithEntriesAtomicAndEntryOnly(t *testing.T) {
	r, _ := openTestRepository(t)
	change := stateio.EntryMutation{Bucket: "granted", Key: "quest:1", Payload: []byte("true")}
	if err := r.SaveWithEntries("wallet", []byte(`{"gold":10}`), []stateio.EntryMutation{change}); err != nil {
		t.Fatal(err)
	}
	core, generation, found, err := r.LoadContext(context.Background(), "wallet")
	if err != nil || !found || generation != 1 || !bytes.Equal(core, []byte(`{"gold":10}`)) {
		t.Fatalf("core=%q generation=%d found=%t err=%v", core, generation, found, err)
	}
	if err := r.SaveWithEntries("wallet", nil, []stateio.EntryMutation{{Bucket: "granted", Key: "quest:2", Payload: []byte("true")}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveWithEntries("wallet", []byte(`{"gold":99}`), []stateio.EntryMutation{{Bucket: "granted", Key: ""}}); err == nil {
		t.Fatal("accepted invalid entry mutation")
	}
	core, generation, _, err = r.LoadContext(context.Background(), "wallet")
	if err != nil || generation != 1 || !bytes.Equal(core, []byte(`{"gold":10}`)) {
		t.Fatalf("entry-only/failed write changed core: %q generation %d, %v", core, generation, err)
	}
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveWithEntries("wallet", []byte(`{"gold":20}`), []stateio.EntryMutation{{Bucket: "granted", Key: "quest:3", Payload: []byte("true")}}); err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err == nil {
		t.Fatal("dirty rollback did not fail stop")
	}
	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM domain_entry WHERE domain_name='wallet' AND bucket='granted'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("entries after rollback=%d: %v", count, err)
	}
	if err := r.db.QueryRow(`SELECT generation FROM domain_state WHERE name='wallet'`).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("generation after rollback=%d: %v", generation, err)
	}
}
