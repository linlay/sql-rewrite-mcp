package platform_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"datascope-server/internal/action"
	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
)

func cloneTarget(t *testing.T, f *fixture, ddl string) *catalog.Database {
	t.Helper()
	path := filepath.Join(f.dir, "target.sqlite")
	db, err := sql.Open("sqlite", path)
	must(t, err)
	_, err = db.Exec(ddl)
	must(t, err)
	db.Close()
	d, err := f.c.Create(f.ctx, catalog.Database{ID: "target", Name: "Target own name", Driver: "sqlite", Path: path, MCPPath: "target_own_route"}, "")
	must(t, err)
	return d
}

func prepareClone(t *testing.T, f *fixture) *catalog.Database {
	t.Helper()
	d := cloneTarget(t, f, `CREATE TABLE items(id INTEGER PRIMARY KEY,owner_id INTEGER,name TEXT,secret TEXT); INSERT INTO items VALUES(99,99,'target data','private'); CREATE TABLE target_only(id INTEGER);`)
	source, err := f.c.Get("default")
	must(t, err)
	source.Actions = []action.Definition{{Name: "rename", Title: "Rename", Description: "Rename item", Script: "function run(a){return db.exec`UPDATE items SET name='new' WHERE id=1`;}", Parameters: map[string]action.Parameter{}}}
	source.WriteEnabled = true
	must(t, f.c.Save(f.ctx, *source))
	source, err = f.c.Get("default")
	must(t, err)
	_, err = f.c.Discover(f.ctx, "default")
	must(t, err)
	source, err = f.c.Get("default")
	must(t, err)
	comment := "Cloned comment"
	_, err = f.c.SaveAnnotation(f.ctx, "default", "items", "name", source.Revision, store.Annotation{DisplayName: "Cloned name", Comment: &comment})
	must(t, err)
	_, err = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "guide", Name: "Guide", Content: "multi\nline", Tables: []string{"items"}, Published: true})
	must(t, err)
	_, err = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: d.ID, ID: "old", Name: "Old", Content: "target only", Tables: []string{"items"}})
	must(t, err)
	_, err = f.m.SaveBusinessAccount(f.ctx, management.BusinessAccount{DatabaseID: d.ID, Username: "own", Name: "Own", BusinessUserID: 99, Active: true, Permissions: []string{"query"}}, "own-account-password", 0)
	must(t, err)
	return d
}

func clonePreview(t *testing.T, f *fixture, req catalog.CloneRequest) *catalog.ClonePreview {
	t.Helper()
	out, err := f.app.Call(f.ctx, "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
	must(t, err)
	return out.(*catalog.ClonePreview)
}
func cloneConfirmed(p *catalog.ClonePreview) catalog.CloneRequest {
	return catalog.CloneRequest{SourceDatabaseID: p.SourceDatabaseID, Scope: &p.Scope, SourceRevision: p.SourceRevision, TargetRevision: p.TargetRevision, SourceToken: p.SourceToken, TargetToken: p.TargetToken, Confirmation: p.Confirmation, Confirmed: true}
}
func cloneCommit(f *fixture, req catalog.CloneRequest) error {
	_, err := f.app.Call(f.ctx, "clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
	return err
}
func managementFiles(t *testing.T, f *fixture, id string) map[string]string {
	t.Helper()
	s, err := f.m.Source(f.ctx, id)
	must(t, err)
	files := map[string]string{}
	must(t, filepath.WalkDir(s.Path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			rel, _ := filepath.Rel(s.Path, path)
			files[rel] = string(b)
		}
		return err
	}))
	return files
}

