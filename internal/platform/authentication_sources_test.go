package platform_test

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/mcpserver"
)

func TestDatabaseOwnedAndGlobalAuthenticationSources(t *testing.T) {
	f := setup(t)
	d, err := f.c.Get("default")
	must(t, err)
	other := *d
	other.ID = "other"
	other.Name = "Other database"
	other.MCPPath = "other"
	_, err = f.c.Create(f.ctx, other, "")
	must(t, err)
	var globalCalls atomic.Int64
	identity := func(token string, counter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if counter != nil {
				counter.Add(1)
			}
			if r.URL.Path == "/verify" {
				var in map[string]string
				if json.NewDecoder(r.Body).Decode(&in) != nil || in["username"] != "reader" || in["password"] != "enterprise-password" || r.Header.Get("Authorization") != "Bearer private-secret" {
					http.Error(w, "denied", 401)
					return
				}
			} else if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "denied", 401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"sub": "employee-subject", "username": "reader", "employee_id": 10, "active": true, "permissions": []string{"raw_query"}})
		}))
	}
	shared := identity("global-token", &globalCalls)
	defer shared.Close()
	private := identity("private-token", nil)
	defer private.Close()
	provider := func(origin string) management.AuthProvider {
		return management.AuthProvider{ID: "same-provider-id", Name: "Corporate authentication", Type: "ENTERPRISE_OAUTH", Enabled: true, Issuer: origin, AuthorizeURL: origin + "/authorize", TokenURL: origin + "/token", UserInfoURL: origin + "/userinfo", ClientID: "datascope", UsernameClaim: "username"}
	}
	cfg, err := f.m.AuthConfig(f.ctx)
	must(t, err)
	cfg.Providers = append(cfg.Providers, provider(shared.URL))
	must(t, f.m.SaveAuthConfig(f.ctx, cfg))
	before, err := f.m.AuthConfig(f.ctx)
	must(t, err)
	sessionToken, err := f.m.NewSession(f.ctx, f.admin, "web", time.Hour)
	must(t, err)
	configure := func(source, mode string, p *management.AuthProvider) {
		t.Helper()
		d, e := f.c.Get("default")
		must(t, e)
		d.Authentication = &catalog.Authentication{Type: "local", Source: source, Provider: p, Access: &management.AuthPolicy{ID: "database-auth", Name: "This database", Mode: mode, ProviderID: "same-provider-id", Permissions: []string{"query"}}}
		_, e = f.app.Call(f.ctx, "save_database", application.Input{DatabaseID: d.ID, Database: d})
		must(t, e)
	}
	local := provider(private.URL)
	configure("DATABASE", "OAUTH", &local)
	p, err := f.m.ResolveAuth(f.ctx, "DATABASE", "default")
	must(t, err)
	if p.Source != "DATABASE" || p.Provider.Issuer != private.URL {
		t.Fatalf("wrong source: %+v", p)
	}
	a, err := p.VerifyBearer(f.ctx, "default", "private-token")
	must(t, err)
	if a.BusinessUserID != 10 || len(a.Permissions) != 1 || a.Permissions[0] != "query" {
		t.Fatalf("untrusted permissions: %+v", a)
	}
	if _, err = p.VerifyBearer(f.ctx, "default", "global-token"); err == nil {
		t.Fatal("private login fell back to global provider")
	}
	if globalCalls.Load() != 0 {
		t.Fatal("private auth contacted global service")
	}
	otherPolicy, err := f.m.ResolveAuth(f.ctx, "DATABASE", "other")
	must(t, err)
	if otherPolicy.Provider.Type != "DEMO_OAUTH" {
		t.Fatal("another database was changed")
	}
	_, err = f.m.Session(f.ctx, sessionToken, "web")
	must(t, err)
	peer, err := management.Open(f.m.Path, f.a)
	must(t, err)
	persisted, err := peer.ResolveAuth(f.ctx, "DATABASE", "default")
	must(t, err)
	must(t, peer.Close())
	if persisted.Provider.Issuer != private.URL {
		t.Fatal("private connection was not persisted")
	}
	src, err := f.m.Source(f.ctx, "default")
	must(t, err)
	bytes, err := os.ReadFile(filepath.Join(src.Path, "authentication.yml"))
	must(t, err)
	raw := string(bytes)
	if !strings.Contains(raw, private.URL) {
		t.Fatal("private configuration is absent from source management")
	}
	globalRaw, _ := json.Marshal(before)
	if strings.Contains(string(globalRaw), private.URL) {
		t.Fatal("private connection leaked into global config")
	}

	business := httptest.NewUnstartedServer(http.NotFoundHandler())
	resource := "http://" + business.Listener.Addr().String() + "/mcp"
	h, err := mcpserver.ManagedHTTPHandler(f.c, f.st, mcpserver.Options{Management: f.m, Application: f.app}, "oauth", "http://127.0.0.1:8081", resource, "/mcp")
	must(t, err)
	business.Config.Handler = h
	business.Start()
	defer business.Close()
	metadata := func(want string) {
		t.Helper()
		res, e := http.Get(business.URL + "/.well-known/oauth-protected-resource/mcp/default")
		must(t, e)
		defer res.Body.Close()
		var out struct {
			Servers []string `json:"authorization_servers"`
		}
		must(t, json.NewDecoder(res.Body).Decode(&out))
		if res.StatusCode != 200 || len(out.Servers) != 1 || out.Servers[0] != want {
			t.Fatalf("wrong discovery: %d %+v", res.StatusCode, out)
		}
	}
	metadata(private.URL)
	client := connectMCP(t, business.URL+"/mcp/default", "private-token")
	out, err := callMCP(t, client, "query", map[string]any{"sql": "SELECT id FROM items ORDER BY id"})
	must(t, err)
	result, _ := json.Marshal(out)
	if out.IsError || !strings.Contains(string(result), `"row_count":2`) {
		t.Fatalf("private query: %s", result)
	}
	client.Close()
	// Different databases and global providers cannot share an enterprise identity
	// merely because their configured provider ID and subject happen to match.
	claims := map[string]any{"sub": "employee-subject", "username": "reader", "employee_id": json.Number("10")}
	scoped, err := p.Identity("other", claims)
	must(t, err)
	globalIdentity := management.ResolvedAuth{Policy: p.Policy, Provider: provider(shared.URL)}
	sharedIdentity, err := globalIdentity.Identity("default", claims)
	must(t, err)
	if a.ID == scoped.ID || a.ID == sharedIdentity.ID {
		t.Fatal("private enterprise identity was not isolated")
	}
	// Reject malformed source configurations before touching the durable record.
	current, err := f.c.Get("default")
	must(t, err)
	cases := []struct {
		name   string
		change func(*catalog.Authentication)
	}{
		{"source", func(a *catalog.Authentication) { a.Source = "invalid" }},
		{"missing provider", func(a *catalog.Authentication) { a.Provider = nil }},
		{"mismatched provider", func(a *catalog.Authentication) { a.Access.ProviderID = "unknown" }},
		{"non TLS", func(a *catalog.Authentication) { a.Provider.AuthorizeURL = "http://identity.example.com/auth" }},
		{"secret reference", func(a *catalog.Authentication) { a.Provider.SecretEnv = "HOME" }},
		{"platform provider", func(a *catalog.Authentication) { a.Access.Mode = "ACCOUNT"; a.Provider.Type = "PLATFORM_ACCOUNT" }},
		{"global with private config", func(a *catalog.Authentication) { a.Source = "GLOBAL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copyDB := *current
			copyAuth := *current.Authentication
			copyPolicy := *copyAuth.Access
			copyProvider := *copyAuth.Provider
			copyAuth.Access = &copyPolicy
			copyAuth.Provider = &copyProvider
			copyDB.Authentication = &copyAuth
			tc.change(&copyAuth)
			if err := f.c.Save(f.ctx, copyDB); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	changed := *current
	auth := *current.Authentication
	auth.Provider = nil
	auth.Source = "GLOBAL"
	changed.Authentication = &auth
	if _, err = f.app.Call(f.readerContext(), "save_database", application.Input{DatabaseID: changed.ID, Database: &changed}); err == nil {
		t.Fatal("reader changed private authentication")
	}

	// Source choice is explicit and updates protected-resource discovery live.
	configure("GLOBAL", "OAUTH", nil)
	metadata(shared.URL)
	if _, err = f.m.Session(f.ctx, sessionToken, "web"); err != nil {
		t.Fatal("source change expired platform session")
	}
	configure("DATABASE", "OAUTH", &local)
	metadata(private.URL)
	// Account APIs can also be private; secrets remain environment references.
	t.Setenv("DATASCOPE_AUTH_PRIVATE_SOURCE_SECRET", "private-secret")
	accountProvider := management.AuthProvider{ID: "same-provider-id", Name: "Private employee API", Type: "ENTERPRISE_ACCOUNT_API", Enabled: true, AccountURL: private.URL + "/verify", SecretEnv: "DATASCOPE_AUTH_PRIVATE_SOURCE_SECRET", UsernameClaim: "username"}
	configure("DATABASE", "ACCOUNT", &accountProvider)
	p, err = f.m.ResolveAuth(f.ctx, "DATABASE", "default")
	must(t, err)
	a, err = f.m.VerifyAccount(f.ctx, p, "default", "reader", "enterprise-password")
	must(t, err)
	if a.BusinessUserID != 10 {
		t.Fatal(a)
	}
	after, err := f.m.AuthConfig(f.ctx)
	must(t, err)
	afterRaw, _ := json.Marshal(after)
	if string(globalRaw) != string(afterRaw) {
		t.Fatal("database editing changed global configuration")
	}
	// A corrupt or missing private provider fails closed, never uses a same-ID global provider.
	rawAuth, err := os.ReadFile(filepath.Join(src.Path, "authentication.yml"))
	must(t, err)
	var authDoc map[string]any
	must(t, yaml.Unmarshal(rawAuth, &authDoc))
	delete(authDoc["authentication"].(map[string]any), "provider")
	rawAuth, err = yaml.Marshal(authDoc)
	must(t, err)
	err = os.WriteFile(filepath.Join(src.Path, "authentication.yml"), rawAuth, 0600)
	must(t, err)
	res, err := http.Get(business.URL + "/.well-known/oauth-protected-resource/mcp/default")
	must(t, err)
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatalf("broken private source fell back: %d", res.StatusCode)
	}
}
