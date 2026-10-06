package accountstate

import (
	"path/filepath"
	"testing"
)

func TestInitializationMarkerMakesSchemaOnlyRollbackRetryable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := repository.InitializationState("mail", "wallet"); err != nil || state != InitializationPending {
		t.Fatalf("initial state=%d err=%v", state, err)
	}
	operation, err := repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("mail", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save("wallet", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkInitializationComplete(); err != nil {
		t.Fatal(err)
	}
	if err := operation.Rollback(); err == nil {
		t.Fatal("dirty rollback did not require repository reopen")
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	}()
	if repository.IsNew() {
		t.Fatal("schema-only retry incorrectly depends on file creation")
	}
	if state, err := repository.InitializationState("mail", "wallet"); err != nil || state != InitializationPending {
		t.Fatalf("post-rollback state=%d err=%v", state, err)
	}
}

func TestInitializationMarkerCommitsCompleteAccountExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := repository.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"mail", "wallet"} {
		if err := repository.Save(domain, []byte(`{"version":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.MarkInitializationComplete(); err != nil {
		t.Fatal(err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	}()
	if state, err := repository.InitializationState("mail", "wallet"); err != nil || state != InitializationComplete {
		t.Fatalf("committed state=%d err=%v", state, err)
	}
}

func TestInitializationStateRejectsPartialAccount(t *testing.T) {
	repository, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := repository.Save("mail", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if state, err := repository.InitializationState("mail", "wallet"); err == nil || state != InitializationCorrupt {
		t.Fatalf("partial state=%d err=%v", state, err)
	}
}
