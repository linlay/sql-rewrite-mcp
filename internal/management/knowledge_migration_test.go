package management

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestKnowledgeTableMigrationPreservesContentAndPublication(t *testing.T) {
	global, _ := migrationFixture(t)
	ctx := context.Background()
	if _, err := MigrateToYAML(ctx, global); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(global), "databases", "source", "management")
	file := filepath.Join(path, "knowledges", "guide.yml")
	var document map[string]any
	if err := readDocument(file, &document); err != nil {
		t.Fatal(err)
	}
	knowledge := document["knowledge"].(map[string]any)
	knowledge["tables"] = []any{"items", "missing_table"}
	original, err := marshalDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := MigrateToYAML(ctx, global)
	if err != nil || len(reports) != 1 || reports[0].Backup == "" {
		t.Fatal("migration did not back up old knowledge", reports, err)
	}
	backup, err := os.ReadFile(filepath.Join(reports[0].Backup, "management", "knowledges", "guide.yml"))
	if err != nil || !bytes.Equal(original, backup) {
		t.Fatal("knowledge backup changed original", err)
	}
	after, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal([]byte(after.Records["knowledge"][0].Data), &record); err != nil {
		t.Fatal(err)
	}
	if _, exists := record["tables"]; exists {
		t.Fatal("retired table restrictions persisted")
	}
	if record["published"] != true || record["revision"] != float64(9) || record["content"] != "第一行\n第二行\n" {
		t.Fatal("content, publication or revision changed unexpectedly", record)
	}
	if !reflect.DeepEqual(before.Database, after.Database) || !reflect.DeepEqual(before.Accounts, after.Accounts) || !reflect.DeepEqual(before.Grants, after.Grants) {
		t.Fatal("knowledge migration changed database or authorization")
	}
	retry, err := MigrateToYAML(ctx, global)
	if err != nil || len(retry) != 1 || retry[0].Backup != "" {
		t.Fatal("knowledge migration is not idempotent", retry, err)
	}
}
