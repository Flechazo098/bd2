package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

type migration struct {
	from int
	to   int
	up   func(context.Context, *sql.Tx) error
}

var schemaMigrations = []migration{
	{from: 1, to: 2, up: migrateV1ToV2},
}

// initialize creates schema v1 for a new database, applies every adjacent Go
// migration, validates the final schema and player state, and only then
// commits. A migration or validation error rolls the entire transaction back.
func initialize(ctx context.Context, db *sql.DB, fresh bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("accountstate: begin schema transaction: %w", err)
	}
	defer tx.Rollback()

	if fresh {
		if err := createV1(ctx, tx); err != nil {
			return err
		}
	}
	version, err := readSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version < 1 || version > schemaVersion {
		return fmt.Errorf("accountstate: unsupported schema version %d (current %d)", version, schemaVersion)
	}
	if err := validateSchemaTables(tx, version); err != nil {
		return err
	}
	startVersion := version
	if err := runMigrations(ctx, tx, version, schemaVersion, schemaMigrations); err != nil {
		return err
	}
	if err := validateSchemaTables(tx, schemaVersion); err != nil {
		return err
	}
	if startVersion < schemaVersion {
		problems, err := validateState(tx)
		if err != nil {
			return fmt.Errorf("accountstate: validate migrated state: %w", err)
		}
		if len(problems) != 0 {
			return validationError(problems)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("accountstate: commit schema migration: %w", err)
	}
	return nil
}

func createV1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE metadata (
			key TEXT PRIMARY KEY NOT NULL,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE domain_state (
			name TEXT PRIMARY KEY NOT NULL,
			payload BLOB NOT NULL,
			generation INTEGER NOT NULL CHECK (generation > 0)
		) WITHOUT ROWID`,
		`CREATE TABLE domain_entry (
			domain_name TEXT NOT NULL,
			bucket TEXT NOT NULL,
			entry_key TEXT NOT NULL,
			payload BLOB NOT NULL,
			PRIMARY KEY (domain_name, bucket, entry_key)
		) WITHOUT ROWID`,
		`INSERT INTO metadata(key, value) VALUES ('schema_version', '1')`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("accountstate: create schema v1: %w", err)
		}
	}
	return nil
}

func migrateV1ToV2(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(tx, "domain_entry")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(columns, func(column schemaColumn) bool { return column.name == "generation" }) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE domain_entry
		ADD COLUMN generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0)`)
	if err != nil {
		return fmt.Errorf("add domain entry generation: %w", err)
	}
	return nil
}

func runMigrations(ctx context.Context, tx *sql.Tx, from, target int, migrations []migration) error {
	current := from
	for current < target {
		var step *migration
		for i := range migrations {
			if migrations[i].from == current {
				if step != nil {
					return fmt.Errorf("accountstate: duplicate migration from version %d", current)
				}
				step = &migrations[i]
			}
		}
		if step == nil {
			return fmt.Errorf("accountstate: missing migration %d->%d", current, current+1)
		}
		if step.to != current+1 {
			return fmt.Errorf("accountstate: migration %d->%d is not adjacent", step.from, step.to)
		}
		if err := step.up(ctx, tx); err != nil {
			return fmt.Errorf("accountstate: migrate %d->%d: %w", step.from, step.to, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value = ? WHERE key = 'schema_version'`, strconv.Itoa(step.to)); err != nil {
			return fmt.Errorf("accountstate: record schema version %d: %w", step.to, err)
		}
		current = step.to
	}
	return nil
}

func readSchemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("accountstate: schema_version is missing")
	}
	if err != nil {
		return 0, fmt.Errorf("accountstate: read schema version: %w", err)
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("accountstate: invalid schema version %q", raw)
	}
	return version, nil
}

type schemaColumn struct {
	name, kind  string
	notNull, pk int
}

func validateSchemaTables(tx *sql.Tx, version int) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("accountstate: inspect schema tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	want := []string{"domain_entry", "domain_state", "metadata"}
	if !slices.Equal(names, want) {
		return fmt.Errorf("accountstate: schema v%d tables %v, want %v", version, names, want)
	}
	entryColumns := []schemaColumn{{"domain_name", "TEXT", 1, 1}, {"bucket", "TEXT", 1, 2}, {"entry_key", "TEXT", 1, 3}, {"payload", "BLOB", 1, 0}}
	if version >= 2 {
		entryColumns = append(entryColumns, schemaColumn{"generation", "INTEGER", 1, 0})
	}
	expected := map[string][]schemaColumn{
		"metadata":     {{"key", "TEXT", 1, 1}, {"value", "TEXT", 1, 0}},
		"domain_state": {{"name", "TEXT", 1, 1}, {"payload", "BLOB", 1, 0}, {"generation", "INTEGER", 1, 0}},
		"domain_entry": entryColumns,
	}
	for _, table := range want {
		actual, err := tableColumns(tx, table)
		if err != nil {
			return err
		}
		if !slices.Equal(actual, expected[table]) {
			return fmt.Errorf("accountstate: %s columns do not match schema version %d", table, version)
		}
	}
	return nil
}

func tableColumns(tx *sql.Tx, table string) ([]schemaColumn, error) {
	columns, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("accountstate: inspect %s columns: %w", table, err)
	}
	var actual []schemaColumn
	for columns.Next() {
		var ordinal int
		var entry schemaColumn
		var defaultValue any
		if err := columns.Scan(&ordinal, &entry.name, &entry.kind, &entry.notNull, &defaultValue, &entry.pk); err != nil {
			columns.Close()
			return nil, err
		}
		actual = append(actual, entry)
	}
	if err := columns.Close(); err != nil {
		return nil, err
	}
	return actual, nil
}
