package management

import (
	"encoding/json"
	"testing"
)

func TestCanonicalKnowledgeDescription(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"name":"Guide","summary":"First\nSecond","revision":7}`, "First\nSecond"},
		{`{"description":"","summary":"DoNotResurrect"}`, ""},
		{`{"description":"Current","summary":"Old"}`, "Current"},
	} {
		data, err := canonicalKnowledgeJSON(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(data), &record); err != nil {
			t.Fatal(err)
		}
		if record["description"] != tc.want {
			t.Fatalf("description: %v", record)
		}
		if _, old := record["summary"]; old {
			t.Fatal("legacy summary retained")
		}
	}
}
