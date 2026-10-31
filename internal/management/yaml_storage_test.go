package management

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func migrationFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	global := filepath.Join(dir, "management.db")
	m, e := Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.DB.Exec(`INSERT INTO SOURCE_REGISTRY VALUES('source','Source','finance','ACTIVE');INSERT INTO AUTH_BINDING VALUES('DATABASE','source','business-account')`)
	if e != nil {
		t.Fatal(e)
	}
	m.Close()
	parent := filepath.Join(dir, "databases", "source")
	if e = os.MkdirAll(parent, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(parent, "management.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA journal_mode=WAL;" + legacySourceDDL); e != nil {
		t.Fatal(e)
	}
	hash, e := passwordHash("legacy-password")
	if e != nil {
		t.Fatal(e)
	}
	d := map[string]any{"id": "source", "name": "Source", "mcp_path": "finance", "revision": 9, "authentication_revision": 6, "driver": "sqlite", "path": "business.db", "connection": map[string]any{"credential_id": "source"}, "config": map[string]any{"version": 1, "schemes": []any{}, "tables": []any{}}, "discovery": map[string]any{"version": 3, "tables": []any{map[string]any{"name": "items", "columns": []any{map[string]any{"name": "CustomerID", "comment": "Source comment"}}, "ddl": "CREATE TABLE items (CustomerID INTEGER)"}}, "annotations": map[string]any{"items": map[string]any{"display_name": "项目", "comment": "说明", "columns": map[string]any{"CustomerID": map[string]any{"display_name": "客户", "comment": "字段说明"}}}}}}
	b, _ := json.Marshal(d)
	policy := `{"table":"items","sql":"SELECT * FROM items WHERE {{scope}}","deny_unless":"{{allowed}}","compiled_sql":"runtime","params":["user_id"],"columns":["CustomerID"]}`
	key := make([]byte, 32)
	rand.Read(key)
	os.WriteFile(filepath.Join(parent, "credentials.key"), key, 0600)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	sealed := gcm.Seal(nonce, nonce, []byte("database-password"), []byte("source"))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO SOURCE_CONFIG VALUES('source',?)", []any{string(b)}},
		{"INSERT INTO QUERY_POLICY VALUES('items',?,?)", []any{"SELECT * FROM items WHERE {{scope}}", policy}},
		{"INSERT INTO QUERY_FRAGMENT VALUES('scope','owner_id=${user_id}');INSERT INTO QUERY_FRAGMENT VALUES('allowed','1=1')", nil},
		{"INSERT INTO ACTION_DEFINITION VALUES('edit','编辑','完整描述',1,?,?)", []any{`{"CustomerID":{"type":"integer","description":"客户"},"params":{"type":"string","description":"参数"}}`, "function run(args,ctx){\n  return {value:args.CustomerID};\n}"}},
		{"INSERT INTO BUSINESS_ACCOUNT VALUES(7,'source','E0001','Employee',10,?,'[\"query\",\"action\",\"knowledge.read\"]',1,4)", []any{hash}},
		{"INSERT INTO PLATFORM_GRANT VALUES(21,'source','[\"query\",\"config.read\"]',10)", nil},
		{"INSERT INTO CONNECTION_CONFIG VALUES('READ','{\"credential_id\":\"source\"}',?)", []any{sealed}},
		{`INSERT INTO KNOWLEDGE VALUES('source/guide','{"id":"guide","database_id":"source","name":"Guide","summary":"Line one\nLine two","content":"第一行\n第二行\n","published":true,"revision":8,"tables":[]}')`, nil},
		{`INSERT INTO CONFIG_VERSION VALUES('databases','source',9,'{"name":"original"}','2026-01-01')`, nil},
	} {
		if _, e = db.Exec(q.sql, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	// Keep committed WAL frames alive while migration snapshots the source.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return global, hash
}

func TestYAMLUpgradePreservesAndStopsUsingLegacySQLite(t *testing.T) {
	global, hash := migrationFixture(t)
	ctx := context.Background()
	m, e := Open(global, nil)
	if e == nil {
		m.Close()
		t.Fatal("unmigrated source silently loaded")
	}
	reports, e := MigrateToYAML(ctx, global)
	if e != nil {
		t.Fatal(e)
	}
	if len(reports) != 1 || reports[0].Revision != float64(9) || reports[0].Accounts != 1 || reports[0].Actions != 1 || reports[0].Knowledge != 1 || reports[0].Policies != 1 {
		t.Fatalf("report: %+v", reports)
	}
	report := reports[0]
	if _, e = os.Stat(report.Backup); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(report.Backup), "credentials.key")); e != nil {
		t.Fatal(e)
	}
	backup, e := readonlySQLite(report.Backup)
	if e != nil {
		t.Fatal(e)
	}
	defer backup.Close()
	var original string
	if e = backup.QueryRow("SELECT DATA_ FROM CONFIG_VERSION").Scan(&original); e != nil || !strings.Contains(original, "original") {
		t.Fatal("history backup missing", e)
	}
	m, e = Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	a, e := m.AuthenticateBusiness(ctx, "source", "E0001", "legacy-password")
	if e != nil || a.ID != 7 || a.AuthVersion != 4 || a.BusinessUserID != 10 {
		t.Fatal(a, e)
	}
	source, e := m.Source(ctx, "source")
	if e != nil {
		t.Fatal(e)
	}
	if e = source.withState(ctx, false, func(st *sourceState) error {
		if st.Accounts[0].PasswordHash != hash || st.BindingPolicyID != "business-account" {
			t.Fatal("hash or binding changed")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	password, e := m.DatabasePassword(ctx, "source")
	if e != nil || password != "database-password" {
		t.Fatal("credential migration", e)
	}
	g, e := m.Grant(ctx, 21, "source")
	if e != nil || g.BusinessUserID != 10 {
		t.Fatal(g, e)
	}
	auth, e := m.ResolveAuth(ctx, "DATABASE", "source")
	if e != nil || auth.Provider.Type != "BUSINESS_ACCOUNT" {
		t.Fatal(auth, e)
	}
	var binds int
	if e = m.DB.QueryRow("SELECT COUNT(*) FROM AUTH_BINDING WHERE TARGET_TYPE_='DATABASE'").Scan(&binds); e != nil || binds != 0 {
		t.Fatal("source binding remains global", e)
	}
	table, e := os.ReadFile(filepath.Join(source.Path, "tables", "items.yml"))
	if e != nil || bytes.Contains(table, []byte("{{")) || !bytes.Contains(table, []byte("owner_id=${user_id}")) || bytes.Contains(table, []byte("COMPILED_SQL_")) || !bytes.Contains(table, []byte("CustomerID:")) {
		t.Fatalf("table conversion: %s %v", table, e)
	}
	action, e := os.ReadFile(filepath.Join(source.Path, "actions", "edit.yml"))
	if e != nil || !bytes.Contains(action, []byte("CustomerID:")) || !bytes.Contains(action, []byte("params:")) || !bytes.Contains(action, []byte("SCRIPT_: |-")) {
		t.Fatalf("action: %s %v", action, e)
	}
	knowledge, e := os.ReadFile(filepath.Join(source.Path, "knowledges", "guide.yml"))
	if e != nil || !bytes.Contains(knowledge, []byte("DESCRIPTION_: |-")) || !bytes.Contains(knowledge, []byte("CONTENT_: |")) {
		t.Fatalf("knowledge: %s %v", knowledge, e)
	}
	for _, forbidden := range []string{"template", "fragment.yml", "version", "state", "prompts"} {
		if _, e = os.Stat(filepath.Join(source.Path, forbidden)); !os.IsNotExist(e) {
			t.Fatal("unexpected directory", forbidden)
		}
	}
	// Old management.db becomes unreadable input. Runtime and repeated migration
	// still use YAML; the snapshot above remains the authoritative recovery copy.
	old := filepath.Join(filepath.Dir(source.Path), "management.db")
	oldBytes, e := os.ReadFile(old)
	if e != nil {
		t.Fatal(e)
	}
	defer os.WriteFile(old, oldBytes, 0600)
	if e = os.WriteFile(old, []byte("not SQLite anymore"), 0600); e != nil {
		t.Fatal(e)
	}
	second, e := Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	if _, e = second.AuthenticateBusiness(ctx, "source", "E0001", "legacy-password"); e != nil {
		t.Fatal(e)
	}
	again, e := MigrateToYAML(ctx, global)
	if e != nil || len(again) != 1 || again[0].Backup != "" {
		t.Fatal("upgrade reimported old SQLite", e)
	}
	if _, e = second.Records(ctx, "knowledge"); e != nil {
		t.Fatal(e)
	}
}

func TestYAMLFailedValidationAndInterruptedPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "management")
	st := emptyState()
	st.Database = map[string]any{"id": "one", "revision": float64(1)}
	if e := publishState(path, st); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(path, "database.yml"))
	if e != nil {
		t.Fatal(e)
	}
	invalid := emptyState()
	invalid.Database = map[string]any{"id": "one", "actions": []any{map[string]any{"name": "../escape"}}}
	if e = publishState(path, invalid); e == nil {
		t.Fatal("invalid generation committed")
	}
	after, _ := os.ReadFile(filepath.Join(path, "database.yml"))
	if !bytes.Equal(before, after) {
		t.Fatal("validation modified old files")
	}
	next := emptyState()
	next.Database = map[string]any{"id": "one", "revision": float64(2)}
	if e = publishState(path+".next", next); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(path, path+".previous"); e != nil {
		t.Fatal(e)
	}
	source := &Source{Path: path, ID: "one"}
	d, e := source.load(context.Background())
	if e != nil || d["revision"] != float64(2) {
		t.Fatal("interrupted publication not recovered", d, e)
	}
	if _, e = os.Stat(path + ".previous"); !os.IsNotExist(e) {
		t.Fatal("stale transaction remains")
	}
}

