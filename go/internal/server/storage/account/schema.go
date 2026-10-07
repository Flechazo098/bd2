package accountstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// initialize creates only the current schema. Unreleased older formats require
// an explicit offline repair; opening a database never migrates player data.
func initialize(ctx context.Context, db *sql.DB, fresh bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("accountstate: begin schema transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if fresh {
		if err := createCurrent(ctx, tx); err != nil {
			return err
		}
	}
	version, err := readSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version != schemaVersion {
		return fmt.Errorf("accountstate: unsupported schema version %d (current %d); explicit repair required", version, schemaVersion)
	}
	if err := validateSchemaTables(tx, schemaVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("accountstate: commit current schema: %w", err)
	}
	return nil
}

func createCurrent(ctx context.Context, tx *sql.Tx) error {
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
			generation INTEGER NOT NULL CHECK (generation > 0),
			PRIMARY KEY (domain_name, bucket, entry_key)
		) WITHOUT ROWID`,
		`INSERT INTO metadata(key, value) VALUES ('schema_version', '3'), ('writer_epoch', '0')`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("accountstate: create current schema: %w", err)
		}
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
			_ = rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	want := []string{"domain_entry", "domain_state", "metadata"}
	if !slices.Equal(names, want) {
		return fmt.Errorf("accountstate: schema v%d tables %v, want %v", version, names, want)
	}
	entryColumns := []schemaColumn{{"domain_name", "TEXT", 1, 1}, {"bucket", "TEXT", 1, 2}, {"entry_key", "TEXT", 1, 3}, {"payload", "BLOB", 1, 0}}
	entryColumns = append(entryColumns, schemaColumn{"generation", "INTEGER", 1, 0})
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
			_ = columns.Close()
			return nil, err
		}
		actual = append(actual, entry)
	}
	if err := columns.Err(); err != nil {
		_ = columns.Close()
		return nil, err
	}
	if err := columns.Close(); err != nil {
		return nil, err
	}
	return actual, nil
}
