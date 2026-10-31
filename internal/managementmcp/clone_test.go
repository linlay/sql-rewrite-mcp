package managementmcp

import (
	"encoding/json"
	"testing"
)

func TestCloneManagementRoutes(t *testing.T) {
	for _, tc := range []struct{ tool, action string }{{"datascope_inspect", "preview_clone_configuration"}, {"datascope_manage", "clone_configuration"}} {
		var in input
		raw := `{"action":"` + tc.action + `","args":{"resourceType":"database","database_id":"target","clone":{"source_database_id":"source","scope":{"knowledge":true}}}}`
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatal(err)
		}
		domain, args, err := decode(tc.tool, in)
		if err != nil || domain != tc.action || args.DatabaseID != "target" || args.Clone == nil || args.Clone.SourceDatabaseID != "source" || !args.Clone.Scope.Knowledge {
			t.Fatalf("clone route %s: %s %+v %v", tc.tool, domain, args, err)
		}
		if _, _, err = decode("datascope_query", in); err == nil {
			t.Fatal("clone accepted by query tool")
		}
		in.Args["clone"] = json.RawMessage(`{"source_database_id":"source","scope":{"authentication":true}}`)
		_, auth, err := decode(tc.tool, in)
		if err != nil || auth.Clone == nil || !auth.Clone.Scope.Authentication {
			t.Fatalf("authentication scope rejected: %v", err)
		}
		in.Args["clone"] = json.RawMessage(`{"source_database_id":"source","scope":{"unknown":true}}`)
		if _, _, err = decode(tc.tool, in); err == nil {
			t.Fatal("unknown clone scope accepted")
		}
	}
}
