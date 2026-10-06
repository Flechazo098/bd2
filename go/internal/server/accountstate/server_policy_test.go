package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestStartingPackPolicyPersistsAndRejectsChanges(t *testing.T) {
	r, path := openTestRepository(t)
	if _, err := r.LockStartingPack(21, true); err == nil {
		t.Fatal("accepted policy outside startup operation")
	}
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.LockStartingPack(21, true); err != nil || got != 21 {
		t.Fatalf("lock=%d: %v", got, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	op, err = r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.LockStartingPack(21, false); err != nil || got != 21 {
		t.Fatalf("reopened lock=%d: %v", got, err)
	}
	if _, err := r.LockStartingPack(1, true); err == nil {
		t.Fatal("accepted changed policy")
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := r.db.QueryRow(`SELECT value FROM metadata WHERE key=?`, startingPackMetadataKey).Scan(&raw); err != nil || raw != "21" {
		t.Fatalf("policy=%q: %v", raw, err)
	}
}

func TestStartingPackPolicyRollbackAllowsAnotherChoice(t *testing.T) {
	r, path := openTestRepository(t)
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.LockStartingPack(21, true); err != nil {
		t.Fatal(err)
	}
	if err := op.Rollback(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := r.db.QueryRow(`SELECT value FROM metadata WHERE key=?`, startingPackMetadataKey).Scan(&raw); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled back policy survived: %q, %v", raw, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	op, err = r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.LockStartingPack(1, true); err != nil || got != 1 {
		t.Fatalf("retry lock=%d: %v", got, err)
	}
	if err := op.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestStartingPackPolicyMissingExistingRejectsInitialization(t *testing.T) {
	r, _ := openTestRepository(t)
	if _, err := r.SaveContext(context.Background(), "progress", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = op.Rollback() }()
	for _, initialize := range []bool{false, true} {
		if _, err := r.LockStartingPack(1, initialize); err == nil {
			t.Fatalf("accepted missing existing policy initialize=%v", initialize)
		}
	}
}

func TestStartingPackPolicyRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"0", "2", "abc", "01", "21 "} {
		t.Run(raw, func(t *testing.T) {
			r, _ := openTestRepository(t)
			if _, err := r.db.Exec(`INSERT INTO metadata(key,value) VALUES(?,?)`, startingPackMetadataKey, raw); err != nil {
				t.Fatal(err)
			}
			op, err := r.BeginOperation()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = op.Rollback() }()
			if _, err := r.LockStartingPack(1, false); err == nil {
				t.Fatal("accepted invalid stored value")
			}
		})
	}
	r, _ := openTestRepository(t)
	op, err := r.BeginOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = op.Rollback() }()
	for _, configured := range []int{0, 2, -1} {
		if _, err := r.LockStartingPack(configured, true); err == nil {
			t.Fatalf("accepted invalid configured value %d", configured)
		}
	}
	if _, err := r.LockStartingPack(1, false); err == nil {
		t.Fatal("initialized empty DB without authorization")
	}
}