func TestConfigurationJournalRecoversRegistryAndAuth(t *testing.T) {
	global := filepath.Join(t.TempDir(), "management.db")
	m, e := Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := m.AuthConfig(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	st := emptyState()
	st.Database = map[string]any{"id": "one", "name": "One", "revision": float64(3)}
	st.BindingPolicyID = "business-account"
	cfg.Bindings = append(cfg.Bindings, AuthBinding{TargetType: "DATABASE", TargetID: "one", PolicyID: "business-account"})
	change := configurationChange{Sources: map[string]*sourceState{"one": st}, Registry: []registryChange{{"one", "One", "one"}}, Authentication: &cfg}
	b, _ := json.Marshal(change)
	if e = os.WriteFile(m.pendingPath(), b, 0600); e != nil {
		t.Fatal(e)
	}
	// Simulate a crash after a YAML publication and before SQLite commit.
	parent := filepath.Join(filepath.Dir(global), "databases", "one")
	os.MkdirAll(parent, 0700)
	if e = publishState(filepath.Join(parent, "management"), st); e != nil {
		t.Fatal(e)
	}
	m.Close()
	peer, e := Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer peer.Close()
	records, e := peer.Records(context.Background(), "databases")
	if e != nil || len(records) != 1 {
		t.Fatal("journal did not recover registry", e)
	}
	auth, e := peer.ResolveAuth(context.Background(), "DATABASE", "one")
	if e != nil || auth.Provider.Type != "BUSINESS_ACCOUNT" || auth.Revision != cfg.Revision+1 {
		t.Fatal(auth, e)
	}
	if _, e = os.Stat(peer.pendingPath()); !os.IsNotExist(e) {
		t.Fatal("commit journal not removed")
	}
}

// Use a real second OS process, rather than two handles, to exercise flock and
// disk freshness independently of the parent process's memory.
func TestYAMLProcessPeer(t *testing.T) {
	global := os.Getenv("DATASCOPE_YAML_PEER_TEST")
	if global != "" {
		m, e := Open(global, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer m.Close()
		a, e := m.BusinessAccount(context.Background(), "source", "E0001")
		if e != nil {
			t.Fatal(e)
		}
		a.Name = "Peer updated"
		if _, e = m.SaveBusinessAccount(context.Background(), a, "", a.AuthVersion); e != nil {
			t.Fatal(e)
		}
		return
	}
	global, _ = migrationFixture(t)
	if _, e := MigrateToYAML(context.Background(), global); e != nil {
		t.Fatal(e)
	}
	m, e := Open(global, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	a, e := m.BusinessAccount(context.Background(), "source", "E0001")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestYAMLProcessPeer$")
	cmd.Env = append(os.Environ(), "DATASCOPE_YAML_PEER_TEST="+global)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("peer failed: %s %v", b, e)
	}
	changed, e := m.BusinessAccount(context.Background(), "source", "E0001")
	if e != nil || changed.Name != "Peer updated" || changed.AuthVersion != a.AuthVersion+1 {
		t.Fatal("peer update not visible", changed, e)
	}
	if _, e = m.SaveBusinessAccount(context.Background(), a, "", a.AuthVersion); e == nil {
		t.Fatal("stale peer revision accepted")
	}
	// Ciphertext stays sealed in YAML and never appears in API serialization.
	sealed, e := m.SealDatabaseCredential("source", "example-password")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(sealed)
	if strings.Contains(string(b), base64.StdEncoding.EncodeToString(sealed.Ciphertext)) {
		t.Fatal("ciphertext leaked")
	}
}

func TestYAMLPreservesFullInt64AccountAndGrantIDs(t *testing.T) {
	const id int64 = 9007199254740993
	st := emptyState()
	st.Database = map[string]any{"id": "source", "revision": float64(1)}
	st.Accounts = []accountState{{BusinessAccount: BusinessAccount{ID: id, DatabaseID: "source", Username: "employee", Name: "Employee", BusinessUserID: id + 2, Active: true, AuthVersion: id + 4}, PasswordHash: "unchanged-hash"}}
	st.Grants = []Grant{{UserID: id + 6, DatabaseID: "source", BusinessUserID: id + 8, Permissions: []string{"query"}}}
	path := filepath.Join(t.TempDir(), "management")
	if err := publishState(path, st); err != nil {
		t.Fatal(err)
	}
	got, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts[0].ID != id || got.Accounts[0].BusinessUserID != id+2 || got.Accounts[0].AuthVersion != id+4 || got.Grants[0].UserID != id+6 || got.Grants[0].BusinessUserID != id+8 {
		t.Fatal("int64 identity lost precision")
	}
}

func TestYAMLMigrationDoesNotResurrectHistoryAfterConfigurationLoss(t *testing.T) {
	global, _ := migrationFixture(t)
	ctx := context.Background()
	reports, err := MigrateToYAML(ctx, global)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(reports[0].Management, reports[0].Management+"-recovery-copy"); err != nil {
		t.Fatal(err)
	}
	if _, err = MigrateToYAML(ctx, global); err == nil || !strings.Contains(err.Error(), "已完成 YAML 迁移") {
		t.Fatal("completed upgrade reimported stale SQLite", err)
	}
}
