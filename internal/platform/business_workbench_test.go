package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/adminapi"
	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/mcpserver"
	"datascope-server/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func setupWorkbenchProxy(t *testing.T, mode string) (*fixture, *httptest.Server) {
	t.Helper()
	f, _ := setupAction(t, true)
	apiServer := httptest.NewUnstartedServer(nil)
	mcpServer := httptest.NewUnstartedServer(nil)
	if os.Getenv("DATASCOPE_BROWSER_HARNESS") != "" {
		apiServer.Listener.Close()
		mcpServer.Listener.Close()
		var err error
		apiServer.Listener, err = net.Listen("tcp", "127.0.0.1:18091")
		must(t, err)
		mcpServer.Listener, err = net.Listen("tcp", "127.0.0.1:18090")
		must(t, err)
	}
	issuer := "http://" + apiServer.Listener.Addr().String()
	resource := "http://" + mcpServer.Listener.Addr().String() + "/mcp"
	f.app.MCPBase = resource
	f.app.ManagementMCPURL = issuer + "/mcp/admin"
	f.app.WebURL = issuer
	d, err := f.c.Get("default")
	must(t, err)
	d.MCPEnabled = true
	provider := "demo-oauth"
	if mode == "ACCOUNT" {
		provider = "business-account"
	}
	if mode == "NONE" {
		provider = ""
	}
	d.Authentication = &catalog.Authentication{Type: "local", Source: "GLOBAL", Access: &management.AuthPolicy{ID: "workbench", Name: "Workbench test", Mode: mode, ProviderID: provider, Permissions: []string{"query", "knowledge.read", "action"}, BusinessUserID: 10}}
	direct := d.Actions[0]
	direct.Name = "rename_direct"
	direct.Title = "Direct rename"
	direct.Confirm = false
	d.Actions = append(d.Actions, direct)
	must(t, f.c.Save(f.ctx, *d))
	as, err := oauth.New(oauth.Config{Management: f.m, Catalog: f.c, Issuer: issuer, Resource: resource, KeyPath: filepath.Join(f.dir, "test-key.pem"), ResourceAllowed: func(r string) bool { return f.c.AllowsMCPResource(resource, r) }, DatabaseForResource: func(r string) (string, error) {
		if r == resource+"/default" {
			return "default", nil
		}
		return "", fmt.Errorf("unknown resource")
	}}, f.st)
	must(t, err)
	apiServer.Config.Handler = as.Handler(adminapi.PlatformHandler(f.app, adminapi.ManagedHandler(f.c, &adminapi.Inspector{Endpoint: resource})))
	handler, err := mcpserver.ManagedHTTPHandler(f.c, f.st, mcpserver.Options{Management: f.m, Application: f.app}, "oauth", issuer, resource, "/mcp")
	must(t, err)
	mcpServer.Config.Handler = handler
	apiServer.Start()
	mcpServer.Start()
	t.Cleanup(func() { mcpServer.Close(); apiServer.Close(); as.Close() })
	return f, apiServer
}

