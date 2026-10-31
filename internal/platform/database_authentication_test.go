package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/oauth"
)

func TestDatabaseAuthenticationSelectionAndAccountTokens(t *testing.T) {
	f := setup(t)
	original, err := f.c.Get("default")
	must(t, err)
	other := *original
	other.ID = "other"
	other.Name = "Other database"
	other.MCPPath = "other"
	_, err = f.c.Create(f.ctx, other, "")
	must(t, err)
	account, err := f.m.SaveBusinessAccount(f.ctx, management.BusinessAccount{DatabaseID: "default", Username: "employee", Name: "Employee", BusinessUserID: 10, Active: true, Permissions: []string{"query"}}, "employee-password", 0)
	must(t, err)
	platformToken, err := f.m.NewSession(f.ctx, f.admin, "web", time.Hour)
	must(t, err)
	before, err := f.m.AuthConfig(f.ctx)
	must(t, err)
	setMode := func(mode, provider string) {
		t.Helper()
		d, e := f.c.Get("default")
		must(t, e)
		d.Authentication = &catalog.Authentication{Type: "local", Access: &management.AuthPolicy{ID: "database-auth", Name: "This database", Mode: mode, ProviderID: provider, Permissions: []string{"query"}, BusinessUserID: 10}}
		_, e = f.app.Call(f.ctx, "save_database", application.Input{DatabaseID: d.ID, Database: d})
		must(t, e)
		p, e := f.m.ResolveAuth(f.ctx, "DATABASE", "default")
		must(t, e)
		if p.Policy.Mode != mode || p.Provider.ID != provider {
			t.Fatalf("mode not applied: %+v", p)
		}
		unchanged, e := f.m.ResolveAuth(f.ctx, "DATABASE", "other")
		must(t, e)
		if unchanged.Policy.Mode != "OAUTH" {
			t.Fatal("changed another database")
		}
		_, e = f.m.Session(f.ctx, platformToken, "web")
		must(t, e)
	}
	setMode("ACCOUNT", "business-account")
	d, err := f.c.Get("default")
	must(t, err)
	if _, err = f.app.Call(f.readerContext(), "save_database", application.Input{DatabaseID: d.ID, Database: &catalog.Database{ID: d.ID, Driver: d.Driver, Path: d.Path, Authentication: &catalog.Authentication{Type: "local"}}}); err == nil {
		t.Fatal("reader changed authentication")
	}
	invalid := *d
	invalid.Authentication = &catalog.Authentication{Type: "local", Access: &management.AuthPolicy{ID: "invalid", Name: "invalid", Mode: "ACCOUNT", ProviderID: "platform-account"}}
	if err = f.c.Save(f.ctx, invalid); err == nil {
		t.Fatal("accepted platform account for business authentication")
	}
	invalid.Authentication.Access.ProviderID = "demo-oauth"
	if err = f.c.Save(f.ctx, invalid); err == nil {
		t.Fatal("accepted mismatched provider")
	}
	invalid.Authentication.Access.Mode = "NONE"
	invalid.Authentication.Access.ProviderID = ""
	invalid.Authentication.Access.Permissions = []string{"config.write"}
	if err = f.c.Save(f.ctx, invalid); err == nil {
		t.Fatal("anonymous gained management permission")
	}

	host := httptest.NewUnstartedServer(http.NotFoundHandler())
	issuer := "http://" + host.Listener.Addr().String()
	resource := issuer + "/mcp"
	as, err := oauth.New(oauth.Config{Catalog: f.c, Management: f.m, Issuer: issuer, Resource: resource, KeyPath: filepath.Join(f.dir, "signing.pem"), StatePath: f.m.Path, ResourceAllowed: func(r string) bool { return r == resource+"/default" || r == resource+"/other" }, DatabaseForResource: func(r string) (string, error) { return strings.TrimPrefix(r, resource+"/"), nil }}, f.st)
	must(t, err)
	defer as.Close()
	host.Config.Handler = as.Handler(http.NotFoundHandler())
	host.Start()
	defer host.Close()
	h, err := mcpserver.ManagedHTTPHandler(f.c, f.st, mcpserver.Options{Management: f.m, Application: f.app}, "oauth", issuer, resource, "/mcp")
	must(t, err)
	business := httptest.NewServer(h)
	defer business.Close()
	loginToken := func(password, requested string) (int, string) {
		t.Helper()
		status, body := request(t, http.DefaultClient, "POST", issuer+"/auth/databases/default/token", map[string]string{"username": "employee", "password": password, "resource": requested})
		var response struct {
			Access string `json:"access_token"`
		}
		if status == 200 {
			must(t, json.Unmarshal(body, &response))
			if response.Access == "" {
				t.Fatal("missing access token")
			}
		}
		return status, response.Access
	}
	if status, _ := loginToken("wrong", ""); status != 401 {
		t.Fatalf("wrong password: %d", status)
	}
	if status, _ := loginToken("employee-password", resource+"/other"); status != 400 {
		t.Fatalf("cross database token issued: %d", status)
	}
	status, token := loginToken("employee-password", "")
	if status != 200 {
		t.Fatalf("token login: %d", status)
	}
	session := connectMCP(t, business.URL+"/mcp/default", token)
	out, err := callMCP(t, session, "query", map[string]any{"sql": "SELECT id FROM items ORDER BY id"})
	must(t, err)
	raw, _ := json.Marshal(out)
	if out.IsError || !strings.Contains(string(raw), `"row_count":2`) {
		t.Fatalf("token query: %s", raw)
	}
	session.Close()
	verifier := &oauth.Verifier{Catalog: f.c, Management: f.m, DatabaseID: "default", Issuer: issuer + "/auth/databases/default", Resource: resource + "/default"}
	_, err = verifier.Verify(context.Background(), token, nil)
	must(t, err)
	// Mode changes, including switching back, cannot resurrect old access tokens.
	setMode("NONE", "")
	if _, err = verifier.Verify(f.ctx, token, nil); err == nil {
		t.Fatal("account token survived mode change")
	}
	anonymous := connectMCP(t, business.URL+"/mcp/default", "")
	out, err = callMCP(t, anonymous, "query", map[string]any{"sql": "SELECT id FROM items ORDER BY id"})
	must(t, err)
	if out.IsError {
		t.Fatal(out)
	}
	anonymous.Close()
	if status, _ := loginToken("employee-password", ""); status != 400 {
		t.Fatalf("NONE allowed password login: %d", status)
	}
	setMode("OAUTH", "demo-oauth")
	setMode("ACCOUNT", "business-account")
	if _, err = verifier.Verify(f.ctx, token, nil); err == nil {
		t.Fatal("old token resurrected")
	}
	_, fresh := loginToken("employee-password", "")
	_, err = verifier.Verify(f.ctx, fresh, nil)
	must(t, err)
	_, err = f.m.SaveBusinessAccount(f.ctx, account, "reset-password", account.AuthVersion)
	must(t, err)
	if _, err = verifier.Verify(f.ctx, fresh, nil); err == nil {
		t.Fatal("password reset retained token")
	}
	after, err := f.m.AuthConfig(f.ctx)
	must(t, err)
	if before.Revision != after.Revision {
		t.Fatal("database settings rewrote global auth")
	}
	source, err := f.m.Source(f.ctx, "default")
	must(t, err)
	bytes, err := os.ReadFile(filepath.Join(source.Path, "authentication.yml"))
	must(t, err)
	stored := string(bytes)
	if !strings.Contains(stored, `ACCESS_`) {
		t.Fatal("missing durable per-database policy")
	}
}

