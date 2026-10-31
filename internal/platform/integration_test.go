package platform_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"datascope-server/internal/adminapi"
	"datascope-server/internal/application"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/managementmcp"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/oauth"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	dir           string
	m             *management.Store
	a             *audit.Store
	c             *catalog.Catalog
	app           *application.Service
	st            *store.Store
	admin, reader management.User
	ctx           context.Context
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	a, e := audit.Open(filepath.Join(dir, "audit.sqlite"))
	must(t, e)
	m, e := management.Open(filepath.Join(dir, "management.sqlite"), a)
	must(t, e)
	m.AllowedRoots = []string{dir}
	must(t, m.Bootstrap(ctx, "admin", "admin-test-password"))
	admin, e := m.ByUsername(ctx, "admin")
	must(t, e)
	path := filepath.Join(dir, "business.sqlite")
	db, e := sql.Open("sqlite", path)
	must(t, e)
	_, e = db.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY,owner_id INTEGER,name TEXT,secret TEXT);INSERT INTO items VALUES(1,10,'one','s1'),(2,20,'two','s2'),(3,10,'three','s3');`)
	must(t, e)
	db.Close()
	st, e := store.Open(path, time.Second)
	must(t, e)
	cfg := &rewrite.Config{Version: 1, Schemes: []rewrite.SchemeInfo{{Scheme: "CUSTOM", Name: "owner"}}, Tables: []*rewrite.Policy{{Table: "items", Scheme: "CUSTOM", SQL: "SELECT * FROM main.items WHERE owner_id = ${userId}"}}}
	_, e = catalog.New(filepath.Join(dir, "databases"), st, cfg, store.DefaultAsOf, time.Second)
	must(t, e)
	c, e := catalog.BootstrapManaged(ctx, m, filepath.Join(dir, "databases"), st, "unused", store.DefaultAsOf, time.Second, filepath.Join(dir, "old.json"))
	must(t, e)
	ctx = guard.WithUser(audit.With(ctx, audit.Metadata{ActorID: admin.ID, Source: "test"}), admin)
	reader, e := m.CreateUser(ctx, "reader", "Reader", "reader-test-password")
	must(t, e)
	must(t, m.SaveGrant(ctx, admin.ID, management.Grant{UserID: reader.ID, DatabaseID: "default", BusinessUserID: 10, Permissions: []string{"query", "knowledge.read"}}))
	must(t, m.SaveGrant(ctx, admin.ID, management.Grant{UserID: admin.ID, DatabaseID: "default", BusinessUserID: 20, Permissions: management.Permissions}))
	f := &fixture{dir: dir, m: m, a: a, c: c, st: st, admin: admin, reader: reader, ctx: ctx}
	f.app = &application.Service{Management: m, Catalog: c, MCPBase: "http://127.0.0.1:8080/mcp"}
	t.Cleanup(func() { st.Close(); m.Close(); a.Close() })
	return f
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) readerContext() context.Context {
	return guard.WithUser(audit.With(context.Background(), audit.Metadata{ActorID: f.reader.ID, Source: "test"}), f.reader)
}
func request(t *testing.T, c *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var e error
		raw, e = json.Marshal(body)
		must(t, e)
	}
	req, e := http.NewRequest(method, url, bytes.NewReader(raw))
	must(t, e)
	req.Header.Set("Content-Type", "application/json")
	res, e := c.Do(req)
	must(t, e)
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	must(t, e)
	return res.StatusCode, b
}
func api(t *testing.T, f *fixture) *httptest.Server {
	t.Helper()
	d, e := f.c.Get("default")
	must(t, e)
	svc, e := f.c.Open(f.ctx, d)
	must(t, e)
	t.Cleanup(func() { svc.Store.Close() })
	s := httptest.NewServer(adminapi.PlatformHandler(f.app, adminapi.ManagedHandler(f.c, &adminapi.Inspector{Service: svc, Endpoint: "http://127.0.0.1:1/mcp"})))
	t.Cleanup(s.Close)
	return s
}
func login(t *testing.T, url, name, password string) *http.Client {
	t.Helper()
	jar, e := cookiejar.New(nil)
	must(t, e)
	c := &http.Client{Jar: jar}
	status, b := request(t, c, "POST", url+"/api/auth/login", map[string]any{"username": name, "password": password})
	if status != 200 {
		t.Fatalf("login %d: %s", status, b)
	}
	return c
}
func TestPlatformHTTPPermissionsAndAudit(t *testing.T) {
	f := setup(t)
	s := api(t, f)
	for _, path := range []string{"/api/databases", "/api/users", "/api/databases/default/tables", "/api/query"} {
		status, _ := request(t, http.DefaultClient, "GET", s.URL+path, nil)
		if status != 401 {
			t.Fatalf("unauthenticated %s: %d", path, status)
		}
	}
	c := login(t, s.URL, "reader", "reader-test-password")
	status, b := request(t, c, "GET", s.URL+"/api/databases", nil)
	if status != 200 || bytes.Contains(b, []byte(f.dir)) {
		t.Fatalf("list leaks connection: %d %s", status, b)
	}
	var views []application.DatabaseView
	must(t, json.Unmarshal(b, &views))
	if len(views) != 1 || views[0].BusinessUserID != 10 {
		t.Fatalf("views %s", b)
	}
	status, _ = request(t, c, "POST", s.URL+"/api/databases/default/query", map[string]any{"sql": "SELECT * FROM items"})
	if status != 403 {
		t.Fatalf("raw query %d", status)
	}
	status, _ = request(t, c, "POST", s.URL+"/api/databases/default/rewrite", map[string]any{"sql": "SELECT * FROM items", "user_id": 20})
	if status != 403 {
		t.Fatalf("impersonation %d", status)
	}
	status, b = request(t, c, "POST", s.URL+"/api/databases/default/rewrite", map[string]any{"sql": "SELECT * FROM items"})
	if status != 200 {
		t.Fatalf("query %d %s", status, b)
	}
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	must(t, json.Unmarshal(b, &result))
	if len(result.Rows) != 2 {
		t.Fatalf("owner filter: %s", b)
	}
	status, _ = request(t, c, "POST", s.URL+"/api/databases/default/rewrite", map[string]any{"sql": "DELETE FROM items"})
	if status != 400 {
		t.Fatalf("write %d", status)
	}
	status, _ = request(t, c, "POST", s.URL+"/api/management/call", map[string]any{"action": "create_user", "arguments": map[string]any{"username": "evil", "password": "evil-test-password"}})
	if status != 403 {
		t.Fatalf("create user %d", status)
	}
	req, _ := http.NewRequest("POST", s.URL+"/api/auth/logout", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, e := c.Do(req)
	must(t, e)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("CSRF %d", res.StatusCode)
	}
	events, e := f.a.List(f.ctx, "default", "", 200, 0)
	must(t, e)
	var execution, denied, invalid bool
	for _, ev := range events {
		if ev.Kind == "sql.execute" {
			execution = true
			if ev.OriginalSQL == "" || ev.RewrittenSQL == "" || ev.ExecutedSQL == "" || ev.ActorID != f.reader.ID || ev.SubjectID != 10 || ev.RequestID == "" {
				t.Fatalf("incomplete execution %+v", ev)
			}
		}
		if ev.Kind == "access.denied" {
			denied = true
		}
		if ev.Kind == "sql.rewrite" && ev.Status == "failed" && ev.OriginalSQL == "DELETE FROM items" {
			invalid = true
		}
	}
	if !execution || !denied || !invalid {
		t.Fatalf("missing events execution=%v denied=%v invalid=%v", execution, denied, invalid)
	}
	must(t, f.m.ResetPassword(f.ctx, f.reader.ID, "reader-new-password"))
	status, _ = request(t, c, "GET", s.URL+"/api/auth/me", nil)
	if status != 401 {
		t.Fatalf("old session %d", status)
	}
}

func TestAdminConnectorDownload(t *testing.T) {
	f := setup(t)
	f.app.ManagementMCPURL = "https://datascope.example.com/mcp/admin"
	s := api(t, f)
	endpoint := s.URL + "/api/connectors/datascope-admin.zip"
	status, _ := request(t, http.DefaultClient, "GET", endpoint, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download: %d", status)
	}
	client := login(t, s.URL, "reader", "reader-test-password")
	response, err := client.Get(endpoint)
	must(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/zip" || response.Header.Get("Content-Disposition") != `attachment; filename="datascope-admin.zip"` {
		t.Fatalf("download response: %d %v", response.StatusCode, response.Header)
	}
	data, err := io.ReadAll(response.Body)
	must(t, err)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		must(t, err)
		files[file.Name], err = io.ReadAll(reader)
		reader.Close()
		must(t, err)
	}
	for _, path := range []string{"connector.json", "mcp.json", "skills/datascope-admin/SKILL.md", "skills/datascope-admin/references/databases.md", "skills/datascope-admin/references/policies.md", "skills/datascope-admin/references/permissions-users.md", "skills/datascope-admin/references/knowledge.md", "skills/datascope-admin/references/audit.md"} {
		if len(files["datascope-admin/"+path]) == 0 {
			t.Fatalf("archive missing %s", path)
		}
	}
	var config struct {
		Servers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	must(t, json.Unmarshal(files["datascope-admin/mcp.json"], &config))
	if config.Servers["main"].URL != f.app.ManagementMCPURL {
		t.Fatalf("download has wrong deployment URL: %s", config.Servers["main"].URL)
	}
	status, _ = request(t, client, "POST", endpoint, nil)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("download accepts POST: %d", status)
	}
}
func TestCatalogMigrationConcurrencyAndInternalIsolation(t *testing.T) {
	f := setup(t)
	d, e := f.c.Get("default")
	must(t, e)
	must(t, os.WriteFile(filepath.Join(f.dir, "databases", "business.yml"), []byte("invalid YAML"), 0600))
	if _, e = f.c.List(); e != nil {
		t.Fatal("SQLite still reads YAML", e)
	}
	for _, path := range []string{f.m.Path, f.a.Path} {
		e = f.c.Add(f.ctx, catalog.Database{ID: "internal", Name: "internal", Driver: "sqlite", Path: path})
		if e == nil {
			t.Fatal("registered internal db")
		}
	}
	link := filepath.Join(f.dir, "disguised.sqlite")
	must(t, os.Symlink(f.m.Path, link))
	if e = f.m.ValidatePath(link); e == nil {
		t.Fatal("registered management symlink")
	}
	c2 := &catalog.Catalog{Path: f.c.Path, Timeout: f.c.Timeout, AsOf: f.c.AsOf, Management: f.m, Audit: f.a} // independent process configuration
	one, two := *d, *d
	one.Name = "one"
	two.Name = "two"
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() { defer wg.Done(); errs <- f.c.Save(f.ctx, one) }()
	go func() { defer wg.Done(); errs <- c2.Save(f.ctx, two) }()
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent writes success=%d", success)
	}
	must(t, f.m.Flush(f.ctx))
	var count int
	must(t, f.m.DB.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='AUDIT_OUTBOX'").Scan(&count))
	if count != 0 {
		t.Fatal("runtime audit queue persisted in management")
	}
	current, e := f.c.Get("default")
	must(t, e)
	if current.Revision != one.Revision+1 {
		t.Fatal("revision did not advance exactly once")
	}
	assertNoBusinessSQLite(t, f, "default")
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
func connectMCP(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, e := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
	must(t, e)
	t.Cleanup(func() { s.Close() })
	return s
}
func callMCP(t *testing.T, s *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	return s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
}
func TestOAuthMCPManagementKnowledgeAndLiveRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	asHTTP := httptest.NewUnstartedServer(http.NotFoundHandler())
	dbHTTP := httptest.NewUnstartedServer(http.NotFoundHandler())
	issuer := "http://" + asHTTP.Listener.Addr().String()
	base := "http://" + dbHTTP.Listener.Addr().String() + "/mcp"
	adminResource := issuer + "/mcp/admin"
	as, e := oauth.New(oauth.Config{Catalog: f.c, Issuer: issuer, Resource: base, Management: f.m, KeyPath: filepath.Join(f.dir, "signing.pem"), StatePath: f.m.Path, ResourceAllowed: func(r string) bool { return r == adminResource || f.c.AllowsMCPResource(base, r) }, DatabaseForResource: func(r string) (string, error) {
		if r == adminResource {
			return "", nil
		}
		if r == base || r == base+"/default" {
			return "default", nil
		}
		return "", fmt.Errorf("unknown")
	}}, nil)
	must(t, e)
	t.Cleanup(func() { as.Close() })
	managementHandler, e := mcpserver.HTTPHandlerWithManagement(managementmcp.New(f.app), nil, "oauth", issuer, adminResource, "/mcp/admin", true, f.m, "")
	must(t, e)
	asHTTP.Config.Handler = as.Handler(managementHandler)
	asHTTP.Start()
	t.Cleanup(asHTTP.Close)
	d, e := f.c.Get("default")
	must(t, e)
	d.Tools = []string{"list_tables", "describe_table", "query", "raw_query", "search_knowledge", "read_knowledge"}

	must(t, f.c.Save(f.ctx, *d))
	h, e := mcpserver.ManagedHTTPHandler(f.c, nil, mcpserver.Options{AuthMode: "oauth", Management: f.m, Application: f.app}, "oauth", issuer, base, "/mcp")
	must(t, e)
	dbHTTP.Config.Handler = h
	dbHTTP.Start()
	t.Cleanup(dbHTTP.Close)
	if _, e = as.SignEmployeeForResource(ctx, f.reader.ID, base+"/default"); e == nil {
		t.Fatal("platform account obtained a business token")
	}
	account, e := f.m.SaveBusinessAccount(f.ctx, management.BusinessAccount{DatabaseID: "default", Username: "E0001", Name: "Employee", BusinessUserID: 10, Active: true, Permissions: []string{"query", "knowledge.read"}}, "employee-default-password", 0)
	must(t, e)
	tokens, _, _ := employeeTokens(t, &realmEnv{f: f, issuer: issuer, resource: base}, "default", "employee-default-password")
	token := tokens["access"]
	session := connectMCP(t, base+"/default", token)
	list, e := session.ListTools(ctx, nil)
	must(t, e)
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if slices.Contains(names, "raw_query") || slices.Contains(names, "find_items") || slices.Contains(names, "read_knowledge") {
		t.Fatalf("tools %v", names)
	}
	out, e := callMCP(t, session, "query", map[string]any{"sql": "SELECT * FROM items"})
	must(t, e)
	if out.IsError {
		t.Fatalf("query %+v", out)
	}
	out, e = callMCP(t, session, "query", map[string]any{"sql": "SELECT id,name FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": "one' OR 1=1 --"}})
	must(t, e)
	if out.IsError {
		t.Fatalf("safe parameter query %+v", out)
	}
	raw, _ := json.Marshal(out.StructuredContent)
	if !bytes.Contains(raw, []byte(`"row_count":0`)) {
		t.Fatalf("parameter injection %s", raw)
	}
	out, e = callMCP(t, session, "query", map[string]any{"sql": "SELECT id,name FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": "one", "userId": 20}})
	if e == nil && !out.IsError {
		t.Fatal("accepted unknown parameter")
	}
	knowledgeDescription1 := "Ownership rules\nScope and applicable questions\nDetailed model guidance"
	_, e = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "items", Name: "Items owner", Description: &knowledgeDescription1, Content: "Query items by owner_id; never sum duplicated joins.", Tables: []string{"items"}, Published: true})
	must(t, e)
	prompts, e := session.ListPrompts(ctx, nil)
	must(t, e)
	raw, _ = json.Marshal(prompts)
	if bytes.Contains(raw, []byte("never sum")) || !bytes.Contains(raw, []byte(`"description"`)) || !bytes.Contains(raw, []byte(`Ownership rules\nScope and applicable questions\nDetailed model guidance`)) {
		t.Fatalf("description disclosure %s", raw)
	}
	prompt, e := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "items"})
	must(t, e)
	raw, _ = json.Marshal(prompt)
	if !bytes.Contains(raw, []byte("never sum")) {
		t.Fatalf("knowledge detail %s", raw)
	}
	adminToken, e := as.SignEmployeeForResource(ctx, f.admin.ID, adminResource)
	must(t, e)
	mgmt := connectMCP(t, adminResource, adminToken)
	managementTools, e := mgmt.ListTools(ctx, nil)
	must(t, e)
	if len(managementTools.Tools) != 3 {
		t.Fatalf("management tools: %+v", managementTools.Tools)
	}
	for _, tool := range managementTools.Tools {
		if !slices.Contains([]string{"datascope_query", "datascope_manage", "datascope_inspect"}, tool.Name) || tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (tool.Name != "datascope_manage") {
			t.Fatalf("unexpected management tool: %+v", tool)
		}
	}
	out, e = callMCP(t, mgmt, "datascope_manage", map[string]any{"action": "create", "args": map[string]any{"resourceType": "user", "username": "created-by-mcp", "name": "Created", "password": "created-test-password"}})
	must(t, e)
	if out.IsError {
		t.Fatalf("management create %+v", out)
	}
	_, e = f.m.ByUsername(ctx, "created-by-mcp")
	must(t, e)
	// Exercise all three grouped tools over OAuth and retain domain audit names.
	for _, tc := range []struct {
		tool, action string
		args         map[string]any
	}{
		{"datascope_query", "get", map[string]any{"resourceType": "database", "database_id": "default"}},
		{"datascope_query", "get", map[string]any{"resourceType": "knowledge", "database_id": "default", "id": "items"}},
		{"datascope_inspect", "test_connection", map[string]any{"resourceType": "database", "database_id": "default"}},
		{"datascope_inspect", "preview_rewrite", map[string]any{"resourceType": "policy", "database_id": "default", "sql": "SELECT * FROM items"}},
		{"datascope_query", "search", map[string]any{"resourceType": "audit", "database_id": "default", "limit": 100}},
	} {
		out, e = callMCP(t, mgmt, tc.tool, map[string]any{"action": tc.action, "args": tc.args})
		must(t, e)
		if out.IsError {
			t.Fatalf("%s %s: %+v", tc.tool, tc.action, out)
		}
	}
	raw, _ = json.Marshal(out.StructuredContent)
	if !bytes.Contains(raw, []byte("action.test_connection")) {
		t.Fatalf("lost domain audit action: %s", raw)
	}
	current, e := f.c.Get("default")
	must(t, e)
	out, e = callMCP(t, mgmt, "datascope_inspect", map[string]any{"action": "validate", "args": map[string]any{"resourceType": "policy", "database_id": "default", "database": current}})
	must(t, e)
	if out.IsError {
		t.Fatalf("validate: %+v", out)
	}
	current.Name = "Renamed over grouped MCP"
	writeArgs := map[string]any{"action": "update", "args": map[string]any{"resourceType": "database", "database_id": "default", "database": current}}
	out, e = callMCP(t, mgmt, "datascope_manage", writeArgs)
	must(t, e)
	if out.IsError {
		t.Fatalf("update: %+v", out)
	}
	// Replaying an old revision must fail, even with an administrator token.
	out, e = callMCP(t, mgmt, "datascope_manage", writeArgs)
	if e == nil && !out.IsError {
		t.Fatal("accepted stale grouped write")
	}
	readerAdminToken, e := as.SignEmployeeForResource(ctx, f.reader.ID, adminResource)
	must(t, e)
	readerMgmt := connectMCP(t, adminResource, readerAdminToken)
	for _, tc := range []struct {
		tool, action string
		args         map[string]any
	}{
		{"datascope_manage", "enable", map[string]any{"resourceType": "user", "user_id": f.admin.ID}},
		{"datascope_query", "get", map[string]any{"resourceType": "knowledge", "database_id": "default", "id": "items"}},
		{"datascope_manage", "update", writeArgs["args"].(map[string]any)},
	} {
		out, e = callMCP(t, readerMgmt, tc.tool, map[string]any{"action": tc.action, "args": tc.args})
		if e == nil && !out.IsError {
			t.Fatalf("grouped tool bypassed permissions: %s %s", tc.tool, tc.action)
		}
	}
	// Published knowledge remains readable without knowledge.write.
	out, e = callMCP(t, readerMgmt, "datascope_query", map[string]any{"action": "read", "args": map[string]any{"resourceType": "knowledge", "database_id": "default", "id": "items"}})
	must(t, e)
	if out.IsError {
		t.Fatalf("published knowledge: %+v", out)
	}
	// A database token must never be accepted by the management resource.
	req, _ := http.NewRequest("POST", adminResource, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	res, e := http.DefaultClient.Do(req)
	must(t, e)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("audience cross-use %d", res.StatusCode)
	}
	account.Permissions = []string{}
	_, e = f.m.SaveBusinessAccount(f.ctx, account, "", account.AuthVersion)
	must(t, e)
	out, e = callMCP(t, session, "query", map[string]any{"sql": "SELECT * FROM items"})
	if e == nil && !out.IsError {
		t.Fatal("revoked token still queries")
	}
	// Resetting a password also invalidates an already issued JWT.
	verifier := &oauth.Verifier{Issuer: issuer, Resource: adminResource, Management: f.m}
	_, e = verifier.Verify(ctx, adminToken, nil)
	must(t, e)
	must(t, f.m.ResetPassword(f.ctx, f.admin.ID, "admin-new-password"))
	_, e = verifier.Verify(ctx, adminToken, nil)
	if e == nil {
		t.Fatal("old JWT valid after reset")
	}
}
func TestAuditFailureIsIsolatedAndGrantEscalation(t *testing.T) {
	f := setup(t)
	_, e := f.app.Call(f.readerContext(), "grant_database_permissions", application.Input{DatabaseID: "default", UserID: f.reader.ID, Permissions: []string{"raw_query"}})
	if !errors.Is(e, management.ErrDenied) {
		t.Fatalf("escalation %v", e)
	}
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", BusinessUserID: 10, Permissions: []string{"query", "grant"}}))
	e = f.m.SaveGrant(f.readerContext(), f.reader.ID, management.Grant{UserID: f.admin.ID, DatabaseID: "default", BusinessUserID: 20, Permissions: []string{"query", "raw_query"}})
	if !errors.Is(e, management.ErrDenied) {
		t.Fatalf("delegation escalation %v", e)
	}
	d, e := f.c.Get("default")
	must(t, e)
	svc, e := f.c.Open(f.readerContext(), d)
	must(t, e)
	defer svc.Store.Close()
	for _, q := range []string{"ATTACH DATABASE '" + f.m.Path + "' AS management", "PRAGMA query_only=0", "PRAGMA writable_schema=ON"} {
		if _, e := svc.Store.RawQuery(f.ctx, q, 20); e == nil {
			t.Fatalf("unsafe raw SQL accepted: %s", q)
		}
	}
	must(t, f.a.Close())
	must(t, f.m.AuditFor("default").Close())
	_, e = svc.Query(f.readerContext(), "SELECT * FROM items", 10, 200)
	if e != nil {
		t.Fatalf("unavailable audit blocked query: %v", e)
	}
}

func TestDirectSQLReplacesNamedTools(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	ctx := context.Background()
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session := connectMCP(t, env.resource+"/default", tokens["access"])
	d, e := f.c.Get("default")
	must(t, e)

	must(t, f.c.Save(f.ctx, *d))
	list, e := session.ListTools(ctx, nil)
	must(t, e)
	if slices.ContainsFunc(list.Tools, func(t *mcp.Tool) bool { return t.Name == "find_items" }) {
		t.Fatal("legacy named tool published")
	}
	if out, e := callMCP(t, session, "find_items", map[string]any{}); e == nil && !out.IsError {
		t.Fatal("legacy tool callable")
	}
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := callMCP(t, session, "query", map[string]any{"sql": "SELECT id FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": "one"}})
			if e != nil {
				failures <- e.Error()
				return
			}
			raw, _ := json.Marshal(out.StructuredContent)
			if out.IsError || !bytes.Contains(raw, []byte(`"row_count":1`)) {
				failures <- string(raw)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for message := range failures {
		t.Fatal(message)
	}
}
func TestKnowledgeVisibilityAndConflict(t *testing.T) {
	f := setup(t)
	knowledgeDescription2 := "Only owned items"
	in := application.Input{DatabaseID: "default", ID: "owner-guide", Name: "Owner guide", Description: &knowledgeDescription2, Content: "Query owner_id in items", Tables: []string{"items"}}
	_, e := f.app.Call(f.ctx, "save_knowledge", in)
	must(t, e)
	_, e = f.app.Call(f.readerContext(), "read_knowledge", application.Input{DatabaseID: "default", ID: in.ID})
	if e == nil {
		t.Fatal("draft knowledge visible")
	}
	in.Published = true
	in.Revision = 1
	_, e = f.app.Call(f.ctx, "save_knowledge", in)
	must(t, e)
	_, e = f.app.Call(f.ctx, "save_knowledge", in)
	if e == nil {
		t.Fatal("stale knowledge overwritten")
	}
	_, e = f.app.Call(f.readerContext(), "read_knowledge", application.Input{DatabaseID: "default", ID: in.ID})
	must(t, e)
	d, e := f.c.Get("default")
	must(t, e)
	d.Config.Tables[0].Columns = []string{"id", "name"}
	must(t, f.c.Save(f.ctx, *d))
	_, e = f.app.Call(f.readerContext(), "read_knowledge", application.Input{DatabaseID: "default", ID: in.ID})
	if e == nil {
		t.Fatal("knowledge exposing restricted fields still visible")
	}
}

func assertNoBusinessSQLite(t *testing.T, f *fixture, id string) {
	t.Helper()
	_, e := os.Stat(filepath.Join(filepath.Dir(f.m.Path), "databases", id, "management.db"))
	if !os.IsNotExist(e) {
		t.Fatal("runtime created a business SQLite database", e)
	}
}
