package catalog

import (
	"context"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
	"encoding/json"
	"fmt"
	"time"
)

// BootstrapManaged imports file configuration exactly once; subsequent reads use split YAML exclusively.
func BootstrapManaged(ctx context.Context, m *management.Store, path string, st *store.Store, config string, asOf time.Time, timeout time.Duration, legacy string) (*Catalog, error) {
	c := &Catalog{Path: path, Management: m, Audit: m.Audit, AsOf: asOf, Timeout: timeout}
	records, e := m.Records(ctx, "databases")
	if e != nil {
		return nil, e
	}
	if len(records) > 0 {
		return c, nil
	}
	old, e := Bootstrap(path, st, config, asOf, timeout, legacy)
	if e != nil {
		return nil, e
	}
	items, e := old.List()
	if e != nil {
		return nil, e
	}
	e = c.updateContext(ctx, "database.import", "", func(current *[]Database) error {
		if len(*current) == 0 {
			*current = items
		}
		return nil
	})
	return c, e
}
func (c *Catalog) listManaged(ctx context.Context) ([]Database, error) {
	records, e := c.Management.Records(ctx, "databases")
	if e != nil {
		return nil, e
	}
	out := []Database{}
	for _, r := range records {
		var d Database
		if e = json.Unmarshal([]byte(r.Data), &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	c.overlayDiscovery(out)
	return out, nil
}
func (c *Catalog) updateManaged(ctx context.Context, action, database string, fn func(*[]Database) error, credentials ...management.DatabaseCredential) error {
	return c.Management.UpdateRecords(ctx, "databases", action, database, func(records []management.Record) ([]management.Record, error) {
		items := []Database{}
		for _, r := range records {
			var d Database
			if e := json.Unmarshal([]byte(r.Data), &d); e != nil {
				return nil, e
			}
			items = append(items, d)
		}
		if e := fn(&items); e != nil {
			return nil, e
		}
		routes := map[string]bool{}
		out := []management.Record{}
		for _, d := range items {
			if !validID.MatchString(d.ID) || !validID.MatchString(d.MCPRoute()) || routes[d.MCPRoute()] || d.MCPRoute() == "admin" {
				return nil, fmt.Errorf("数据库 ID 或 MCP 路径无效、重复或保留")
			}
			routes[d.MCPRoute()] = true
			if e := store.ValidateConnection(d.Driver, d.Connection); e != nil {
				return nil, e
			}
			if d.Driver == "sqlite" {
				if e := c.Management.ValidatePath(d.Path); e != nil {
					return nil, e
				}
			}
			d.ETag = ""
			raw, e := json.Marshal(d)
			if e != nil {
				return nil, e
			}
			out = append(out, management.Record{Key: d.ID, Data: string(raw)})
		}
		return out, nil
	}, credentials...)
}
