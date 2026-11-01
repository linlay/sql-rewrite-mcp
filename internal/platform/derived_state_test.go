package platform_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func configurationFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(path, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[p] = string(raw)
		return nil
	})
	must(t, err)
	return files
}

func TestDiscoveryCandidateMemoryLifecycle(t *testing.T) {
	f := setup(t)
	d, err := f.c.Discover(f.ctx, "default")
	must(t, err)
	source, err := f.m.Source(f.ctx, d.ID)
	must(t, err)
	before := configurationFiles(t, source.Path)
	w, err := sql.Open("sqlite", f.st.Path())
	must(t, err)
	defer w.Close()
	_, err = w.Exec("ALTER TABLE items ADD COLUMN later TEXT")
	must(t, err)
	d, err = f.c.Discover(f.ctx, d.ID)
	must(t, err)
	if d.Discovery.Pending == nil {
		t.Fatal("no candidate")
	}
	candidate := d.Discovery.Pending.ID
	again, err := f.c.Discover(f.ctx, d.ID)
	must(t, err)
	if again.Discovery.Pending.ID != candidate {
		t.Fatal("unchanged rescan replaced candidate")
	}
	if !reflect.DeepEqual(before, configurationFiles(t, source.Path)) {
		t.Fatal("scan wrote derived state to YAML")
	}
	// A fresh server process starts without candidates/checks/reports. Formal
	// snapshots remain available, and an explicit rescan restores reviewability.
	peerStore, err := management.Open(f.m.Path, nil)
	must(t, err)
	defer peerStore.Close()
	peerStore.AllowedRoots = []string{f.dir}
	peer := &catalog.Catalog{Management: peerStore, AsOf: store.DefaultAsOf, Timeout: time.Second}
	restarted, err := peer.Get(d.ID)
	must(t, err)
	if restarted.Discovery.Pending != nil || !restarted.Discovery.AttemptedAt.IsZero() || len(restarted.Discovery.Reports) != 0 {
		t.Fatal("runtime state revived after restart")
	}
	if _, err = peer.ApplyDiscovery(f.ctx, d.ID, candidate, d.Revision, nil); err == nil {
		t.Fatal("candidate from another process accepted")
	}
	restarted, err = peer.Discover(f.ctx, d.ID)
	must(t, err)
	if restarted.Discovery.Pending == nil || restarted.Discovery.Pending.ID == candidate {
		t.Fatal("rescan did not create a new process-owned candidate")
	}
	restarted, err = peer.ApplyDiscovery(f.ctx, d.ID, restarted.Discovery.Pending.ID, restarted.Revision, nil)
	must(t, err)
	if restarted.Discovery.Pending != nil || restarted.Discovery.Version != d.Discovery.Version+1 {
		t.Fatal("review did not persist formal snapshot")
	}
	stale, err := f.c.Get(d.ID)
	must(t, err)
	if stale.Discovery.Pending != nil {
		t.Fatal("old process retained obsolete baseline candidate")
	}
	for _, raw := range configurationFiles(t, source.Path) {
		for _, field := range []string{"pending:", "reports:", "checkedAt:", "attemptedAt:", "compiledSql:", "records:"} {
			if strings.Contains(raw, field) {
				t.Fatal("derived state persisted", field)
			}
		}
	}
}

// The child modifies formal configuration while the parent keeps an existing
// MCP session, exercising cross-process freshness without TTLs or disk indexes.
func TestDerivedStateProcessPeer(t *testing.T) {
	if path := os.Getenv("DATASCOPE_DERIVED_PEER_TEST"); path != "" {
		m, err := management.Open(path, nil)
		must(t, err)
		defer m.Close()
		m.AllowedRoots = []string{filepath.Dir(path)}
		admin, err := m.ByUsername(context.Background(), "admin")
		must(t, err)
		ctx := guard.WithUser(context.Background(), admin)
		c := &catalog.Catalog{Management: m, AsOf: store.DefaultAsOf, Timeout: time.Second}
		app := &application.Service{Management: m, Catalog: c}
		if os.Getenv("DATASCOPE_DERIVED_PEER_PHASE") == "revoke" {
			a, err := m.BusinessAccount(ctx, "default", "E0001")
			must(t, err)
			a.Active = false
			_, err = m.SaveBusinessAccount(ctx, a, "", a.AuthVersion)
			must(t, err)
			return
		}
		description := "ChangedInAnotherProcess\n第二行"
		_, err = app.Call(ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "peer-guide", Name: "Guide", Description: &description, Content: "New body", Published: true, Tables: []string{}, Revision: 1})
		must(t, err)
		d, err := c.Get("default")
		must(t, err)
		d.Config.Tables[0].SQL = "SELECT * FROM main.items WHERE owner_id=${userId} AND id=3"
		must(t, c.Save(ctx, *d))
		return
	}
	env := setupRealms(t)
	f := env.f
	description := "OldSearchToken"
	_, err := f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "peer-guide", Name: "Guide", Description: &description, Content: "Old body", Published: true, Tables: []string{}})
	must(t, err)
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session := connectMCP(t, env.resource+"/default", tokens["access"])
	ctx := context.Background()
	_, err = session.ListPrompts(ctx, nil)
	must(t, err)
	_, err = f.m.SearchKnowledge(ctx, "OldSearchToken")
	must(t, err)
	run := func(phase string) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDerivedStateProcessPeer$")
		cmd.Env = append(os.Environ(), "DATASCOPE_DERIVED_PEER_TEST="+f.m.Path, "DATASCOPE_DERIVED_PEER_PHASE="+phase)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("peer: %s %v", raw, err)
		}
	}
	run("edit")
	matches, err := f.m.SearchKnowledge(ctx, "ChangedInAnotherProcess")
	must(t, err)
	if !matches["default/peer-guide"] {
		t.Fatal("search retained stale knowledge")
	}
	matches, err = f.m.SearchKnowledge(ctx, "OldSearchToken")
	must(t, err)
	if len(matches) != 0 {
		t.Fatal("search retained old description")
	}
	prompts, err := session.ListPrompts(ctx, nil)
	must(t, err)
	if len(prompts.Prompts) != 1 || prompts.Prompts[0].Description != "ChangedInAnotherProcess\n第二行" {
		t.Fatal("stale prompt list", prompts)
	}
	full, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "peer-guide"})
	must(t, err)
	if full.Messages[0].Content.(*mcp.TextContent).Text != "New body" {
		t.Fatal("stale prompt body")
	}
	out, err := callMCP(t, session, "query", map[string]any{"sql": "SELECT id FROM items"})
	must(t, err)
	raw, err := json.Marshal(out.StructuredContent)
	must(t, err)
	if out.IsError || !strings.Contains(string(raw), `"row_count":1`) || !strings.Contains(string(raw), `"id":3`) {
		t.Fatal("stale compiled permission SQL", string(raw))
	}
	run("revoke")
	if _, err = session.ListPrompts(ctx, nil); err == nil {
		t.Fatal("revoked identity retained knowledge access")
	}
	if out, err = callMCP(t, session, "query", map[string]any{"sql": "SELECT id FROM items"}); err == nil && !out.IsError {
		t.Fatal("revoked identity retained query access")
	}
}
