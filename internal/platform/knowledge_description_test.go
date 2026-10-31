package platform_test

import (
	"datascope-server/internal/management"
	"encoding/json"
	"strings"
	"testing"

	"datascope-server/internal/application"
)

func TestKnowledgeDescriptionDisclosureAndLegacyAlias(t *testing.T) {
	f := setup(t)
	text := "第一行：用途与适用范围\n第二行：DescriptionOnlySearchToken 的查询指导\n第三行：指标口径与注意事项"
	legacy := "legacy must not replace canonical description"
	in := application.Input{DatabaseID: "default", ID: "description-guide", Name: "Knowledge guide", Description: &text, Summary: &legacy, Content: "FULL_BODY_NOT_IN_LIST", Published: true, Tables: []string{}}
	_, err := f.app.Call(f.ctx, "save_knowledge", in)
	must(t, err)
	for _, action := range []string{"list_knowledge", "search_knowledge"} {
		ctx := f.ctx
		if action == "search_knowledge" {
			ctx = f.readerContext()
		}
		value, e := f.app.Call(ctx, action, application.Input{DatabaseID: "default", Query: "DescriptionOnlySearchToken"})
		must(t, e)
		raw, e := json.Marshal(value)
		must(t, e)
		var entries []map[string]any
		must(t, json.Unmarshal(raw, &entries))
		if len(entries) != 1 || entries[0]["description"] != text {
			t.Fatalf("description not fully disclosed: %s", raw)
		}
		if _, ok := entries[0]["summary"]; ok {
			t.Fatal("legacy field in model output")
		}
		if strings.Contains(string(raw), "FULL_BODY_NOT_IN_LIST") {
			t.Fatal("body disclosed in list")
		}
	}
	// Existing clients may still submit summary; an explicitly empty description wins.
	in.Description = nil
	in.Summary = &text
	in.Revision = 1
	_, err = f.app.Call(f.ctx, "save_knowledge", in)
	must(t, err)
	empty := ""
	in.Description = &empty
	in.Revision = 2
	value, err := f.app.Call(f.ctx, "save_knowledge", in)
	must(t, err)
	if value.(application.Knowledge).Description != "" {
		t.Fatal("empty description did not override summary")
	}
	// Legacy input is canonicalized before publication; searches use the same YAML.
	e := f.m.UpdateRecords(f.ctx, "knowledge", "knowledge.save", "default", func(records []management.Record) ([]management.Record, error) {
		return append(records, management.Record{Key: "default/legacy", Data: `{"id":"legacy","database_id":"default","name":"Legacy","summary":"LegacyDescriptionToken\n第二行","content":"Old body","tables":[],"published":true,"revision":4}`}), nil
	})
	must(t, e)
	value, err = f.app.Call(f.readerContext(), "search_knowledge", application.Input{DatabaseID: "default", Query: "LegacyDescriptionToken"})
	must(t, err)
	entries := value.([]application.Knowledge)
	if len(entries) != 1 || entries[0].Description != "LegacyDescriptionToken\n第二行" {
		t.Fatal(entries)
	}
}
