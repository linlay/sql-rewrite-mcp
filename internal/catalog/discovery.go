package catalog

import (
	"context"
	"datascope-server/internal/audit"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"
)

type SchemaChange struct {
	Table    string  `json:"table" yaml:"table"`
	Column   string  `json:"column,omitempty" yaml:"column,omitempty"`
	Kind     string  `json:"kind" yaml:"kind"`
	Property string  `json:"property" yaml:"property"`
	Before   string  `json:"before" yaml:"before"`
	After    string  `json:"after" yaml:"after"`
	Manual   *string `json:"manual,omitempty" yaml:"manual,omitempty"`
	Conflict bool    `json:"conflict" yaml:"conflict"`
}

type SchemaReport struct {
	Version int            `json:"version" yaml:"version"`
	At      time.Time      `json:"at" yaml:"at"`
	Changes []SchemaChange `json:"changes" yaml:"changes"`
}

type Discovery struct {
	Pending     *PendingDiscovery                `json:"pending,omitempty" yaml:"-"`
	Version     int                              `json:"version" yaml:"version"`
	CheckedAt   time.Time                        `json:"checked_at" yaml:"-"`
	AttemptedAt time.Time                        `json:"attempted_at" yaml:"-"`
	Error       string                           `json:"error,omitempty" yaml:"-"`
	Tables      []store.TableSchema              `json:"tables" yaml:"tables"`
	Annotations map[string]store.TableAnnotation `json:"annotations" yaml:"annotations"`
	Reports     []SchemaReport                   `json:"reports" yaml:"-"`
}

// OpenStore deliberately does not compile policies: inspection must remain
// available when the source schema has made an old policy invalid.
func (c *Catalog) OpenStore(ctx context.Context, d *Database) (*store.Store, error) {
	if c.Management != nil && d.Driver == "sqlite" {
		if err := c.Management.ValidatePath(d.Path); err != nil {
			return nil, err
		}
	}
	var st *store.Store
	var err error
	if d.Connection != nil && d.Connection.CredentialID != "" {
		if c.Management == nil || d.Connection.CredentialID != d.ID {
			return nil, errors.New("数据库凭据引用无效")
		}
		password, e := c.Management.DatabasePassword(ctx, d.ID)
		if e != nil {
			return nil, e
		}
		st, err = store.OpenConnectionWithPassword(ctx, d.Driver, d.Path, d.Connection, password, c.Timeout)
	} else {
		st, err = store.OpenConnection(ctx, d.Driver, d.Path, d.Connection, c.Timeout)
	}
	if err != nil {
		return nil, err
	}
	st.Audit = c.Audit
	if c.Management != nil {
		st.Audit = c.Management.AuditFor(d.ID)
	}
	if d.Discovery != nil {
		st.Annotations = d.Discovery.Annotations
	}
	return st, nil
}

func captureSchema(ctx context.Context, st *store.Store) ([]store.TableSchema, error) {
	// Capture source comments, never the presentation overlay.
	st.Annotations = nil
	names, err := st.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.TableSchema, 0, len(names))
	for _, name := range names {
		schema, err := st.Describe(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("读取表 %s: %w", name, err)
		}
		out = append(out, *schema)
	}
	after, err := st.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(names, after) {
		return nil, errors.New("扫描期间表结构发生变化，请重试")
	}
	return out, nil
}

