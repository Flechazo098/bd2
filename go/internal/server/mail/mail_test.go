package mail

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bd2server/internal/server/gamedata"
	"bd2server/internal/server/player"
	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

func TestStarterAnswersMailInfoWithoutCapture(t *testing.T) {
	seed, err := Load(filepath.Join("..", "..", "..", "seed", "v2_35_10", "mail.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, proto, ok, err := seed.Handle("/MailInfo", wire.AppendVarint(nil, 1, 249))
	if err != nil || !ok || code != packetCode || len(proto) == 0 {
		t.Fatalf("MailInfo: code=%d bytes=%d ok=%t err=%v", code, len(proto), ok, err)
	}
	if _, _, ok, err := seed.Handle("/MailOpen", wire.AppendVarint(nil, 1, 1)); ok || err != nil {
		t.Fatal("seed claimed an endpoint it does not own")
	}
	if _, _, ok, err := seed.Handle("/MailInfo", nil); !ok || err == nil {
		t.Fatal("missing sequence was accepted")
	}
}

func TestDynamicCompensationMailPersistsAndIsIdempotent(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 1, MaxMailID: 100}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	rewards := []gamedata.Reward{{Type: 4, Count: 123}}
	if err := service.EnqueueCompensation("daily:2026-09-23:1", "日常任务到期补发", "任务已完成但未领取。", rewards, now); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueCompensation("daily:2026-09-23:1", "日常任务到期补发", "任务已完成但未领取。", rewards, now); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.dynamic) != 1 || reopened.issued["daily:2026-09-23:1"] != 101 {
		t.Fatalf("dynamic=%+v issued=%+v", reopened.dynamic, reopened.issued)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendVarint(request, 2, 101)
	if _, _, _, err := reopened.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Gold != 123 {
		t.Fatalf("gold=%d", wallet.Snapshot().Gold)
	}
}

func TestMailOpenGrantsItemsAndCurrencyAndPersists(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 3, MaxMailID: 12, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{3, 4}, RewardIDs: []uint64{0, 0}, RewardCounts: []uint64{70, 123456789}},
		{MailID: 12, MailType: 2, ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{8}, RewardIDs: []uint64{9}, RewardCounts: []uint64{10}},
	}}
	starter := &player.Starter{Version: "2.35.10"}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Gold: 321})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{11, 12}))
	code, response, ok, err := service.Handle("/MailOpen", request)
	if err != nil || !ok || code != 132 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if wallet.Snapshot().FreeJewelry != 70 {
		t.Fatalf("wallet=%+v", wallet.Snapshot())
	}
	if wallet.Snapshot().Gold != 123457110 {
		t.Fatalf("gold did not stack into wallet: %+v", wallet.Snapshot())
	}
	if len(inv.All()) != 1 || inv.All()[0].ID != 9 || inv.All()[0].Count != 10 {
		t.Fatalf("items=%+v", inv.All())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found || len(bundle) == 0 {
		t.Fatal("reward bundle missing")
	}
	infoReq := wire.AppendVarint(nil, 1, 2)
	_, info, _, err := service.Handle("/MailInfo", infoReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := wire.Bytes(info, 1); found {
		t.Fatal("opened mail remained visible")
	}
	count, found, _ := wire.Varint(info, 2)
	if !found || count != 1 {
		t.Fatalf("mail count=%d found=%v", count, found)
	}
	service, err = OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	_, info, _, _ = service.Handle("/MailInfo", infoReq)
	if _, found, _ := wire.Bytes(info, 1); found {
		t.Fatal("opened mail was not persisted")
	}
}

func TestMailHistoryPersistsPagesAndKeepsFirstOpenTime(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 4, MaxMailID: 13, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, Title: "first", ExpiresAt: 100, SentAt: 10, RewardTypes: []uint64{4}, RewardIDs: []uint64{0}, RewardCounts: []uint64{1}},
		{MailID: 12, MailType: 2, Title: "second", ExpiresAt: 200, SentAt: 20, RewardTypes: []uint64{4}, RewardIDs: []uint64{0}, RewardCounts: []uint64{2}},
		{MailID: 13, MailType: 2, Title: "third", ExpiresAt: 300, SentAt: 30, RewardTypes: []uint64{4}, RewardIDs: []uint64{0}, RewardCounts: []uint64{3}},
	}}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	openedAt := time.Date(2026, 10, 3, 5, 6, 7, 8_000_000, time.UTC)
	service.now = func() time.Time { return openedAt }
	open := wire.AppendVarint(nil, 1, 1)
	open = wire.AppendBytes(open, 2, packed([]uint64{11, 12, 13}))
	if _, _, _, err := service.Handle("/MailOpen", open); err != nil {
		t.Fatal(err)
	}

	request := wire.AppendVarint(nil, 1, 2)
	request = wire.AppendVarint(request, 2, 0)
	request = wire.AppendVarint(request, 3, 2)
	code, response, handled, err := service.Handle("/MailHistoryInfo", request)
	if err != nil || !handled || code != historyPacketCode {
		t.Fatalf("code=%d handled=%v err=%v", code, handled, err)
	}
	entries := historyEntries(t, response)
	if len(entries) != 2 || historyID(t, entries[0]) != 13 || historyID(t, entries[1]) != 12 {
		t.Fatalf("first page IDs=%v", historyIDs(t, entries))
	}
	total, found, err := wire.Varint(response, 2)
	if err != nil || !found || total != 3 {
		t.Fatalf("total=%d found=%v err=%v", total, found, err)
	}
	if isOpen, found, _ := wire.Varint(entries[0], 11); !found || isOpen != 1 {
		t.Fatalf("history is_open=%d found=%v", isOpen, found)
	}
	if got, found, _ := wire.Varint(entries[0], 12); !found || got != uint64(openedAt.UnixMilli()) {
		t.Fatalf("open_time=%d found=%v", got, found)
	}
	if got, found, _ := wire.Varint(entries[0], 14); !found || got != uint64(openedAt.Add(mailHistoryPeriod).UnixMilli()) {
		t.Fatalf("history_delete_time=%d found=%v", got, found)
	}

	next := wire.AppendVarint(nil, 1, 3)
	next = wire.AppendVarint(next, 2, 12)
	next = wire.AppendVarint(next, 3, 2)
	_, response, _, err = service.Handle("/MailHistoryInfo", next)
	if err != nil {
		t.Fatal(err)
	}
	entries = historyEntries(t, response)
	if len(entries) != 1 || historyID(t, entries[0]) != 11 {
		t.Fatalf("second page IDs=%v", historyIDs(t, entries))
	}

	service.now = func() time.Time { return openedAt.Add(24 * time.Hour) }
	if _, _, _, err := service.Handle("/MailOpen", wire.AppendVarint(wire.AppendVarint(nil, 1, 4), 2, 13)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = service.now
	_, response, _, err = reopened.Handle("/MailHistoryInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	entries = historyEntries(t, response)
	if got, _, _ := wire.Varint(entries[0], 12); got != uint64(openedAt.UnixMilli()) {
		t.Fatalf("retry changed first open time to %d", got)
	}

	reopened.now = func() time.Time { return openedAt.Add(mailHistoryPeriod) }
	_, response, _, err = reopened.Handle("/MailHistoryInfo", request)
	if err != nil {
		t.Fatal(err)
	}
	if entries := historyEntries(t, response); len(entries) != 0 {
		t.Fatalf("expired history remains: %v", historyIDs(t, entries))
	}
	if total, _, _ := wire.Varint(response, 2); total != 0 {
		t.Fatalf("expired total=%d", total)
	}
}

func TestMailHistoryRejectsInvalidPagination(t *testing.T) {
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, &Starter{Version: "2.35.10", MailCount: 1, MaxMailID: 10}, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, _, handled, err := service.Handle("/MailHistoryInfo", request); !handled || err == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

type mailCostumeDesign map[uint64]gamedata.CharacterDesign

func (d mailCostumeDesign) Character(costumeID uint64) (gamedata.CharacterDesign, bool) {
	value, ok := d[costumeID]
	return value, ok
}

func TestMailCostumeRewardCreatesEnhancementFiveIdempotently(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 11, Mails: []MailDBInfo{{
		MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10,
		RewardTypes: []uint64{11}, RewardIDs: []uint64{206}, RewardCounts: []uint64{6},
	}}}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.BindBaseCharacters(nil); err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachCostumeRewards(collection, mailCostumeDesign{206: {
		ID: 20, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, 11)
	_, response, _, err := service.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	if got := collection.Costumes(); len(got) != 1 || got[0].ID != 206 || got[0].Level != 5 {
		t.Fatalf("costumes=%+v", got)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("bundle: found=%v err=%v", found, err)
	}
	var characters, costumes, upgrades int
	if err := wire.Walk(bundle, func(field wire.Field) error {
		switch field.Number {
		case 2:
			characters++
		case 3:
			costumes++
		case 9:
			upgrades++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if characters != 1 || costumes != 1 || upgrades != 5 {
		t.Fatalf("characters=%d costumes=%d upgrades=%d", characters, costumes, upgrades)
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if got := collection.Costumes(); len(got) != 1 || got[0].Level != 5 {
		t.Fatalf("retry costumes=%+v", got)
	}
}

func TestEnsureStarterLimitedCostumesIsDurablyIdempotent(t *testing.T) {
	storage := stateio.NewMemory()
	service, _, _ := spoolTestService(t, storage)
	design := mailCostumeDesign{
		206: {ID: 20, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 10},
		306: {ID: 30, HP: 100, CostumeMaxLevel: 5, OverflowItemType: 20, OverflowItemCount: 10},
	}
	collection, err := player.OpenCollectionStore(storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachCostumeRewards(collection, design); err != nil {
		t.Fatal(err)
	}
	firstTime := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	if err := service.EnsureStarterLimitedCostumes([]uint64{206}, firstTime); err != nil {
		t.Fatal(err)
	}
	firstID := service.issued[starterLimitedCostumeIdentity]
	if firstID == 0 || len(service.dynamic) != 1 {
		t.Fatalf("first issue: id=%d dynamic=%v", firstID, service.dynamic)
	}
	// A retry after the schedule changed must preserve the originally frozen
	// mail rather than allocating a second ID or replacing its attachments.
	if err := service.EnsureStarterLimitedCostumes([]uint64{306}, firstTime.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if service.issued[starterLimitedCostumeIdentity] != firstID || len(service.dynamic) != 1 || service.dynamic[firstID].RewardIDs[0] != 206 {
		t.Fatalf("in-process retry changed gift: issued=%v dynamic=%v", service.issued, service.dynamic)
	}

	reopened, _, _ := spoolTestService(t, storage)
	if err := reopened.AttachCostumeRewards(collection, design); err != nil {
		t.Fatal(err)
	}
	if err := reopened.EnsureStarterLimitedCostumes([]uint64{306}, firstTime.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reopened.issued[starterLimitedCostumeIdentity] != firstID || len(reopened.dynamic) != 1 || reopened.dynamic[firstID].RewardIDs[0] != 206 {
		t.Fatalf("restart retry changed gift: issued=%v dynamic=%v", reopened.issued, reopened.dynamic)
	}
}

func TestStarterPrestigeSkinMailClaimsAndPersistsOwnershipOnce(t *testing.T) {
	storage := stateio.NewMemory()
	service, _, _ := spoolTestService(t, storage)
	sentAt := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	skins := []uint64{101, 202}
	if err := service.EnsureStarterPrestigeSkins(skins, sentAt); err != nil {
		t.Fatal(err)
	}
	id := service.issued[starterPrestigeSkinIdentity]
	if id == 0 || len(service.dynamic) != 1 {
		t.Fatalf("gift missing: issued=%v dynamic=%v", service.issued, service.dynamic)
	}
	original := append([]byte(nil), service.dynamic[id].encode()...)
	if err := service.EnsureStarterPrestigeSkins(skins, sentAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	service, inventory, _ := spoolTestService(t, storage)
	if service.issued[starterPrestigeSkinIdentity] != id || len(service.dynamic) != 1 || !bytes.Equal(original, service.dynamic[id].encode()) {
		t.Fatal("retry or restart changed the unclaimed gift")
	}
	request := wire.AppendVarint(wire.AppendVarint(nil, 1, 1), 2, id)
	code, response, handled, err := service.Handle("/MailOpen", request)
	if err != nil || !handled || code != 132 {
		t.Fatalf("MailOpen: code=%d handled=%v err=%v", code, handled, err)
	}
	bundle, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("reward bundle: found=%v err=%v", found, err)
	}
	// Check the actual client-visible ItemDBInfo, including the inventory
	// indices which must remain stable when this mail is claimed again.
	assertSkins := func(payload []byte) map[uint64]uint64 {
		t.Helper()
		indices := make(map[uint64]uint64)
		for _, item := range historyEntries(t, payload) {
			index, _, _ := wire.Varint(item, 1)
			design, _, _ := wire.Varint(item, 2)
			typ, _, _ := wire.Varint(item, 3)
			count, _, _ := wire.Varint(item, 4)
			if index == 0 || typ != 45 || count != 1 || (design != skins[0] && design != skins[1]) || indices[design] != 0 {
				t.Fatalf("invalid or duplicate skin ItemDBInfo: index=%d design=%d type=%d count=%d", index, design, typ, count)
			}
			indices[design] = index
		}
		if len(indices) != len(skins) {
			t.Fatalf("skin ownership=%v", indices)
		}
		return indices
	}
	granted := assertSkins(bundle)
	assertOwnership := func(inv *player.Inventory) {
		t.Helper()
		_, info, handled, err := inv.Handle("/ItemInfo", wire.AppendVarint(nil, 1, 2))
		if err != nil || !handled {
			t.Fatalf("ItemInfo: handled=%v err=%v", handled, err)
		}
		owned := assertSkins(info)
		for design, index := range granted {
			if owned[design] != index {
				t.Fatalf("skin %d inventory index changed: got=%d want=%d", design, owned[design], index)
			}
		}
	}
	assertOwnership(inventory)
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	assertOwnership(inventory)
	if err := service.EnsureStarterPrestigeSkins([]uint64{303}, sentAt.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	service, inventory, _ = spoolTestService(t, storage)
	if err := service.EnsureStarterPrestigeSkins([]uint64{404}, sentAt.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if service.issued[starterPrestigeSkinIdentity] != id || len(service.dynamic) != 1 || !bytes.Equal(original, service.dynamic[id].encode()) || !containsID(service.state.Opened, id) {
		t.Fatal("restart lost the claimed gift ledger or replaced its frozen attachments")
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	assertOwnership(inventory)
}

func historyEntries(t *testing.T, response []byte) [][]byte {
	t.Helper()
	var entries [][]byte
	if err := wire.Walk(response, func(field wire.Field) error {
		if field.Number == 1 {
			entries = append(entries, append([]byte(nil), field.Value...))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func historyID(t *testing.T, entry []byte) uint64 {
	t.Helper()
	id, found, err := wire.Varint(entry, 1)
	if err != nil || !found {
		t.Fatalf("history ID: found=%v err=%v", found, err)
	}
	return id
}

func historyIDs(t *testing.T, entries [][]byte) []uint64 {
	t.Helper()
	ids := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, historyID(t, entry))
	}
	return ids
}

func TestStarterContentTicketOnlyAllowsAuditedSingleUseReward(t *testing.T) {
	for _, reward := range []struct {
		id, count uint64
		valid     bool
	}{{450030, 1, true}, {450029, 1, true}, {450030, 0, false}, {450030, 2, false}} {
		seed := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 13, Mails: []MailDBInfo{{
			MailID: 13, MailType: 2, ExpiresAt: 100, SentAt: 10,
			RewardTypes: []uint64{19}, RewardIDs: []uint64{reward.id}, RewardCounts: []uint64{reward.count},
		}}}
		if err := seed.Validate(); (err == nil) != reward.valid {
			t.Fatalf("id=%d count=%d valid=%v err=%v", reward.id, reward.count, reward.valid, err)
		}
	}
}

func TestMailOpenGrantsNonResourceItemDBInfoType(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 13, Mails: []MailDBInfo{{
		MailID: 13, MailType: 2, ExpiresAt: 100, SentAt: 10,
		RewardTypes: []uint64{14}, RewardIDs: []uint64{1}, RewardCounts: []uint64{2},
	}}}
	starter := &player.Starter{Version: "2.35.10"}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, starter)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{13}))
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	items := inv.All()
	if len(items) != 1 || items[0].ID != 1 || items[0].Type != 14 || items[0].Count != 2 {
		t.Fatalf("items=%+v", items)
	}
}

func TestMailOpenGrantsCatalystAndMileageCurrenciesAndPersists(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 14, Mails: []MailDBInfo{{
		MailID: 14, MailType: 2, ExpiresAt: 100, SentAt: 10,
		RewardTypes: []uint64{12, 20}, RewardIDs: []uint64{0, 0}, RewardCounts: []uint64{250, 300},
	}}}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{Catalyst: 10, Mileage: 20})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, seed, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	request = wire.AppendBytes(request, 2, packed([]uint64{14}))
	_, response, _, err := service.Handle("/MailOpen", request)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Catalyst != 260 || wallet.Snapshot().Mileage != 320 || len(inv.All()) != 0 {
		t.Fatalf("wallet=%+v items=%+v", wallet.Snapshot(), inv.All())
	}
	bundle, found, _ := wire.Bytes(response, 1)
	if !found {
		t.Fatal("reward bundle missing")
	}
	gotCurrencies := map[uint64]uint64{}
	if err := wire.Walk(bundle, func(field wire.Field) error {
		if field.Number != 1 {
			return nil
		}
		typ, _, err := wire.Varint(field.Value, 3)
		if err != nil {
			return err
		}
		count, _, err := wire.Varint(field.Value, 4)
		if err != nil {
			return err
		}
		gotCurrencies[typ] = count
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if gotCurrencies[12] != 250 || gotCurrencies[20] != 300 {
		t.Fatalf("currency rewards=%v", gotCurrencies)
	}
	if _, _, _, err := service.Handle("/MailOpen", request); err != nil {
		t.Fatal(err)
	}
	if wallet.Snapshot().Catalyst != 260 || wallet.Snapshot().Mileage != 320 {
		t.Fatalf("replay wallet=%+v", wallet.Snapshot())
	}
	reopened, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Catalyst != 260 || reopened.Snapshot().Mileage != 320 {
		t.Fatalf("persisted wallet=%+v", reopened.Snapshot())
	}
}

func TestWatchedSeedReloadsOnlyValidAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.json")
	first := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 11, Mails: []MailDBInfo{{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := first.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	starter, err := Load(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, starter, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AttachSeedPath(seedPath); err != nil {
		t.Fatal(err)
	}
	request := wire.AppendVarint(nil, 1, 1)
	if _, response, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	} else if id, _, _ := wire.Varint(mustFirstMail(t, response), 1); id != 11 {
		t.Fatalf("initial mail ID=%d", id)
	}
	second := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 12, Mails: []MailDBInfo{{MailID: 12, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := second.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	if _, response, _, err := service.Handle("/MailInfo", request); err != nil {
		t.Fatal(err)
	} else if id, _, _ := wire.Varint(mustFirstMail(t, response), 1); id != 12 {
		t.Fatalf("reloaded mail ID=%d", id)
	}
	if err := os.WriteFile(seedPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", request); err == nil {
		t.Fatal("accepted malformed watched seed")
	}
	// The malformed file did not replace the last known-good mailbox.
	if service.Starter.Mails[0].MailID != 12 {
		t.Fatalf("last known-good seed lost: %+v", service.Starter.Mails)
	}
}

func TestExpandedWatchedSeedAdvancesDynamicMailAllocator(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.json")
	first := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 11, Mails: []MailDBInfo{{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10}}}
	if err := first.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	storage := stateio.NewMemory()
	inv, err := player.OpenInventory(storage, &player.Starter{Version: "2.35.10"})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := player.OpenWallet(storage, player.Currency{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := OpenService(storage, first, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EnsurePersisted(); err != nil {
		t.Fatal(err)
	}
	if err := service.AttachSeedPath(seedPath); err != nil {
		t.Fatal(err)
	}
	second := &Starter{Version: "2.35.10", MailCount: 3, MaxMailID: 101, Mails: []MailDBInfo{
		{MailID: 11, MailType: 2, ExpiresAt: 100, SentAt: 10},
		{MailID: 101, MailType: 2, ExpiresAt: 100, SentAt: 10},
	}}
	if err := second.Write(seedPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Handle("/MailInfo", wire.AppendVarint(nil, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if service.state.NextDynamicMailID != 102 {
		t.Fatalf("next dynamic mail ID=%d", service.state.NextDynamicMailID)
	}
	reopened, err := OpenService(storage, second, inv, wallet)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.NextDynamicMailID != 102 {
		t.Fatalf("reopened next dynamic mail ID=%d", reopened.state.NextDynamicMailID)
	}
}

func mustFirstMail(t *testing.T, response []byte) []byte {
	t.Helper()
	entry, found, err := wire.Bytes(response, 1)
	if err != nil || !found {
		t.Fatalf("missing first mail: found=%v err=%v", found, err)
	}
	return entry
}

func TestMailOpenAcceptsOfficialPackedRequest(t *testing.T) {
	request := []byte{0x08, 0xef, 0x01, 0x12, 0x0f, 0x85, 0xc1, 0xe1, 0xce, 0x30, 0x88, 0xc1, 0xe1, 0xce, 0x30, 0x8a, 0xc1, 0xe1, 0xce, 0x30}
	ids, err := requestMailIDs(request)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{13050077317, 13050077320, 13050077322}
	if len(ids) != len(want) {
		t.Fatalf("ids=%v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids=%v", ids)
		}
	}
}

func TestStarterMailProtocolEncodingAndRoundTrip(t *testing.T) {
	seed := &Starter{Version: "2.35.10", MailCount: 2, MaxMailID: 7, Mails: []MailDBInfo{{
		MailID: 7, MailType: 2, Title: "Welcome", Body: "Rewards", ExpiresAt: 1000, SentAt: 10,
		RewardTypes: []uint64{3, 8}, RewardIDs: []uint64{0, 17}, RewardCounts: []uint64{5, 300},
	}}}
	path := filepath.Join(t.TempDir(), "mail.json")
	if err := seed.Write(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// MailInfoResponse contains MailDBInfo field 1 plus count and maximum ID.
	// Reward arrays are packed protobuf varints; default ID 0 remains a slot.
	wantMail := []byte{0x08, 7, 0x10, 2, 0x2a, 7, 'W', 'e', 'l', 'c', 'o', 'm', 'e', 0x32, 7, 'R', 'e', 'w', 'a', 'r', 'd', 's', 0x38, 0xe8, 7, 0x42, 2, 3, 8, 0x4a, 2, 0, 17, 0x52, 3, 5, 0xac, 2, 0x68, 10}
	want := append([]byte{0x0a, byte(len(wantMail))}, wantMail...)
	want = append(want, 0x10, 2, 0x18, 7)
	code, actual, handled, err := loaded.Handle("/MailInfo", wire.AppendVarint(nil, 1, 249))
	if err != nil || !handled || code != 131 || !bytes.Equal(actual, want) {
		t.Fatalf("code=%d proto=%x handled=%v err=%v", code, actual, handled, err)
	}
}

func TestValidateRejectsRewardLengthMismatch(t *testing.T) {
	seed := &Starter{Version: "2.35.10", Mails: []MailDBInfo{{MailID: 1, MailType: 2, ExpiresAt: 1, SentAt: 1, RewardTypes: []uint64{8}, RewardIDs: []uint64{1}}}, MailCount: 2, MaxMailID: 1}
	if seed.Validate() == nil {
		t.Fatal("accepted bad rewards")
	}
}
