package platform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"datascope-server/internal/application"
	"datascope-server/internal/management"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestKnowledgeSwitchSavesWithoutOtherChanges(t *testing.T) {
	f := setup(t)
	d, err := f.c.Get("default")
	must(t, err)
	revision := d.Revision
	on := true
	d.KnowledgeEnabled = &on
	_, err = f.app.Call(f.ctx, "save_database", application.Input{DatabaseID: d.ID, Database: d})
	must(t, err)
	saved, err := f.c.Get(d.ID)
	must(t, err)
	if saved.KnowledgeEnabled == nil || !*saved.KnowledgeEnabled || saved.Revision != revision+1 {
		t.Fatal("knowledge-only change was skipped", saved)
	}
	_, err = f.app.Call(f.ctx, "save_database", application.Input{DatabaseID: d.ID, Database: saved})
	must(t, err)
	again, err := f.c.Get(d.ID)
	must(t, err)
	if again.Revision != saved.Revision {
		t.Fatal("unchanged save created another revision")
	}
}

func TestKnowledgeOnlyAccountCanAuthorizeRefreshAndReadPrompts(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	ctx := context.Background()
	a := env.accounts["default"]
	a.Permissions = []string{"knowledge.read"}
	a, err := f.m.SaveBusinessAccount(f.ctx, a, "", a.AuthVersion)
	must(t, err)
	_, err = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "guide", Name: "Guide", Content: "Knowledge without SQL access", Published: true})
	must(t, err)
	d, err := f.c.Get("default")
	must(t, err)
	off := false
	d.Config.Tables[0].Enabled = &off
	d.Config.Tables[0].Columns = []string{"id"}
	must(t, f.c.Save(f.ctx, *d))
	// Simulate a directly edited YAML generation with no query configuration.
	// Knowledge must remain available without opening a query service.
	must(t, f.m.UpdateRecords(f.ctx, "databases", "test.query-policy-unavailable", "default", func(records []management.Record) ([]management.Record, error) {
		for i, record := range records {
			if record.Key == "default" {
				var fields map[string]any
				if err := json.Unmarshal([]byte(record.Data), &fields); err != nil {
					return nil, err
				}
				fields["config"] = nil
				raw, err := json.Marshal(fields)
				if err != nil {
					return nil, err
				}
				records[i].Data = string(raw)
			}
		}
		return records, nil
	}))
	_, err = f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: "guide", Name: "Guide", Content: "Knowledge without SQL access", Published: true, Revision: 1})
	must(t, err)

	// Exercise the complete OAuth entry and refresh flow with no query/action grant.
	tokens, realm, _ := employeeTokens(t, env, "default", "employee-default-password")
	status, _, body := formRequest(t, http.DefaultClient, "POST", realm+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh"]}, "client_id": {tokens["client"]}, "resource": {env.resource + "/default"}})
	if status != 200 {
		t.Fatalf("knowledge-only refresh %d: %s", status, body)
	}
	var refreshed struct {
		Access string `json:"access_token"`
	}
	must(t, json.Unmarshal([]byte(body), &refreshed))
	session := connectMCP(t, env.resource+"/default", refreshed.Access)
	list, err := session.ListPrompts(ctx, nil)
	must(t, err)
	if len(list.Prompts) != 1 || list.Prompts[0].Name != "guide" {
		t.Fatal(list)
	}
	full, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "guide"})
	must(t, err)
	if full.Messages[0].Content.(*mcp.TextContent).Text != "Knowledge without SQL access" {
		t.Fatal(full)
	}
	tools, err := session.ListTools(ctx, nil)
	must(t, err)
	if len(tools.Tools) != 0 {
		t.Fatal("knowledge grant exposed tools", tools)
	}
	for _, name := range []string{"query", "invoke_action"} {
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"sql": "SELECT * FROM items"}})
		if callErr == nil && !result.IsError {
			t.Fatalf("knowledge grant allowed %s", name)
		}
	}
	a.Permissions = []string{"query"}
	_, err = f.m.SaveBusinessAccount(f.ctx, a, "", a.AuthVersion)
	must(t, err)
	if _, err = session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "guide"}); err == nil {
		t.Fatal("revoked knowledge permission retained")
	}
}

func TestKnowledgePromptsPaginationAndLivePermissions(t *testing.T) {
	env := setupRealms(t)
	f := env.f
	ctx := context.Background()
	for i := 0; i < 53; i++ {
		description := fmt.Sprintf("Guide %d\n完整第二行\n第三行", i)
		_, err := f.app.Call(f.ctx, "save_knowledge", application.Input{DatabaseID: "default", ID: fmt.Sprintf("guide-%02d", i), Name: fmt.Sprintf("Guide %d", i), Description: &description, Content: "BODY_ONLY_ON_GET", Published: i != 52})
		must(t, err)
	}
	tokens, _, _ := employeeTokens(t, env, "default", "employee-default-password")
	session := connectMCP(t, env.resource+"/default", tokens["access"])
	first, err := session.ListPrompts(ctx, nil)
	must(t, err)
	if len(first.Prompts) != 50 || first.NextCursor == "" {
		t.Fatal("pagination missing", len(first.Prompts))
	}
	if first.CacheScope != "private" || first.TTLMs != 0 {
		t.Fatal("knowledge cached publicly")
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "BODY_ONLY_ON_GET") || !strings.Contains(string(raw), `完整第二行\n第三行`) {
		t.Fatal(string(raw))
	}
	second, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: first.NextCursor})
	must(t, err)
	if len(second.Prompts) != 2 || second.NextCursor != "" {
		t.Fatal("last page", second)
	}
	full, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "guide-00"})
	must(t, err)
	if len(full.Messages) != 1 || full.Messages[0].Role != "user" || full.Messages[0].Content.(*mcp.TextContent).Text != "BODY_ONLY_ON_GET" {
		t.Fatal(full)
	}
	for _, params := range []*mcp.GetPromptParams{{Name: "guide-52"}, {Name: "missing"}, {Name: "guide-00", Arguments: map[string]string{"template": "injected"}}} {
		if _, err = session.GetPrompt(ctx, params); err == nil {
			t.Fatal("draft, missing or parameterized prompt accepted", params)
		}
	}
	d, err := f.c.Get("default")
	must(t, err)
	off := false
	d.KnowledgeEnabled = &off
	must(t, f.c.Save(f.ctx, *d))
	list, err := session.ListPrompts(ctx, nil)
	must(t, err)
	if len(list.Prompts) != 0 {
		t.Fatal("disabled knowledge listed")
	}
	if _, err = session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "guide-00"}); err == nil {
		t.Fatal("disabled knowledge readable")
	}
	on := true
	d, err = f.c.Get("default")
	must(t, err)
	d.KnowledgeEnabled = &on
	must(t, f.c.Save(f.ctx, *d))
	a := env.accounts["default"]
	a.Permissions = []string{"query"}
	_, err = f.m.SaveBusinessAccount(f.ctx, a, "", a.AuthVersion)
	must(t, err)
	if _, err = session.ListPrompts(ctx, nil); err == nil {
		t.Fatal("revoked knowledge permission retained")
	}
}