func TestConfigurationCloneAPIAtomicPreservation(t *testing.T) {
	f := setup(t)
	target := prepareClone(t, f)
	before := managementFiles(t, f, target.ID)
	sourceBefore := managementFiles(t, f, "default")
	receipt := management.ActionRequest{DatabaseID: target.ID, Actor: "own", RequestID: "target-receipt", Fingerprint: "target-private", State: "running"}
	_, fresh, err := f.m.ReserveAction(f.ctx, receipt)
	must(t, err)
	if !fresh {
		t.Fatal("receipt setup failed")
	}
	sourceReceipt := management.ActionRequest{DatabaseID: "default", Actor: "own", RequestID: "source-receipt", Fingerprint: "source-private", State: "running"}
	_, _, err = f.m.ReserveAction(f.ctx, sourceReceipt)
	must(t, err)
	p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	if len(p.Issues) != 0 || len(p.Items) != 4 {
		t.Fatalf("preview: %+v", p)
	}
	if !reflect.DeepEqual(before, managementFiles(t, f, target.ID)) {
		t.Fatal("preview wrote configuration")
	}
	s := api(t, f)
	client := login(t, s.URL, "admin", "admin-test-password")
	status, raw := request(t, client, "POST", s.URL+"/api/management/call", map[string]any{"action": "clone_configuration", "arguments": application.Input{DatabaseID: target.ID, Clone: ptrClone(cloneConfirmed(p))}})
	if status != http.StatusOK {
		t.Fatalf("clone API %d: %s", status, raw)
	}
	var result catalog.ClonePreview
	must(t, json.Unmarshal(raw, &result))
	if !result.Applied {
		t.Fatal("not applied")
	}
	saved, err := f.c.Get(target.ID)
	must(t, err)
	if saved.Revision != target.Revision+1 || saved.ID != target.ID || saved.Name != target.Name || saved.Path != target.Path || saved.MCPRoute() != target.MCPRoute() || saved.MCPEnabled != target.MCPEnabled || saved.WriteEnabled || !reflect.DeepEqual(saved.Authentication, target.Authentication) {
		t.Fatalf("target settings changed: %+v", saved)
	}
	if len(saved.Actions) != 1 || saved.Actions[0].Name != "rename" {
		t.Fatalf("actions: %+v", saved.Actions)
	}
	after := managementFiles(t, f, target.ID)
	for _, name := range []string{"account.yml", "grant.yml", "authentication.yml"} {
		if before[name] != after[name] {
			t.Fatalf("changed preserved %s", name)
		}
	}
	if !reflect.DeepEqual(sourceBefore, managementFiles(t, f, "default")) {
		t.Fatal("source modified")
	}
	retained, fresh, err := f.m.ReserveAction(f.ctx, receipt)
	must(t, err)
	if fresh || retained.Fingerprint != "target-private" {
		t.Fatal("target receipt replaced")
	}
	sourceReceipt.DatabaseID = target.ID
	_, fresh, err = f.m.ReserveAction(f.ctx, sourceReceipt)
	must(t, err)
	if !fresh {
		t.Fatal("source receipt copied")
	}
	if !strings.Contains(after["tables/items.yml"], "Cloned name") {
		t.Fatal("missing annotation")
	}
	if _, exists := after["knowledges/old.yml"]; exists {
		t.Fatal("target-only knowledge retained")
	}
	records, err := f.m.Records(f.ctx, "knowledge")
	must(t, err)
	for _, record := range records {
		if record.Key == "target/guide" {
			var k application.Knowledge
			must(t, json.Unmarshal([]byte(record.Data), &k))
			if k.DatabaseID != "target" || k.ID != "guide" || k.Content != "multi\nline" {
				t.Fatalf("knowledge %+v", k)
			}
		}
	}
	db, err := sql.Open("sqlite", target.Path)
	must(t, err)
	defer db.Close()
	var name string
	must(t, db.QueryRow("SELECT name FROM items WHERE id=99").Scan(&name))
	if name != "target data" {
		t.Fatal("business data changed")
	}
	if len(saved.Discovery.Tables) != 2 {
		t.Fatal("source schema replaced target's real schema")
	}
	if saved.Config.Validate() != nil {
		t.Fatal("invalid resulting policies")
	}
	account, err := f.m.AuthenticateBusiness(f.ctx, target.ID, "own", "own-account-password")
	must(t, err)
	if account.BusinessUserID != 99 {
		t.Fatal("identity changed")
	}
	// Whole-clone audit is best effort and contains no scripts or credentials.
	src, err := f.m.Source(f.ctx, target.ID)
	must(t, err)
	src.Audit.Flush(f.ctx)
	events, err := src.Audit.List(f.ctx, target.ID, "", 100, 0)
	must(t, err)
	found := false
	for _, ev := range events {
		found = found || ev.Kind == "database.clone"
	}
	if !found {
		t.Fatal("clone audit absent")
	}
}
func ptrClone(r catalog.CloneRequest) *catalog.CloneRequest { return &r }

func TestConfigurationCloneRejectsChangesAndRequiresConfirmation(t *testing.T) {
	f := setup(t)
	prepareClone(t, f)
	p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	req := cloneConfirmed(p)
	before := managementFiles(t, f, "target")
	for _, mutate := range []func(*catalog.CloneRequest){func(r *catalog.CloneRequest) { r.Confirmed = false }, func(r *catalog.CloneRequest) { r.Confirmation = "forged" }, func(r *catalog.CloneRequest) { r.SourceRevision++ }, func(r *catalog.CloneRequest) { r.SourceToken = "" }, func(r *catalog.CloneRequest) { r.Scope = &catalog.CloneScope{Knowledge: true} }} {
		r := req
		mutate(&r)
		if cloneCommit(f, r) == nil {
			t.Fatal("accepted stale/missing confirmation")
		}
		if !reflect.DeepEqual(before, managementFiles(t, f, "target")) {
			t.Fatal("failed clone wrote partial state")
		}
	}
	// Knowledge saves do not increment Database.Revision; full-state tokens must catch them.
	_, err := f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "guide", Name: "Changed", Content: "changed", Revision: 1})
	must(t, err)
	if cloneCommit(f, req) == nil {
		t.Fatal("accepted concurrent source knowledge change")
	}
	p = clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	req = cloneConfirmed(p)
	_, err = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "target", ID: "new", Name: "New", Content: "changed"})
	must(t, err)
	if cloneCommit(f, req) == nil {
		t.Fatal("accepted concurrent target knowledge change")
	}
	p = clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	req = cloneConfirmed(p)
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "target.sqlite"))
	must(t, err)
	_, err = db.Exec("ALTER TABLE items ADD COLUMN changed TEXT")
	must(t, err)
	db.Close()
	if cloneCommit(f, req) == nil {
		t.Fatal("accepted physical schema change after preview")
	}
	p = clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	req = cloneConfirmed(p)
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errorsOut <- cloneCommit(f, req) }()
	}
	wg.Wait()
	close(errorsOut)
	success := 0
	for err := range errorsOut {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent confirmations applied %d times", success)
	}
}

