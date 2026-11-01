package demoactions_test

import (
	"context"
	"datascope-server/internal/adminapi"
	"datascope-server/internal/catalog"
	"datascope-server/internal/demoactions"
	"datascope-server/internal/management"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/oauth"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// Execute the actual SQL embedded in the shipped HTML through permission
// rewriting, so field drift or unsupported subqueries fail before release.
func TestSecuritiesViewQueriesAndInstallation(t *testing.T) {
	app, ctx := platformFixture(t)
	if _, err := demoactions.Install(ctx, app, "default", nil, false); err != nil {
		t.Fatal(err)
	}
	d, err := app.Catalog.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Views) != 3 || d.QueryViewID != "query" {
		t.Fatal("missing view bundle")
	}
	for _, a := range d.Actions {
		if a.ViewID != "action" {
			t.Fatal("missing action view", a.Name)
		}
	}
	v, err := d.View("securities_account", true)
	if err != nil {
		t.Fatal(err)
	}
	queries := regexp.MustCompile(`'(SELECT [^']+)'`).FindAllStringSubmatch(v.HTML, -1)
	if len(queries) != 6 {
		t.Fatal("expected 6 account queries", len(queries))
	}
	svc, err := app.Catalog.Open(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Store.Close()
	for i, q := range queries {
		result, err := svc.QueryWithParameters(ctx, q[1], 1, 200, map[string]any{"arg_account": "880000000079"})
		if err != nil {
			t.Fatalf("query %d: %v\n%s", i, err, q[1])
		}
		if i == 0 && len(result.Rows) != 1 {
			t.Fatal("manager cannot see demo account")
		}
	}
}

// The opt-in browser run uses isolated fixture services and business records.
func TestSecuritiesViewBrowserHarness(t *testing.T) {
	script := os.Getenv("DATASCOPE_VIEW_BROWSER_SCRIPT")
	if script == "" {
		t.Skip("browser harness disabled")
	}
	app, ctx := platformFixture(t)
	if _, err := demoactions.Install(ctx, app, "default", []string{"E0001", "E0121"}, false); err != nil {
		t.Fatal(err)
	}
	d, err := app.Catalog.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	d.Authentication = &catalog.Authentication{Type: "local", EmployeeDirectory: true, Source: "GLOBAL", Access: &management.AuthPolicy{ID: "views-browser", Name: "Views browser", Mode: "ACCOUNT", ProviderID: "business-account"}}
	if err = app.Catalog.Save(ctx, *d); err != nil {
		t.Fatal(err)
	}
	apiServer, mcpServer := httptest.NewUnstartedServer(nil), httptest.NewUnstartedServer(nil)
	apiServer.Listener.Close()
	mcpServer.Listener.Close()
	apiServer.Listener, err = net.Listen("tcp", "127.0.0.1:18191")
	if err != nil {
		t.Fatal(err)
	}
	mcpServer.Listener, err = net.Listen("tcp", "127.0.0.1:18190")
	if err != nil {
		apiServer.Listener.Close()
		t.Fatal(err)
	}
	app.WebURL = "http://127.0.0.1:18192"
	issuer := "http://" + apiServer.Listener.Addr().String()
	app.MCPBase = "http://" + mcpServer.Listener.Addr().String() + "/mcp"
	app.ManagementMCPURL = issuer + "/mcp/admin"
	st, err := app.Catalog.OpenStore(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	as, err := oauth.New(oauth.Config{Management: app.Management, Catalog: app.Catalog, Issuer: issuer, Resource: app.MCPBase, KeyPath: filepath.Join(t.TempDir(), "signing.pem"), ResourceAllowed: func(r string) bool { return app.Catalog.AllowsMCPResource(app.MCPBase, r) }, DatabaseForResource: func(r string) (string, error) {
		if r == app.MCPBase+"/default" {
			return "default", nil
		}
		return "", fmt.Errorf("unknown resource")
	}}, st)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	apiServer.Config.Handler = as.Handler(adminapi.PlatformHandler(app, adminapi.ManagedHandler(app.Catalog, &adminapi.Inspector{Endpoint: app.MCPBase})))
	h, err := mcpserver.ManagedHTTPHandler(app.Catalog, st, mcpserver.Options{Management: app.Management, Application: app}, "oauth", issuer, app.MCPBase, "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	mcpServer.Config.Handler = h
	apiServer.Start()
	mcpServer.Start()
	defer apiServer.Close()
	defer mcpServer.Close()
	command := exec.CommandContext(context.Background(), "node", script)
	command.Env = append(os.Environ(), "ADMIN_API_URL="+issuer, "WEB_PORT=18192")
	out, err := command.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
}
