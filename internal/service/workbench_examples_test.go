package service

import (
	"context"
	"gopkg.in/yaml.v3"
	"os"
	"testing"

	"datascope-server/internal/rewrite"
)

// Validate the optional securities YAML examples against the demo schema.
func TestWorkbenchExamples(t *testing.T) {
	data, err := os.ReadFile("../../config/securities-sql-examples.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Examples []struct {
			Label string `yaml:"label"`
			SQL   string `yaml:"sql"`
		} `yaml:"sqlExamples"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Examples) == 0 {
		t.Fatal("workbench has no examples")
	}
	svc := newService(t)
	ctx := context.Background()
	for _, example := range config.Examples {
		t.Run(example.Label, func(t *testing.T) {
			raw, err := svc.Store.RawQuery(ctx, example.SQL, 5)
			if err != nil || raw.RowCount == 0 {
				t.Fatalf("raw query: result=%+v error=%v", raw, err)
			}
			admin, err := svc.Query(ctx, example.SQL, 175, 5)
			if err != nil || admin.RowCount == 0 {
				t.Fatalf("admin rewrite: result=%+v error=%v", admin, err)
			}
			employee, err := svc.Query(ctx, example.SQL, 1, 5)
			if example.Label == "管理范围（仅管理员）" {
				if !rewrite.IsDenied(err) {
					t.Fatalf("expected employee permission denial, got %v", err)
				}
			} else if err != nil || employee.RowCount == 0 {
				t.Fatalf("employee rewrite: result=%+v error=%v", employee, err)
			}
		})
	}
}
