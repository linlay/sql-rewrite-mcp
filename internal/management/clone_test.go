package management

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCloneStorageRejectsProtectedChangesAndStagingFailure(t *testing.T) {
	ctx := context.Background()
	m, err := Open(filepath.Join(t.TempDir(), "management.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	err = m.UpdateRecords(ctx, "databases", "database.import", "", func(_ []Record) ([]Record, error) {
		return []Record{{"source", `{"id":"source","name":"Source","mcp_path":"source","revision":1}`}, {"target", `{"id":"target","name":"Target","mcp_path":"target","revision":7}`}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.Records(ctx, "databases")
	if err != nil {
		t.Fatal(err)
	}
	var a, b CloneSnapshot
	err = m.CloneConfiguration(ctx, "source", "target", "", "", false, func(src, dst CloneSnapshot) (CloneReplacement, error) {
		a, b = src, dst
		return CloneReplacement{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "connection", "write_enabled", "authentication", "actions"} {
		err = m.CloneConfiguration(ctx, "source", "target", a.Token, b.Token, true, func(src, dst CloneSnapshot) (CloneReplacement, error) {
			var d map[string]any
			if e := json.Unmarshal(dst.Database, &d); e != nil {
				return CloneReplacement{}, e
			}
			if field == "actions" {
				d[field] = []any{map[string]any{"name": "bad/name", "script": "never executed"}}
			} else {
				d[field] = "must not be written"
			}
			d["revision"] = 8
			raw, e := json.Marshal(d)
			return CloneReplacement{Database: raw}, e
		})
		if err == nil {
			t.Fatalf("accepted protected field or invalid staged filename %s", field)
		}
		after, e := m.Records(ctx, "databases")
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("failure partially wrote target for %s", field)
		}
	}
}
