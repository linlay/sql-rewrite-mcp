package platform_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/managementmcp"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/oauth"
	"datascope-server/internal/platform"
	"datascope-server/internal/rewrite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type realmEnv struct {
	f                *fixture
	as               *httptest.Server
	ms               *httptest.Server
	issuer, resource string
	accounts         map[string]management.BusinessAccount
}

func setupRealms(t *testing.T) *realmEnv {
	t.Helper()
	f := setup(t)
	d, e := f.c.Get("default")
	must(t, e)
	d.Authentication = &catalog.Authentication{Type: "local", Label: "证券测试员工认证"}
	must(t, f.c.Save(f.ctx, *d))
	must(t, f.c.Add(f.ctx, catalog.Database{ID: "second", Name: "Second", Driver: "sqlite", Path: f.st.Path()}))
	other, e := f.c.Get("second")
	must(t, e)
	other.Config = d.Config
	other.MCPEnabled = true
	other.Tools = []string{"query", "list_tables", "raw_query", "search_knowledge", "read_knowledge"}
	must(t, f.c.Save(f.ctx, *other))
	d, e = f.c.Get("default")
	must(t, e)
	d.Tools = other.Tools
	must(t, f.c.Save(f.ctx, *d))
	accounts := map[string]management.BusinessAccount{}
	for database, id := range map[string]int64{"default": 10, "second": 20} {
		account, e := f.m.SaveBusinessAccount(f.ctx, management.BusinessAccount{DatabaseID: database, Username: "E0001", Name: "Employee", BusinessUserID: id, Active: true, Permissions: []string{"query", "knowledge.read"}}, "employee-"+database+"-password", 0)
		must(t, e)
		accounts[database] = account
	}
	a := httptest.NewUnstartedServer(nil)
	m := httptest.NewUnstartedServer(nil)
	issuer := "http://" + a.Listener.Addr().String()
	resource := "http://" + m.Listener.Addr().String() + "/mcp"
	adminResource := issuer + "/mcp/admin"
	as, e := oauth.New(oauth.Config{Management: f.m, Catalog: f.c, Issuer: issuer, Resource: resource, KeyPath: filepath.Join(f.dir, "realm-key.pem"), ResourceAllowed: func(r string) bool { return r == adminResource || f.c.AllowsMCPResource(resource, r) }, DatabaseForResource: func(r string) (string, error) {
		if r == adminResource {
			return "", nil
		}
		for _, id := range []string{"default", "second"} {
			if r == resource+"/"+id {
				return id, nil
			}
		}
		return "", fmt.Errorf("unknown resource")
	}}, f.st)
	must(t, e)
	f.app.MCPBase = resource
	adminHandler, e := mcpserver.HTTPHandlerWithManagement(managementmcp.New(f.app), nil, "oauth", issuer, adminResource, "/mcp/admin", true, f.m, "")
	must(t, e)
	a.Config.Handler = as.Handler(adminHandler)
	a.Start()
	h, e := mcpserver.ManagedHTTPHandler(f.c, f.st, mcpserver.Options{Management: f.m, Application: f.app, AuthMode: "oauth"}, "oauth", issuer, resource, "/mcp")
	must(t, e)
	m.Config.Handler = h
	m.Start()
	t.Cleanup(func() { m.Close(); a.Close(); as.Close() })
	return &realmEnv{f: f, as: a, ms: m, issuer: issuer, resource: resource, accounts: accounts}
}
func formRequest(t *testing.T, client *http.Client, method, endpoint string, values url.Values) (int, http.Header, string) {
	t.Helper()
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req, e := http.NewRequest(method, endpoint, body)
	must(t, e)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, e := client.Do(req)
	must(t, e)
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	must(t, e)
	return resp.StatusCode, resp.Header, string(b)
}
func employeeTokens(t *testing.T, env *realmEnv, database, password string) (map[string]string, string, url.Values) {
	t.Helper()
	realm := env.issuer + "/auth/databases/" + database
	status, b := request(t, http.DefaultClient, "POST", realm+"/oauth/register", map[string]any{"client_name": "Employee test", "redirect_uris": []string{"http://127.0.0.1/callback"}})
	if status != 201 {
		t.Fatalf("register %d: %s", status, b)
	}
	// redirect_uris is an array; only extract client_id.
	var registration struct {
		ID string `json:"client_id"`
	}
	must(t, json.Unmarshal(b, &registration))
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	params := url.Values{"client_id": {registration.ID}, "redirect_uri": {"http://127.0.0.1/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {challenge}, "resource": {env.resource + "/" + database}, "state": {"employee-state"}}
	jar, e := cookiejar.New(nil)
	must(t, e)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, headers, body := formRequest(t, client, "GET", realm+"/oauth/authorize?"+params.Encode(), nil)
	if status != 200 || !strings.Contains(body, "账号授权") && !strings.Contains(body, "员工认证") {
		t.Fatalf("authorize %d: %s", status, body)
	}
	var nonce string
	endpoint, _ := url.Parse(realm + "/oauth/authorize")
	for _, cookie := range jar.Cookies(endpoint) {
		if strings.HasPrefix(cookie.Name, "oauth_login_") {
			nonce = cookie.Value
		}
	}
	status, headers, body = formRequest(t, client, "POST", realm+"/oauth/authorize", url.Values{"nonce": {nonce}, "emp_no": {"E0001"}, "password": {password}})
	if password != "employee-"+database+"-password" {
		if status != 401 {
			t.Fatalf("wrong realm credentials accepted: %d %s", status, body)
		}
		return nil, "", nil
	}
	if status != 303 {
		t.Fatalf("login %d: %s", status, body)
	}
	callback, e := url.Parse(headers.Get("Location"))
	must(t, e)
	if callback.Query().Get("iss") != realm || callback.Query().Get("state") != "employee-state" {
		t.Fatalf("wrong issuer/state: %s", callback)
	}
	exchange := url.Values{"client_id": {registration.ID}, "redirect_uri": {"http://127.0.0.1/callback"}, "grant_type": {"authorization_code"}, "code": {callback.Query().Get("code")}, "code_verifier": {verifier}, "resource": {env.resource + "/" + database}}
	status, _, body = formRequest(t, client, "POST", realm+"/oauth/token", exchange)
	if status != 200 {
		t.Fatalf("token %d: %s", status, body)
	}
	var tokens map[string]any
	must(t, json.Unmarshal([]byte(body), &tokens))
	result := map[string]string{"access": tokens["access_token"].(string), "refresh": tokens["refresh_token"].(string), "client": registration.ID}
	return result, realm, exchange
}
func TestIndependentBusinessRealms(t *testing.T) {
	env := setupRealms(t)
	// A username may exist in both databases with different passwords and row scopes.
	employeeTokens(t, env, "second", "employee-default-password")
	tokens, realm, _ := employeeTokens(t, env, "default", "employee-default-password")
	second, _, _ := employeeTokens(t, env, "second", "employee-second-password")
	for database, token := range map[string]string{"default": tokens["access"], "second": second["access"]} {
		client := &http.Client{Transport: bearer{token: token}}
		session, e := mcp.NewClient(&mcp.Implementation{Name: "business-test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: env.resource + "/" + database, HTTPClient: client, DisableStandaloneSSE: true}, nil)
		must(t, e)
		out, e := callMCP(t, session, "query", map[string]any{"sql": "SELECT id FROM items ORDER BY id"})
		must(t, e)
		raw, _ := json.Marshal(out)
		expected := `"row_count":2`
		if database == "second" {
			expected = `"row_count":1`
		}
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("wrong row scope %s: %s", database, raw)
		}
		tools, e := session.ListTools(context.Background(), nil)
		must(t, e)
		for _, tool := range tools.Tools {
			if tool.Name == "raw_query" {
				t.Fatal("raw query exposed to ordinary employee")
			}
		}
		session.Close()
	}
	for _, endpoint := range []string{env.resource + "/second", env.issuer + "/mcp/admin"} {
		code, _, _ := formRequest(t, &http.Client{Transport: bearer{token: tokens["access"]}}, "POST", endpoint, nil)
		if code != 401 {
			t.Fatalf("cross-realm token accepted at %s: %d", endpoint, code)
		}
	}
	// Employee credentials do not authenticate to the platform account system.
	if _, e := env.f.m.Authenticate(context.Background(), "E0001", "employee-default-password"); e == nil {
		t.Fatal("employee became a platform user")
	}
	if _, e := env.f.m.AuthenticateBusiness(context.Background(), "default", "admin", "admin-test-password"); e == nil {
		t.Fatal("platform admin became a business user")
	}
	// Per-database MCP discovery selects the realm before the login flow.
	code, b := request(t, http.DefaultClient, "GET", env.ms.URL+"/.well-known/oauth-protected-resource/mcp/default", nil)
	if code != 200 || !strings.Contains(string(b), realm) {
		t.Fatalf("wrong database discovery %d: %s", code, b)
	}
	code, b = request(t, http.DefaultClient, "GET", env.issuer+"/.well-known/oauth-authorization-server/auth/databases/default", nil)
	if code != 200 || !strings.Contains(string(b), realm+"/oauth/authorize") {
		t.Fatalf("wrong authorization discovery %d: %s", code, b)
	}
	// Reset invalidates access and refresh tokens for this database only.
	account := env.accounts["default"]
	_, e := env.f.m.SaveBusinessAccount(env.f.ctx, account, "new-employee-password", account.AuthVersion)
	must(t, e)
	code, _, _ = formRequest(t, &http.Client{Transport: bearer{token: tokens["access"]}}, "POST", env.resource+"/default", nil)
	if code != 401 {
		t.Fatal("reset retained access token")
	}
	code, _, _ = formRequest(t, http.DefaultClient, "POST", realm+"/oauth/token", url.Values{"client_id": {tokens["client"]}, "grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh"]}, "resource": {env.resource + "/default"}})
	if code != 400 {
		t.Fatal("reset retained refresh token")
	}
	if _, e := env.f.m.AuthenticateBusiness(context.Background(), "second", "E0001", "employee-second-password"); e != nil {
		t.Fatal("reset affected other realm")
	}
}

func TestBusinessAccountManagementAndKnowledge(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	_, e := f.app.Call(f.readerContext(), "save_business_account", application.Input{DatabaseID: "default", Account: &management.BusinessAccount{DatabaseID: "default", Username: "evil", Name: "evil", BusinessUserID: 20}, Password: "a-long-password"})
	if e == nil {
		t.Fatal("reader created business identity")
	}
	knowledgeDescription1 := "员工业务知识"
	_, e = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "business-guide", Name: "查询指南", Description: &knowledgeDescription1, Content: "员工独立认证知识正文", Published: true})
	must(t, e)
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session, e := mcp.NewClient(&mcp.Implementation{Name: "business-knowledge", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: env.resource + "/default", HTTPClient: &http.Client{Transport: bearer{token: tokens["access"]}}, DisableStandaloneSSE: true}, nil)
	must(t, e)
	defer session.Close()
	out, e := session.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "business-guide"})
	must(t, e)
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "员工独立认证知识正文") {
		t.Fatalf("knowledge unavailable: %s", raw)
	}
	// Configuration changes invalidate issued tokens and refresh grants.
	d, e := f.c.Get("default")
	must(t, e)
	d.Authentication.Label = "Changed realm"
	must(t, f.c.Save(f.ctx, *d))
	status, _, _ := formRequest(t, &http.Client{Transport: bearer{token: tokens["access"]}}, "POST", env.resource+"/default", nil)
	if status != 401 {
		t.Fatal("changed authentication retained old token")
	}
}