func schemaDiff(before, after []store.TableSchema, annotations map[string]store.TableAnnotation) []SchemaChange {
	out := []SchemaChange{}
	add := func(table, col, kind, property string, old, next any) {
		if reflect.DeepEqual(old, next) {
			return
		}
		value := func(v any) string {
			if v == nil {
				return ""
			}
			if s, ok := v.(string); ok {
				return s
			}
			raw, _ := json.Marshal(v)
			return string(raw)
		}
		change := SchemaChange{Table: table, Column: col, Kind: kind, Property: property, Before: value(old), After: value(next)}
		a := annotations[table].Annotation
		if col != "" {
			a = annotations[table].Columns[col]
		}
		if property == "comment" && a.Comment != nil {
			change.Manual = a.Comment
			change.Conflict = *a.Comment != change.After
		}
		out = append(out, change)
	}
	oldTables := map[string]store.TableSchema{}
	for _, t := range before {
		oldTables[t.Name] = t
	}
	for _, t := range after {
		old, exists := oldTables[t.Name]
		delete(oldTables, t.Name)
		if !exists {
			add(t.Name, "", "added", "table", nil, t)
			continue
		}
		add(t.Name, "", "modified", "comment", old.Comment, t.Comment)
		add(t.Name, "", "modified", "indexes", old.Indexes, t.Indexes)
		add(t.Name, "", "modified", "ddl", old.DDL, t.DDL)
		oldCols := map[string]store.Column{}
		for _, col := range old.Columns {
			oldCols[col.Name] = col
		}
		for _, col := range t.Columns {
			prev, exists := oldCols[col.Name]
			delete(oldCols, col.Name)
			if !exists {
				add(t.Name, col.Name, "added", "column", nil, col)
				continue
			}
			add(t.Name, col.Name, "modified", "type", prev.Type, col.Type)
			add(t.Name, col.Name, "modified", "not_null", prev.NotNull, col.NotNull)
			add(t.Name, col.Name, "modified", "primary_key", prev.PrimaryKey, col.PrimaryKey)
			add(t.Name, col.Name, "modified", "default", prev.Default, col.Default)
			add(t.Name, col.Name, "modified", "references", prev.References, col.References)
			add(t.Name, col.Name, "modified", "comment", prev.Comment, col.Comment)
		}
		for _, col := range old.Columns {
			if _, ok := oldCols[col.Name]; ok {
				add(t.Name, col.Name, "removed", "column", col, nil)
			}
		}
	}
	for _, t := range before {
		if _, ok := oldTables[t.Name]; ok {
			add(t.Name, "", "removed", "table", t, nil)
		}
	}
	return out
}

// Reconcile only removes unavailable access and adds denied policies. An
// explicit existing wildcard remains a deliberate opt-in to future columns.
func reconcilePolicies(cfg *rewrite.Config, tables []store.TableSchema) {
	old := map[string]*rewrite.Policy{}
	for _, p := range cfg.Tables {
		old[strings.ToLower(p.Table)] = p
	}
	next := []*rewrite.Policy{}
	for _, table := range tables {
		p := old[strings.ToLower(table.Name)]
		if p == nil {
			no := false
			p = &rewrite.Policy{Table: table.Name, Enabled: &no, Scheme: cfg.Schemes[0].Scheme, Passthrough: true, MaxRows: store.DefaultMaxRows, Columns: []string{}}
			for _, col := range table.Columns {
				p.Columns = append(p.Columns, col.Name)
			}
		} else if p.Columns != nil {
			cols := []string{}
			for _, name := range p.Columns {
				if slices.ContainsFunc(table.Columns, func(col store.Column) bool { return strings.EqualFold(name, col.Name) }) {
					cols = append(cols, name)
				}
			}
			p.Columns = cols
			if len(cols) == 0 {
				no := false
				p.Enabled = &no
			}
		}
		next = append(next, p)
	}
	cfg.Tables = next
}

func (c *Catalog) Discover(ctx context.Context, id string) (*Database, error) {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	d, err := c.Get(id)
	if err != nil {
		return nil, err
	}
	if d.Config == nil || len(d.Config.Schemes) == 0 {
		return nil, errors.New("数据库缺少有效策略方案，请先修复配置")
	}
	st, err := c.OpenStore(ctx, d)
	var tables []store.TableSchema
	if err == nil {
		defer st.Close()
		tables, err = captureSchema(ctx, st)
	}
	if err != nil {
		return nil, err
	} // A partial or failed scan never replaces a good snapshot.
	if d.Discovery != nil && d.Discovery.Version > 0 {
		return c.stageDiscovery(ctx, d, tables)
	}
	err = c.updateContext(ctx, "database.discover", id, func(items *[]Database) error {
		for i := range *items {
			current := &(*items)[i]
			if current.ID != id {
				continue
			}
			if current.Revision != d.Revision || current.ETag != d.ETag {
				return errors.New("扫描期间配置已更新，请重试")
			}
			if c.Management != nil && current.Discovery != nil && current.Discovery.Version > 0 && len(schemaDiff(current.Discovery.Tables, tables, current.Discovery.Annotations)) == 0 {
				return nil
			}
			applyDiscovery(current, tables)
			current.Revision++
			return nil
		}
		return errors.New("数据库不存在")
	})
	if err != nil {
		return nil, err
	}
	c.recordDiscoveryReport(id, SchemaReport{Version: 1, At: time.Now().UTC(), Changes: schemaDiff(nil, tables, nil)})
	c.recordDiscoveryCheck(id, time.Now().UTC(), nil)
	return c.Get(id)
}

