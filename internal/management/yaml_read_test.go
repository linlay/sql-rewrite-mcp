package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReadDocumentCanonicalFields(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"canonical", "name: Example\nmaxRows: 10\n", true},
		{"legacy", "NAME_: Example\n", false},
		{"snake case", "max_rows: 10\n", false},
		{"missing suffix", "NAME: Example\n", false},
		{"mixed case", "Name_: Example\n", false},
		{"collision", "NAME_: Example\nname: Other\n", false},
		{"duplicate", "name: Example\nname: Other\n", false},
		{"nested legacy", "policy:\n  TABLE_: items\n", false},
		{"multiple documents", "name: Example\n---\nname: Other\n", false},
		{"dynamic keys", "parameters:\n  Customer_ID_:\n    type: integer\n", true},
		{"dynamic field case", "parameters:\n  CustomerID:\n    TYPE_: integer\n", false},
		{"enum data", "enum:\n  - BUSINESS_KEY_: value\n", true},
		{"acronym", "mcpURL: value\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.yml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := readDocument(path, &got); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestReadDocumentPreservesBusinessValues(t *testing.T) {
	want := map[string]any{
		"name":             "中文说明",
		"mcp_enabled":      true,
		"business_user_id": "9007199254740993",
		"max_rows":         10.0,
		"script":           "function run(args) {\n  return args.CustomerID;\n}\n",
		"parameters":       map[string]any{"Customer_ID_": map[string]any{"type": "integer", "max_length": 20.0, "enum": []any{map[string]any{"BUSINESS_KEY_": "value"}}}},
		"annotations":      map[string]any{"mixedCaseTable": map[string]any{"columns": map[string]any{"CustomerID": map[string]any{"comment": "客户"}}}},
	}
	raw, err := marshalDocument(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mcpEnabled:", "businessUserId:", "maxRows:", "maxLength:", "Customer_ID_:", "BUSINESS_KEY_:"} {
		if !bytes.Contains(raw, []byte(key)) {
			t.Fatalf("missing field %s", key)
		}
	}
	path := filepath.Join(t.TempDir(), "document.yml")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = readDocument(path, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("business values changed: %#v", got)
	}
}

func TestCamelYAMLMigrationPreservesGenerationAndBackup(t *testing.T) {
	global, _ := migrationFixture(t)
	ctx := context.Background()
	if _, err := MigrateToYAML(ctx, global); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(global), "databases", "source", "management")
	before, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	originals := map[string][]byte{}
	err = filepath.WalkDir(path, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		var value any
		if err := readDocument(file, &value); err != nil {
			return err
		}
		legacy, err := yaml.Marshal(convertDocumentFields(jsonValue(value), true, false, true))
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, file)
		if err != nil {
			return err
		}
		originals[rel] = legacy
		return os.WriteFile(file, legacy, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readState(path); err == nil {
		t.Fatal("runtime accepted legacy field names")
	}
	reports, err := MigrateToYAML(ctx, global)
	if err != nil || len(reports) != 1 || reports[0].Backup == "" {
		t.Fatal("missing migration backup", reports, err)
	}
	after, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(after)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("field migration changed configuration, credentials or revision", err)
	}
	for rel, original := range originals {
		backup, err := os.ReadFile(filepath.Join(reports[0].Backup, "management", rel))
		if err != nil || !bytes.Equal(original, backup) {
			t.Fatal("backup changed original document", rel, err)
		}
	}
	key, err := os.ReadFile(filepath.Join(filepath.Dir(path), "credentials.key"))
	if err != nil {
		t.Fatal(err)
	}
	backupKey, err := os.ReadFile(filepath.Join(reports[0].Backup, "credentials.key"))
	if err != nil || !bytes.Equal(key, backupKey) {
		t.Fatal("credential key backup mismatch", err)
	}
	retry, err := MigrateToYAML(ctx, global)
	if err != nil || len(retry) != 1 || retry[0].Backup != "" {
		t.Fatal("migration is not idempotent", retry, err)
	}
}

func TestMigrationRejectsMixedFieldNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.yml")
	for _, raw := range []string{
		"DATABASE_:\n  id: source\n",
		"database:\n  ID_: source\n",
		"NAME_: first\nname: second\n",
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var value any
		if err := readMigrationDocument(path, &value); err == nil {
			t.Fatal("accepted mixed or colliding names")
		}
	}
}

func BenchmarkReadDocument(b *testing.B) {
	columns := make([]any, 40)
	for i := range columns {
		columns[i] = map[string]any{"name": fmt.Sprintf("column_%d", i), "type": "VARCHAR(200)", "comment": "业务字段说明", "not_null": true}
	}
	raw, err := marshalDocument(map[string]any{"name": "items", "structure": map[string]any{"name": "items", "columns": columns}, "policy": map[string]any{"table": "items", "sql": "SELECT * FROM items WHERE owner_id=${userId}"}})
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "items.yml")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var got map[string]any
		if err := readDocument(path, &got); err != nil {
			b.Fatal(err)
		}
	}
}
