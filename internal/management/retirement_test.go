package management

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFragmentMigrationFailurePreservesPermissions(t *testing.T) {
	for _, body := range []string{"{{missing}}", "{{scope}}", "{{malformed"} {
		t.Run(body, func(t *testing.T) {
			global, _ := migrationFixture(t)
			old := filepath.Join(filepath.Dir(global), "databases", "source", "management.db")
			// The fixture retains its writable connection with committed WAL frames.
			writer, err := sql.Open("sqlite", old)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err = writer.Exec("UPDATE QUERY_FRAGMENT SET SQL_=? WHERE NAME_='scope'", body); err != nil {
				t.Fatal(err)
			}
			if _, err = MigrateToYAML(context.Background(), global); err == nil {
				t.Fatal("invalid permissions migrated")
			}
			var policy string
			if err = writer.QueryRow("SELECT DATA_ FROM QUERY_POLICY WHERE TABLE_NAME_='items'").Scan(&policy); err != nil || !strings.Contains(policy, "{{scope}}") {
				t.Fatal("old permission lost", policy, err)
			}
			if _, err = os.Stat(filepath.Join(filepath.Dir(old), "management", "database.yml")); !os.IsNotExist(err) {
				t.Fatal("failed migration published a configuration", err)
			}
		})
	}
}

func TestRuntimeRejectsRetiredStorage(t *testing.T) {
	global := filepath.Join(t.TempDir(), "management.db")
	for i := 0; i < 2; i++ {
		m, err := Open(global, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"QUERY_TOOL", "QUERY_FRAGMENT", "CONFIG_RECORD", "CONFIG_VERSION", "KNOWLEDGE_SEARCH", "ACTION_RECEIPT"} {
			var count int
			if err = m.DB.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name=?", table).Scan(&count); err != nil || count != 0 {
				t.Fatal("retired runtime table", table, count, err)
			}
		}
		if err = m.UpdateRecords(context.Background(), "discovery_pending", "test", "source", func([]Record) ([]Record, error) { t.Fatal("runtime state callback called"); return nil, nil }); err == nil {
			t.Fatal("runtime state accepted as formal configuration")
		}
		m.Close()
	}
}

func TestOfflineYAMLCleanupPreservesRevisionAndBackup(t *testing.T) {
	global, _ := migrationFixture(t)
	ctx := context.Background()
	if _, err := MigrateToYAML(ctx, global); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(global), "databases", "source", "management")
	file := filepath.Join(path, "database.yml")
	var root map[string]any
	if err := readDocument(file, &root); err != nil {
		t.Fatal(err)
	}
	d := root["database"].(map[string]any)
	d["discovery"].(map[string]any)["reports"] = []any{map[string]any{"version": 3}}
	root["records"] = []any{map[string]any{"bucket": "discovery_pending", "key": "source", "data": nil}}
	raw, err := marshalDocument(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reports, err := MigrateToYAML(ctx, global)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Backup == "" || reports[0].Revision != float64(9) {
		t.Fatal(reports)
	}
	backup, err := os.ReadFile(filepath.Join(reports[0].Backup, "management", "database.yml"))
	if err != nil || string(backup) != string(raw) {
		t.Fatal("original YAML not backed up", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || strings.Contains(string(after), "records:") || strings.Contains(string(after), "reports:") {
		t.Fatal("operational state retained", err)
	}
	retry, err := MigrateToYAML(ctx, global)
	if err != nil || retry[0].Backup != "" {
		t.Fatal("cleanup was not one-time", retry, err)
	}
}

func TestLegacyExpansionKeepsBindsAndDropsDerivedState(t *testing.T) {
	d := map[string]any{"business_tools": []any{}, "config": map[string]any{"fragments": map[string]any{"nested": "${userId} AND :arg_scope = :arg_scope", "scope": "owner={{nested}}"}, "tables": []any{map[string]any{"table": "items", "sql": "SELECT * FROM main.items WHERE {{scope}}", "deny_unless": "{{nested}}", "compiled_sql": "old", "params": []any{"old"}}}}, "discovery": map[string]any{"version": float64(3), "pending": map[string]any{}, "reports": []any{}, "checked_at": "old"}}
	if err := migrateLegacyDatabase(d); err != nil {
		t.Fatal(err)
	}
	cfg := d["config"].(map[string]any)
	p := cfg["tables"].([]any)[0].(map[string]any)
	if p["sql"] != "SELECT * FROM main.items WHERE owner=${userId} AND :arg_scope = :arg_scope" || p["deny_unless"] != "${userId} AND :arg_scope = :arg_scope" {
		t.Fatal("binding semantics changed", p)
	}
	if _, ok := cfg["fragments"]; ok {
		t.Fatal("fragment dictionary retained")
	}
	if _, ok := p["compiled_sql"]; ok {
		t.Fatal("compiled SQL persisted")
	}
	if len(d["discovery"].(map[string]any)) != 1 {
		t.Fatal("operational discovery state persisted")
	}
	if err := normalizeDatabase(map[string]any{"config": map[string]any{"fragments": map[string]any{}}}); err == nil {
		t.Fatal("runtime fragment configuration accepted")
	}
}
