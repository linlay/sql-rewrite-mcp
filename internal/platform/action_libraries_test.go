package platform_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"datascope-server/internal/action"
	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"gopkg.in/yaml.v3"
)

func TestActionLibraryStoragePermissionsAndConfirmation(t *testing.T) {
	f, business := setupAction(t, true)
	d, err := f.c.Get("default")
	must(t, err)
	original := d.Actions[0].Script
	d.ActionLibraries = []action.Library{{Name: "items", Title: "Items", Description: "Shared item update", Script: strings.Replace(original, "function run(", "export function rename(", 1)}}
	d.Actions[0].Script = `import {rename as updateItem} from "items"; function run(args,ctx){return updateItem(args,ctx);}`
	_, err = f.app.Call(f.ctx, "configure_actions", application.Input{DatabaseID: d.ID, Database: d})
	must(t, err)
	source, err := f.m.Source(f.ctx, d.ID)
	must(t, err)
	path := filepath.Join(source.Path, "action-libraries", "items.yml")
	raw, err := os.ReadFile(path)
	must(t, err)
	if !strings.Contains(string(raw), "script:") || !strings.Contains(string(raw), "export function rename") {
		t.Fatal("library was not saved as camel-case YAML")
	}
	peer, err := management.Open(f.m.Path, nil)
	must(t, err)
	defer peer.Close()
	peer.AllowedRoots = f.m.AllowedRoots
	peerCatalog := &catalog.Catalog{Management: peer, AsOf: f.c.AsOf, Timeout: f.c.Timeout}
	saved, err := peerCatalog.Get(d.ID)
	must(t, err)
	if !reflect.DeepEqual(saved.ActionLibraries, d.ActionLibraries) {
		t.Fatal("peer failed to read shared libraries")
	}
	_, err = f.app.Call(f.readerContext(), "configure_actions", application.Input{DatabaseID: saved.ID, Database: saved})
	if err == nil {
		t.Fatal("library save bypassed action.write")
	}
	before := managementFiles(t, f, d.ID)
	missing := *saved
	missing.ActionLibraries = []action.Library{}
	if _, err = f.app.Call(f.ctx, "configure_actions", application.Input{DatabaseID: d.ID, Database: &missing}); err == nil {
		t.Fatal("deleted a referenced library")
	}
	if !reflect.DeepEqual(before, managementFiles(t, f, d.ID)) {
		t.Fatal("failed library deletion changed configuration")
	}
	args := map[string]any{"id": 1, "name": "imported update"}
	preview, err := f.app.InvokeAction(business, d.ID, "rename_item", "library-old", args)
	must(t, err)
	if preview.Status != "confirm_required" {
		t.Fatal(preview)
	}
	// Even a manual library edit without a revision bump must invalidate a
	// pending preview. Completed receipts remain reusable below.
	var document map[string]any
	must(t, yaml.Unmarshal(raw, &document))
	document["script"] = document["script"].(string) + "\n// new shared implementation\n"
	raw, err = yaml.Marshal(document)
	must(t, err)
	must(t, os.WriteFile(path, raw, 0600))
	if _, err = f.app.ConfirmAction(context.Background(), preview.ConfirmationToken, "business-password"); err == nil {
		t.Fatal("confirmation accepted a changed library")
	}
	preview, err = f.app.InvokeAction(business, d.ID, "rename_item", "library-fresh", args)
	must(t, err)
	if preview.Status != "confirm_required" {
		t.Fatal(preview)
	}
	result, err := f.app.ConfirmAction(context.Background(), preview.ConfirmationToken, "business-password")
	must(t, err)
	if result.Status != "done" {
		t.Fatal(result)
	}
	saved, err = peerCatalog.Get(d.ID)
	must(t, err)
	saved.ActionLibraries[0].Script += "\n// another version\n"
	must(t, peerCatalog.Save(f.ctx, *saved))
	result, err = f.app.InvokeAction(business, d.ID, "rename_item", "library-fresh", args)
	must(t, err)
	if result.Status != "done" {
		t.Fatal("library update invalidated a completed receipt")
	}
}

func TestActionLibraryCloningCopiesDependencies(t *testing.T) {
	f := setup(t)
	target := cloneTarget(t, f, `CREATE TABLE items(id INTEGER PRIMARY KEY, owner_id INTEGER, name TEXT, secret TEXT);`)
	d, err := f.c.Get("default")
	must(t, err)
	d.ActionLibraries = []action.Library{{Name: "items", Title: "Items", Script: "export function rename(){return db.exec`UPDATE items SET name='new' WHERE id=1`; }"}}
	d.Actions = []action.Definition{{Name: "rename", Title: "Rename", Description: "Rename", Parameters: map[string]action.Parameter{}, Script: `import {rename} from "items"; function run(){return rename();}`}}
	must(t, f.c.Save(f.ctx, *d))
	request := catalog.CloneRequest{SourceDatabaseID: d.ID, Scope: &catalog.CloneScope{Actions: true}}
	preview := clonePreview(t, f, request)
	if len(preview.Issues) > 0 {
		t.Fatal(preview.Issues)
	}
	must(t, cloneCommit(f, cloneConfirmed(preview)))
	copied, err := f.c.Get(target.ID)
	must(t, err)
	if !reflect.DeepEqual(copied.ActionLibraries, d.ActionLibraries) || !reflect.DeepEqual(copied.Actions, d.Actions) {
		t.Fatal("clone lost action dependencies")
	}
}
