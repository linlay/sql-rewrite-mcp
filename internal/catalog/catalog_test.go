package catalog_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"datascope-server/internal/adminapi"
	"datascope-server/internal/catalog"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func setup(t *testing.T) (*catalog.Catalog, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE records(id INTEGER PRIMARY KEY, owner INTEGER, secret TEXT); INSERT INTO records VALUES(1,1,'hidden-one'),(2,2,'hidden-two'),(3,1,'hidden-three'); CREATE TABLE other(id INTEGER); INSERT INTO other VALUES(1),(2),(3);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, err := rewrite.ParseConfig([]byte(`version: 1
schemes: [{scheme: NONE, name: Public}]
tables:
 - {table: records, scheme: NONE, passthrough: true}
 - {table: other, scheme: NONE, passthrough: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.New(filepath.Join(dir, "databases"), st, cfg, store.DefaultAsOf, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}
func mustGet(t *testing.T, c *catalog.Catalog, id string) *catalog.Database {
	t.Helper()
	d, err := c.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestPoliciesAndPersistence(t *testing.T) {
	c, path := setup(t)
	ctx := context.Background()
	if err := c.Add(ctx, catalog.Database{ID: "second", Name: "第二库", Driver: "sqlite", Path: path}); err != nil {
		t.Fatal(err)
	}
	d := mustGet(t, c, "second")
	if d.MCPEnabled || !slices.Equal(d.Tools, []string{"list_tables", "describe_table", "query"}) {
		t.Fatal("new database must keep MCP closed with the three basic tools selected")
	}
	svc, err := c.Open(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Query(ctx, "SELECT * FROM records", 1, 1000); !rewrite.IsDenied(err) {
		t.Fatalf("closed table: %v", err)
	}
	svc.Store.Close()
	yes := true
	p := d.Config.Tables[0] // SQLite names are sorted: other, records.
	for _, candidate := range d.Config.Tables {
		if candidate.Table == "records" {
			p = candidate
		}
	}
	p.Enabled = &yes
	p.Columns = []string{"id", "owner"}
	p.MaxRows = 1
	p.Passthrough = false
	p.SQL = "SELECT * FROM main.records WHERE owner = ${userId}"
	d.MCPEnabled = true
	d.Tools = []string{"query", "list_tables", "describe_table"}
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	stale := *d
	if err = c.Save(ctx, stale); err == nil {
		t.Fatal("stale update accepted")
	}
	// A restart must not reread or overwrite the original YAML source.
	if _, err := catalog.Bootstrap(c.Path, nil, "no-longer-present.yml", store.DefaultAsOf, 0); err != nil {
		t.Fatal(err)
	}
	// Independent catalog instances see the same persisted policy.
	c2 := &catalog.Catalog{Path: c.Path, AsOf: store.DefaultAsOf}
	svc, err = c2.Open(ctx, mustGet(t, c2, "second"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.Close()
	got, err := svc.Query(ctx, "WITH r AS (SELECT * FROM records) SELECT * FROM r", 1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 1 || !got.Truncated || !slices.Equal(got.Columns, []string{"id", "owner"}) {
		t.Fatalf("result %+v", got)
	}
	for _, query := range []string{"SELECT secret FROM records", "SELECT id FROM records WHERE secret='hidden-one'", "SELECT r.secret FROM records r", "SELECT * FROM records UNION ALL SELECT * FROM other", "SELECT * FROM main.records"} {
		if _, err := svc.Query(ctx, query, 1, 1000); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	defaultSvc, err := c.Open(ctx, mustGet(t, c, "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer defaultSvc.Store.Close()
	all, err := defaultSvc.Query(ctx, "SELECT * FROM records", 1, 1000)
	if err != nil || all.RowCount != 3 || len(all.Columns) != 3 {
		t.Fatalf("other database changed: %v %v", all, err)
	}
	// Invalid columns and empty enabled allowlists are rejected without saving.
	d = mustGet(t, c, "second")
	for _, p := range d.Config.Tables {
		if p.Table == "records" {
			p.Columns = []string{"missing"}
		}
	}
	if err = c.Save(ctx, *d); err == nil {
		t.Fatal("unknown field accepted")
	}
	d = mustGet(t, c, "second")
	for _, p := range d.Config.Tables {
		if p.Table == "records" {
			p.Columns = []string{}
		}
	}
	if err = c.Save(ctx, *d); err == nil {
		t.Fatal("empty whitelist accepted")
	}
}
func TestMCPRevocationAndDiscovery(t *testing.T) {
	c, path := setup(t)
	ctx := context.Background()
	if err := c.Add(ctx, catalog.Database{ID: "second", Name: "Second", Driver: "sqlite", Path: path}); err != nil {
		t.Fatal(err)
	}
	d := mustGet(t, c, "default")
	d.Tools = []string{"query", "list_tables", "describe_table"}
	yes := true
	no := false
	for _, p := range d.Config.Tables {
		if p.Table == "records" {
			p.Enabled = &yes
			p.Columns = []string{"id"}
			p.MaxRows = 1
		} else {
			p.Enabled = &no
		}
	}
	if err := c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := mcpserver.ManagedHTTPHandler(c, st, mcpserver.Options{AuthMode: "header"}, "header", "", "", "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp/default"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	tables, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_tables"})
	if err != nil || tables.IsError {
		t.Fatalf("tables: %v %v", tables, err)
	}
	raw, _ := json.Marshal(tables.StructuredContent)
	if bytes.Contains(raw, []byte(`"other"`)) {
		t.Fatalf("hidden table leaked: %s", raw)
	}
	schema, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "describe_table", Arguments: map[string]any{"table": "records"}})
	if err != nil || schema.IsError {
		t.Fatalf("schema %v %v", schema, err)
	}
	raw, _ = json.Marshal(schema.StructuredContent)
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatalf("hidden column metadata leaked: %s", raw)
	}
	d = mustGet(t, c, "default")
	d.Tools = []string{"list_tables"}
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	tools, err = session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("revocation not live: %v %v", tools, err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT * FROM records"}})
	if err == nil && !res.IsError {
		t.Fatal("revoked tool callable")
	}
	d = mustGet(t, c, "default")
	d.MCPEnabled = false
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/mcp/default", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal(response.Status)
	}
	response, err = http.Post(server.URL+"/mcp/second", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal(response.Status)
	}
}
func TestManagedAdminAPI(t *testing.T) {
	c, path := setup(t)
	st, err := store.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc, err := c.Open(context.Background(), mustGet(t, c, "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.Close()
	handler := adminapi.ManagedHandler(c, &adminapi.Inspector{Endpoint: "http://localhost:8080/mcp", Service: svc})
	for _, url := range []string{"/api/databases", "/api/databases/default", "/api/databases/default/tables", "/api/databases/default/tables/records", "/api/tables"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body.String())
		}
	}
	d := mustGet(t, c, "default")
	d.Name = "Updated"
	data, _ := json.Marshal(d)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("PUT", "/api/databases/default", bytes.NewReader(data)))
	if w.Code != 200 {
		t.Fatalf("save: %s", w.Body.String())
	}
	if mustGet(t, c, "default").Name != "Updated" {
		t.Fatal("API save not persisted")
	}
}

func TestLongLivedTransportUsesLivePolicies(t *testing.T) {
	c, _ := setup(t)
	ctx := context.Background()
	svc, err := c.Open(ctx, mustGet(t, c, "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.Close()
	server := mcpserver.ManagedServer(c, "default", svc, mcpserver.Options{AuthMode: "none", DefaultUserID: 1})
	left, right := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	// The initial connection uses the unrestricted rule.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT * FROM records"}})
	if err != nil || res.IsError {
		t.Fatalf("initial query: %v %v", res, err)
	}
	d := mustGet(t, c, "default")
	for _, p := range d.Config.Tables {
		p.MaxRows = 2
		if p.Table == "records" {
			p.Columns = []string{"id"}
			p.MaxRows = 1
		}
	}
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT r.id FROM records r JOIN other o ON o.id=r.id", "max_rows": 1000}})
	if err != nil || res.IsError {
		t.Fatalf("live query: %v %v", res, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var result store.QueryResult
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 1 || !result.Truncated {
		t.Fatalf("join max rows not enforced: %s", raw)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT secret FROM records"}})
	if err == nil && !res.IsError {
		t.Fatal("new whitelist not applied to existing connection")
	}
	d = mustGet(t, c, "default")
	d.Tools = []string{"describe_table"}
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "describe_table" {
		t.Fatalf("live tools: %v %v", tools, err)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT * FROM records"}})
	if err == nil && !res.IsError {
		t.Fatal("revoked stdio tool callable")
	}
	d = mustGet(t, c, "default")
	d.MCPEnabled = false
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	if _, err = session.ListTools(ctx, nil); err == nil {
		t.Fatal("disabled MCP still usable")
	}
}

func TestYAMLFilesAndLegacyMigration(t *testing.T) {
	c, _ := setup(t)
	first := mustGet(t, c, "default")
	second := mustGet(t, c, "default")
	second.ID = "second"
	second.MCPPath = "second"
	second.Name = "Second"
	no := false
	second.Config.Tables[0].Enabled = &no
	second.Config.Tables[0].Columns = []string{}
	legacy := filepath.Join(t.TempDir(), "databases.json")
	data, err := json.Marshal([]catalog.Database{*first, *second})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(legacy, data, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "databases")
	migrated, err := catalog.Bootstrap(target, nil, "missing.yml", store.DefaultAsOf, 0, legacy)
	if err != nil {
		t.Fatal(err)
	}
	firstFile := filepath.Join(target, "test.yml")
	secondFile := filepath.Join(target, "second.yml")
	raw, err := os.ReadFile(firstFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcpEnabled:") || strings.Contains(string(raw), "compiledSql:") {
		t.Fatalf("unexpected YAML: %s", raw)
	}
	if mustGet(t, migrated, "default").Config.Tables[0].Columns != nil {
		t.Fatal("null whitelist became empty")
	}
	cols := mustGet(t, migrated, "second").Config.Tables[0].Columns
	if cols == nil || len(cols) != 0 {
		t.Fatal("empty whitelist became unrestricted")
	}
	// A manual file edit invalidates a previously fetched editor snapshot.
	stale := mustGet(t, migrated, "default")
	raw = append(raw, []byte("\n# manual edit\n")...)
	if err = os.WriteFile(firstFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = migrated.Save(context.Background(), *stale); err == nil {
		t.Fatal("manual edit was silently overwritten")
	}
	untouched, err := os.ReadFile(secondFile)
	if err != nil {
		t.Fatal(err)
	}
	untouched = append(untouched, []byte("\n# keep this comment\n")...)
	if err = os.WriteFile(secondFile, untouched, 0600); err != nil {
		t.Fatal(err)
	}
	latest := mustGet(t, migrated, "default")
	latest.Name = "Renamed"
	if err = migrated.Save(context.Background(), *latest); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(secondFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, untouched) {
		t.Fatal("saving one database rewrote another file")
	}
	// Later startup reads YAML even if the old JSON changes.
	if err = os.WriteFile(legacy, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Bootstrap(target, nil, "missing.yml", store.DefaultAsOf, 0, legacy); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(secondFile, []byte("not_a_setting: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = migrated.List(); err == nil {
		t.Fatal("malformed config was ignored")
	}
}

func TestCustomMCPPaths(t *testing.T) {
	c, path := setup(t)
	ctx := context.Background()
	st, err := store.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := mcpserver.ManagedHTTPHandler(c, st, mcpserver.Options{AuthMode: "none", DefaultUserID: 1}, "none", "", "", "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	connectPath := func(path string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "path-test", Version: "1"}, nil)
		s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + path}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	old := connectPath("/mcp/default")
	d := mustGet(t, c, "default")
	d.MCPPath = "finance"
	if err = c.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	if _, err = old.ListTools(ctx, nil); err == nil {
		t.Fatal("old endpoint still served the database")
	}
	if tools, err := connectPath("/mcp/finance").ListTools(ctx, nil); err != nil || len(tools.Tools) != 3 {
		t.Fatalf("new endpoint %v %v", tools, err)
	}
	if _, err := connectPath("/mcp").ListTools(ctx, nil); err != nil {
		t.Fatal("legacy root endpoint lost", err)
	}
	if err = c.Add(ctx, catalog.Database{ID: "conflict", Name: "Conflict", Driver: "sqlite", Path: path, MCPPath: "finance"}); err == nil {
		t.Fatal("duplicate path accepted during add")
	}
	if err = c.Add(ctx, catalog.Database{ID: "second", Name: "Second", Driver: "sqlite", Path: path, MCPPath: "retail"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"finance", "../test", "a/b", "/finance", "a b", "a?b", strings.Repeat("x", 65)} {
		d = mustGet(t, c, "second")
		d.MCPPath = invalid
		if err = c.Save(ctx, *d); err == nil {
			t.Fatalf("accepted path %q", invalid)
		}
	}
	if mustGet(t, c, "second").MCPRoute() != "retail" {
		t.Fatal("failed save changed existing route")
	}
	restarted := &catalog.Catalog{Path: c.Path, AsOf: store.DefaultAsOf}
	if found, err := restarted.ByMCPPath("finance"); err != nil || found.ID != "default" {
		t.Fatalf("path persistence: %v %v", found, err)
	}
	svc, err := c.Open(ctx, mustGet(t, c, "default"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.Close()
	admin := adminapi.ManagedHandler(c, &adminapi.Inspector{Endpoint: server.URL + "/mcp", Service: svc})
	for _, url := range []string{"/api/databases/default", "/api/databases/default/mcp/info"} {
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body.String())
		}
		if !bytes.Contains(w.Body.Bytes(), []byte(server.URL+"/mcp/finance")) {
			t.Fatalf("old inspector endpoint: %s", w.Body.String())
		}
	}
	// Custom paths retain OAuth discovery and reject unauthenticated calls.
	resource := "http://127.0.0.1:8083/mcp"
	oauthHandler, err := mcpserver.ManagedHTTPHandler(c, st, mcpserver.Options{AuthMode: "oauth"}, "oauth", "http://127.0.0.1:8081", resource, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	oauthHandler.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp/finance", nil))
	var metadata struct {
		Resource string `json:"resource"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || metadata.Resource != resource+"/finance" {
		t.Fatalf("OAuth metadata %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	oauthHandler.ServeHTTP(w, httptest.NewRequest("POST", "/mcp/finance", bytes.NewBufferString(`{}`)))
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("custom route bypassed OAuth: %d", w.Code)
	}
}