func TestConfigurationCloneMissingStructureAndStaticReferences(t *testing.T) {
	f := setup(t)
	target := cloneTarget(t, f, `CREATE TABLE items(id INTEGER PRIMARY KEY,owner_id INTEGER,name TEXT);`)
	_, err := f.c.Discover(f.ctx, "default")
	must(t, err)
	before := managementFiles(t, f, target.ID)
	p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	if !strings.Contains(strings.Join(p.Issues, " "), "items.secret") {
		t.Fatalf("missing column not reported: %+v", p)
	}
	if cloneCommit(f, cloneConfirmed(p)) == nil {
		t.Fatal("accepted incompatible tables")
	}
	if !reflect.DeepEqual(before, managementFiles(t, f, target.ID)) {
		t.Fatal("incompatible clone changed state")
	}
	source, err := f.c.Get("default")
	must(t, err)
	source.Actions = []action.Definition{{Name: "missing", Title: "Missing", Description: "Missing dependency", Script: "function run(){return db.one`SELECT value FROM absent`;}", Parameters: map[string]action.Parameter{}}}
	must(t, f.c.Save(f.ctx, *source))
	p = clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default", Scope: &catalog.CloneScope{Actions: true}})
	if !strings.Contains(strings.Join(p.Issues, " "), "absent") {
		t.Fatalf("missing action reference: %+v", p)
	}
	if cloneCommit(f, cloneConfirmed(p)) == nil {
		t.Fatal("accepted absent Action table")
	}
	_, err = f.app.Call(f.ctx, "preview_clone_configuration", application.Input{DatabaseID: "default", Clone: &catalog.CloneRequest{SourceDatabaseID: "default"}})
	if err == nil {
		t.Fatal("same database accepted")
	}
}

func TestConfigurationClonePermissionsAndSelectedScopes(t *testing.T) {
	f := setup(t)
	prepareClone(t, f)
	req := catalog.CloneRequest{SourceDatabaseID: "default", Scope: &catalog.CloneScope{Knowledge: true}}
	ctx := f.readerContext()
	if _, err := f.app.Call(ctx, "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req}); !errors.Is(err, management.ErrDenied) {
		t.Fatalf("missing source permission: %v", err)
	}
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "default", Permissions: []string{"config.read"}}))
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "target", Permissions: []string{"config.read", "knowledge.write"}}))
	out, err := f.app.Call(ctx, "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
	must(t, err)
	p := out.(*catalog.ClonePreview)
	confirmed := cloneConfirmed(p)
	_, err = f.app.Call(ctx, "clone_configuration", application.Input{DatabaseID: "target", Clone: &confirmed})
	must(t, err)
	saved, err := f.c.Get("target")
	must(t, err)
	if len(saved.Actions) != 0 || saved.WriteEnabled {
		t.Fatal("selected knowledge scope modified actions")
	}
	for _, scope := range []catalog.CloneScope{{Tables: true}, {Policies: true}, {Actions: true}} {
		req.Scope = &scope
		_, err = f.app.Call(ctx, "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
		if !errors.Is(err, management.ErrDenied) {
			t.Fatalf("missing target write accepted: %v", err)
		}
	}
	req.Scope = &catalog.CloneScope{Knowledge: true}
	out, err = f.app.Call(ctx, "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
	must(t, err)
	confirmed = cloneConfirmed(out.(*catalog.ClonePreview))
	must(t, f.m.SaveGrant(f.ctx, f.admin.ID, management.Grant{UserID: f.reader.ID, DatabaseID: "target", Permissions: []string{"config.read"}}))
	if _, err = f.app.Call(ctx, "clone_configuration", application.Input{DatabaseID: "target", Clone: &confirmed}); !errors.Is(err, management.ErrDenied) {
		t.Fatalf("revoked permission accepted: %v", err)
	}
	_, err = f.app.Call(guard.WithBusiness(f.ctx, management.BusinessAccount{DatabaseID: "target", Active: true, Permissions: management.BusinessPermissions}), "preview_clone_configuration", application.Input{DatabaseID: "target", Clone: &req})
	if !errors.Is(err, management.ErrDenied) {
		t.Fatalf("business identity accepted: %v", err)
	}
}

func TestConfigurationCloneAuditFailureIsNonBlocking(t *testing.T) {
	f := setup(t)
	prepareClone(t, f)
	p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: "default"})
	source, err := f.m.Source(f.ctx, "target")
	must(t, err)
	source.Audit.Close()
	f.a.Close()
	must(t, cloneCommit(f, cloneConfirmed(p)))
}
