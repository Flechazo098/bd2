package commerce

import (
	"bd2server/internal/server/accountstate"
	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/mail"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type clearInventory struct{ items []player.Item }

func (i *clearInventory) All() []player.Item { return i.items }
func clearRequest(kind, ticket uint64) []byte {
	b := wire.AppendVarint(nil, 1, 1)
	if kind != 0 {
		b = wire.AppendVarint(b, 2, kind)
	}
	row := wire.AppendVarint(nil, 1, 10)
	row = wire.AppendVarint(row, 2, ticket)
	row = wire.AppendVarint(row, 3, 2)
	b = wire.AppendBytes(b, 3+int(kind), row)
	return wire.AppendBytes(b, 4-int(kind), nil)
}

func TestClearPackageNativeProtoDefaultsAndInactiveRows(t *testing.T) {
	for _, kind := range []uint64{0, 1} {
		for _, placeholder := range []bool{false, true} {
			b := wire.AppendVarint(nil, 1, 1)
			if kind != 0 {
				b = wire.AppendVarint(b, 2, kind)
			}
			row := wire.AppendVarint(nil, 1, 10)
			row = wire.AppendVarint(row, 2, 12)
			row = wire.AppendVarint(row, 3, 2)
			if placeholder {
				// The native tower request serializes its empty pack placeholder
				// before the active tower row; pack serializes it afterwards.
				if kind == 1 {
					b = wire.AppendBytes(b, 3, nil)
				}
			}
			b = wire.AppendBytes(b, int(kind)+3, row)
			if placeholder && kind == 0 {
				b = wire.AppendBytes(b, 4, nil)
			}
			got, err := parseClearClaim(b)
			want := clearClaim{kind, 10, 12, 2, 0}
			if err != nil || got != want {
				t.Fatalf("kind %d placeholder %t: %+v %v", kind, placeholder, got, err)
			}
		}
	}
	// Explicit defaults are also valid protobuf; they must not look like a
	// second active claim, nor be confused with absent active message presence.
	row := wire.AppendVarint(nil, 1, 10)
	row = wire.AppendVarint(row, 2, 12)
	row = wire.AppendVarint(row, 3, 2)
	b := wire.AppendVarint(nil, 1, 1)
	b = wire.AppendVarint(b, 2, 0)
	b = wire.AppendBytes(b, 3, wire.AppendVarint(row, 4, 0))
	var defaults []byte
	for f := 1; f <= 4; f++ {
		defaults = wire.AppendVarint(defaults, f, 0)
	}
	if _, err := parseClearClaim(wire.AppendBytes(b, 4, defaults)); err != nil {
		t.Fatal(err)
	}
}

func TestClearPackageRejectsConflictingMalformedAndSpoofedRows(t *testing.T) {
	row := wire.AppendVarint(nil, 1, 10)
	row = wire.AppendVarint(row, 2, 12)
	row = wire.AppendVarint(row, 3, 2)
	prefix := wire.AppendVarint(nil, 1, 1)
	row = row[:len(row):len(row)]
	prefix = prefix[:len(prefix):len(prefix)]
	active := wire.AppendBytes(append([]byte(nil), prefix...), 3, row)
	active = active[:len(active):len(active)]
	requests := map[string][]byte{
		"missing row":           prefix,
		"empty active":          wire.AppendBytes(prefix, 3, nil),
		"only inactive":         wire.AppendBytes(prefix, 4, nil),
		"conflicting inactive":  wire.AppendBytes(active, 4, row),
		"duplicate active":      wire.AppendBytes(active, 3, row),
		"duplicate inactive":    wire.AppendBytes(wire.AppendBytes(active, 4, nil), 4, nil),
		"duplicate kind":        wire.AppendVarint(wire.AppendVarint(active, 2, 0), 2, 1),
		"duplicate seq":         wire.AppendVarint(active, 1, 1),
		"kind wire type":        wire.AppendBytes(active, 2, nil),
		"active wire type":      wire.AppendVarint(prefix, 3, 1),
		"inactive wire type":    wire.AppendVarint(active, 4, 0),
		"row scalar wire type":  wire.AppendBytes(prefix, 3, wire.AppendBytes(row, 4, nil)),
		"duplicate row scalar":  wire.AppendBytes(prefix, 3, wire.AppendVarint(row, 3, 2)),
		"overflow seq":          wire.AppendBytes(wire.AppendVarint(nil, 1, uint64(math.MaxInt32)+1), 3, row),
		"negative kind":         wire.AppendVarint(active, 2, math.MaxUint64),
		"negative level":        wire.AppendBytes(prefix, 3, wire.AppendVarint(row, 4, math.MaxUint64)),
		"truncated inactive":    append(append([]byte(nil), active...), 34, 2, 8),
		"unknown inactive data": wire.AppendBytes(active, 4, wire.AppendVarint(nil, 5, 1)),
		"unknown static row":    wire.AppendBytes(prefix, 3, wire.AppendVarint(row, 4, 1)),
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			eco := &purchaseEconomy{}
			design := &gamedata.ClearPackageCatalog{Rewards: []gamedata.ClearPackageRewardDesign{{GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 100}}}
			s, err := NewClearPackages(stateio.NewMemory(), design, eco, &clearInventory{})
			if err != nil {
				t.Fatal(err)
			}
			s.AttachProgress(func(uint64, uint64) bool { return true }, nil)
			if _, _, _, err := s.Handle("/ClearPackageReward", request); err == nil || eco.calls != 0 {
				t.Fatal("invalid request granted a reward", err, eco.calls)
			}
		})
	}
}

