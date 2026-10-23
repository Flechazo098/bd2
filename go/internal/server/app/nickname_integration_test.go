//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/gateway/session"
	"bd2server/internal/server/runtime/player"
)

func TestNicknamePersistsAndProjectsWithoutChangingIdentity(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	t.Cleanup(func() { closeNicknamePlayer(t, owner) })
	seedNicknameTickets(t, owner, 6)
	identity, err := factory.profiles.GameIdentity(context.Background(), accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	beforeTickets, beforeWallet := nicknameAssets(t, owner)
	seq := uint64(0)
	call := func(path string, fields []byte) []byte {
		t.Helper()
		seq++
		c := registryCommand(accounts[0], fmt.Sprintf("nickname-%d", seq), path, append(appScalar(1, seq), fields...))
		c.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
		reply, err := owner.Execute(context.Background(), c)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(reply.Responses) != 1 {
			t.Fatalf("%s returned %d responses", path, len(reply.Responses))
		}
		return reply.Responses[0].Body
	}
	assertName := func(name string) {
		t.Helper()
		profile := call("/UserContentsInfo", appScalar(2, uint64(identity.OwnerIndex)))
		if appValue(profile, 1) != uint64(identity.OwnerIndex) || nicknameField(t, profile, 2) != name {
			t.Fatalf("profile identity/name is stale: %x", profile)
		}
		room := appRows(profile, 14)
		if len(room) != 1 || appValue(room[0], 1) != uint64(identity.OwnerIndex) || nicknameField(t, room[0], 2) != name {
			t.Fatalf("profile room identity/name is stale: %x", room)
		}
		login := appRows(call("/LoginUser", nil), 1)
		if len(login) != 1 || appValue(login[0], 1) != uint64(identity.OwnerIndex) || nicknameField(t, login[0], 2) != name {
			t.Fatalf("login identity/name is stale: %x", login)
		}
	}
	seq++
	rename := registryCommand(accounts[0], fmt.Sprintf("nickname-%d", seq), "/UserNicknameChange", append(appScalar(1, seq), appMessage(2, []byte("CodexName7"))...))
	reply, err := owner.Execute(context.Background(), rename)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Responses) != 1 || reply.Responses[0].PacketCode != 74 || len(reply.Responses[0].Body) != 0 {
		t.Fatalf("invalid nickname success response: %+v", reply)
	}
	if _, err = owner.Execute(context.Background(), rename); err != nil {
		t.Fatal("exact rename replay", err)
	}
	assertName("CodexName7")
	if tickets, wallet := nicknameAssets(t, owner); tickets != beforeTickets-1 || wallet != beforeWallet {
		t.Fatalf("rename/replay assets: tickets %d want %d; wallet %+v want %+v", tickets, beforeTickets-1, wallet, beforeWallet)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = openNicknamePlayer(t, factory, accounts[0])
	assertName("CodexName7")
	if _, err = owner.Execute(context.Background(), rename); err != nil {
		t.Fatal("durable rename replay", err)
	}
	if tickets, _ := nicknameAssets(t, owner); tickets != beforeTickets-1 {
		t.Fatalf("durable replay spent again: %d", tickets)
	}
	for _, name := range []string{"123456789012345678901234", "青山明月春风白云", "Flechazo"} {
		call("/UserNicknameChange", appMessage(2, []byte(name)))
		assertName(name)
	}
	if tickets, wallet := nicknameAssets(t, owner); tickets != beforeTickets-4 || wallet != beforeWallet {
		t.Fatalf("boundary renames charged wrong assets: %d %+v", tickets, wallet)
	}
	second := openNicknamePlayer(t, factory, accounts[1])
	t.Cleanup(func() { closeNicknamePlayer(t, second) })
	seedNicknameTickets(t, second, 1)
	other, err := factory.profiles.GameIdentity(context.Background(), accounts[1])
	if err != nil {
		t.Fatal(err)
	}
	if other.AccountID == identity.AccountID || other.OwnerIndex == identity.OwnerIndex {
		t.Fatal("two account fixtures share identity")
	}
	if _, err = second.Execute(context.Background(), registryCommand(accounts[1], "duplicate-display-name", "/UserNicknameChange", append(appScalar(1, 1), appMessage(2, []byte("Flechazo"))...))); err != nil {
		t.Fatal("explicit duplicate display-name policy", err)
	}
	if second.assembly.nicknameService.CurrentNickname() != "Flechazo" {
		t.Fatal("second account nickname not committed")
	}
	afterIdentity, err := factory.profiles.GameIdentity(context.Background(), accounts[0])
	if err != nil || afterIdentity != identity {
		t.Fatalf("rename changed authentication identity: %+v %v", afterIdentity, err)
	}
}

func TestNicknameRejectionsAndBatchFailurePreserveCommittedAssets(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	t.Cleanup(func() { closeNicknamePlayer(t, owner) })
	seedNicknameTickets(t, owner, 2)
	initial := owner.assembly.nicknameService.CurrentNickname()
	tickets, wallet := nicknameAssets(t, owner)
	seq := uint64(10)
	cases := []struct {
		name   string
		fields []byte
	}{
		{"short", appMessage(2, []byte("abc"))},
		{"over24", appMessage(2, []byte("1234567890123456789012345"))},
		{"oneHan", appMessage(2, []byte("山"))},
		{"nineHan", appMessage(2, []byte("青山明月春风白云海"))},
		{"space", appMessage(2, []byte("Name 7"))},
		{"underscore", appMessage(2, []byte("Name_7"))},
		{"emoji", appMessage(2, []byte("Name😀"))},
		{"invalidUTF8", appMessage(2, []byte{0xff, 0xfe, 0xfd, 0xfc})},
		{"missingName", nil},
		{"wrongNameWireType", appScalar(2, 1)},
		{"duplicateName", append(appMessage(2, []byte("CodexName7")), appMessage(2, []byte("CodexName8"))...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq++
			_, err := owner.Execute(context.Background(), registryCommand(accounts[0], fmt.Sprintf("reject-%d", seq), "/UserNicknameChange", append(appScalar(1, seq), tc.fields...)))
			if err == nil {
				t.Fatal("invalid nickname accepted")
			}
			if owner.assembly.nicknameService.CurrentNickname() != initial {
				t.Fatal("rejection changed name")
			}
			if got, balance := nicknameAssets(t, owner); got != tickets || balance != wallet {
				t.Fatalf("rejection changed assets: %d %+v", got, balance)
			}
		})
	}
	batch := registryCommand(accounts[0], "nickname-rollback", "/UserNicknameChange", append(appScalar(1, 80), appMessage(2, []byte("CodexName8"))...))
	batch.Requests = append(batch.Requests, player.Request{Path: "/UserNicknameChange", Body: append(appScalar(1, 81), appMessage(2, []byte("bad space"))...)})
	batch.Digest = sha256.Sum256(append(batch.Requests[0].Body, batch.Requests[1].Body...))
	if _, err := owner.Execute(context.Background(), batch); err == nil {
		t.Fatal("invalid second batch action accepted")
	}
	if owner.assembly.nicknameService.CurrentNickname() != initial {
		t.Fatal("batch rollback kept uncommitted nickname")
	}
	if got, balance := nicknameAssets(t, owner); got != tickets || balance != wallet {
		t.Fatalf("batch rollback lost assets: %d %+v", got, balance)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = openNicknamePlayer(t, factory, accounts[0])
	if owner.assembly.nicknameService.CurrentNickname() != initial {
		t.Fatal("reopen resurrected rolled-back nickname")
	}
	if got, balance := nicknameAssets(t, owner); got != tickets || balance != wallet {
		t.Fatalf("reopen resurrected rolled-back assets: %d %+v", got, balance)
	}
	empty := openNicknamePlayer(t, factory, accounts[1])
	t.Cleanup(func() { closeNicknamePlayer(t, empty) })
	emptyTickets, emptyWallet := nicknameAssets(t, empty)
	if emptyTickets != 1 {
		t.Fatalf("starter fixture has %d tickets, want the independently seeded one", emptyTickets)
	}
	if _, err := empty.Execute(context.Background(), registryCommand(accounts[1], "starter-ticket", "/UserNicknameChange", append(appScalar(1, 1), appMessage(2, []byte("CodexName8"))...))); err != nil {
		t.Fatal("existing starter ticket is unusable", err)
	}
	emptyName := "CodexName8"
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	empty = openNicknamePlayer(t, factory, accounts[1])
	if _, err := empty.Execute(context.Background(), registryCommand(accounts[1], "no-ticket", "/UserNicknameChange", append(appScalar(1, 2), appMessage(2, []byte("CodexName7"))...))); err == nil {
		t.Fatal("rename without ticket accepted")
	}
	if got, balance := nicknameAssets(t, empty); got != 0 || balance != emptyWallet || empty.assembly.nicknameService.CurrentNickname() != emptyName {
		t.Fatal("no-ticket rejection changed state")
	}
}

func TestNicknameNativeAlreadyUsedErrorUsesActiveSessionEnvelope(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	seedNicknameTickets(t, owner, 2)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	registry := newPlayerRegistry(factory, time.Minute)
	t.Cleanup(func() {
		if err := registry.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server, err := session.NewServer(registry, nicknameAuthenticator{accounts[0]})
	if err != nil {
		t.Fatal(err)
	}
	fixed := []byte("abcdefghijkrstuv024680wxyzlmnopq")
	login, err := server.DispatchRaw(context.Background(), "/LoginUser", nicknameEncrypt(t, appScalar(1, 1), fixed), "")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		PacketCode int
		ErrorType  int
		Data       string
	}
	if err = json.Unmarshal(login.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	user := appRows(nicknameDecrypt(t, envelope.Data, fixed), 1)
	if len(user) != 1 {
		t.Fatal("invalid login user envelope")
	}
	key := []byte(nicknameField(t, user[0], 3))
	if len(key) != 32 {
		t.Fatal("login did not supply session key")
	}
	cookie := "s=" + login.Cookie
	request := append(appScalar(1, 2), appMessage(2, []byte("CodexName7"))...)
	if _, err = server.DispatchRaw(context.Background(), "/UserNicknameChange", nicknameEncrypt(t, request, key), cookie); err != nil {
		t.Fatal(err)
	}
	request = append(appScalar(1, 3), appMessage(2, []byte("CodexName7"))...)
	for range 2 {
		reply, err := server.DispatchRaw(context.Background(), "/UserNicknameChange", nicknameEncrypt(t, request, key), cookie)
		if err != nil {
			t.Fatal("native nickname rejection invalidated session", err)
		}
		if err = json.Unmarshal(reply.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.PacketCode != 74 || envelope.ErrorType != 1 || len(nicknameDecrypt(t, envelope.Data, key)) != 0 {
			t.Fatalf("wrong native error envelope: %s", reply.Body)
		}
	}
	if _, err = server.DispatchRaw(context.Background(), "/UserNicknameChange", nicknameEncrypt(t, append(appScalar(1, 4), appMessage(2, []byte("bad space"))...), key), cookie); err == nil {
		t.Fatal("unclassified request failure became a success envelope")
	}
	if _, err = server.DispatchRaw(context.Background(), "/ItemInfo", nicknameEncrypt(t, appScalar(1, 5), key), cookie); err != nil {
		t.Fatal("read-only rejection invalidated active session", err)
	}
	if err = registry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := openNicknamePlayer(t, factory, accounts[0])
	defer closeNicknamePlayer(t, reopened)
	if tickets, _ := nicknameAssets(t, reopened); tickets != 2 || reopened.assembly.nicknameService.CurrentNickname() != "CodexName7" {
		t.Fatal("native errors consumed tickets or changed committed nickname")
	}
}

func openNicknamePlayer(t *testing.T, f *PlayerFactory, account string) *playerInstance {
	t.Helper()
	owner, err := f.open(account)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
func closeNicknamePlayer(t *testing.T, owner *playerInstance) {
	t.Helper()
	if err := owner.Close(); err != nil {
		t.Error(err)
	}
}
func seedNicknameTickets(t *testing.T, owner *playerInstance, count uint64) {
	t.Helper()
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx, InstanceIDs: owner.factory.instanceIDs(), Identity: command.Identity{AccountID: owner.accountID}}
	if _, err = owner.assembly.ownedItems.GrantOnce(ctx, "nickname-ticket-fixture", []gamedata.BattleReward{{Type: 14, ID: 1, Count: count}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func nicknameAssets(t *testing.T, owner *playerInstance) (uint64, assets.Currency) {
	t.Helper()
	tx, err := owner.repository.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx := command.Context{State: tx}
	var count uint64
	for _, item := range owner.assembly.ownedItems.All(ctx) {
		if item.Type == 14 && item.ID == 1 {
			count += item.Count
		}
	}
	return count, owner.assembly.wallet.Snapshot(ctx)
}
func nicknameField(t *testing.T, body []byte, field int) string {
	t.Helper()
	rows := appRows(body, field)
	if len(rows) != 1 {
		t.Fatalf("field %d has %d values", field, len(rows))
	}
	return string(rows[0])
}

type nicknameAuthenticator struct{ account string }

func (a nicknameAuthenticator) AuthenticateLogin([]byte) (string, error) {
	if a.account == "" {
		return "", errors.New("missing fixture account")
	}
	return a.account, nil
}

// The client transport oracle independently implements AES-CBC/base64 and
// obtains the active key from LoginUser, rather than using server cryptox.
func nicknameEncrypt(t *testing.T, plain, key []byte) []byte {
	t.Helper()
	plain = []byte(base64.StdEncoding.EncodeToString(plain))
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, plain)
	return []byte(base64.StdEncoding.EncodeToString(out))
}
func nicknameDecrypt(t *testing.T, encoded string, key []byte) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		t.Fatalf("invalid encrypted response: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(plain, raw)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || !bytes.Equal(plain[len(plain)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		t.Fatal("invalid response padding")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(plain[:len(plain)-pad]))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
