package platform_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
)

func TestWorkbenchSQLParametersAndIndependentRawPermission(t *testing.T) {
	f := setup(t)
	server := api(t, f)
	client := login(t, server.URL, "reader", "reader-test-password")
	for _, preview := range []bool{true, false} {
		code, raw := request(t, client, "POST", server.URL+"/api/databases/default/rewrite", map[string]any{"sql": "SELECT id,name FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": "one"}, "preview_only": preview})
		if code != 200 || !strings.Contains(string(raw), `"arg_name":"one"`) {
			t.Fatalf("bound query %d %s", code, raw)
		}
		if !preview && !strings.Contains(string(raw), `"row_count":1`) {
			t.Fatal(string(raw))
		}
	}
	for _, parameters := range []map[string]any{{"userId": 20}, {"asOf": "2099"}, {"arg_bad": []any{1}}, {"arg_name": map[string]any{"bad": true}}} {
		code, raw := request(t, client, "POST", server.URL+"/api/rewrite", map[string]any{"sql": "SELECT * FROM items", "parameters": parameters})
		if code == 200 {
			t.Fatalf("invalid binds %s", raw)
		}
	}
	code, _ := request(t, client, "POST", server.URL+"/api/query", map[string]any{"sql": "SELECT * FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": "one"}})
	if code != http.StatusForbidden {
		t.Fatal("rewrite permission granted raw access", code)
	}
	admin := login(t, server.URL, "admin", "admin-test-password")
	for _, value := range []string{"one", "one' OR 1=1 --"} {
		code, raw := request(t, admin, "POST", server.URL+"/api/query", map[string]any{"sql": "SELECT * FROM items WHERE name=:arg_name", "parameters": map[string]any{"arg_name": value}})
		want := `"row_count":1`
		if value != "one" {
			want = `"row_count":0`
		}
		if code != 200 || !strings.Contains(string(raw), want) {
			t.Fatalf("raw binding %d %s", code, raw)
		}
	}
}

func TestKnowledgeReadPermissionIsIndependentOfQueryPolicies(t *testing.T) {
	f := setup(t)
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", Permissions: []string{"knowledge.read"}}))
	_, err := f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "guide", Name: "Guide", Content: "How to use items", Published: true})
	must(t, err)
	d, err := f.c.Get("default")
	must(t, err)
	off := false
	d.Config.Tables[0].Enabled = &off
	d.Config.Tables[0].Columns = []string{"id"}
	must(t, f.c.Save(f.ctx, *d))
	in := application.Input{DatabaseID: "default", ID: "guide"}
	out, err := f.app.ReadKnowledge(f.readerContext(), in, true, false)
	must(t, err)
	if out.(application.Knowledge).Content != "How to use items" {
		t.Fatal(out)
	}
	if _, err = f.app.Call(f.readerContext(), "preview_rewrite", application.Input{DatabaseID: "default", SQL: "SELECT id FROM items"}); err == nil {
		t.Fatal("knowledge permission granted query access")
	}
	save := application.Input{DatabaseID: "default", ID: "guide", Name: "Edited", Content: "Updated guide", Published: true, Revision: 1}
	if _, err = f.app.Call(f.readerContext(), "save_knowledge", save); err == nil {
		t.Fatal("knowledge reader edited content")
	}
	_, err = f.app.Call(f.ctx, "save_knowledge", save)
	must(t, err)
	out, err = f.app.ReadKnowledge(f.readerContext(), in, true, false)
	must(t, err)
	if out.(application.Knowledge).Content != "Updated guide" {
		t.Fatal(out)
	}
	in.DatabaseID = "other"
	if _, err = f.app.ReadKnowledge(f.readerContext(), in, true, false); err == nil {
		t.Fatal("cross-database read")
	}
	in.DatabaseID = "default"
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", Permissions: []string{"query"}}))
	if _, err = f.app.ReadKnowledge(f.readerContext(), in, true, false); err == nil {
		t.Fatal("query permission granted knowledge access")
	}
}

func TestKnowledgeHTTPRejectsRetiredRestrictions(t *testing.T) {
	f := setup(t)
	server := api(t, f)
	client := login(t, server.URL, "admin", "admin-test-password")
	for _, key := range []string{"tables", "release_knowledge_restrictions"} {
		args := map[string]any{"database_id": "default", "id": "guide", "name": "Guide", "content": "Body", "published": true, "revision": 0}
		if key == "tables" {
			args[key] = []string{"items"}
		} else {
			args[key] = true
		}
		status, raw := request(t, client, "POST", server.URL+"/api/management/call", map[string]any{"action": "save_knowledge", "arguments": args})
		if status != http.StatusBadRequest || !strings.Contains(string(raw), key+" 已移除") {
			t.Fatalf("retired %s accepted: %d %s", key, status, raw)
		}
	}
	if _, err := f.app.Call(f.ctx, "get_knowledge", application.Input{DatabaseID: "default", ID: "guide"}); err == nil {
		t.Fatal("rejected knowledge save wrote a record")
	}
}