func TestClearPackagePremiumTicketExpiryAndTowerProof(t *testing.T) {
	items := &clearInventory{items: []player.Item{{Type: 19, ID: 77, Count: 1, ExpiryTime: 1000}}}
	eco := &purchaseEconomy{}
	design := &gamedata.ClearPackageCatalog{Rewards: []gamedata.ClearPackageRewardDesign{{Kind: 0, GroupID: 10, TicketID: 77, TargetID: 2, RandomBoxID: 100, Type: 1}, {Kind: 1, GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 101}}}
	s, err := NewClearPackages(stateio.NewMemory(), design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.UnixMilli(1000) }
	s.AttachProgress(func(uint64, uint64) bool { return true }, nil)
	if _, _, _, err := s.Handle("/ClearPackageReward", clearRequest(0, 77)); err == nil || eco.calls != 0 {
		t.Fatal("expired premium accepted", err)
	}
	if _, _, _, err := s.Handle("/ClearPackageReward", clearRequest(1, 12)); err == nil || eco.calls != 0 {
		t.Fatal("pack proof authorized tower", err)
	}
	s.AttachProgress(nil, func(tower, floor uint64) bool { return tower == 2 && floor == 0 })
	if _, _, _, err := s.Handle("/ClearPackageReward", clearRequest(1, 12)); err != nil {
		t.Fatal(err)
	}
	p, e, err := s.RewardDBInfos()
	if err != nil || len(p) != 0 || len(e) != 1 {
		t.Fatal("tower receipt not separated", p, e, err)
	}
	if got, _, _ := wire.Varint(e[0], 3); got != 2 {
		t.Fatal("tower_type field wrong", got)
	}
}

type clearReceiptFailure struct {
	stateio.Store
	fail bool
}

func (s *clearReceiptFailure) Save(name string, payload []byte) error {
	if s.fail && name == "commerce_clear_claims" {
		return fmt.Errorf("injected clear receipt failure")
	}
	return s.Store.Save(name, payload)
}