func TestBusinessRealmRevocationAndManagementBoundary(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	// The same name may also be a platform administrator's managed user, with
	// independent credentials and no implicit business credential fallback.
	_, e := f.m.CreateUser(f.ctx, "E0001", "Platform employee alias", "platform-alias-password")
	must(t, e)
	employeeTokens(t, env, "default", "platform-alias-password")
	tokens, realm, _ := employeeTokens(t, env, "default", "employee-default-password")
	a := env.accounts["default"]
	input := application.Input{DatabaseID: "default", Account: &a, Revision: int(a.AuthVersion)}
	a.DatabaseID = "second"
	if _, e = f.app.Call(f.ctx, "save_business_account", input); e == nil {
		t.Fatal("cross-database account save accepted")
	}
	a = env.accounts["default"]
	a.Active = false
	_, e = f.m.SaveBusinessAccount(f.ctx, a, "", a.AuthVersion)
	must(t, e)
	code, _, _ := formRequest(t, &http.Client{Transport: bearer{token: tokens["access"]}}, "POST", env.resource+"/default", nil)
	if code != 401 {
		t.Fatal("disabled account retained token")
	}
	code, _, _ = formRequest(t, http.DefaultClient, "POST", realm+"/oauth/token", url.Values{"client_id": {tokens["client"]}, "grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh"]}, "resource": {env.resource + "/default"}})
	if code != 400 {
		t.Fatal("disabled account retained refresh")
	}
	// Stale updates cannot overwrite a concurrent reset or permission change.
	if _, e = f.m.SaveBusinessAccount(f.ctx, a, "", a.AuthVersion); e == nil {
		t.Fatal("stale account revision accepted")
	}
}

