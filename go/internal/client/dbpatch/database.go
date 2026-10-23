package dbpatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"bd2server/internal/common/clientpatch"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/dbcrypt"
	"bd2server/internal/common/util"

	_ "modernc.org/sqlite"
)

const maxDatabase = 128 << 20

type memoryDatabase interface {
	Deserialize([]byte) error
	Serialize() ([]byte, error)
}

type Result struct {
	Revision uint64
	Applied  []string
	Backup   string
}

func openDatabase(ctx context.Context, raw []byte) (*sql.DB, *sql.Conn, error) {
	plain, err := dbcrypt.DecryptPages(raw)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.HasPrefix(plain, dbcrypt.Header) {
		return nil, nil, errors.New("dbpatch: invalid encrypted ClientLocal database")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err == nil {
		err = conn.Raw(func(driver any) error {
			memory, ok := driver.(memoryDatabase)
			if !ok {
				return errors.New("dbpatch: SQLite serialization is unavailable")
			}
			return memory.Deserialize(plain)
		})
	}
	if err == nil {
		_, err = conn.ExecContext(ctx, "PRAGMA trusted_schema=OFF; PRAGMA temp_store=MEMORY; PRAGMA foreign_keys=ON")
	}
	if err == nil {
		err = integrity(ctx, conn)
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		_ = db.Close()
		return nil, nil, err
	}
	return db, conn, nil
}

func integrity(ctx context.Context, conn *sql.Conn) error {
	var result string
	if err := conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("dbpatch: SQLite integrity check: %s", result)
	}
	return nil
}