func TestEnterpriseAccountIssuesDatabaseBearerToken(t *testing.T) {
	f := setup(t)
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil || in["username"] != "employee" || in["password"] != "enterprise-password" {
			http.Error(w, "denied", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"sub": "enterprise-subject", "username": "employee", "employee_id": 10, "active": true, "permissions": []string{"raw_query"}})
	}))
	defer identity.Close()
	cfg := enterpriseConfig(t, f, "ACCOUNT", identity.URL)
	must(t, f.m.SaveAuthConfig(f.ctx, cfg))
	host := httptest.NewUnstartedServer(http.NotFoundHandler())
	issuer := "http://" + host.Listener.Addr().String()
	resource := issuer + "/mcp/default"
	as, err := oauth.New(oauth.Config{Catalog: f.c, Management: f.m, Issuer: issuer, Resource: issuer + "/mcp", KeyPath: filepath.Join(f.dir, "signing.pem"), StatePath: f.m.Path, ResourceAllowed: func(r string) bool { return r == resource }, DatabaseForResource: func(string) (string, error) { return "default", nil }}, f.st)
	must(t, err)
	defer as.Close()
	host.Config.Handler = as.Handler(http.NotFoundHandler())
	host.Start()
	defer host.Close()
	status, body := request(t, http.DefaultClient, "POST", issuer+"/auth/databases/default/token", map[string]string{"username": "employee", "password": "enterprise-password"})
	if status != 200 {
		t.Fatalf("enterprise token: %d %s", status, body)
	}
	var result struct {
		Access string `json:"access_token"`
	}
	must(t, json.Unmarshal(body, &result))
	v := &oauth.Verifier{Catalog: f.c, Management: f.m, DatabaseID: "default", Issuer: issuer + "/auth/databases/default", Resource: resource}
	info, err := v.Verify(f.ctx, result.Access, nil)
	must(t, err)
	a := info.Extra["business_account"].(management.BusinessAccount)
	if a.BusinessUserID != 10 || a.Username != "employee" || len(a.Permissions) != 2 {
		t.Fatalf("wrong enterprise identity: %+v", a)
	}
	if _, err = f.m.BusinessAccount(f.ctx, "default", "employee"); err == nil {
		t.Fatal("enterprise login provisioned local account")
	}
}