func TestBusinessWorkbenchUsesActualMCPAuthentication(t *testing.T) {
	for _, mode := range []string{"ACCOUNT", "OAUTH", "NONE"} {
		t.Run(mode, func(t *testing.T) {
			f, s := setupWorkbenchProxy(t, mode)
			client := login(t, s.URL, "admin", "admin-test-password")
			endpoint := s.URL + "/api/databases/default/business-mcp"
			token := ""
			if mode == "ACCOUNT" {
				status, raw := request(t, client, "POST", endpoint, map[string]any{"operation": "account_token", "username": "E1", "password": "business-password"})
				if status != 200 {
					t.Fatalf("token %d %s", status, raw)
				}
				var out struct {
					Token string `json:"access_token"`
				}
				must(t, json.Unmarshal(raw, &out))
				token = out.Token
			} else if mode == "OAUTH" {
				env := &realmEnv{f: f, issuer: s.URL, resource: f.app.MCPBase}
				// Reuse the existing PKCE flow with its dedicated employee fixture.
				_, err := f.m.SaveBusinessAccount(f.ctx, management.BusinessAccount{DatabaseID: "default", Username: "E0001", Name: "OAuth employee", BusinessUserID: 10, Permissions: []string{"query", "action"}, Active: true}, "employee-default-password", 0)
				must(t, err)
				tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
				token = tokens["access"]
			}
			call := func(name string, args map[string]any, bearer string) (int, *mcp.CallToolResult) {
				raw, _ := json.Marshal(map[string]any{"operation": "call", "name": name, "arguments": args})
				req, err := http.NewRequest("POST", endpoint, bytes.NewReader(raw))
				must(t, err)
				req.Header.Set("Content-Type", "application/json")
				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}
				res, err := client.Do(req)
				must(t, err)
				defer res.Body.Close()
				body, err := io.ReadAll(res.Body)
				must(t, err)
				out := &mcp.CallToolResult{}
				if res.StatusCode == 200 {
					must(t, json.Unmarshal(body, out))
				}
				return res.StatusCode, out
			}
			if mode != "NONE" {
				status, _ := call("list_actions", nil, "")
				if status == 200 {
					t.Fatal("platform session impersonated business")
				}
			}
			for _, name := range []string{"list_actions", "describe_action"} {
				arguments := map[string]any{}
				if name == "describe_action" {
					arguments["name"] = "rename_item"
				}
				status, out := call(name, arguments, token)
				if status != 200 || out.IsError {
					raw, _ := json.Marshal(out)
					t.Fatal(status, string(raw))
				}
			}
			args := map[string]any{"name": "rename_item", "arguments": map[string]any{"id": 1, "name": "via-workbench"}, "request_id": "business-ui"}
			status, out := call("invoke_action", args, token)
			if status != 200 || out.IsError {
				raw, _ := json.Marshal(out)
				t.Fatal(status, string(raw))
			}
			raw, _ := json.Marshal(out.StructuredContent)
			var preview application.ActionResponse
			must(t, json.Unmarshal(raw, &preview))
			if preview.Status != "confirm_required" {
				t.Fatal(string(raw))
			}
			rows, err := f.st.Query(context.Background(), "SELECT name FROM items WHERE id=1", 1)
			must(t, err)
			if rows.Rows[0]["name"] != "one" {
				t.Fatal("preview committed")
			}
			if mode == "NONE" {
				// Anonymous previews cannot invent a confirming account.
				if _, err = f.app.ConfirmAction(context.Background(), preview.ConfirmationToken, "business-password"); err == nil {
					t.Fatal("anonymous confirmation impersonated account")
				}
			} else {
				password := "business-password"
				if mode == "OAUTH" {
					password = "employee-default-password"
				}
				_, err = f.app.ConfirmAction(context.Background(), preview.ConfirmationToken, password)
				must(t, err)
				status, out = call("invoke_action", map[string]any{"name": "rename_item", "arguments": map[string]any{}, "request_id": "business-ui", "result_only": true}, token)
				raw, _ = json.Marshal(out.StructuredContent)
				if status != 200 || out.IsError || !strings.Contains(string(raw), `"status":"done"`) {
					t.Fatal(status, string(raw))
				}
			}
			f.a.Flush(f.ctx)
			events, err := f.a.List(f.ctx, "default", "", 1000, 0)
			must(t, err)
			raw, _ = json.Marshal(events)
			if token != "" && strings.Contains(string(raw), token) {
				t.Fatal("business token logged")
			}
		})
	}
}

// Optional real-browser harness. Every file and business write is isolated in
// t.TempDir; the harness never starts against the user's configured data.
func TestWorkbenchBrowserHarness(t *testing.T) {
	path := os.Getenv("DATASCOPE_BROWSER_HARNESS")
	if path == "" {
		t.Skip("browser harness disabled")
	}
	f, s := setupWorkbenchProxy(t, "OAUTH")
	f.app.WebURL = "http://127.0.0.1:18092"
	prepareClone(t, f)
	// prepareClone installs its own action; restore the browser actions.
	d, err := f.c.Get("default")
	must(t, err)
	d.Actions = []action.Definition{{Name: "rename_item", Title: "确认改名", Description: "Browser isolated write", Confirm: true, Parameters: map[string]action.Parameter{"id": {Type: "integer"}, "name": {Type: "string"}}, Script: "function run(a,ctx){if(!my.one`SELECT id FROM items WHERE id=${a.id}`)deny('invisible');return db.exec`UPDATE items SET name=${a.name} WHERE id=${a.id} AND owner_id=${ctx.userId}`}"}}
	direct := d.Actions[0]
	direct.Name = "rename_direct"
	direct.Title = "直接改名"
	direct.Confirm = false
	d.Actions = append(d.Actions, direct)
	d.Config.Tables[0].DenyUnless = "${userId} > 0"
	d.Config.Tables[0].DenyMessage = "需要有效身份"
	must(t, f.c.Save(f.ctx, *d))
	raw, _ := json.Marshal(map[string]string{"api": s.URL, "mcp": f.app.MCPBase, "directory": f.dir})
	must(t, os.WriteFile(path, raw, 0600))
	if script := os.Getenv("DATASCOPE_BROWSER_SCRIPT"); script != "" {
		cmd := exec.Command("node", script)
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		must(t, err)
		return
	}
	deadline := time.Now().Add(25 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(path + ".done"); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("browser harness timeout")
}
