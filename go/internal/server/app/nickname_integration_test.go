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