func TestAuthenticationSettingsDoNotResurrectBusinessTokens(t *testing.T) {
	env := setupRealms(t)
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	d, e := env.f.c.Get("default")
	must(t, e)
	original := *d.Authentication
	d.Authentication.Label = "Temporary login label"
	must(t, env.f.c.Save(env.f.ctx, *d))
	d, e = env.f.c.Get("default")
	must(t, e)
	d.Authentication = &original
	must(t, env.f.c.Save(env.f.ctx, *d))
	code, _, _ := formRequest(t, &http.Client{Transport: bearer{token: tokens["access"]}}, "POST", env.resource+"/default", nil)
	if code != 401 {
		t.Fatal("switching authentication back resurrected old token")
	}
}

func TestBusinessMCPRejectsPlatformAuthentication(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	d, e := f.c.Get("default")
	must(t, e)
	d.Authentication = &catalog.Authentication{Type: "platform"}
	if e = f.c.Save(f.ctx, *d); e == nil {
		t.Fatal("platform authentication configuration accepted")
	}
	if e = f.c.Add(f.ctx, catalog.Database{ID: "legacy", Name: "Legacy", Driver: "sqlite", Path: f.st.Path(), Authentication: d.Authentication}); e == nil {
		t.Fatal("new database accepted platform authentication")
	}
	// Omitting optional settings still uses business accounts and OAuth.
	d.Authentication = nil
	must(t, f.c.Save(f.ctx, *d))
	d, e = f.c.Get("default")
	must(t, e)
	if !d.LocalAuthentication() {
		t.Fatal("absent settings fell back to platform authentication")
	}
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session := connectMCP(t, env.resource+"/default", tokens["access"])
	if _, e = session.ListTools(f.ctx, nil); e != nil {
		t.Fatal(e)
	}
	// Simulate a previously issued platform JWT with the correct key, issuer
	// and audience. It must be rejected even when the platform user has grants.
	issuer := d.AuthorizationIssuer(env.issuer)
	resource := env.resource + "/default"
	old, e := oauth.New(oauth.Config{Management: f.m, Issuer: issuer, Resource: resource, KeyPath: filepath.Join(f.dir, "realm-key.pem"), DatabaseForResource: func(string) (string, error) { return "", nil }}, nil)
	must(t, e)
	defer old.Close()
	token, e := old.SignEmployeeForResource(f.ctx, f.admin.ID, resource)
	must(t, e)
	for _, c := range []*catalog.Catalog{f.c, nil} {
		verifier := &oauth.Verifier{Management: f.m, Catalog: c, Issuer: issuer, Resource: resource, DatabaseID: d.ID}
		if _, e = verifier.Verify(f.ctx, token, nil); e == nil {
			t.Fatal("platform token accepted by business MCP")
		}
	}
}