func TestClearPackageMailSQLiteAtomicRetryAndReconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	design := &gamedata.ClearPackageCatalog{Rewards: []gamedata.ClearPackageRewardDesign{{GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 100}}}
	graph := &deliveryGraph{}
	open := func(fail bool) (*accountstate.Repository, *ClearPackages, *mail.Service, *player.Wallet, *player.Inventory) {
		t.Helper()
		repo, err := accountstate.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		items, err := player.OpenInventory(repo, &player.Starter{Version: "2.35.10"})
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := player.OpenWallet(repo, player.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		for _, persist := range []func() error{items.EnsurePersisted, wallet.EnsurePersisted} {
			if err := persist(); err != nil {
				t.Fatal(err)
			}
		}
		economy, err := NewEntitlementEconomy(repo, deliveryBase{wallet, items}, graph, items, &gamedata.CashEntitlementDesign{})
		if err != nil {
			t.Fatal(err)
		}
		mailbox, err := mail.OpenService(repo, &mail.Starter{Version: "2.35.10", MailCount: 1}, items, wallet)
		if err != nil {
			t.Fatal(err)
		}
		if err := mailbox.AttachCashRewards(economy, map[uint64]bool{40: true}); err != nil {
			t.Fatal(err)
		}
		if err := economy.AttachCashMail(mailbox); err != nil {
			t.Fatal(err)
		}
		s, err := NewClearPackages(&clearReceiptFailure{Store: repo, fail: fail}, design, economy, items)
		if err != nil {
			t.Fatal(err)
		}
		s.AttachProgress(func(pack, level uint64) bool { return pack == 2 && level == 0 }, nil)
		return repo, s, mailbox, wallet, items
	}
	repo, s, _, _, _ := open(true)
	op, err := repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Handle("/ClearPackageReward", clearRequest(0, 12)); err == nil {
		t.Fatal("receipt failure lost")
	}
	if err := op.Rollback(); err != nil && !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, s, mailbox, wallet, items := open(false)
	if ids, _ := cashList(t, mailbox, 0, 100); len(ids) != 0 {
		t.Fatal("mail survived failed transaction", ids)
	}
	for _, name := range []string{"commerce_clear_claims", "commerce_entitlements"} {
		if raw, err := repo.Load(name); err != nil || raw != nil {
			t.Fatal("receipt survived rollback", name, err)
		}
	}
	op, err = repo.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	_, response, _, err := s.Handle("/ClearPackageReward", clearRequest(0, 12))
	if err != nil {
		_ = op.Rollback()
		t.Fatal(err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 0 || len(items.All()) != 0 {
		t.Fatal("mail contents directly granted")
	}
	bundle, ok, err := wire.Bytes(response, 1)
	if err != nil || !ok {
		t.Fatal("response bundle missing", err)
	}
	if _, direct, _ := wire.Bytes(bundle, 1); direct {
		t.Fatal("mailed items returned as inventory")
	}
	// CommonPacket updates the claimed row from the request and shows the
	// localized "sent to mail" notice. An empty bundle is valid for mail-only
	// delivery; it must not claim that those attachments entered inventory.
	if len(bundle) != 0 {
		t.Fatal("mail-only clear returned direct rewards", bundle)
	}
	ids, _ := cashList(t, mailbox, 0, 100)
	if len(ids) != 1 {
		t.Fatal("one clear reward should issue one mail", ids)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, s, mailbox, _, _ = open(false)
	// A replay must restore the exact response without requiring progress again
	// or issuing a second mail, including after sequence/session changes.
	s.AttachProgress(nil, nil)
	request, _, err := wire.ReplaceVarint(clearRequest(0, 12), 1, 9)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, _, err := s.HandleSession("/ClearPackageReward", request, "reconnected")
	if err != nil || !bytes.Equal(replay, response) {
		t.Fatal("reconnect response differs", err)
	}
	if got, _ := cashList(t, mailbox, 0, 100); !reflect.DeepEqual(got, ids) {
		t.Fatal("retry duplicated mail", got, ids)
	}
	p, e, err := s.RewardDBInfos()
	if err != nil || len(p) != 1 || len(e) != 0 {
		t.Fatal("claimed row not restored", p, e, err)
	}
	for f, want := range []uint64{10, 12, 2, 0} {
		if got, _, err := wire.Varint(p[0], f+1); err != nil || got != want {
			t.Fatal("claim protocol field", f+1, got, want, err)
		}
	}
}

func TestInstalledClearPackageDeliveryAndNativeClaims23510(t *testing.T) {
	root := os.Getenv("BD2_REAL_GAMEDATA")
	if root == "" {
		t.Skip("BD2_REAL_GAMEDATA not configured")
	}
	const version = "20260923193640"
	design, err := gamedata.LoadClearPackageCatalog(root, version)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := gamedata.LoadCashRewardResolver(root, version)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := gamedata.LoadCashMailTemplates(root, version)
	if err != nil {
		t.Fatal(err)
	}
	store := stateio.NewMemory()
	items, err := player.OpenInventory(store, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(store, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	eco, err := NewEntitlementEconomy(store, deliveryBase{wallet, items}, resolver, items, &gamedata.CashEntitlementDesign{})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := mail.OpenService(store, &mail.Starter{Version: "2.35.10", MailCount: 1}, items, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := mailbox.AttachCashRewards(eco, templates); err != nil {
		t.Fatal(err)
	}
	if err := eco.AttachCashMail(mailbox); err != nil {
		t.Fatal(err)
	}
	s, err := NewClearPackages(store, design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	// Current design has normal and premium rows for both pack and tower.
	// Every row must derive its mail from GameData and retain its claim identity.
	counts := [2][2]int{}
	for index, row := range design.Rewards {
		counts[row.Kind][row.Type]++
		plan, err := resolver.ResolveDelivery([]gamedata.BattleReward{{Type: 9, ID: row.RandomBoxID, Count: 1}})
		if err != nil || len(plan.Direct) != 0 || len(plan.Mail) != 1 || plan.Mail[0].TemplateID == 0 || len(plan.Mail[0].Rewards) == 0 {
			t.Fatalf("clear row %+v delivery %+v: %v", row, plan, err)
		}
		if row.Type == 1 {
			if _, err := items.GrantOnce(fmt.Sprintf("installed-clear-ticket:%d", index), []gamedata.BattleReward{{Type: 19, ID: row.TicketID, Count: 1}}); err != nil {
				t.Fatal(err)
			}
		}
		proof := func(target, level uint64) bool { return target == row.TargetID && level == row.Level }
		if row.Kind == 0 {
			s.AttachProgress(proof, nil)
		} else {
			s.AttachProgress(nil, proof)
		}
		request := wire.AppendVarint(nil, 1, 1)
		if row.Kind != 0 {
			request = wire.AppendVarint(request, 2, row.Kind)
		}
		var nested []byte
		for f, value := range []uint64{row.GroupID, row.TicketID, row.TargetID, row.Level} {
			if value != 0 {
				nested = wire.AppendVarint(nested, f+1, value)
			}
		}
		request = wire.AppendBytes(request, 3+int(row.Kind), nested)
		request = wire.AppendBytes(request, 4-int(row.Kind), nil)
		_, response, _, err := s.Handle("/ClearPackageReward", request)
		if err != nil {
			t.Fatalf("native clear row %+v: %v", row, err)
		}
		bundle, present, err := wire.Bytes(response, 1)
		if err != nil || !present || len(bundle) != 0 {
			t.Fatal("mail-only native response", row, response, err)
		}
		_, replay, _, err := s.Handle("/ClearPackageReward", request)
		if err != nil || !bytes.Equal(response, replay) {
			t.Fatal("native clear retry", row, err)
		}
		listRequest := wire.AppendVarint(nil, 1, 1)
		listRequest = wire.AppendVarint(listRequest, 3, 1)
		_, list, _, err := mailbox.Handle("/CashMailInfo", listRequest)
		if err != nil {
			t.Fatal(err)
		}
		if total, _, err := wire.Varint(list, 2); err != nil || total != uint64(index+1) {
			t.Fatal("mail duplicated or missing", row, total, err)
		}
		entry, present, err := wire.Bytes(list, 1)
		if err != nil || !present {
			t.Fatal("newest clear mail missing", row, err)
		}
		if template, _, err := wire.Varint(entry, 3); err != nil || template != plan.Mail[0].TemplateID {
			t.Fatal("GameData clear mail template mismatch", row, template, plan.Mail[0].TemplateID, err)
		}
		// MailDBInfo represents attachment vectors as packed repeated int32.
		for field, expected := range map[int]func(gamedata.BattleReward) uint64{
			8:  func(r gamedata.BattleReward) uint64 { return r.Type },
			9:  func(r gamedata.BattleReward) uint64 { return r.ID },
			10: func(r gamedata.BattleReward) uint64 { return r.Count },
		} {
			packed, present, err := wire.Bytes(entry, field)
			if err != nil || !present {
				t.Fatal("clear mail attachments missing", row, field, err)
			}
			for _, reward := range plan.Mail[0].Rewards {
				value, n := binary.Uvarint(packed)
				if n <= 0 || value != expected(reward) {
					t.Fatal("clear mail attachment mismatch", row, field, value, reward)
				}
				packed = packed[n:]
			}
			if len(packed) != 0 {
				t.Fatal("extra clear mail attachment", row, field)
			}
		}
	}
	if wallet.Snapshot() != (player.Currency{}) {
		t.Fatal("mail contents directly credited", wallet.Snapshot())
	}
	p, e, err := s.RewardDBInfos()
	if err != nil || len(p) != counts[0][0]+counts[0][1] || len(e) != counts[1][0]+counts[1][1] {
		t.Fatal("native claims not restored by kind", len(p), len(e), counts, err)
	}
	for kind, byType := range counts {
		for typ, n := range byType {
			if n == 0 {
				t.Fatalf("missing kind %d type %d design rows", kind, typ)
			}
		}
	}
}
func TestClearPackageRequiresServerProgressAndPremiumEntitlement(t *testing.T) {
	store := stateio.NewMemory()
	eco := &purchaseEconomy{}
	items := &clearInventory{}
	design := &gamedata.ClearPackageCatalog{Rewards: []gamedata.ClearPackageRewardDesign{{Kind: 0, GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 100, Type: 0}, {Kind: 0, GroupID: 10, TicketID: 77, TargetID: 2, RandomBoxID: 101, Type: 1}, {Kind: 1, GroupID: 10, TicketID: 12, TargetID: 2, RandomBoxID: 102, Type: 0}}}
	s, err := NewClearPackages(store, design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 12)); err == nil || eco.calls != 0 {
		t.Fatal("client spoofed clear progress")
	}
	s.AttachProgress(func(pack, level uint64) bool { return pack == 2 && level == 0 }, nil)
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 77)); err == nil {
		t.Fatal("unpaid premium claim accepted")
	}
	code, _, ok, err := s.Handle("/ClearPackageReward", clearRequest(0, 12))
	if err != nil || !ok || code != 286 || eco.rewards[0].ID != 100 {
		t.Fatal(code, err)
	}
	items.items = []player.Item{{Type: 19, ID: 77, Count: 1}}
	if _, _, _, err = s.Handle("/ClearPackageReward", clearRequest(0, 77)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewClearPackages(store, design, eco, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = reloaded.Handle("/ClearPackageReward", clearRequest(0, 77)); err != nil || eco.calls != 2 {
		t.Fatal("restart duplicated grant", err, eco.calls)
	}
	p, e, err := reloaded.RewardDBInfos()
	if err != nil || len(p) != 2 || len(e) != 0 {
		t.Fatal(p, e, err)
	}
	if _, _, _, err = reloaded.Handle("/ClearPackageReward", clearRequest(1, 12)); err == nil {
		t.Fatal("unimplemented evil progress granted reward")
	}
}
