package platform_test

import (
	"datascope-server/internal/adminapi"
	"datascope-server/internal/application"
	"datascope-server/internal/management"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestDatabaseCreationGeneratesStableIDs(t *testing.T) {
	f := setup(t)
	srv := httptest.NewServer(adminapi.PlatformHandler(f.app, adminapi.ManagedHandler(f.c, &adminapi.Inspector{Endpoint: "http://127.0.0.1:8083/mcp"})))
	defer srv.Close()
	client := login(t, srv.URL, "admin", "admin-test-password")
	ids := map[string]bool{}
	for i := 0; i < 2; i++ {
		status, body := request(t, client, "POST", srv.URL+"/api/databases", map[string]any{"name": "同一个显示名称", "driver": "sqlite", "path": f.st.Path()})
		if status != 200 {
			t.Fatalf("create without ID: %d %s", status, body)
		}
		var d application.DatabaseView
		must(t, json.Unmarshal(body, &d))
		if !strings.HasPrefix(d.ID, "db_") || ids[d.ID] || d.Name != "同一个显示名称" {
			t.Fatal("automatic ID is missing or not unique")
		}
		ids[d.ID] = true
		if d.MCPEnabled || !slices.Equal(d.Tools, []string{"list_tables", "describe_table", "query"}) {
			t.Fatalf("unexpected MCP defaults: enabled=%v tools=%v", d.MCPEnabled, d.Tools)
		}
		// Defaults apply on creation only; deliberately clearing tools survives saves.
		d.Tools = []string{}
		must(t, f.c.Save(f.ctx, d.Database))
		cleared, e := f.c.Get(d.ID)
		must(t, e)
		d.Database = *cleared
		if len(d.Tools) != 0 {
			t.Fatal("saving restored tools explicitly turned off")
		}
		d.Name = "改名后"
		_, err := f.app.Call(f.ctx, "update_database", application.Input{DatabaseID: d.ID, Database: &d.Database})
		must(t, err)
		saved, err := f.c.Get(d.ID)
		must(t, err)
		if saved.ID != d.ID {
			t.Fatal("display name changed internal identity")
		}
	}
	password := "DoNotLeak-DatabasePassword"
	status, body := request(t, client, "POST", srv.URL+"/api/management/call", map[string]any{"action": "test_database_connection", "arguments": map[string]any{"password": password, "database": map[string]any{"name": "test", "driver": "mysql", "connection": map[string]any{"host": "127.0.0.1", "port": 1, "database": "business", "username": "reader", "tls_mode": "disable"}}}})
	if status == 200 || strings.Contains(string(body), password) || strings.Contains(string(body), "password_env") {
		t.Fatalf("direct password was not accepted safely: %d %s", status, body)
	}
	_, err := f.app.Call(f.readerContext(), "create_database", application.Input{Password: password})
	if !errors.Is(err, management.ErrDenied) {
		t.Fatal("unprivileged source creation accepted")
	}
	if _, e := f.m.DatabasePassword(f.ctx, "default"); e == nil {
		t.Fatal("connection test stored a password")
	}
	assertNoBusinessSQLite(t, f, "default")
}
