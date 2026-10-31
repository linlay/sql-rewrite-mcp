package platform_test

import (
	"database/sql"
	"datascope-server/internal/adminapi"
	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestDiscoveryAuthorizationAndDriftRecovery(t *testing.T) {
	f := setup(t)
	_, err := f.app.Call(f.readerContext(), "discover_database", application.Input{DatabaseID: "default"})
	if !errors.Is(err, management.ErrDenied) {
		t.Fatalf("query user scanned metadata: %v", err)
	}
	_, err = f.app.Call(f.readerContext(), "save_annotation", application.Input{DatabaseID: "default", Table: "items", Annotation: &store.Annotation{DisplayName: "secret"}})
	if !errors.Is(err, management.ErrDenied) {
		t.Fatal("query user wrote metadata")
	}
	out, err := f.app.Call(f.ctx, "discover_database", application.Input{DatabaseID: "default"})
	must(t, err)
	d := out.(application.DatabaseView)
	if d.Discovery.Version != 1 {
		t.Fatal("no managed snapshot")
	}
	unchanged, err := f.app.Call(f.ctx, "discover_database", application.Input{DatabaseID: "default"})
	must(t, err)
	if unchanged.(application.DatabaseView).Revision != d.Revision {
		t.Fatal("unchanged scan invalidated an editor revision")
	}
	assertNoBusinessSQLite(t, f, "default")
	comment := "人工说明"
	out, err = f.app.Call(f.ctx, "save_annotation", application.Input{DatabaseID: d.ID, Table: "items", Revision: d.Revision, Annotation: &store.Annotation{DisplayName: "业务记录", Comment: &comment}})
	must(t, err)
	d = out.(application.DatabaseView)
	// A broad PUT cannot forge source snapshots or modify metadata with only policy.write.
	d.Discovery = &catalog.Discovery{Version: 999}
	_, err = f.app.Call(f.ctx, "save_database", application.Input{DatabaseID: d.ID, Database: &d.Database})
	must(t, err)
	saved, err := f.c.Get(d.ID)
	must(t, err)
	if saved.Discovery.Version != 1 {
		t.Fatal("client overwrote authoritative snapshot")
	}
	list, err := f.app.Call(f.readerContext(), "list_databases", application.Input{})
	must(t, err)
	raw, err := json.Marshal(list)
	must(t, err)
	var views []application.DatabaseView
	must(t, json.Unmarshal(raw, &views))
	if len(views) != 1 || views[0].Discovery != nil {
		t.Fatal("metadata leaked into query-only database catalog")
	}
	// Drift breaks strict policy coverage; table inspection still needs to work.
	w, err := sql.Open("sqlite", f.st.Path())
	must(t, err)
	defer w.Close()
	_, err = w.Exec(`CREATE TABLE later(id INTEGER);`)
	must(t, err)
	srv := httptest.NewServer(adminapi.PlatformHandler(f.app, adminapi.ManagedHandler(f.c, &adminapi.Inspector{Endpoint: "http://127.0.0.1:8080/mcp"})))
	defer srv.Close()
	admin := login(t, srv.URL, "admin", "admin-test-password")
	for _, path := range []string{"/api/databases/default/tables", "/api/databases/default/tables/items", "/api/databases/default/tables/items/rows?limit=1", "/api/databases/default/tables/later"} {
		status, body := request(t, admin, "GET", srv.URL+path, nil)
		if status != 200 {
			t.Fatalf("drift recovery blocked %s: %d %s", path, status, body)
		}
	}
	reader := login(t, srv.URL, "reader", "reader-test-password")
	status, _ := request(t, reader, "GET", srv.URL+"/api/databases/default/tables/items/rows", nil)
	if status != 403 {
		t.Fatalf("raw inspection bypassed authorization: %d", status)
	}
	// config.read alone can inspect structure, but cannot scan/write or read rows.
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", Permissions: []string{"config.read"}}))
	_, err = f.app.Call(f.readerContext(), "get_discovery", application.Input{DatabaseID: "default"})
	must(t, err)
	_, err = f.app.Call(f.readerContext(), "discover_database", application.Input{DatabaseID: "default"})
	if !errors.Is(err, management.ErrDenied) {
		t.Fatal("read-only config user updated schema")
	}
}
