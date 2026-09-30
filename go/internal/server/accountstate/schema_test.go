package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func createV1Database(t *testing.T, path string, domain, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := createV1(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if domain != "" {
		if _, err := tx.Exec(`INSERT INTO domain_state(name, payload, generation) VALUES (?, ?, 1)`, domain, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFreshDatabaseRunsEveryMigration(t *testing.T) {
	r, _ := openTestRepository(t)
	version, err := r.SchemaVersion()
	if err != nil || version != schemaVersion {
		t.Fatalf("schema version %d, error %v", version, err)
	}
	var entryTables int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'domain_entry'`).Scan(&entryTables); err != nil || entryTables != 1 {
		t.Fatalf("domain_entry tables %d, error %v", entryTables, err)
	}
}

func TestMigrationV1ToV2IsRepeatSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createV1Database(t, path, "", "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for range 2 {
		if err := migrateV1ToV2(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateSchemaTables(tx, 2); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createV1Database(t, path, "", "")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	version, err := r.SchemaVersion()
	if err != nil || version != 2 {
		t.Fatalf("schema version %d, error %v", version, err)
	}
}

func TestMigrationsRejectMissingAndNonAdjacentSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createV1Database(t, path, "", "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, test := range []struct {
		name  string
		steps []migration
		want  string
	}{
		{name: "missing", want: "missing migration 1->2"},
		{name: "skip", steps: []migration{{from: 1, to: 3, up: func(context.Context, *sql.Tx) error { return nil }}}, want: "not adjacent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = runMigrations(context.Background(), tx, 1, 3, test.steps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidationFailureRollsBackMigrationAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createV1Database(t, path, "progress", `{"quests":{"0:1":{"QuestID":1,"PackID":0}},"cleared_quests":{}}`)
	if r, err := Open(path); err == nil {
		r.Close()
		t.Fatal("opened state rejected by final validation")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&version); err != nil || version != "1" {
		t.Fatalf("schema version %q after rollback: %v", version, err)
	}
	var generationColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('domain_entry') WHERE name = 'generation'`).Scan(&generationColumns); err != nil || generationColumns != 0 {
		t.Fatalf("migration column survived rollback: %d, %v", generationColumns, err)
	}
}

func TestMigrationFailureRollsBackEarlierStepWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	createV1Database(t, path, "", "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	steps := []migration{{from: 1, to: 2, up: func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE partial_write(value INTEGER)`); err != nil {
			return err
		}
		return errors.New("injected migration failure")
	}}}
	if err := runMigrations(context.Background(), tx, 1, 2, steps); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'partial_write'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("partial migration survived rollback: %d, %v", tables, err)
	}
}
