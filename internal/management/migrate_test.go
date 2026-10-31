package management

import (
	"testing"
)

func TestDerivedCompilationDoesNotEraseActionParameters(t *testing.T) {
	d := map[string]any{"actions": []any{map[string]any{"parameters": map[string]any{"params": map[string]any{"type": "string"}}}}, "config": map[string]any{"tables": []any{map[string]any{"params": []any{"user_id"}, "compiled_sql": "SELECT 1"}}}}
	cleanDerived(d)
	params := d["actions"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if _, ok := params["params"]; !ok {
		t.Fatal("action parameter removed")
	}
	policy := d["config"].(map[string]any)["tables"].([]any)[0].(map[string]any)
	if _, ok := policy["params"]; ok {
		t.Fatal("derived policy parameters persisted")
	}
}
