//go:build integration

package app

import (
	"bd2server/internal/server/design/gameconfig"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/events/calendar"
	"bd2server/internal/server/domain/identity"
	"bd2server/internal/server/domain/roster"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/runtime/player"
	identitystore "bd2server/internal/server/storage/identity"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// This integration uses the selected repository GameData and seeds. Missing
// resources are a setup failure, never a silently skipped asset-isolation test.
// Network fields are independently taken from the 2.35.10 client classes:
// DeckSaveRequest/DeckDBInfo, EquipUseRequest, SaveUserPositionRequest,
// MailOpenRequest/RewardDBInfoBundle, CostumeDBInfo and QuestUpdateRequest.
func newIntegrationFactory(t *testing.T) (*PlayerFactory, []string) {
	t.Helper()
	versions, err := versionconfig.Find()
	if err != nil {
		t.Fatal(err)
	}
	versionconfig.Use(versions)
	resources := versions.Resolve("data/resources/GameData")
	if _, err = gamedata.Validate(resources, versions.GameDataVersion); err != nil {
		t.Fatalf("selected GameData required; fetch repository resources before integration tests: %v", err)
	}
	calendars, err := calendar.LoadDirectory(versions.Resolve("schedules"), versions.GameVersion, versions.GameDataVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err = calendars.ValidateDesign(resources, versions.GameDataVersion); err != nil {
		t.Fatal(err)
	}
	seed := versions.Resolve(versions.SeedDirectory)
	root := t.TempDir()
	config := &configuration{versions: versions, calendars: calendars, gameRules: gameconfig.Default(), gameData: resources, gameDataVersion: versions.GameDataVersion, stateDirectory: filepath.Join(root, "state"), devToolsConfig: filepath.Join(root, "development.json"), accountSeed: filepath.Join(seed, "login_user.json"), playerSeed: filepath.Join(seed, "starter_player.json"), readonlySeed: filepath.Join(seed, "readonly.json"), mailSeed: filepath.Join(seed, "mail.json"), deckSeed: filepath.Join(seed, "decks.json"), worldSeed: filepath.Join(seed, "world.json")}
	if err = lockServerState(config.stateDirectory, config.gameRules.Story.StartPackID); err != nil {
		t.Fatal(err)
	}
	seeds, err := loadSeeds(config)
	if err != nil {
		t.Fatal(err)
	}
	design, err := loadDesign(config, seeds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gamedata.CloseDatabaseCache(); err != nil {
			t.Error(err)
		}
	})
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	profileStore, err := identitystore.Open(filepath.Join(root, "identity.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := profileStore.Close(); err != nil {
			t.Error(err)
		}
	})
	registration, err := identity.New(identity.Config{Providers: map[string]string{"discord": "integration-client"}, DeviceTTL: time.Minute, AccessTTL: time.Hour, RefreshTTL: 24 * time.Hour}, profileStore)
	if err != nil {
		t.Fatal(err)
	}
	accounts := []string{}
	for _, subject := range []string{"11111111", "22222222"} {
		device, err := registration.CreateDevice("discord", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		authorization, err := registration.Start("discord", device.ID, device.StartTicket)
		if err != nil {
			t.Fatal(err)
		}
		if err = registration.CompleteDevice(authorization.ID, "discord", identity.ProviderIdentity{Issuer: "https://discord.com", Subject: subject}); err != nil {
			t.Fatal(err)
		}
		result, err := registration.Poll(device.ID, device.Secret)
		if err != nil {
			t.Fatal(err)
		}
		id, err := registration.ValidateAccess(result.Tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, id)
	}
	return &PlayerFactory{options: config, seeds: seeds, design: design, profiles: profileStore}, accounts
}
func appScalar(field int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field<<3)), value)
}
func appMessage(field int, value []byte) []byte {
	p := binary.AppendUvarint(nil, uint64(field<<3|2))
	p = binary.AppendUvarint(p, uint64(len(value)))
	return append(p, value...)
}
func appRows(body []byte, wanted int) [][]byte {
	var rows [][]byte
	for len(body) > 0 {
		k, n := binary.Uvarint(body)
		if n <= 0 {
			return nil
		}
		body = body[n:]
		if k&7 == 0 {
			_, n = binary.Uvarint(body)
			if n <= 0 {
				return nil
			}
			body = body[n:]
			continue
		}
		if k&7 != 2 {
			return nil
		}
		size, n := binary.Uvarint(body)
		if n <= 0 || size > uint64(len(body)-n) {
			return nil
		}
		value := body[n : n+int(size)]
		if int(k>>3) == wanted {
			rows = append(rows, value)
		}
		body = body[n+int(size):]
	}
	return rows
}
func appValue(body []byte, wanted int) uint64 {
	for len(body) > 0 {
		k, n := binary.Uvarint(body)
		if n <= 0 {
			return 0
		}
		body = body[n:]
		if k&7 == 0 {
			v, m := binary.Uvarint(body)
			if m <= 0 {
				return 0
			}
			if int(k>>3) == wanted {
				return v
			}
			body = body[m:]
			continue
		}
		if k&7 != 2 {
			return 0
		}
		size, m := binary.Uvarint(body)
		if m <= 0 || size > uint64(len(body)-m) {
			return 0
		}
		body = body[m+int(size):]
	}
	return 0
}
func appPacked(body []byte, field int) []uint64 {
	var values []uint64
	for _, row := range appRows(body, field) {
		for len(row) > 0 {
			v, n := binary.Uvarint(row)
			if n <= 0 {
				return nil
			}
			values = append(values, v)
			row = row[n:]
		}
	}
	return values
}
func appItemTotals(body []byte) map[[2]uint64]uint64 {
	totals := map[[2]uint64]uint64{}
	for _, row := range appRows(body, 1) {
		totals[[2]uint64{appValue(row, 3), appValue(row, 2)}] += appValue(row, 4)
	}
	return totals
}
func TestTwoRealPlayerBundlesKeepEquipmentFormationAndWorldSeparate(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	aID, bID := accounts[0], accounts[1]
	instances := map[string]*playerInstance{}
	runtimes := map[string]*player.Runtime{}
	var equipmentID uint64
	ids := make([]uint64, 0, len(factory.design.equipmentSlots))
	for id, slot := range factory.design.equipmentSlots {
		if slot == 1 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		t.Fatal("real GameData has no weapon slot")
	}
	equipmentID = ids[0]
	equipmentIndex := map[string]uint64{}
	characterIndex := map[string]uint64{}
	for _, accountID := range accounts {
		instance, err := factory.open(accountID)
		if err != nil {
			t.Fatal("assemble actual account", accountID, err)
		}
		instances[accountID] = instance
		// Establish one owned weapon as an explicit test grant before the actor is
		// exposed. Both players may legitimately use equal inventory numbers.
		tx, err := instance.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := command.Context{Identity: command.Identity{AccountID: accountID, SessionID: "fixture", RequestID: "weapon"}, State: tx}
		equipment, err := instance.assembly.ownedEquipment.GrantOnce(ctx, "test-owned-weapon", equipmentID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		equipmentIndex[accountID] = equipment.InvenIndex
		for _, c := range instance.assembly.worldService.CharacterService().RawAll() {
			if !roster.IsStoryCharacter(c) && !roster.IsCharmCharacter(c) && !roster.CharacterExpired(c, time.Now()) {
				characterIndex[accountID] = c.InvenIndex
				break
			}
		}
		if characterIndex[accountID] == 0 {
			t.Fatal("seed has no permanent character")
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		runtime, err := player.New(accountID, instance, player.Limits{CompletedReceipts: 1})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[accountID] = runtime
	}
	t.Cleanup(func() {
		for _, runtime := range runtimes {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := runtime.Close(ctx); err != nil {
				t.Error(err)
			}
			cancel()
		}
	})
	makeCommand := func(accountID, identity string, requests []player.Request) player.Command {
		content := []byte{}
		for _, r := range requests {
			content = append(content, []byte(r.Path)...)
			content = append(content, r.Body...)
		}
		return player.Command{Identity: command.Identity{AccountID: accountID, SessionID: "client-session", RequestID: identity}, Digest: sha256.Sum256(content), Requests: requests}
	}
	run := func(c player.Command) (player.Reply, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		future, err := runtimes[c.Identity.AccountID].Submit(ctx, c)
		if err != nil {
			return player.Reply{}, err
		}
		return future.Wait(ctx)
	}
	query := func(accountID, path string, sequence uint64) []byte {
		t.Helper()
		reply, err := run(makeCommand(accountID, fmt.Sprint("query-", path, "-", sequence), []player.Request{{Path: path, Body: appScalar(1, sequence)}}))
		if err != nil {
			t.Fatal(accountID, path, err)
		}
		return reply.Responses[0].Body
	}

	// New-player ownership is instantiated separately even though versioned seeds
	// contain the same public design IDs. Mail claims must affect only the claimant.
	beforeBCostumes := query(bID, "/CostumeInfo", 100)
	beforeBItems := appItemTotals(query(bID, "/ItemInfo", 101))
	beforeAItems := appItemTotals(query(aID, "/ItemInfo", 102))
	beforeACostumes := query(aID, "/CostumeInfo", 103)
	if len(appRows(beforeACostumes, 1)) == 0 || len(appRows(beforeBCostumes, 1)) == 0 {
		t.Fatal("new player received no starter costume ownership")
	}
	owned := map[uint64]bool{}
	for _, row := range appRows(beforeACostumes, 1) {
		owned[appValue(row, 2)] = true
	}
	mailbox := query(aID, "/MailInfo", 104)
	var costumeMail uint64
	expectedCostumes := map[uint64]uint64{}
	for _, row := range appRows(mailbox, 1) {
		types, ids, counts := appPacked(row, 8), appPacked(row, 9), appPacked(row, 10)
		if len(types) != len(ids) || len(types) != len(counts) {
			t.Fatal("client mail reward arrays differ in length")
		}
		candidate := map[uint64]uint64{}
		valid := len(types) > 0
		for i, kind := range types {
			if kind != 11 || counts[i] != 6 || owned[ids[i]] {
				valid = false
				break
			}
			candidate[ids[i]] = counts[i] - 1
		}
		if valid {
			costumeMail = appValue(row, 1)
			expectedCostumes = candidate
			break
		}
	}
	if costumeMail == 0 {
		t.Fatal("versioned new-player entitlement has no unowned six-copy costume mail")
	}
	claim := func(id, seq uint64) player.Command {
		return makeCommand(aID, fmt.Sprint("mail-claim-", seq), []player.Request{{Path: "/MailOpen", Body: append(appScalar(1, seq), appScalar(2, id)...)}})
	}
	costumeClaim := claim(costumeMail, 105)
	costumeReply, err := run(costumeClaim)
	if err != nil {
		t.Fatal("claim real costume entitlement", err)
	}
	bundles := appRows(costumeReply.Responses[0].Body, 1)
	if len(bundles) != 1 {
		t.Fatal("mail claim omitted reward bundle")
	}
	// During the introductory chapter CostumeInfo intentionally shows the story
	// roster. MailOpen's RewardDBInfoBundle is the client's ownership update.
	for id, level := range expectedCostumes {
		found := false
		for _, row := range appRows(bundles[0], 3) {
			if appValue(row, 2) == id {
				found = true
				if appValue(row, 3) != level {
					t.Fatal("six copies did not yield acquisition plus five enhancements", id, appValue(row, 3), level)
				}
			}
		}
		if !found {
			t.Fatal("costume reward message omitted listed costume", id)
		}
	}
	collection := instances[aID].assembly.collection.Costumes()
	for id, level := range expectedCostumes {
		found := false
		for _, entry := range collection {
			if entry.ID == id {
				found = true
				if entry.Level != level {
					t.Fatal("durable costume level differs from gift")
				}
			}
		}
		if !found {
			t.Fatal("costume ownership not committed", id)
		}
	}
	if !bytes.Equal(beforeBCostumes, query(bID, "/CostumeInfo", 107)) {
		t.Fatal("A costume entitlement granted to B")
	}
	var starterMail uint64
	expectedItems := map[[2]uint64]uint64{}
	for _, entry := range factory.seeds.mailbox.Mails {
		valid := len(entry.RewardTypes) > 0
		for i, kind := range entry.RewardTypes {
			if kind != 8 || entry.RewardIDs[i] == 0 {
				valid = false
			}
		}
		if valid && entry.ExpiresAt > uint64(time.Now().UnixMilli()) {
			starterMail = entry.MailID
			for i, kind := range entry.RewardTypes {
				expectedItems[[2]uint64{kind, entry.RewardIDs[i]}] += entry.RewardCounts[i]
			}
			break
		}
	}
	if starterMail == 0 {
		t.Fatal("versioned starter mail requires a current resource gift")
	}
	resourceClaim := claim(starterMail, 108)
	if _, err := run(resourceClaim); err != nil {
		t.Fatal("claim versioned starter resources", err)
	}
	actualItems := appItemTotals(query(aID, "/ItemInfo", 109))
	for key, gift := range expectedItems {
		if actualItems[key] != beforeAItems[key]+gift {
			t.Fatal("mail claim did not deliver listed resource count", key, actualItems[key], gift)
		}
	}
	for key, old := range beforeBItems {
		if appItemTotals(query(bID, "/ItemInfo", 110))[key] != old {
			t.Fatal("starter mail changed unrelated player's inventory")
		}
	}
	// Memo is one item; query eviction forces the next claim retry to use SQL.
	if _, err := run(resourceClaim); err != nil {
		t.Fatal("retry starter claim", err)
	}
	afterRetry := appItemTotals(query(aID, "/ItemInfo", 111))
	for key, value := range actualItems {
		if afterRetry[key] != value {
			t.Fatal("starter claim retry issued duplicate resources")
		}
	}
	// QuestUpdate stores the client's counters without granting QuestClear's
	// GameData rewards. The selected starting quest is a genuine active quest.
	selection, selected := instances[aID].assembly.progressState.Selection(factory.options.gameRules.Story.StartPackID)
	if !selected || selection.QuestID <= 0 {
		t.Fatal("new-player active quest missing")
	}
	beforeGold := instances[aID].assembly.wallet.Snapshot(command.Context{}).Gold
	questBody := append(appScalar(1, 120), appScalar(2, uint64(selection.QuestID))...)
	questBody = append(questBody, appScalar(3, uint64(factory.options.gameRules.Story.StartPackID))...)
	questBody = append(questBody, appScalar(4, 1)...)
	if _, err := run(makeCommand(aID, "quest-counter", []player.Request{{Path: "/QuestUpdate", Body: questBody}})); err != nil {
		t.Fatal("actual quest progress", err)
	}
	current, found := instances[aID].assembly.progressState.QuestInPack(selection.QuestID, factory.options.gameRules.Story.StartPackID)
	if !found || len(current.Values) != 1 || current.Values[0] != 1 {
		t.Fatal("quest update did not retain player counter")
	}
	if other, found := instances[bID].assembly.progressState.QuestInPack(selection.QuestID, factory.options.gameRules.Story.StartPackID); found && len(other.Values) > 0 {
		t.Fatal("quest counter leaked to B")
	}
	if instances[aID].assembly.wallet.Snapshot(command.Context{}).Gold != beforeGold {
		t.Fatal("quest update incorrectly granted clear reward")
	}
	beforeBDeck := query(bID, "/DeckInfo", 10)
	makeDeck := func(index, position, sequence uint64) []byte {
		entry := append(appScalar(1, index), appScalar(2, position)...)
		entry = append(entry, appScalar(3, 1)...)
		return append(appScalar(1, sequence), appMessage(2, entry)...)
	}
	equip := append(appScalar(1, 11), appScalar(2, equipmentIndex[aID])...)
	equip = append(equip, appScalar(3, characterIndex[aID])...)
	position := []byte(`{"MapId":1,"PlayerPosition":{"x":12,"y":3,"z":4},"ColleaguePositions":[]}`)
	save := append(appScalar(1, 13), appScalar(2, uint64(factory.options.gameRules.Story.StartPackID))...)
	save = append(save, appMessage(3, position)...)
	changes := makeCommand(aID, "asset-batch", []player.Request{{Path: "/EquipUse", Body: equip}, {Path: "/DeckSave", Body: makeDeck(characterIndex[aID], 5, 12)}, {Path: "/SaveUserPosition", Body: save}})
	if _, err := run(changes); err != nil {
		t.Fatal("actual asset batch", err)
	}
	if !bytes.Equal(beforeBDeck, query(bID, "/DeckInfo", 20)) {
		t.Fatal("another account's formation changed")
	}
	bEquipment := appRows(query(bID, "/EquipInfo", 21), 1)
	if len(bEquipment) != 1 || appValue(bEquipment[0], 2) != 0 {
		t.Fatal("A equipped B's equal-index weapon")
	}
	aEquipment := appRows(query(aID, "/EquipInfo", 22), 1)
	if len(aEquipment) != 1 || appValue(aEquipment[0], 2) != characterIndex[aID] {
		t.Fatal("owner weapon not bound to owner's character")
	}
	aDeck := query(aID, "/DeckInfo", 23)
	entries := appRows(aDeck, 1)
	if len(entries) != 1 || appValue(entries[0], 1) != characterIndex[aID] || appValue(entries[0], 2) != 5 {
		t.Fatal("ordinary deck did not preserve selected battle-grid position")
	}
	if _, found := instances[bID].assembly.progressState.Position(); found {
		t.Fatal("world position leaked to unrelated account")
	}
	if saved, found := instances[aID].assembly.progressState.Position(); !found || saved.Position.PlayerPosition.X != 12 {
		t.Fatal("owner world position missing")
	}
	// Evict the small in-memory memo before replaying an asset operation. The
	// committed database receipt must preserve the exact response and ownership.
	replay, err := run(changes)
	if err != nil || len(replay.Responses) != 3 {
		t.Fatal("durable asset retry failed", err)
	}
	if !bytes.Equal(aDeck, query(aID, "/DeckInfo", 24)) {
		t.Fatal("retry modified formation")
	}
	// A later invalid equipment member must roll back an earlier valid deck
	// replacement in the same client batch and recover only this player's state.
	invalid := append(appScalar(1, 31), appScalar(2, ^uint64(0))...)
	invalid = append(invalid, appScalar(3, characterIndex[aID])...)
	failed := makeCommand(aID, "rejected-batch", []player.Request{{Path: "/DeckSave", Body: makeDeck(characterIndex[aID], 8, 30)}, {Path: "/EquipUse", Body: invalid}})
	if _, err = run(failed); err == nil {
		t.Fatal("invalid equipment accepted")
	}
	if !bytes.Equal(aDeck, query(aID, "/DeckInfo", 32)) {
		t.Fatal("failed batch published uncommitted formation")
	}
	if !bytes.Equal(beforeBDeck, query(bID, "/DeckInfo", 33)) {
		t.Fatal("A recovery replaced B state")
	}
	// Unload and recreate real bundles from their separate SQLite databases.
	for _, accountID := range accounts {
		if err := runtimes[accountID].Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		instance, err := factory.open(accountID)
		if err != nil {
			t.Fatal("reopen account", err)
		}
		instances[accountID] = instance
		runtime, err := player.New(accountID, instance, player.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[accountID] = runtime
	}
	if !bytes.Equal(aDeck, query(aID, "/DeckInfo", 40)) || !bytes.Equal(beforeBDeck, query(bID, "/DeckInfo", 41)) {
		t.Fatal("reopen combined player formations")
	}
	if _, found := instances[bID].assembly.progressState.Position(); found {
		t.Fatal("reopen mixed world progress")
	}
	if saved, found := instances[aID].assembly.progressState.Position(); !found || saved.Position.PlayerPosition.X != 12 {
		t.Fatal("owner progress lost on reopen")
	}
	retainedQuest, found := instances[aID].assembly.progressState.QuestInPack(selection.QuestID, factory.options.gameRules.Story.StartPackID)
	if !found || len(retainedQuest.Values) != 1 || retainedQuest.Values[0] != 1 {
		t.Fatal("owner task counter lost on reopen")
	}
	if other, found := instances[bID].assembly.progressState.QuestInPack(selection.QuestID, factory.options.gameRules.Story.StartPackID); found && len(other.Values) > 0 {
		t.Fatal("reopen mixed task counters")
	}
	for id, level := range expectedCostumes {
		found := false
		for _, entry := range instances[aID].assembly.collection.Costumes() {
			if entry.ID == id {
				found = true
				if entry.Level != level {
					t.Fatal("reopen changed costume enhancement")
				}
			}
		}
		if !found {
			t.Fatal("reopen lost claimed costume", id)
		}
	}
	bProfile, err := factory.profiles.GameIdentity(context.Background(), bID)
	if err != nil {
		t.Fatal(err)
	}
	aProfile, err := factory.profiles.GameIdentity(context.Background(), aID)
	if err != nil {
		t.Fatal(err)
	}
	aLogin := makeCommand(aID, "fresh-login-A", []player.Request{{Path: "/LoginUser", Body: appScalar(1, 121)}})
	aLogin.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	bLogin := makeCommand(bID, "fresh-login-B", []player.Request{{Path: "/LoginUser", Body: appScalar(1, 121)}})
	bLogin.LoginSessionKey = []byte("fedcba9876543210fedcba9876543210")
	aReply, err := run(aLogin)
	if err != nil {
		t.Fatal(err)
	}
	bReply, err := run(bLogin)
	if err != nil {
		t.Fatal(err)
	}
	aUser, bUser := appRows(aReply.Responses[0].Body, 1), appRows(bReply.Responses[0].Body, 1)
	if len(aUser) != 1 || len(bUser) != 1 || appValue(aUser[0], 1) != uint64(aProfile.OwnerIndex) || appValue(bUser[0], 1) != uint64(bProfile.OwnerIndex) || appValue(aUser[0], 1) == appValue(bUser[0], 1) {
		t.Fatal("actual login responses share player numeric identity")
	}
	aUserID, bUserID := appRows(aUser[0], 2), appRows(bUser[0], 2)
	if len(aUserID) != 1 || len(bUserID) != 1 || string(aUserID[0]) != aProfile.UserID || string(bUserID[0]) != bProfile.UserID || bytes.Equal(aUserID[0], bUserID[0]) {
		t.Fatal("actual login responses share player string identity")
	}
	for _, instance := range instances {
		tx, err := instance.repository.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		records, err := tx.ListEntries("missions", "command_receipts")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for _, raw := range records {
			if bytes.Contains(raw, aLogin.LoginSessionKey) || bytes.Contains(raw, bLogin.LoginSessionKey) {
				_ = tx.Rollback()
				t.Fatal("ephemeral login encryption key persisted as command receipt")
			}
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}

}