func TestDemoRealmInitializationAndEmployeeDirectory(t *testing.T) {
	f := setup(t)
	db, e := sql.Open("sqlite", f.st.Path())
	must(t, e)
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE roles(id INTEGER PRIMARY KEY,code TEXT,name TEXT);
 CREATE TABLE employees(employee_id INTEGER PRIMARY KEY,emp_no TEXT,name TEXT,role_id INTEGER,branch_code TEXT,department_code TEXT,status TEXT);
 INSERT INTO roles VALUES(1,'MANAGER','客户经理');
 INSERT INTO employees VALUES(1,'E0001','在职员工',1,NULL,NULL,'active'),(2,'E0002','离职员工',1,NULL,NULL,'resigned');`)
	must(t, e)
	configured, e := f.c.Get("default")
	must(t, e)
	configured.Config.Tables = append(configured.Config.Tables, &rewrite.Policy{Table: "roles", Scheme: "CUSTOM", Passthrough: true}, &rewrite.Policy{Table: "employees", Scheme: "CUSTOM", Passthrough: true})
	must(t, f.c.Save(f.ctx, *configured))
	if e = platform.InitializeDemoRealm(f.ctx, f.c, ""); e == nil {
		t.Fatal("empty demo password accepted")
	}
	must(t, platform.InitializeDemoRealm(f.ctx, f.c, "initial-employee-password"))
	d, e := f.c.Get("default")
	must(t, e)
	if !d.LocalAuthentication() || !d.Authentication.EmployeeDirectory {
		t.Fatal("demo realm not migrated")
	}
	a, e := f.m.AuthenticateBusiness(f.ctx, "default", "E0001", "initial-employee-password")
	must(t, e)
	if a.BusinessUserID != 1 {
		t.Fatal("wrong employee mapping")
	}
	d.Config = nil
	if _, e = f.c.BusinessAccount(f.ctx, d, "E0001"); e != nil {
		t.Fatal("employee authentication depends on query configuration", e)
	}
	if _, e = f.m.ByUsername(f.ctx, "E0001"); e == nil {
		t.Fatal("demo employee imported into platform users")
	}
	if _, e = f.c.BusinessAccount(f.ctx, d, "E0002"); e == nil {
		t.Fatal("resigned employee accepted")
	}
	_, e = f.m.SaveBusinessAccount(f.ctx, a, "changed-employee-password", a.AuthVersion)
	must(t, e)
	must(t, platform.InitializeDemoRealm(f.ctx, f.c, "different-startup-password"))
	_, e = f.m.AuthenticateBusiness(f.ctx, "default", "E0001", "changed-employee-password")
	must(t, e)
	_, e = db.Exec("UPDATE employees SET status='resigned' WHERE employee_id=1")
	must(t, e)
	if _, e = f.c.BusinessAccount(f.ctx, d, "E0001"); e == nil {
		t.Fatal("employee resignation not applied live")
	}
}

func TestRootAuthorizationRoutesToConfiguredDatabaseRealm(t *testing.T) {
	env := setupRealms(t)
	status, body := request(t, http.DefaultClient, "POST", env.issuer+"/oauth/register", map[string]any{"redirect_uris": []string{"http://127.0.0.1/callback"}})
	if status != 201 {
		t.Fatalf("register: %d %s", status, body)
	}
	var client struct {
		ID string `json:"client_id"`
	}
	must(t, json.Unmarshal(body, &client))
	params := url.Values{"client_id": {client.ID}, "redirect_uri": {"http://127.0.0.1/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {strings.Repeat("a", 43)}, "resource": {env.resource + "/default"}, "state": {"preserve-state"}}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, headers, _ := formRequest(t, noRedirect, "GET", env.issuer+"/oauth/authorize?"+params.Encode(), nil)
	if status != 303 {
		t.Fatalf("root did not route employee login: %d", status)
	}
	target, e := url.Parse(headers.Get("Location"))
	must(t, e)
	if target.Scheme+"://"+target.Host+target.Path != env.issuer+"/auth/databases/default/oauth/authorize" || target.RawQuery != params.Encode() {
		t.Fatalf("wrong realm or lost parameters: %s", target)
	}
	status, _, _ = formRequest(t, noRedirect, "GET", env.issuer+"/auth/databases/second/oauth/authorize?"+params.Encode(), nil)
	if status != 400 {
		t.Fatalf("realm accepted another database resource: %d", status)
	}
}
