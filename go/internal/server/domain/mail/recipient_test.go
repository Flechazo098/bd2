package mail_test

import (
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/platform/versionconfig"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/stateio"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mailScalar(field int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field<<3)), value)
}
func firstMailboxID(body []byte) uint64 {
	key, n := binary.Uvarint(body)
	if key != 10 || n <= 0 {
		return 0
	}
	size, m := binary.Uvarint(body[n:])
	if m <= 0 || size > uint64(len(body)-n-m) {
		return 0
	}
	row := body[n+m : n+m+int(size)]
	tag, k := binary.Uvarint(row)
	if tag != 8 || k <= 0 {
		return 0
	}
	id, _ := binary.Uvarint(row[k:])
	return id
}
func TestOperatorCompensationGoesOnlyToTheNamedAccount(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "grants.json")
	now := time.Now().UnixMilli()
	document := mail.GrantSpool{Version: 2, Grants: []mail.Grant{{AccountID: "original-owner", Identity: "compensation-ticket", Title: "Personal compensation", Body: "This is an individual correction, not a new-player gift", SentAt: now, Rewards: []mail.GrantReward{{Type: 4, Count: 700}}}}}
	write := func() {
		t.Helper()
		raw, e := json.Marshal(document)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(spool, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	query := func(accountID string, claim bool) uint64 {
		t.Helper()
		directory := filepath.Join(root, accountID)
		if e := os.MkdirAll(directory, 0700); e != nil {
			t.Fatal(e)
		}
		repo, e := accountstate.Open(filepath.Join(directory, "state.db"))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = repo.Close() }()
		tx, e := repo.BeginCommand(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = tx.Rollback() }()
		ctx := command.Context{Identity: command.Identity{AccountID: accountID, SessionID: "login", RequestID: "mail"}, State: tx}
		scope := stateio.RootStore{}
		wallet, e := inventory.OpenWallet(ctx, scope, inventory.Currency{})
		if e != nil {
			t.Fatal(e)
		}
		items, e := inventory.OpenInventory(ctx, scope, nil)
		if e != nil {
			t.Fatal(e)
		}
		service, e := mail.OpenService(ctx, scope, &mail.Starter{Version: versionconfig.State(), MailCount: 1}, items, wallet)
		if e != nil {
			t.Fatal(e)
		}
		for _, persist := range []func(command.Context) error{wallet.EnsurePersisted, items.EnsurePersisted, service.EnsurePersisted} {
			if e = persist(ctx); e != nil {
				t.Fatal(e)
			}
		}
		if e = service.AttachGrantSpoolPath(ctx, spool); e != nil {
			t.Fatal(e)
		}
		_, body, handled, e := service.Handle(ctx, "/MailInfo", mailScalar(1, 1))
		if e != nil || !handled {
			t.Fatal("mail query rejected", e)
		}
		id := firstMailboxID(body)
		if claim {
			if id == 0 {
				t.Fatal("recipient did not receive compensation")
			}
			req := append(mailScalar(1, 2), mailScalar(2, id)...)
			if _, _, _, e = service.Handle(ctx, "/MailOpen", req); e != nil {
				t.Fatal(e)
			}
		} else if id != 0 {
			t.Fatal("compensation sent to unrelated player", accountID, id)
		}
		gold := wallet.Snapshot(ctx).Gold
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		return gold
	}
	if gold := query("new-player", false); gold != 0 {
		t.Fatal("new player acquired original owner compensation")
	}
	if gold := query("original-owner", true); gold != 700 {
		t.Fatal("claim did not credit named recipient exactly once", gold)
	}
	if gold := query("original-owner", false); gold != 700 {
		t.Fatal("restart/import duplicated compensation", gold)
	}
	if gold := query("new-player", false); gold != 0 {
		t.Fatal("recipient claim leaked to other account")
	}
}
