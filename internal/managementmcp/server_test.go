package managementmcp

import (
	"encoding/json"
	"testing"
)

func TestRejectInvalidManagementArguments(t *testing.T) {
	for _, tc := range []struct{ name, tool, raw string }{
		{"write through query", "datascope_query", `{"action":"save","args":{"resourceType":"policy","database_id":"default","database":{}}}`},
		{"wrong resource", "datascope_inspect", `{"action":"validate","args":{"resourceType":"user"}}`},
		{"missing resource", "datascope_query", `{"action":"list","args":{}}`},
		{"missing database", "datascope_query", `{"action":"get","args":{"resourceType":"database"}}`},
		{"foreign field", "datascope_query", `{"action":"list","args":{"resourceType":"database","password":"secret"}}`},
		{"null argument", "datascope_query", `{"action":"search","args":{"resourceType":"audit","limit":null}}`},
		{"string integer", "datascope_manage", `{"action":"enable","args":{"resourceType":"user","user_id":"42"}}`},
		{"zero user", "datascope_manage", `{"action":"enable","args":{"resourceType":"user","user_id":0}}`},
		{"negative pagination", "datascope_query", `{"action":"search","args":{"resourceType":"audit","offset":-1}}`},
		{"unknown nested field", "datascope_manage", `{"action":"create","args":{"resourceType":"database","database":{"id":"test","unexpected":true}}}`},
		{"mismatched database", "datascope_manage", `{"action":"save","args":{"resourceType":"policy","database_id":"one","database":{"id":"two","revision":1}}}`},
		{"missing revision", "datascope_manage", `{"action":"save","args":{"resourceType":"mcp","database_id":"one","database":{"id":"one"}}}`},
		{"missing replacement permissions", "datascope_manage", `{"action":"grant","args":{"resourceType":"grant","database_id":"default","user_id":42,"business_user_id":0}}`},
		{"missing identity binding", "datascope_manage", `{"action":"grant","args":{"resourceType":"grant","database_id":"default","user_id":42,"permissions":[]}}`},
		{"missing publish intent", "datascope_manage", `{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"note","name":"Note","summary":"","content":"","revision":0}}`},
		{"retired knowledge tables", "datascope_manage", `{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"note","name":"Note","description":"","content":"","tables":["items"],"published":true,"revision":0}}`},
		{"retired knowledge release flag", "datascope_manage", `{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"note","name":"Note","description":"","content":"","release_knowledge_restrictions":true,"published":true,"revision":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in input
			if err := json.Unmarshal([]byte(tc.raw), &in); err != nil {
				t.Fatal(err)
			}
			if _, _, err := decode(tc.tool, in); err == nil {
				t.Fatal("accepted invalid arguments")
			}
		})
	}
}

func TestExplicitEmptyReplacementValues(t *testing.T) {
	var in input
	if err := json.Unmarshal([]byte(`{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"note","name":"Note","summary":"","content":"","published":false,"revision":0}}`), &in); err != nil {
		t.Fatal(err)
	}
	action, out, err := decode("datascope_manage", in)
	if err != nil || action != "save_knowledge" || out.Published || out.Revision != 0 {
		t.Fatalf("draft values lost: %s %+v %v", action, out, err)
	}
	in = input{}
	if err := json.Unmarshal([]byte(`{"action":"grant","args":{"resourceType":"grant","database_id":"default","user_id":42,"permissions":[],"business_user_id":0}}`), &in); err != nil {
		t.Fatal(err)
	}
	action, out, err = decode("datascope_manage", in)
	if err != nil || action != "grant_database_permissions" || out.Permissions == nil || out.BusinessUserID != 0 {
		t.Fatalf("replacement values lost: %s %+v %v", action, out, err)
	}
}

func TestActionDescriptionRoute(t *testing.T) {
	for _, name := range []string{"", "rename_item"} {
		op := "describe"
		domainName := "describe_action"
		argsIn := map[string]any{"resourceType": "action", "database_id": "default"}
		if name == "" {
			op = "list"
			domainName = "list_actions"
		} else {
			argsIn["name"] = name
		}
		var in input
		raw, _ := json.Marshal(map[string]any{"action": op, "args": argsIn})
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		domain, args, err := decode("datascope_query", in)
		if err != nil || domain != domainName || args.DatabaseID != "default" || args.Name != name {
			t.Fatalf("action description route: %s %+v %v", domain, args, err)
		}
		in.Args["script"] = json.RawMessage(`"injected"`)
		if _, _, err = decode("datascope_query", in); err == nil {
			t.Fatal("read-only description accepted script mutation")
		}
	}
}

func TestCreationAndConnectionTestAcceptPasswordWithoutAnID(t *testing.T) {
	for _, tool := range []string{"datascope_manage", "datascope_inspect"} {
		action, resource := "create", "database"
		if tool == "datascope_inspect" {
			action, resource = "test", "connection"
		}
		var in input
		raw := map[string]any{"action": action, "args": map[string]any{"resourceType": resource, "password": "database-password", "database": map[string]any{"name": "业务数据库", "driver": "mysql", "connection": map[string]any{"host": "db.local", "database": "business", "username": "reader"}}}}
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(encoded, &in); err != nil {
			t.Fatal(err)
		}
		_, out, err := decode(tool, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.Database.ID != "" || out.Password != "database-password" {
			t.Fatal("generated identity or password input was lost")
		}
	}
}

func TestAuditPageAcceptsFiltersAndRejectsInvalidTypes(t *testing.T) {
	var in input
	if err := json.Unmarshal([]byte(`{"action":"page","args":{"resourceType":"audit","database_id":"default","actor_id":0,"status":"failed","source":"web","kind":"sql.execute","tool":"query","query":"timeout","started_from":"2026-10-28T00:00:00+08:00","started_to":"2026-10-28T23:59:59+08:00","limit":50,"offset":0}}`), &in); err != nil {
		t.Fatal(err)
	}
	action, out, err := decode("datascope_query", in)
	if err != nil || action != "get_audit_page" || out.ActorID == nil || *out.ActorID != 0 || out.Status != "failed" {
		t.Fatal("audit filters were not decoded", action, out, err)
	}
	for _, raw := range []string{`{"action":"page","args":{"resourceType":"audit","actor_id":-1}}`, `{"action":"page","args":{"resourceType":"audit","platform_only":"true"}}`, `{"action":"page","args":{"resourceType":"audit","status":null}}`} {
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		if _, _, err := decode("datascope_query", in); err == nil {
			t.Fatal("invalid audit filter accepted", raw)
		}
	}
}

func TestKnowledgeDescriptionArgumentsAndLegacyAlias(t *testing.T) {
	for _, text := range []string{`"description":"first\nsecond\nthird"`, `"summary":"first\nsecond\nthird"`, `"description":"","summary":"legacy"`} {
		var in input
		raw := `{"action":"save","args":{"resourceType":"knowledge","database_id":"default","id":"guide","name":"Guide",` + text + `,"content":"Body","published":true,"revision":0}}`
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		_, out, err := decode("datascope_manage", in)
		if err != nil || out.Description == nil {
			t.Fatalf("description rejected: %+v %v", out, err)
		}
		if text == `"description":"","summary":"legacy"` && *out.Description != "" {
			t.Fatal("explicit empty description overwritten")
		}
		if text != `"description":"","summary":"legacy"` && *out.Description != "first\nsecond\nthird" {
			t.Fatal(*out.Description)
		}
	}
}