func serialize(conn *sql.Conn) ([]byte, error) {
	var plain []byte
	err := conn.Raw(func(driver any) error {
		var err error
		plain, err = driver.(memoryDatabase).Serialize()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(plain) > maxDatabase {
		return nil, errors.New("dbpatch: patched database exceeds size limit")
	}
	return dbcrypt.EncryptPages(plain)
}

func readDatabase(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDatabase {
		return nil, errors.New("dbpatch: database must be a bounded regular file")
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if entry, err := os.Stat(path + suffix); err == nil && entry.Size() > 0 {
			return nil, errors.New("dbpatch: database has an active SQLite journal; close the game before migrating")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return os.ReadFile(path)
}

func apply(ctx context.Context, path string, plan clientpatch.Plan, running func() (bool, error)) (Result, error) {
	result := Result{Revision: plan.Revision}
	if err := plan.Validate(); err != nil {
		return result, err
	}
	if err := stopped(running); err != nil {
		return result, err
	}
	lock, err := util.TryLockFile(path + ".patch-lock")
	if err != nil {
		return result, fmt.Errorf("dbpatch: another launcher is migrating this database: %w", err)
	}
	defer func() { _ = lock.Close() }()
	raw, err := readDatabase(path)
	if err != nil {
		return result, err
	}
	db, conn, err := openDatabase(ctx, raw)
	if err != nil {
		return result, err
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _bd2_patch_history (
		origin TEXT NOT NULL, id TEXT NOT NULL, owner_uid INTEGER NOT NULL,
		digest TEXT NOT NULL, PRIMARY KEY(origin,id,owner_uid));
		CREATE TABLE IF NOT EXISTS _bd2_patch_catalog (
		origin TEXT PRIMARY KEY, revision TEXT NOT NULL, digest TEXT NOT NULL)`); err != nil {
		return result, err
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return result, err
	}
	planDigest := digest(planBytes)
	var revisionText, oldDigest string
	err = tx.QueryRowContext(ctx, "SELECT revision,digest FROM _bd2_patch_catalog WHERE origin=?", plan.Origin).Scan(&revisionText, &oldDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var previous uint64
	if err == nil {
		if _, err := fmt.Sscan(revisionText, &previous); err != nil {
			return result, errors.New("dbpatch: invalid migration revision journal")
		}
		if previous > plan.Revision || previous == plan.Revision && oldDigest != planDigest {
			return result, errors.New("dbpatch: patch catalog rollback or revision reuse detected")
		}
	}
	tables, err := databaseTables(ctx, tx)
	if err != nil {
		return result, err
	}
	owners, err := databaseOwners(ctx, tx, tables)
	if err != nil {
		return result, err
	}
	for _, patch := range orderedPatches(plan.Patches) {
		if patch.OwnerUID != 0 && !owners[patch.OwnerUID] {
			continue
		}
		changed, err := applyPatch(ctx, tx, plan.Origin, patch, tables)
		if err != nil {
			return result, fmt.Errorf("dbpatch: migration %s (UID %d): %w", patch.ID, patch.OwnerUID, err)
		}
		if changed {
			result.Applied = append(result.Applied, patch.ID)
		}
	}
	if previous == plan.Revision && len(result.Applied) == 0 {
		return result, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO _bd2_patch_catalog(origin,revision,digest) VALUES(?,?,?) ON CONFLICT(origin) DO UPDATE SET revision=excluded.revision,digest=excluded.digest", plan.Origin, fmt.Sprint(plan.Revision), planDigest); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	if err := integrity(ctx, conn); err != nil {
		return result, err
	}
	updated, err := serialize(conn)
	if err != nil {
		return result, err
	}
	if err := stopped(running); err != nil {
		return result, err
	}
	current, err := readDatabase(path)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(current, raw) {
		return result, errors.New("dbpatch: game database changed during migration")
	}
	backup := filepath.Join(filepath.Dir(path), ".bd2-patch-backups", filepath.Base(path)+"-"+digest(raw)+".bak")
	if err := backupDatabase(backup, raw); err != nil {
		return result, err
	}
	saved, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(saved, raw) {
		return result, errors.New("dbpatch: backup verification failed")
	}
	if err := stopped(running); err != nil {
		return result, err
	}
	if err := commonconfig.WriteFile(path, updated); err != nil {
		return result, err
	}
	result.Backup = backup
	return result, nil
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func backupDatabase(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".backup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	if err = staged.Chmod(0o600); err == nil {
		_, err = staged.Write(raw)
	}
	if err == nil {
		err = staged.Sync()
	}
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(staged.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func orderedPatches(patches []clientpatch.Patch) []clientpatch.Patch {
	byID := make(map[string]clientpatch.Patch, len(patches))
	for _, p := range patches {
		byID[p.ID] = p
	}
	seen := map[string]bool{}
	var ordered []clientpatch.Patch
	var visit func(clientpatch.Patch)
	visit = func(p clientpatch.Patch) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		for _, id := range p.Requires {
			visit(byID[id])
		}
		ordered = append(ordered, p)
	}
	for _, p := range patches {
		visit(p)
	}
	return ordered
}

func stopped(running func() (bool, error)) error {
	if running == nil {
		return errors.New("dbpatch: game process check is required")
	}
	active, err := running()
	if err != nil {
		return err
	}
	if active {
		return errors.New("dbpatch: close the game before applying local database migrations")
	}
	return nil
}

func applyPatch(ctx context.Context, tx *sql.Tx, origin string, patch clientpatch.Patch, tables []localTable) (bool, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return false, err
	}
	checksum := digest(raw)
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM _bd2_patch_history WHERE origin=? AND id=? AND owner_uid=?", origin, patch.ID, patch.OwnerUID).Scan(&existing)
	if err == nil {
		if existing != checksum {
			return false, errors.New("applied migration ID was reused with different content")
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	for _, id := range patch.Requires {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM _bd2_patch_history WHERE origin=? AND id=? AND (owner_uid=0 OR owner_uid=?)", origin, id, patch.OwnerUID).Scan(&count); err != nil {
			return false, err
		}
		if count == 0 {
			return false, fmt.Errorf("required migration %s is missing", id)
		}
	}
	if err := assertions(ctx, tx, patch.Before, patch.OwnerUID); err != nil {
		return false, fmt.Errorf("precondition: %w", err)
	}
	var untouched string
	if patch.OwnerUID != 0 {
		untouched, err = otherOwnersDigest(ctx, tx, tables, patch.OwnerUID)
		if err != nil {
			return false, err
		}
	}
	for index, statement := range patch.Statements {
		if err := validateSQL(statement.SQL, false); err != nil {
			return false, fmt.Errorf("statement %d: %w", index+1, err)
		}
		args, err := parameters(statement.Args, patch.OwnerUID)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, statement.SQL, args...); err != nil {
			return false, fmt.Errorf("statement %d: %w", index+1, err)
		}
	}
	if err := assertions(ctx, tx, patch.After, patch.OwnerUID); err != nil {
		return false, fmt.Errorf("postcondition: %w", err)
	}
	if patch.OwnerUID != 0 {
		actual, err := otherOwnersDigest(ctx, tx, tables, patch.OwnerUID)
		if err != nil {
			return false, err
		}
		if actual != untouched {
			return false, errors.New("scoped migration changed another UID or an unscoped table")
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO _bd2_patch_history(origin,id,owner_uid,digest) VALUES(?,?,?,?)", origin, patch.ID, patch.OwnerUID, checksum)
	return err == nil, err
}

func assertions(ctx context.Context, tx *sql.Tx, checks []clientpatch.Assertion, owner int64) error {
	for index, check := range checks {
		if err := validateSQL(check.SQL, true); err != nil {
			return err
		}
		args, err := parameters(check.Args, owner)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, check.SQL, args...)
		if err != nil {
			return fmt.Errorf("check %d: %w", index+1, err)
		}
		var actual int64
		err = func() error {
			defer func() { _ = rows.Close() }()
			columns, err := rows.Columns()
			if err != nil {
				return err
			}
			if len(columns) != 1 || !rows.Next() {
				return errors.New("assertion must return one integer row")
			}
			if err := rows.Scan(&actual); err != nil {
				return err
			}
			if rows.Next() {
				return errors.New("assertion returned multiple rows")
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if actual != check.Equals {
				return fmt.Errorf("check %d expected %d, got %d", index+1, check.Equals, actual)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

type localTable struct {
	name  string
	owner bool
}

func quoteIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func databaseTables(ctx context.Context, tx *sql.Tx) ([]localTable, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_bd2_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []localTable
	for rows.Next() {
		var table localTable
		if err := rows.Scan(&table.name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, table)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range tables {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info(?) WHERE lower(name)='ownerindex'", tables[i].name).Scan(&count); err != nil {
			return nil, err
		}
		tables[i].owner = count != 0
	}
	return tables, nil
}

func databaseOwners(ctx context.Context, tx *sql.Tx, tables []localTable) (map[int64]bool, error) {
	owners := map[int64]bool{}
	for _, table := range tables {
		if !table.owner {
			continue
		}
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT ownerIndex FROM "+quoteIdentifier(table.name)+" WHERE ownerIndex > 0")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var owner float64
			if err := rows.Scan(&owner); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if float64(int64(owner)) != owner {
				_ = rows.Close()
				return nil, errors.New("dbpatch: invalid owner UID")
			}
			owners[int64(owner)] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return owners, nil
}

func otherOwnersDigest(ctx context.Context, tx *sql.Tx, tables []localTable, owner int64) (string, error) {
	hash := sha256.New()
	for _, table := range tables {
		query := "SELECT * FROM " + quoteIdentifier(table.name)
		var args []any
		if table.owner {
			query += " WHERE ownerIndex IS NULL OR ownerIndex != ?"
			args = append(args, owner)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return "", err
		}
		var records []string
		err = func() error {
			defer func() { _ = rows.Close() }()
			columns, err := rows.Columns()
			if err != nil {
				return err
			}
			for rows.Next() {
				values := make([]any, len(columns))
				addresses := make([]any, len(columns))
				for i := range values {
					addresses[i] = &values[i]
				}
				if err := rows.Scan(addresses...); err != nil {
					return err
				}
				raw, err := json.Marshal(values)
				if err != nil {
					return err
				}
				records = append(records, string(raw))
			}
			return rows.Err()
		}()
		if err != nil {
			return "", err
		}
		slices.Sort(records)
		raw, err := json.Marshal([]any{table.name, records})
		if err != nil {
			return "", err
		}
		_, _ = hash.Write(raw)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