func applyDiscovery(d *Database, tables []store.TableSchema) {
	if d.Discovery == nil {
		d.Discovery = &Discovery{Tables: []store.TableSchema{}, Annotations: map[string]store.TableAnnotation{}, Reports: []SchemaReport{}}
	}
	s := d.Discovery
	changes := schemaDiff(s.Tables, tables, s.Annotations)
	s.CheckedAt = time.Now().UTC()
	s.AttemptedAt = s.CheckedAt
	s.Error = ""
	if s.Version == 0 || len(changes) > 0 {
		s.Version++
	}
	s.Tables = tables
	reconcilePolicies(d.Config, tables)
}

// SaveAnnotation keeps snapshots immutable and uses the database revision to
// prevent an editor from overwriting a concurrent scan or another editor.
func (c *Catalog) SaveAnnotation(ctx context.Context, id, table, column string, revision int, annotation store.Annotation) (*Database, error) {
	if len(annotation.DisplayName) > 512 || (annotation.Comment != nil && len(*annotation.Comment) > 20000) {
		return nil, errors.New("名称最多 512 字节，说明最多 20000 字节")
	}
	err := c.updateContext(ctx, "database.annotate", id, func(items *[]Database) error {
		for i := range *items {
			d := &(*items)[i]
			if d.ID != id {
				continue
			}
			if d.Revision != revision {
				return errors.New("配置已更新，请刷新后重试")
			}
			if d.Discovery == nil {
				return errors.New("请先侦查数据库")
			}
			found := false
			for _, t := range d.Discovery.Tables {
				if t.Name == table && (column == "" || slices.ContainsFunc(t.Columns, func(c store.Column) bool { return c.Name == column })) {
					found = true
				}
			}
			if !found {
				return errors.New("表或字段不在当前结构中")
			}
			if d.Discovery.Annotations == nil {
				d.Discovery.Annotations = map[string]store.TableAnnotation{}
			}
			a := d.Discovery.Annotations[table]
			if column == "" {
				a.Annotation = annotation
			} else {
				if a.Columns == nil {
					a.Columns = map[string]store.Annotation{}
				}
				a.Columns[column] = annotation
			}
			d.Discovery.Annotations[table] = a
			d.Revision++
			return nil
		}
		return errors.New("数据库不存在")
	})
	if err != nil {
		return nil, err
	}
	return c.Get(id)
}

// RunDiscovery is owned by the admin server, so it works without an open UI.
func (c *Catalog) RunDiscovery(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		items, err := c.List()
		if err != nil {
			slog.Error("读取自动侦查目录失败", "error", err)
		}
		if err == nil {
			for _, d := range items {
				if ctx.Err() != nil {
					return
				}
				if d.Discovery != nil && time.Since(maxCheckTime(d.Discovery)) < interval {
					continue
				}
				scanCtx := audit.With(ctx, audit.Metadata{Source: "discovery", DatabaseID: d.ID, Tool: "database.discover"})
				if _, err := c.Discover(scanCtx, d.ID); err != nil && ctx.Err() == nil {
					slog.Warn("数据库自动侦查失败", "database_id", d.ID, "error", err)
					// Preserve the last complete snapshot and report a failed attempt.
					checked := time.Time{}
					if d.Discovery != nil {
						checked = d.Discovery.CheckedAt
					}
					c.recordDiscoveryCheck(d.ID, checked, err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func maxCheckTime(d *Discovery) time.Time {
	if d.AttemptedAt.After(d.CheckedAt) {
		return d.AttemptedAt
	}
	return d.CheckedAt
}