func TestAuthenticationCloneOptInPreservesTargetAuthorization(t *testing.T) {
	for _, sourceKind := range []string{"GLOBAL", "DATABASE"} {
		t.Run(sourceKind, func(t *testing.T) {
			f := setup(t)
			prepareClone(t, f)
			d, err := f.c.Get("default")
			must(t, err)
			provider := &management.AuthProvider{ID: "own-provider", Name: "Own", Type: "ENTERPRISE_ACCOUNT_API", Enabled: true, AccountURL: "https://identity.example.test/account", SecretEnv: "DATASCOPE_AUTH_CLONE_TEST"}
			d.Authentication = &catalog.Authentication{Type: "local", EmployeeDirectory: true, Source: sourceKind, Access: &management.AuthPolicy{ID: "source-policy", Name: "Source", Mode: "ACCOUNT", ProviderID: "business-account", Permissions: []string{"action", "raw_query"}, BusinessUserID: 20}}
			if sourceKind == "DATABASE" {
				d.Authentication.Provider = provider
				d.Authentication.Access.ProviderID = provider.ID
			}
			must(t, f.c.Save(f.ctx, *d))
			target, err := f.c.Get("target")
			must(t, err)
			target.Authentication = &catalog.Authentication{Type: "local", Source: "GLOBAL", Access: &management.AuthPolicy{ID: "target-policy", Name: "Target", Mode: "OAUTH", ProviderID: "demo-oauth", Permissions: []string{"query"}, BusinessUserID: 99}}
			must(t, f.c.Save(f.ctx, *target))
			target, err = f.c.Get("target")
			must(t, err)
			accountBefore, err := f.m.BusinessAccount(f.ctx, "target", "own")
			must(t, err)
			p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
			if p.Scope.Authentication {
				t.Fatal("auth selected by default")
			}
			p = clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default", Scope: &catalog.CloneScope{Authentication: true}})
			if len(p.Issues) != 0 {
				t.Fatal(p.Issues)
			}
			must(t, cloneCommit(f, cloneConfirmed(p)))
			got, err := f.c.Get("target")
			must(t, err)
			if got.AuthenticationRevision != target.AuthenticationRevision+1 || got.AuthenticationVersion() == target.AuthenticationVersion() {
				t.Fatal("auth version not revoked")
			}
			auth := got.Authentication
			if auth.Source != sourceKind || auth.EmployeeDirectory || auth.Access.Mode != "ACCOUNT" || !reflect.DeepEqual(auth.Access.Permissions, []string{"query"}) || auth.Access.BusinessUserID != 99 || got.Path != target.Path {
				t.Fatalf("target identity/grants changed: %+v", auth)
			}
			if sourceKind == "DATABASE" && auth.Provider.SecretEnv != provider.SecretEnv {
				t.Fatal("secret reference lost")
			}
			accountAfter, err := f.m.BusinessAccount(f.ctx, "target", "own")
			must(t, err)
			if !reflect.DeepEqual(accountBefore, accountAfter) {
				t.Fatal("target account changed")
			}
			pending := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default", Scope: &catalog.CloneScope{Authentication: true}})
			cfg, err := f.m.AuthConfig(f.ctx)
			must(t, err)
			cfg.Providers[0].Name += " changed"
			must(t, f.m.SaveAuthConfig(f.ctx, cfg))
			if cloneCommit(f, cloneConfirmed(pending)) == nil {
				t.Fatal("accepted changed global authentication after preview")
			}
			must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", Permissions: []string{"config.read"}}))
			must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "target", Permissions: management.Permissions}))
			req := catalog.CloneRequest{SourceDatabaseID: "default", Scope: &catalog.CloneScope{Authentication: true}}
			if _, err = f.app.Call(f.readerContext(), "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req}); err == nil {
				t.Fatal("non-system admin copied auth")
			}
		})
	}
}

func TestActionReadOnlyReceiptLookupCannotRepeatWrites(t *testing.T) {
	f, ctx := setupAction(t, true)
	args := map[string]any{"id": float64(1), "name": "confirmed"}
	first, err := f.app.InvokeAction(ctx, "default", "rename_item", "lookup", args)
	must(t, err)
	got, err := f.app.ActionResult(ctx, "default", "rename_item", "lookup")
	must(t, err)
	if got.Status != "confirm_required" || got.ConfirmationToken != first.ConfirmationToken {
		t.Fatal(got)
	}
	_, err = f.app.ConfirmAction(ctx, first.ConfirmationToken, "business-password")
	must(t, err)
	got, err = f.app.ActionResult(ctx, "default", "rename_item", "lookup")
	must(t, err)
	if got.Status != "done" {
		t.Fatal(got)
	}
	if _, err = f.app.ActionResult(f.ctx, "default", "rename_item", "lookup"); err == nil {
		t.Fatal("platform impersonated business")
	}
	a, _ := guard.Business(ctx)
	a.ID++
	other := guard.WithBusiness(ctx, a)
	got, err = f.app.ActionResult(other, "default", "rename_item", "lookup")
	must(t, err)
	if got.Status != "unknown" {
		t.Fatal("receipt crossed account")
	}
	f.m.Actions = management.NewMemoryActions()
	got, err = f.app.ActionResult(ctx, "default", "rename_item", "lookup")
	must(t, err)
	if got.Status != "unknown" {
		t.Fatal("missing receipt replayed")
	}
	rows, err := f.st.Query(ctx, "SELECT name FROM items WHERE id=1", 1)
	must(t, err)
	if rows.Rows[0]["name"] != "confirmed" {
		t.Fatal("lookup mutated data")
	}
}
