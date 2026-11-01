package platform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/view"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestViewConfigurationPersistenceReferencesAndAuthorization(t *testing.T) {
	f := setup(t)
	d, err := f.c.Get("default")
	must(t, err)
	d.Views = view.Templates()
	d.QueryViewID = "query"
	_, err = f.app.Call(f.readerContext(), "configure_views", application.Input{DatabaseID: d.ID, Database: d})
	if err == nil {
		t.Fatal("reader wrote HTML")
	}
	_, err = f.app.Call(f.readerContext(), "save_database", application.Input{DatabaseID: d.ID, Database: d})
	if err == nil {
		t.Fatal("save_database bypassed view.write")
	}
	_, err = f.app.Call(f.ctx, "configure_views", application.Input{DatabaseID: d.ID, Database: d})
	must(t, err)
	if _, err = f.app.Call(f.ctx, "configure_views", application.Input{DatabaseID: d.ID, Database: d}); err == nil {
		t.Fatal("accepted stale revision")
	}
	saved, err := f.c.Get(d.ID)
	must(t, err)
	if len(saved.Views) != 3 || saved.Views[2].HTML != d.Views[2].HTML || saved.QueryViewID != "query" {
		t.Fatal("HTML changed in persistence")
	}
	source, err := f.m.Source(f.ctx, d.ID)
	must(t, err)
	if _, err = os.Stat(filepath.Join(source.Path, "views", "securities_account.yml")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(source.Path, "database.yml"))
	must(t, err)
	if strings.Contains(string(raw), "<!doctype") {
		t.Fatal("HTML was not split into views directory")
	}
	for _, mutate := range []func(*catalog.Database){
		func(d *catalog.Database) { d.Views = nil },
		func(d *catalog.Database) { d.Views[0].Published = false },
		func(d *catalog.Database) { d.Views[0].Type = "html" },
		func(d *catalog.Database) { d.Views[0].ID = "../escape" },
	} {
		next, err := f.c.Get(d.ID)
		must(t, err)
		mutate(next)
		if err = f.c.Save(f.ctx, *next); err == nil {
			t.Fatal("accepted invalid view or dangling reference")
		}
	}
	// Cloning views must rewrite resource ownership without changing HTML.
	target := prepareClone(t, f)
	p := clonePreview(t, f, catalog.CloneRequest{SourceDatabaseID: d.ID, Scope: &catalog.CloneScope{Views: true}})
	if len(p.Issues) != 0 {
		t.Fatal(p.Issues)
	}
	req := cloneConfirmed(p)
	_, err = f.app.Call(f.ctx, "clone_configuration", application.Input{DatabaseID: target.ID, Clone: &req})
	must(t, err)
	cloned, err := f.c.Get(target.ID)
	must(t, err)
	if len(cloned.Views) != 3 || cloned.Views[2].HTML != saved.Views[2].HTML || view.URI(cloned.ID, "query") == view.URI(saved.ID, "query") {
		t.Fatal("invalid view clone")
	}
}

func TestViewResourcesPaginationScopeAndQueryMetadata(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	d, err := f.c.Get("default")
	must(t, err)
	for i := 0; i < 53; i++ {
		d.Views = append(d.Views, view.Definition{ID: fmt.Sprintf("view_%02d", i), Name: fmt.Sprintf("View %d", i), Description: "HTML view", Type: "view", HTML: "<!doctype html><h1>BODY_ONLY_ON_READ</h1>", Published: i != 52})
	}
	d.QueryViewID = "view_00"
	must(t, f.c.Save(f.ctx, *d))
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session := connectMCP(t, env.resource+"/default", tokens["access"])
	ctx := context.Background()
	first, err := session.ListResources(ctx, nil)
	must(t, err)
	if len(first.Resources) != 50 || first.NextCursor == "" || first.CacheScope != "private" {
		t.Fatal("invalid resources pagination", first)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "BODY_ONLY_ON_READ") || first.Resources[0].Meta["type"] != "view" {
		t.Fatal("body leaked in list or type missing")
	}
	second, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: first.NextCursor})
	must(t, err)
	if len(second.Resources) != 2 || second.NextCursor != "" {
		t.Fatal("invalid final resources page")
	}
	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: view.URI("default", "view_00")})
	must(t, err)
	if len(read.Contents) != 1 || read.Contents[0].MIMEType != "text/html" || read.Contents[0].Meta["type"] != "view" || !strings.Contains(read.Contents[0].Text, "BODY_ONLY_ON_READ") || read.CacheScope != "private" {
		t.Fatal(read)
	}
	for _, uri := range []string{view.URI("other", "view_00"), view.URI("default", "view_52"), view.URI("default", "../view_00")} {
		if _, err = session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatal("read inaccessible resource", uri)
		}
	}
	out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT id FROM items ORDER BY id"}})
	must(t, err)
	if out.IsError || out.Meta["datascope/viewUri"] != view.URI("default", "view_00") || len(out.Content) == 0 {
		t.Fatal("missing query data or view hint", out)
	}
	override, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT id FROM items", "view_id": "view_01"}})
	must(t, err)
	if override.IsError || override.Meta["datascope/viewUri"] != view.URI("default", "view_01") {
		t.Fatal(override)
	}
	invalid, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT id FROM items", "view_id": "view_52"}})
	must(t, err)
	if !invalid.IsError {
		t.Fatal("query accepted draft view")
	}
	d, err = f.c.Get("default")
	must(t, err)
	d.QueryViewID = ""
	d.Views[0].Published = false
	must(t, f.c.Save(f.ctx, *d))
	if _, err = session.ReadResource(ctx, &mcp.ReadResourceParams{URI: view.URI("default", "view_00")}); err == nil {
		t.Fatal("unpublished resource still readable")
	}
}
