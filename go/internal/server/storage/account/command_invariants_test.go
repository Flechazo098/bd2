package accountstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	assets "bd2server/internal/server/domain/inventory"
	"bd2server/internal/server/storage/stateio"
)

func openTestAccount(t *testing.T, path string) *Repository {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}
func beginTestCommand(t *testing.T, r *Repository) (*CommandStore, command.Context) {
	t.Helper()
	tx, err := r.BeginCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil {
			t.Error(err)
		}
	})
	return tx, command.Context{Identity: command.Identity{AccountID: "account-under-test", SessionID: "authenticated-login"}, State: tx}
}
func openTestWallet(t *testing.T, ctx command.Context, initial uint64) *assets.Wallet {
	t.Helper()
	w, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{Gold: initial})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Credits and debits form one command; a discarded candidate may never charge
// the account, issue a reward, or poison another account's independent command.
func TestAbortedAssetCommandDoesNotJoinAnotherAccount(t *testing.T) {
	dir := t.TempDir()
	a := openTestAccount(t, filepath.Join(dir, "a", "state.db"))
	b := openTestAccount(t, filepath.Join(dir, "b", "state.db"))
	txA, ctxA := beginTestCommand(t, a)
	wa := openTestWallet(t, ctxA, 100)
	if err := wa.EnsurePersisted(ctxA); err != nil {
		t.Fatal(err)
	}
	if _, err := wa.GrantQuestOnce(ctxA, "reward", []gamedata.Reward{{Type: 4, Count: 240}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wa.SpendGoldOnce(ctxA, "cost", 35); err != nil {
		t.Fatal(err)
	}
	txB, ctxB := beginTestCommand(t, b)
	wb := openTestWallet(t, ctxB, 1000)
	if err := wb.EnsurePersisted(ctxB); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.GrantQuestOnce(ctxB, "reward", []gamedata.Reward{{Type: 4, Count: 17}}); err != nil {
		t.Fatal(err)
	}
	if err := txA.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := a.Check(); err != nil {
		t.Fatalf("a normal rollback must permit account recovery: %v", err)
	}
	// An escaped capability remains bound to the discarded transaction even while
	// another account is still writing. Its reward cannot enter that transaction.
	stale := make(chan error, 1)
	go func() {
		_, err := wa.GrantQuestOnce(ctxA, "escaped", []gamedata.Reward{{Type: 4, Count: 9999}})
		stale <- err
	}()
	if err := <-stale; !errors.Is(err, ErrClosed) {
		t.Fatalf("escaped write: %v", err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
	checkA, readA := beginTestCommand(t, a)
	recoveredA := openTestWallet(t, readA, 100)
	if got := recoveredA.Snapshot(readA).Gold; got != 100 {
		t.Fatalf("rollback violated asset conservation: %d", got)
	}
	if recoveredA.WasGranted("reward") || recoveredA.WasSpent("cost") {
		t.Fatal("rollback left entitlement markers")
	}
	if err := checkA.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, readB := beginTestCommand(t, b)
	recoveredB := openTestWallet(t, readB, 0)
	if got := recoveredB.Snapshot(readB).Gold; got != 1000+17 {
		t.Fatalf("account isolation violated conservation: %d", got)
	}
	if recoveredB.WasGranted("escaped") {
		t.Fatal("escaped account a reward reached account b")
	}
}

// The helper exits without deferred cleanup to exercise real WAL recovery,
// both before the commit point and after commit but before a response exists.
func TestSQLiteCrashKeepsWholeAssetCommand(t *testing.T) {
	if path := os.Getenv("BD2_COMMAND_CRASH_DATABASE"); path != "" {
		r, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := r.BeginCommand(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := command.Context{State: tx}
		w, err := assets.OpenWallet(ctx, stateio.RootStore{}, assets.Currency{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.GrantQuestOnce(ctx, "crash-reward", []gamedata.Reward{{Type: 4, Count: 120}}); err != nil {
			t.Fatal(err)
		}
		if _, err = w.SpendGoldOnce(ctx, "crash-cost", 35); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("BD2_COMMAND_CRASH_COMMIT") == "yes" {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(0)
	}
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_commit"}[commit], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			tx, ctx := beginTestCommand(t, r)
			w := openTestWallet(t, ctx, 70)
			if err := w.EnsurePersisted(ctx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			helper := exec.Command(os.Args[0], "-test.run=^TestSQLiteCrashKeepsWholeAssetCommand$")
			helper.Env = append(os.Environ(), "BD2_COMMAND_CRASH_DATABASE="+path, "BD2_COMMAND_CRASH_COMMIT="+map[bool]string{false: "no", true: "yes"}[commit])
			if output, err := helper.CombinedOutput(); err != nil {
				t.Fatalf("crash helper: %v\n%s", err, output)
			}
			recovered := openTestAccount(t, path)
			verify, read := beginTestCommand(t, recovered)
			rw := openTestWallet(t, read, 0)
			expected := uint64(70)
			if commit {
				expected += 120 - 35
			}
			if got := rw.Snapshot(read).Gold; got != expected {
				t.Fatalf("crash broke conservation: got %d want %d", got, expected)
			}
			if rw.WasGranted("crash-reward") != commit || rw.WasSpent("crash-cost") != commit {
				t.Fatal("core and idempotency entries recovered different commands")
			}
			if commit {
				if _, err := rw.GrantQuestOnce(read, "crash-reward", []gamedata.Reward{{Type: 4, Count: 120}}); err != nil {
					t.Fatal(err)
				}
				if _, err := rw.SpendGoldOnce(read, "crash-cost", 35); err != nil {
					t.Fatal(err)
				}
				if got := rw.Snapshot(read).Gold; got != expected {
					t.Fatalf("lost response retry applied assets twice: %d", got)
				}
			}
			if err := verify.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDatabaseWriterOwnershipAndFailedStorageFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	r := openTestAccount(t, path)
	if other, err := Open(path); !errors.Is(err, ErrWriterLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("overlapping actor database: %v", err)
	}
	tx, ctx := beginTestCommand(t, r)
	w := openTestWallet(t, ctx, 23)
	if err := w.EnsurePersisted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := r.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec("PRAGMA max_page_count = " + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	failed, _ := beginTestCommand(t, r)
	if err := failed.Save("large-write", make([]byte, 1<<20)); err == nil {
		t.Fatal("SQLite capacity fault was not reached")
	}
	if err := r.Check(); !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatalf("disk storage failure was not fenced: %v", err)
	}
	if err := failed.Commit(); err == nil {
		t.Fatal("failed SQL command was committed")
	}
	if _, err := r.BeginCommand(context.Background()); !errors.Is(err, stateio.ErrStateRecoveryRequired) {
		t.Fatalf("failed database accepted another writer: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestAccount(t, path)
	_, read := beginTestCommand(t, reopened)
	recovered := openTestWallet(t, read, 0)
	if got := recovered.Snapshot(read).Gold; got != 23 {
		t.Fatalf("failed disk write lost previously committed assets: %d", got)
	}
}

func TestRejectedRewardLedgerCannotCommitPartialAssets(t *testing.T) {
	r := openTestAccount(t, filepath.Join(t.TempDir(), "state.db"))
	initial, ctx := beginTestCommand(t, r)
	wallet := openTestWallet(t, ctx, 50)
	if err := wallet.EnsurePersisted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initial.Commit(); err != nil {
		t.Fatal(err)
	}
	// Fail after SaveWithEntries updates the balance but before the issuance
	// marker. Even a caller attempting Commit must not publish half a reward.
	if _, err := r.db.Exec(`CREATE TRIGGER reject_reward BEFORE INSERT ON domain_entry
 WHEN NEW.domain_name='wallet' AND NEW.bucket='granted'
 BEGIN SELECT RAISE(ABORT,'reject reward issuance'); END`); err != nil {
		t.Fatal(err)
	}
	failed, attempt := beginTestCommand(t, r)
	candidate := openTestWallet(t, attempt, 0)
	if _, err := candidate.GrantQuestOnce(attempt, "rejected", []gamedata.Reward{{Type: 4, Count: 31}}); err == nil {
		t.Fatal("issuance fault was not reached")
	}
	if err := failed.Commit(); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("partial command accepted commit: %v", err)
	}
	if err := r.Check(); err != nil {
		t.Fatalf("deterministic rejected command fenced database: %v", err)
	}
	if _, err := r.db.Exec(`DROP TRIGGER reject_reward`); err != nil {
		t.Fatal(err)
	}
	verify, read := beginTestCommand(t, r)
	recovered := openTestWallet(t, read, 0)
	if recovered.WasGranted("rejected") || recovered.Snapshot(read).Gold != 50 {
		t.Fatal("failed issuance published a balance or entitlement marker")
	}
	if _, err := recovered.GrantQuestOnce(read, "rejected", []gamedata.Reward{{Type: 4, Count: 31}}); err != nil {
		t.Fatal(err)
	}
	if err := verify.Commit(); err != nil {
		t.Fatal(err)
	}
	_, confirmed := beginTestCommand(t, r)
	durable := openTestWallet(t, confirmed, 0)
	if durable.Snapshot(confirmed).Gold != 50+31 || !durable.WasGranted("rejected") {
		t.Fatal("account failed to recover and issue exactly one complete reward")
	}
}
func TestFirstRejectedWalletWritePreservesCommittedAssets(t *testing.T) {
	r := openTestAccount(t, filepath.Join(t.TempDir(), "state.db"))
	initial, ctx := beginTestCommand(t, r)
	wallet := openTestWallet(t, ctx, 73)
	if err := wallet.EnsurePersisted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initial.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`CREATE TRIGGER reject_first_wallet BEFORE UPDATE OF payload ON domain_state
 WHEN NEW.name='wallet' BEGIN SELECT RAISE(ABORT,'reject first wallet write'); END`); err != nil {
		t.Fatal(err)
	}
	rejected, attempt := beginTestCommand(t, r)
	candidate := openTestWallet(t, attempt, 0)
	if _, err := candidate.SpendGoldOnce(attempt, "first-charge", 29); err == nil {
		t.Fatal("first wallet write was not rejected")
	}
	if err := rejected.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`DROP TRIGGER reject_first_wallet`); err != nil {
		t.Fatal(err)
	}
	retry, retryCtx := beginTestCommand(t, r)
	recovered := openTestWallet(t, retryCtx, 0)
	if recovered.Snapshot(retryCtx).Gold != 73 || recovered.WasSpent("first-charge") {
		t.Fatal("rejected first write changed committed assets or retained its charge")
	}
	if _, err := recovered.SpendGoldOnce(retryCtx, "first-charge", 29); err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.GrantQuestOnce(retryCtx, "followup-reward", []gamedata.Reward{{Type: 4, Count: 11}}); err != nil {
		t.Fatal(err)
	}
	if err := retry.Commit(); err != nil {
		t.Fatal(err)
	}
	_, verify := beginTestCommand(t, r)
	durable := openTestWallet(t, verify, 0)
	if durable.Snapshot(verify).Gold != 73-29+11 || !durable.WasSpent("first-charge") || !durable.WasGranted("followup-reward") {
		t.Fatal("recovering a rejected first charge violated asset conservation")
	}
}
