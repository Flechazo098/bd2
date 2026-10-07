package player_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/domain/mail"
	"bd2server/internal/server/platform/versionconfig"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/runtime/player"
	accountstate "bd2server/internal/server/storage/account"
	"bd2server/internal/server/storage/stateio"
)

// The fixture uses the server's explicit compensation-mail rule: issue a named
// gift once, grant its gold only when opened, and retain the claim across login.
// Runtime controls scheduling; real mail, wallet and SQLite own all asset logic.
type mailExecutor struct {
	repository *accountstate.Repository
	wallet     *inventory.Wallet
	items      *inventory.Inventory
	mail       *mail.Service
	gold       uint64
	before     func(player.Command)
	failClaim  atomic.Bool
}

func newMailActor(t *testing.T, accountID, path string, gold uint64, limits player.Limits) (*player.Runtime, *mailExecutor) {
	t.Helper()
	repository, err := accountstate.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	executor := &mailExecutor{repository: repository, gold: gold}
	if err := executor.reload(context.Background()); err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	runtime, err := player.New(accountID, executor, limits)
	if err != nil {
		_ = repository.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runtime.Close(ctx); err != nil {
			t.Errorf("drain actor: %v", err)
		}
	})
	return runtime, executor
}

func (e *mailExecutor) reload(ctx context.Context) (err error) {
	tx, err := e.repository.BeginCommand(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	commandCtx := command.Context{Cancellation: ctx, State: tx}
	scope := stateio.RootStore{}
	wallet, err := inventory.OpenWallet(commandCtx, scope, inventory.Currency{})
	if err != nil {
		return err
	}
	items, err := inventory.OpenInventory(commandCtx, scope, nil)
	if err != nil {
		return err
	}
	mailbox, err := mail.OpenService(commandCtx, scope, &mail.Starter{Version: versionconfig.State(), MailCount: 1}, items, wallet)
	if err != nil {
		return err
	}
	for _, persist := range []func(command.Context) error{wallet.EnsurePersisted, items.EnsurePersisted, mailbox.EnsurePersisted} {
		if err := persist(commandCtx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.wallet, e.items, e.mail = wallet, items, mailbox
	return nil
}

func (e *mailExecutor) Execute(ctx context.Context, c player.Command) (reply player.Reply, err error) {
	if e.before != nil {
		e.before(c)
	}
	tx, err := e.repository.BeginCommand(ctx)
	if err != nil {
		return reply, player.Failure{Cause: err, RecoveryRequired: true}
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	commandCtx := command.Context{Identity: c.Identity, Cancellation: c.Cancellation, State: tx}
	for _, request := range c.Requests {
		response := player.Response{}
		switch request.Path {
		case "/admin/compensation":
			err = e.mail.EnqueueCompensation(commandCtx, string(request.Body), "Fixture compensation", "One claim per account", []gamedata.Reward{{Type: 4, Count: e.gold}}, time.Now().UTC())
		case "/MailOpen", "/MailInfo":
			var owned bool
			response.PacketCode, response.Body, owned, err = e.mail.Handle(commandCtx, request.Path, request.Body)
			if err == nil && !owned {
				err = errors.New("mail handler did not own its request")
			}
		case "/admin/spend":
			_, err = e.wallet.SpendGoldOnce(commandCtx, c.Identity.RequestID, e.gold+1)
		default:
			err = errors.New("unsupported fixture command")
		}
		if err != nil {
			if tx.Dirty() {
				err = player.Failure{Cause: err, RecoveryRequired: true}
			}
			return player.Reply{}, err
		}
		reply.Responses = append(reply.Responses, response)
		if request.Path == "/MailOpen" && e.failClaim.Swap(false) {
			return player.Reply{}, player.Failure{Cause: errors.New("injected failure after wallet and mail writes, before commit"), RecoveryRequired: true}
		}
	}
	if err = tx.Commit(); err != nil {
		return player.Reply{}, player.Failure{Cause: err, RecoveryRequired: true}
	}
	committed = true
	return reply, nil
}

func (e *mailExecutor) Recover(ctx context.Context) error {
	if err := e.reload(ctx); err != nil {
		return err
	}
	return nil
}

func (e *mailExecutor) Close() error { return e.repository.Close() }

func assetCommand(accountID, requestID, path string, body []byte) player.Command {
	return player.Command{Identity: command.Identity{AccountID: accountID, SessionID: "login-1", RequestID: requestID}, Digest: sha256.Sum256(append([]byte(path), body...)), Requests: []player.Request{{Path: path, Body: body}}}
}

func openMail(accountID string, seq, mailID uint64) player.Command {
	body := wire.AppendVarint(nil, 1, seq)
	body = wire.AppendBytes(body, 2, binary.AppendUvarint(nil, mailID))
	return assetCommand(accountID, "open:"+strconv.FormatUint(seq, 10), "/MailOpen", body)
}

func executeAsset(t *testing.T, runtime *player.Runtime, c player.Command) player.Reply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	future, err := runtime.Submit(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := future.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func durableAssets(t *testing.T, repository *accountstate.Repository) (gold uint64, opened, histories int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := repository.BeginCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	}()
	walletBytes, err := tx.Load("wallet")
	if err != nil {
		t.Fatal(err)
	}
	var wallet inventory.Currency
	if err := json.Unmarshal(walletBytes, &wallet); err != nil {
		t.Fatal(err)
	}
	mailBytes, err := tx.Load("mail")
	if err != nil {
		t.Fatal(err)
	}
	var mailbox struct {
		Opened []uint64 `json:"opened"`
	}
	if err := json.Unmarshal(mailBytes, &mailbox); err != nil {
		t.Fatal(err)
	}
	history, err := tx.ListEntries("mail", "history")
	if err != nil {
		t.Fatal(err)
	}
	return wallet.Gold, len(mailbox.Opened), len(history)
}

func TestMailAssetsRemainOwnedAcrossCancellationDrainAndReconnect(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "a", "state.db")
	pathB := filepath.Join(t.TempDir(), "b", "state.db")
	a, ownerA := newMailActor(t, "a", pathA, 101, player.Limits{})
	b, ownerB := newMailActor(t, "b", pathB, 207, player.Limits{})
	for _, account := range []struct {
		id       string
		runtime  *player.Runtime
		executor *mailExecutor
	}{{"a", a, ownerA}, {"b", b, ownerB}} {
		executeAsset(t, account.runtime, assetCommand(account.id, "gift-1", "/admin/compensation", []byte("compensation-1")))
		executeAsset(t, account.runtime, assetCommand(account.id, "read-mail", "/MailInfo", wire.AppendVarint(nil, 1, 1)))
		gold, opened, history := durableAssets(t, account.executor.repository)
		if gold != 0 || opened != 0 || history != 0 {
			t.Fatalf("unclaimed %s mail released assets: gold=%d opened=%d history=%d", account.id, gold, opened, history)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var gated atomic.Bool
	ownerA.before = func(c player.Command) {
		if c.Requests[0].Path == "/MailOpen" && gated.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	waitCtx, cancelWait := context.WithCancel(context.Background())
	claim := openMail("a", 2, 1)
	future, err := a.Submit(waitCtx, claim)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("mail claim did not start")
	}
	cancelWait()
	if _, err := future.Wait(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	duplicate, err := a.Submit(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := claim
	conflicting.Digest = sha256.Sum256([]byte("another mail attachment request"))
	if _, err := a.Submit(context.Background(), conflicting); !errors.Is(err, player.ErrIdentityConflict) {
		t.Fatalf("conflicting claim identity admitted: %v", err)
	}
	if _, err := a.Submit(context.Background(), openMail("b", 2, 1)); !errors.Is(err, player.ErrWrongAccount) {
		t.Fatalf("foreign account claim admitted: %v", err)
	}
	// Player B must be able to claim while A's accepted asset operation is paused.
	executeAsset(t, b, openMail("b", 2, 1))
	goldB, openedB, historyB := durableAssets(t, ownerB.repository)
	if goldB != 207 || openedB != 1 || historyB != 1 {
		t.Fatalf("B's independent claim: %d/%d/%d", goldB, openedB, historyB)
	}
	secondGift, err := a.Submit(context.Background(), assetCommand("a", "gift-2", "/admin/compensation", []byte("compensation-2")))
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := a.Submit(context.Background(), openMail("a", 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	closedCtx, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	if err := a.Close(closedCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled drain waiter: %v", err)
	}
	if _, err := a.Submit(context.Background(), assetCommand("a", "late-gift", "/admin/compensation", []byte("late"))); !errors.Is(err, player.ErrClosed) {
		t.Fatalf("closing actor admitted an asset command: %v", err)
	}
	close(release)
	for _, accepted := range []*player.Future{future, duplicate, secondGift, secondClaim} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := accepted.Wait(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconnected, recovered := newMailActor(t, "a", pathA, 101, player.Limits{CompletedReceipts: 1})
	goldA, openedA, historyA := durableAssets(t, recovered.repository)
	if goldA != 202 || openedA != 2 || historyA != 2 {
		t.Fatalf("drain lost accepted mail assets: %d/%d/%d", goldA, openedA, historyA)
	}
	claim.Identity.SessionID = "login-2"
	executeAsset(t, reconnected, claim)
	executeAsset(t, reconnected, assetCommand("a", "read-after-reconnect", "/MailInfo", wire.AppendVarint(nil, 1, 4)))
	executeAsset(t, reconnected, claim)
	goldA, openedA, historyA = durableAssets(t, recovered.repository)
	if goldA != 202 || openedA != 2 || historyA != 2 {
		t.Fatalf("reconnect/cache eviction duplicated mail rewards: %d/%d/%d", goldA, openedA, historyA)
	}
}

func TestMailClaimRollbackRecoversWithoutLosingOrDuplicatingAssets(t *testing.T) {
	runtime, owner := newMailActor(t, "account", filepath.Join(t.TempDir(), "state.db"), 313, player.Limits{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Submit(cancelled, assetCommand("account", "cancel-before-accept", "/admin/compensation", []byte("not-issued"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("preaccept cancellation: %v", err)
	}
	executeAsset(t, runtime, assetCommand("account", "gift", "/admin/compensation", []byte("issued")))
	owner.failClaim.Store(true)
	claim := openMail("account", 2, 1)
	future, err := runtime.Submit(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if _, err := future.Wait(ctx); err == nil {
		t.Fatal("injected uncommitted mail claim succeeded")
	}
	gold, opened, history := durableAssets(t, owner.repository)
	if gold != 0 || opened != 0 || history != 0 {
		t.Fatalf("failed claim published partial assets: %d/%d/%d", gold, opened, history)
	}
	executeAsset(t, runtime, claim)
	gold, opened, history = durableAssets(t, owner.repository)
	if gold != 313 || opened != 1 || history != 1 {
		t.Fatalf("retry did not preserve one complete mail award: %d/%d/%d", gold, opened, history)
	}
	invalidSpend := assetCommand("account", "spend-too-much", "/admin/spend", nil)
	refused, err := runtime.Submit(ctx, invalidSpend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Wait(ctx); err == nil {
		t.Fatal("insufficient funds were accepted")
	}
	executeAsset(t, runtime, assetCommand("account", "next-gift", "/admin/compensation", []byte("next")))
	gold, opened, history = durableAssets(t, owner.repository)
	if gold != 313 || opened != 1 || history != 1 {
		t.Fatalf("business refusal changed assets: %d/%d/%d", gold, opened, history)
	}
}

func TestMailboxAdmissionNeverLosesRejectedMailGifts(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits player.Limits
	}{
		{"command-count", player.Limits{MaxCommands: 1}},
		{"total-bytes", player.Limits{MaxCommands: 8, MaxBytes: 400}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, owner := newMailActor(t, "account", filepath.Join(t.TempDir(), "state.db"), 97, test.limits)
			executeAsset(t, runtime, assetCommand("account", "gift-1", "/admin/compensation", []byte("gift-1")))
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			owner.before = func(c player.Command) {
				if c.Identity.RequestID == "open:2" {
					close(entered)
					<-release
				}
			}
			claim := openMail("account", 2, 1)
			accepted, err := runtime.Submit(context.Background(), claim)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("claim did not enter owner")
			}
			gift := assetCommand("account", "gift-2", "/admin/compensation", []byte("gift-2"))
			if _, err := runtime.Submit(context.Background(), gift); !errors.Is(err, player.ErrMailboxFull) {
				t.Fatalf("unbounded asset admission: %v", err)
			}
			if _, err := runtime.Submit(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			close(release)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := accepted.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			executeAsset(t, runtime, gift)
			executeAsset(t, runtime, openMail("account", 3, 2))
			gold, opened, histories := durableAssets(t, owner.repository)
			if gold != 194 || opened != 2 || histories != 2 {
				t.Fatalf("capacity rejection/retry lost or duplicated a gift: %d/%d/%d", gold, opened, histories)
			}
		})
	}
}

func TestBatchMailClaimAndFailedSpendRollBackTogether(t *testing.T) {
	runtime, owner := newMailActor(t, "account", filepath.Join(t.TempDir(), "state.db"), 127, player.Limits{})
	executeAsset(t, runtime, assetCommand("account", "gift", "/admin/compensation", []byte("batch-gift")))
	batch := openMail("account", 2, 1)
	batch.Identity.RequestID = "batch-claim-and-spend"
	batch.Requests = append(batch.Requests, player.Request{Path: "/admin/spend"})
	batch.Digest = sha256.Sum256([]byte("claim then unaffordable spend"))
	future, err := runtime.Submit(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := future.Wait(ctx); err == nil {
		t.Fatal("batch with unaffordable spend committed its earlier mail claim")
	}
	gold, opened, histories := durableAssets(t, owner.repository)
	if gold != 0 || opened != 0 || histories != 0 {
		t.Fatalf("batch published a partial mail reward: %d/%d/%d", gold, opened, histories)
	}
	executeAsset(t, runtime, openMail("account", 3, 1))
	gold, opened, histories = durableAssets(t, owner.repository)
	if gold != 127 || opened != 1 || histories != 1 {
		t.Fatalf("failed batch lost the unclaimed mail: %d/%d/%d", gold, opened, histories)
	}
}

func TestRecoveryRejectsQueuedOldSessionMailClaimBeforeAnyAssetsExecute(t *testing.T) {
	runtime, owner := newMailActor(t, "account", filepath.Join(t.TempDir(), "state.db"), 149, player.Limits{})
	executeAsset(t, runtime, assetCommand("account", "gift", "/admin/compensation", []byte("recovery-gift")))
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var gated atomic.Bool
	owner.before = func(c player.Command) {
		if c.Requests[0].Path == "/MailOpen" && gated.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	owner.failClaim.Store(true)
	oldGeneration := runtime.Generation()
	failed, err := runtime.Submit(context.Background(), openMail("account", 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("claim did not enter executor")
	}
	queued, err := runtime.Submit(context.Background(), openMail("account", 3, 1))
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := failed.Wait(ctx); err == nil {
		t.Fatal("injected failed claim succeeded")
	}
	if _, err := queued.Wait(ctx); !errors.Is(err, player.ErrGenerationExpired) {
		t.Fatalf("old-generation queued claim was not explicitly rejected: %v", err)
	}
	gold, opened, histories := durableAssets(t, owner.repository)
	if gold != 0 || opened != 0 || histories != 0 {
		t.Fatalf("old-session queued command executed after recovery: %d/%d/%d", gold, opened, histories)
	}
	retry := openMail("account", 4, 1)
	retry.Identity.SessionID = "login-after-recovery"
	stale := retry
	stale.ExpectedGeneration = oldGeneration
	if _, err := runtime.Submit(ctx, stale); !errors.Is(err, player.ErrGenerationExpired) {
		t.Fatalf("stale authenticated generation was accepted during submit: %v", err)
	}
	retry.ExpectedGeneration = runtime.Generation()
	executeAsset(t, runtime, retry)
	gold, opened, histories = durableAssets(t, owner.repository)
	if gold != 149 || opened != 1 || histories != 1 {
		t.Fatalf("reconnected player lost claimable mail assets: %d/%d/%d", gold, opened, histories)
	}
}
