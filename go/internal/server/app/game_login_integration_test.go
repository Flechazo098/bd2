//go:build integration

package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNewPlayerLoginSeparatesUIDFromDefaultNickname(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	for _, account := range accounts {
		owner := openNicknamePlayer(t, factory, account)
		t.Cleanup(func() { closeNicknamePlayer(t, owner) })
		identity, err := factory.profiles.GameIdentity(context.Background(), account)
		if err != nil {
			t.Fatal(err)
		}
		request := registryCommand(account, "first-game-login", "/LoginUser", appScalar(1, 1))
		request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
		reply, err := owner.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		users := appRows(reply.Responses[0].Body, 1)
		if len(users) != 1 || appValue(users[0], 1) != uint64(identity.OwnerIndex) || nicknameField(t, users[0], 2) != "Guest_"+strconv.FormatInt(identity.OwnerIndex, 10) {
			t.Fatal("new-player login must expose a numeric UID and a separate Guest nickname")
		}
	}
}

func TestGameLoginTimestampTracksSuccessfulEntryOnly(t *testing.T) {
	factory, accounts := newIntegrationFactory(t)
	owner := openNicknamePlayer(t, factory, accounts[0])
	t.Cleanup(func() { closeNicknamePlayer(t, owner) })
	db, err := sql.Open("sqlite", filepath.Join(filepath.Dir(factory.options.stateDirectory), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	readLogin := func(account string) int64 {
		t.Helper()
		var value int64
		if err := db.QueryRow(`SELECT last_login_at FROM accounts WHERE id=?`, account).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if readLogin(accounts[0]) != 0 || readLogin(accounts[1]) != 0 {
		t.Fatal("OAuth registration or player loading must not count as entering the game")
	}
	request := registryCommand(accounts[0], "rejected-game-login", "/LoginUser", appScalar(1, 1))
	if _, err := owner.Execute(context.Background(), request); err == nil {
		t.Fatal("login without a session key unexpectedly succeeded")
	}
	if readLogin(accounts[0]) != 0 {
		t.Fatal("rejected login was recorded as a successful game entry")
	}
	request = registryCommand(accounts[0], "successful-game-login", "/LoginUser", appScalar(1, 2))
	request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	started := time.Now().Unix()
	if _, err := owner.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	lastLogin := readLogin(accounts[0])
	if lastLogin < started || lastLogin > time.Now().Unix() || readLogin(accounts[1]) != 0 {
		t.Fatal("successful entry did not record the correct player and time")
	}
	previous := started - 3600
	if _, err := db.Exec(`UPDATE accounts SET last_login_at=? WHERE id=?`, previous, accounts[0]); err != nil {
		t.Fatal(err)
	}
	profile, err := factory.profiles.GameIdentity(context.Background(), accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Execute(context.Background(), registryCommand(accounts[0], "view-profile", "/UserContentsInfo", append(appScalar(1, 3), appScalar(2, uint64(profile.OwnerIndex))...))); err != nil {
		t.Fatal(err)
	}
	if readLogin(accounts[0]) != previous {
		t.Fatal("ordinary gameplay request changed the game login timestamp")
	}
	request = registryCommand(accounts[0], "next-game-login", "/LoginUser", appScalar(1, 4))
	request.LoginSessionKey = []byte("0123456789abcdef0123456789abcdef")
	if _, err := owner.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if readLogin(accounts[0]) < started || readLogin(accounts[1]) != 0 {
		t.Fatal("subsequent successful entry did not refresh only its own login timestamp")
	}
}
