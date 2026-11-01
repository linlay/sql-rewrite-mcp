// Command migrate-state upgrades per-source SQLite and legacy YAML field names offline.
package main

import (
	"context"
	"datascope-server/internal/management"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func run() error {
	dir := flag.String("data-dir", "data", "全局 management.db 所在目录；先停止 API 和 MCP")
	flag.Parse()
	reports, e := management.MigrateToYAML(context.Background(), filepath.Join(*dir, "management.db"))
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"global_management": filepath.Join(*dir, "management.db"), "sources": reports})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
