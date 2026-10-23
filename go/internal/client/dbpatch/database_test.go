package dbpatch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bd2server/internal/common/clientpatch"
	"bd2server/internal/common/release"
)

// This schema and owner/instance distinction come from the native
// ClientLocalPresetDTO and ClientLocalInfo.AddPresetItem/AlterRecallPresetPinData.
// Old instance IDs must be replaced before native cleanup removes the pin.
func presetDatabase(t *testing.T) string {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(context.Background(), `PRAGMA page_size=4096;
	CREATE TABLE Preset(ownerIndex REAL,saveId INTEGER,charInvenIndex BIGINT,saveOrder INTEGER,
	packId INTEGER,contentsType INTEGER,saveIndex INTEGER NOT NULL DEFAULT 0,
	typeOrder INTEGER NOT NULL DEFAULT 0,classType INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(ownerIndex,saveId,charInvenIndex));
	INSERT INTO Preset VALUES(30738202,1,920000076,0,0,0,0,0,0),
	(30738203,1,920000076,0,0,0,0,0,0);
	CREATE TABLE LocalOptions(name TEXT PRIMARY KEY,value INTEGER);
	INSERT INTO LocalOptions VALUES('volume',50)`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := serialize(conn)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ClientLocal.encrypted")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func migrationPlan() clientpatch.Plan {
	return clientpatch.Plan{Schema: 1, Origin: "https://example.com", GameVersion: "2.36.7", Database: clientpatch.Database, Revision: 1,
		Patches: []clientpatch.Patch{{ID: "global-instance-ids-uid-30738202", OwnerUID: 30738202,
			Before:     []clientpatch.Assertion{{SQL: "SELECT count(*) FROM Preset WHERE ownerIndex=:owner AND charInvenIndex=:old", Args: map[string]any{"old": int64(920000076)}, Equals: 1}},
			Statements: []clientpatch.Statement{{SQL: "UPDATE Preset SET charInvenIndex=:new WHERE ownerIndex=:owner AND charInvenIndex=:old", Args: map[string]any{"new": int64(4294968766), "old": int64(920000076)}}},
			After:      []clientpatch.Assertion{{SQL: "SELECT count(*) FROM Preset WHERE ownerIndex=:owner AND charInvenIndex=:new", Args: map[string]any{"new": int64(4294968766)}, Equals: 1}},
		}},
	}
}

func notRunning() (bool, error) { return false, nil }

func inspectPresets(t *testing.T, path string) map[int64]int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, conn, err := openDatabase(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	rows, err := conn.QueryContext(context.Background(), "SELECT CAST(ownerIndex AS INTEGER),charInvenIndex FROM Preset")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	result := map[int64]int64{}
	for rows.Next() {
		var owner, id int64
		if err := rows.Scan(&owner, &id); err != nil {
			t.Fatal(err)
		}
		result[owner] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEncryptedPresetMigrationSurvivesReopenAndIsIdempotent(t *testing.T) {
	path := presetDatabase(t)
	original, _ := os.ReadFile(path)
	plan := migrationPlan()
	result, err := apply(context.Background(), path, plan, notRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || result.Backup == "" {
		t.Fatalf("missing migration receipt or backup: %+v", result)
	}
	if backup, err := os.ReadFile(result.Backup); err != nil || !bytes.Equal(backup, original) {
		t.Fatal("backup did not preserve the original encrypted database", err)
	}
	actual := inspectPresets(t, path)
	if actual[30738202] != 4294968766 || actual[30738203] != 920000076 {
		t.Fatalf("owner-specific native quickslot references: %v", actual)
	}
	first, _ := os.ReadFile(path)
	result, err = apply(context.Background(), path, plan, notRunning)
	if err != nil || len(result.Applied) != 0 || result.Backup != "" {
		t.Fatalf("repeat reapplied a migration: %+v %v", result, err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent launch rewrote database")
	}
}

func TestFailedSQLOrPostconditionLeavesOriginalDatabaseUnchanged(t *testing.T) {
	for _, failure := range []string{"sql", "postcondition", "owner scope", "unscoped table"} {
		t.Run(failure, func(t *testing.T) {
			path := presetDatabase(t)
			original, _ := os.ReadFile(path)
			plan := migrationPlan()
			switch failure {
			case "sql":
				plan.Patches[0].Statements = append(plan.Patches[0].Statements, clientpatch.Statement{SQL: "UPDATE Missing SET value=1"})
			case "postcondition":
				plan.Patches[0].After[0].Equals = 2
			case "owner scope":
				plan.Patches[0].Statements[0].SQL = "UPDATE Preset SET charInvenIndex=:new WHERE charInvenIndex=:old"
			case "unscoped table":
				plan.Patches[0].Statements = append(plan.Patches[0].Statements, clientpatch.Statement{SQL: "UPDATE LocalOptions SET value=0"})
			}
			if _, err := apply(context.Background(), path, plan, notRunning); err == nil {
				t.Fatal("invalid migration succeeded")
			}
			actual, _ := os.ReadFile(path)
			if !bytes.Equal(original, actual) {
				t.Fatal("failed migration changed encrypted database")
			}
		})
	}
}

func TestDatabaseStructureMismatchAndRunningGamePreventMigration(t *testing.T) {
	for _, scenario := range []string{"schema", "running", "starts before replacement"} {
		t.Run(scenario, func(t *testing.T) {
			path := presetDatabase(t)
			original, _ := os.ReadFile(path)
			plan := migrationPlan()
			calls := 0
			running := func() (bool, error) {
				calls++
				return scenario == "running" || scenario == "starts before replacement" && calls >= 2, nil
			}
			if scenario == "schema" {
				plan.Patches[0].Before[0].SQL = "SELECT absent_column FROM Preset LIMIT 1"
			}
			if _, err := apply(context.Background(), path, plan, running); err == nil {
				t.Fatal("unsafe migration accepted")
			}
			actual, _ := os.ReadFile(path)
			if !bytes.Equal(original, actual) {
				t.Fatal("precondition/process rejection changed database")
			}
		})
	}
}

func TestMigrationJournalRejectsRollbackAndReusedIDs(t *testing.T) {
	path := presetDatabase(t)
	plan := migrationPlan()
	plan.Revision = 2
	if _, err := apply(context.Background(), path, plan, notRunning); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"rollback", "revision reuse", "ID reuse"} {
		t.Run(scenario, func(t *testing.T) {
			changed := migrationPlan()
			changed.Revision = 2
			switch scenario {
			case "rollback":
				changed.Revision = 1
			case "revision reuse":
				changed.Patches[0].After[0].Equals = 0
			case "ID reuse":
				changed.Revision = 3
				changed.Patches[0].After[0].Equals = 0
			}
			before, _ := os.ReadFile(path)
			if _, err := apply(context.Background(), path, changed, notRunning); err == nil {
				t.Fatal("migration journal accepted conflicting publication")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected catalog changed database")
			}
		})
	}
}

func TestSQLRestrictionUnderstandsQuotedContentAndRejectsFileAndBatchOperations(t *testing.T) {
	for _, source := range []string{
		"UPDATE Preset SET charInvenIndex=1; ATTACH DATABASE '/tmp/other.db' AS ext",
		"UPDATE Preset SET charInvenIndex=1; DELETE FROM Preset",
		"UPDATE [Preset] SET saveId=load_extension('unsafe')",
		"UPDATE Preset SET saveId=(SELECT writefile('/tmp/a','b'))",
		"UPDATE \"_bd2_patch_history\" SET digest='forged'",
		"UPDATE Preset SET saveId=(SELECT * FROM pragma_user_version)",
		"PRAGMA writable_schema=ON", "DROP TABLE Preset", "ATTACH ':memory:' AS other",
		"UPDATE Preset SET saveId=(SELECT unknown_file_function('file'))",
	} {
		if err := validateSQL(source, false); err == nil {
			t.Errorf("accepted %s", source)
		}
	}
	if err := validateSQL("UPDATE LocalOptions SET name='DROP; ATTACH ''x''' /* ; */ WHERE name=:name;", false); err != nil {
		t.Fatal("SQL literal/comment incorrectly treated as executable", err)
	}
}

func TestSQLRestrictionAcceptsKeywordsBeforeParenthesesButStillChecksFunctions(t *testing.T) {
	for _, c := range []struct {
		source    string
		assertion bool
	}{
		{"SELECT count(*) FROM (SELECT ownerIndex, charInvenIndex FROM Preset LIMIT 0)", true},
		{"SELECT count(*) FROM Preset p JOIN (SELECT ownerIndex FROM PresetTalent) t USING (ownerIndex)", true},
		{"SELECT count(*) FROM json_each(:decks)", true},
		{"DELETE FROM Preset WHERE ownerIndex IN (SELECT ownerIndex FROM PresetTalent)", false},
		{"INSERT INTO Preset(ownerIndex) VALUES (1)", false},
		{"UPDATE Preset SET charInvenIndex=1 WHERE NOT EXISTS (SELECT 1 FROM PresetTalent) AND (ownerIndex=1 OR ownerIndex=2)", false},
	} {
		if err := validateSQL(c.source, c.assertion); err != nil {
			t.Errorf("rejected %s: %v", c.source, err)
		}
	}
	for _, c := range []struct{ source, rejected string }{
		{"SELECT count(*) FROM (SELECT unknown_file_function('file'))", `"unknown_file_function"`},
		{"SELECT count(*) FROM unknown_table_function(1)", `"unknown_table_function"`},
		{"SELECT count(*) FROM Preset WHERE ownerIndex IN (SELECT y FROM (SELECT nested_function(1)))", `"nested_function"`},
	} {
		if err := validateSQL(c.source, true); err == nil || !strings.Contains(err.Error(), c.rejected) {
			t.Errorf("%s: got %v, want rejection of %s", c.source, err, c.rejected)
		}
	}
}

func TestLaunchFetchVerifiesBeforeDatabaseWriteAndFailsClosedAfterEnrollment(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := []release.TrustedKey{{ID: release.KeyID(public), PublicKey: base64.StdEncoding.EncodeToString(public)}}
	path := presetDatabase(t)
	plan := migrationPlan()
	var signed []byte
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "plan targets game 2.36.6", status)
			return
		}
		var request struct {
			RecipientKey string `json:"recipient_key"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/client/patches" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		key, err := base64.StdEncoding.DecodeString(request.RecipientKey)
		if err != nil {
			t.Error(err)
			return
		}
		file, err := clientpatch.Seal(signed, key)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = w.Write(file)
	}))
	defer server.Close()
	plan.Origin = server.URL
	sign := func() {
		payload, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		signed, err = json.Marshal(clientpatch.Envelope{KeyID: keys[0].ID, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, append([]byte(clientpatch.SignatureDomain), payload...))), Plan: payload})
		if err != nil {
			t.Fatal(err)
		}
	}
	sign()
	options := Options{Origin: server.URL, GameVersion: "2.36.7", Client: server.Client(), Running: notRunning}
	original, _ := os.ReadFile(path)
	signed = bytes.Replace(signed, []byte(`"revision":1`), []byte(`"revision":2`), 1)
	if _, err := run(context.Background(), path, options, keys); err == nil {
		t.Fatal("tampered package reached database execution")
	}
	actual, _ := os.ReadFile(path)
	if !bytes.Equal(original, actual) {
		t.Fatal("tampered download changed local database")
	}
	sign()
	if _, err := run(context.Background(), path, options, keys); err != nil {
		t.Fatal(err)
	}
	if got := inspectPresets(t, path)[30738202]; got != 4294968766 {
		t.Fatalf("downloaded migration left native quickslot at %d", got)
	}
	status = http.StatusServiceUnavailable
	if _, err := run(context.Background(), path, options, keys); err == nil || !strings.Contains(err.Error(), "503 Service Unavailable: plan targets game 2.36.6") {
		t.Fatalf("server refusal lost its reason: %v", err)
	}
	status = http.StatusNotFound
	if _, err := run(context.Background(), path, options, keys); err == nil || !strings.Contains(err.Error(), "lost the migration catalog") {
		t.Fatalf("missing enrolled migration source: %v", err)
	}
}
