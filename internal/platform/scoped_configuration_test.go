package platform_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
)

func TestScopedConfigurationReadsIgnoreUnrelatedYAML(t *testing.T) {
	f := setup(t)
	must(t, f.c.Add(f.ctx, catalog.Database{ID: "other", Name: "Other", Driver: "sqlite", Path: f.st.Path()}))
	for _, id := range []string{"default", "other"} {
		_, err := f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: id, ID: "guide", Name: "Guide", Content: "ScopedKnowledgeToken " + id, Published: true})
		must(t, err)
	}
	before, err := f.c.Get("default")
	must(t, err)
	other, err := f.m.Source(f.ctx, "other")
	must(t, err)
	// If a scoped request parses unrelated tables this fails, independently of
	// machine speed. The full catalog must still detect the invalid source.
	must(t, os.WriteFile(filepath.Join(other.Path, "tables", "items.yml"), []byte("name: ["), 0600))
	if _, err = f.c.List(); err == nil {
		t.Fatal("full catalog ignored invalid YAML")
	}
	value, err := f.app.Call(f.ctx, "get_database", application.Input{DatabaseID: "default"})
	must(t, err)
	db := value.(application.DatabaseView)
	if db.ID != "default" || db.ETag != before.ETag || len(db.Config.Tables) != len(before.Config.Tables) {
		t.Fatal("scoped database view changed configuration")
	}
	for _, action := range []string{"list_knowledge", "search_knowledge", "get_knowledge"} {
		ctx := f.ctx
		in := application.Input{DatabaseID: "default"}
		if action == "search_knowledge" {
			ctx = f.readerContext()
			in.Query = "ScopedKnowledgeToken"
		}
		if action == "get_knowledge" {
			in.ID = "guide"
		}
		value, err = f.app.Call(ctx, action, in)
		must(t, err)
		if action == "get_knowledge" {
			k := value.(application.Knowledge)
			if k.DatabaseID != "default" || k.Content != "ScopedKnowledgeToken default" {
				t.Fatal("wrong knowledge body", k)
			}
		} else {
			entries := value.([]application.Knowledge)
			if len(entries) != 1 || entries[0].DatabaseID != "default" || entries[0].Content != "" {
				t.Fatal("knowledge list leaked another source or content", entries)
			}
		}
	}
	for _, target := range []struct{ kind, id string }{{"API", "platform"}, {"DATABASE", "default"}} {
		_, err = f.m.ResolveAuth(context.Background(), target.kind, target.id)
		must(t, err)
	}
	selected, err := f.m.Source(f.ctx, "default")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(selected.Path, "tables", "items.yml"), []byte("name: ["), 0600))
	if _, err = f.c.Get("default"); err == nil {
		t.Fatal("scoped read ignored invalid YAML in selected source")
	}
}
