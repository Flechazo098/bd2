package mail_test

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/common/versionconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/life"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

type lifeMailDesign struct{}

func (lifeMailDesign) Character(uint64) (gamedata.CharacterDesign, bool) {
	return gamedata.CharacterDesign{}, false
}
func (lifeMailDesign) RollOptions(uint64) ([]gamedata.EquipmentOptionChoice, []gamedata.EquipmentOptionChoice, *gamedata.EquipmentOptionChoice, error) {
	return nil, nil, nil, nil
}
func (lifeMailDesign) Resolve(rewards []gamedata.BattleReward) ([]gamedata.BattleReward, error) {
	return rewards, nil
}

func TestOperatorLifeRewardsClaimIntoLifeInventoryExactlyOnce(t *testing.T) {
	encode := func(values ...uint64) []byte {
		var b []byte
		for i := 0; i < len(values); i += 2 {
			b = wire.AppendVarint(b, int(values[i]), values[i+1])
		}
		return b
	}
	value := func(b []byte, field int) uint64 {
		t.Helper()
		v, _, err := wire.Varint(b, field)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	message := func(b []byte, field int) []byte {
		t.Helper()
		v, ok, err := wire.Bytes(b, field)
		if err != nil || !ok {
			t.Fatalf("missing field %d: %x (%v)", field, b, err)
		}
		return v
	}
	design, err := gamedata.NewLifeCatalog(map[string][][]byte{
		"LifeDefaultTable":     {encode(21, 0)},
		"LifeWorldTable":       {encode(4, 91)},
		"LifeObjectGroupTable": {encode(5, 101, 12, 1)},
		"LifeItemTable":        {encode(5, 201, 8, 9999)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := command.Context{Identity: command.Identity{AccountID: "life-mail-recipient", SessionID: "login"}, State: stateio.NewMemory()}
	spool := filepath.Join(t.TempDir(), "grants.json")
	var service *mail.Service
	var living *life.Service
	open := func() {
		t.Helper()
		store := stateio.RootStore{}
		items, err := inventory.OpenInventory(ctx, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := inventory.OpenWallet(ctx, store, inventory.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		collection, err := roster.OpenCollectionStore(ctx, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		equipment, err := inventory.OpenEquipmentInventory(ctx, store)
		if err != nil {
			t.Fatal(err)
		}
		living, err = life.Open(ctx, design, life.Options{})
		if err != nil {
			t.Fatal(err)
		}
		economy, err := events.NewEconomy(ctx, stateio.EntrySnapshotStore{Domain: "collection", Bucket: "gameplay"}, items, wallet, collection, equipment, lifeMailDesign{}, lifeMailDesign{}, lifeMailDesign{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		economy.AttachLife(living)
		service, err = mail.OpenService(ctx, store, &mail.Starter{Version: versionconfig.State(), MailCount: 1}, items, wallet)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.AttachAttachmentRewardEconomy(ctx, economy); err != nil {
			t.Fatal(err)
		}
		if err := service.AttachGrantSpoolPath(ctx, spool); err != nil {
			t.Fatal(err)
		}
	}
	open()
	if _, err := living.Apply(ctx, "existing-life-inventory", nil, []gamedata.Reward{{Type: 64, Count: 10}, {Type: 65, ID: 101, Count: 1}, {Type: 66, ID: 201, Count: 5}}); err != nil {
		t.Fatal(err)
	}
	grant := mail.Grant{AccountID: ctx.AccountID, Identity: "operator-life-package", Title: "Life supplies", Body: "Furniture, materials and life coins", SentAt: time.Now().UnixMilli(), Rewards: []mail.GrantReward{{Type: 64, Count: 7}, {Type: 65, ID: 101, Count: 2}, {Type: 66, ID: 201, Count: 3}}}
	if err := mail.AppendGrant(spool, grant); err != nil {
		t.Fatal(err)
	}
	call := func(path string, request []byte) []byte {
		t.Helper()
		_, body, handled, err := service.Handle(ctx, path, request)
		if err != nil || !handled {
			t.Fatalf("%s: handled=%t err=%v", path, handled, err)
		}
		return body
	}
	id := firstMailboxID(call("/MailInfo", encode(1, 1)))
	if id == 0 {
		t.Fatal("operator grant was not issued to the mailbox")
	}
	claim := encode(1, 2, 2, id)
	first := call("/MailOpen", claim)
	check := func() {
		t.Helper()
		coin, err := living.Coin(ctx)
		if err != nil || coin != 17 {
			t.Fatalf("life coin balance=%d, want 17 (%v)", coin, err)
		}
		for _, reward := range []gamedata.Reward{{Type: 65, ID: 101, Count: 3}, {Type: 66, ID: 201, Count: 8}} {
			got, err := living.Balance(ctx, reward.Type, reward.ID)
			if err != nil || got != reward.Count {
				t.Fatalf("life inventory %d:%d=%d, want %d (%v)", reward.Type, reward.ID, got, reward.Count, err)
			}
		}
	}
	check()
	gotDeltas := map[uint64]uint64{}
	if err := wire.Walk(message(first, 1), func(f wire.Field) error {
		if f.Number == 1 && f.Type == 2 {
			gotDeltas[value(f.Value, 3)] += value(f.Value, 4)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(gotDeltas) != 3 || gotDeltas[64] != 7 || gotDeltas[65] != 2 || gotDeltas[66] != 3 {
		t.Fatalf("claim must report granted amounts, not existing balances: %v", gotDeltas)
	}
	if again := call("/MailOpen", claim); !bytes.Equal(first, again) {
		t.Fatal("claim retry changed its reward bundle")
	}
	check()
	open()
	if id := firstMailboxID(call("/MailInfo", encode(1, 3))); id != 0 {
		t.Fatal("service reopen reissued an already claimed operator grant")
	}
	call("/MailOpen", encode(1, 4, 2, id))
	check()
}
