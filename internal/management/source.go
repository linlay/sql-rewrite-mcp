package management

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"datascope-server/internal/audit"
)

type Source struct {
	owner *Store
	Path  string
	Audit *audit.Store
	ID    string
}

func (s *Source) Close() {
	if s.Audit != nil {
		s.Audit.Close()
	}
}

var sourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func (s *Store) source(ctx context.Context, id string, create bool) (*Source, error) {
	if err := s.recoverPending(ctx); err != nil {
		return nil, err
	}
	if !sourceID.MatchString(id) {
		return nil, fmt.Errorf("非法数据源 ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !create {
		var n int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM SOURCE_REGISTRY WHERE DATABASE_ID_=?", id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, sql.ErrNoRows
		}
	}
	if x := s.sources[id]; x != nil {
		return x, nil
	}
	parent := filepath.Join(filepath.Dir(s.Path), "databases", id)
	path := filepath.Join(parent, "management")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if err := fileLock(ctx, filepath.Join(parent, ".management.lock"), func() error {
		if err := recoverPublication(path); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(path, "database.yml")); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := os.Stat(filepath.Join(parent, "management.db")); err == nil {
			return fmt.Errorf("数据源 %s 尚未迁移，请停服后执行 migrate-state -data-dir %s", id, filepath.Dir(s.Path))
		}
		if !create {
			return fmt.Errorf("数据源 %s YAML 配置缺失", id)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	a, _ := audit.Open(filepath.Join(parent, "audit.db"))
	x := &Source{Path: path, Audit: a, ID: id, owner: s}
	s.sources[id] = x
	return x, nil
}
func (s *Store) Source(ctx context.Context, id string) (*Source, error) {
	return s.source(ctx, id, false)
}
func (s *Store) AuditFor(id string) *audit.Store {
	x, e := s.Source(context.Background(), id)
	if e != nil {
		return s.Audit
	}
	return x.Audit
}
func (s *Store) sourceIDs() ([]string, error) {
	if err := s.recoverPending(context.Background()); err != nil {
		return nil, err
	}
	rows, e := s.DB.Query("SELECT DATABASE_ID_ FROM SOURCE_REGISTRY ORDER BY DATABASE_ID_")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) sourceChange(ctx context.Context, id, kind string, detail any, fn func(*sourceState) error) error {
	return fileLock(ctx, s.Path+".config.lock", func() error {
		if err := s.recoverPendingLocked(ctx); err != nil {
			return err
		}
		x, e := s.Source(ctx, id)
		if e != nil {
			return e
		}
		if e = x.withState(ctx, true, fn); e != nil {
			return e
		}
		s.auditChange(ctx, x.Audit, id, kind, detail)
		return nil
	})
}
func (s *Store) auditChange(ctx context.Context, a *audit.Store, id, kind string, detail any) {
	if a == nil {
		return
	}
	ev := audit.Event{Metadata: audit.From(ctx), ID: audit.ID(), Kind: kind, Status: "success", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Detail: detail}
	ev.DatabaseID = id
	_ = a.Put(ctx, ev)
}
func (s *Source) load(ctx context.Context) (map[string]any, error) {
	var d map[string]any
	err := s.withState(ctx, false, func(st *sourceState) error {
		if st.Database == nil {
			return sql.ErrNoRows
		}
		d = st.Database
		return nil
	})
	return d, err
}
func normalizeDatabase(d map[string]any) error {
	cleanDerived(d)
	delete(d, "etag")
	if _, retired := d["business_tools"]; retired {
		return errors.New("旧命名查询配置已退役，请执行一次性迁移")
	}
	if cfg, ok := d["config"].(map[string]any); ok {
		if _, retired := cfg["fragments"]; retired {
			return errors.New("公共 SQL 片段已退役，请执行一次性迁移")
		}
	}
	return nil
}
func (s *Source) save(ctx context.Context, id string, d map[string]any, credentials []DatabaseCredential) error {
	if err := normalizeDatabase(d); err != nil {
		return err
	}
	return s.withState(ctx, true, func(st *sourceState) error {
		st.Database = d
		for _, c := range credentials {
			purpose := "READ"
			if c.DatabaseID == WriteCredentialID(id) {
				purpose = "WRITE"
			}
			st.Credentials[purpose] = base64.StdEncoding.EncodeToString(c.Ciphertext)
		}
		return nil
	})
}
func (s *Store) records(ctx context.Context, bucket string) ([]Record, error) {
	if bucket != "databases" && bucket != "knowledge" {
		return nil, fmt.Errorf("不支持的正式配置类型 %q", bucket)
	}
	ids, e := s.sourceIDs()
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for _, id := range ids {
		x, e := s.Source(ctx, id)
		if e != nil {
			return nil, e
		}
		if bucket == "databases" {
			d, e := x.load(ctx)
			if e != nil {
				return nil, e
			}
			delete(d, "etag")
			canonical, _ := json.Marshal(d)
			d["etag"] = fmt.Sprintf("%x", sha256.Sum256(canonical))
			b, _ := json.Marshal(d)
			out = append(out, Record{id, string(b)})
			continue
		}
		e = x.withState(ctx, false, func(st *sourceState) error { out = append(out, st.Records[bucket]...); return nil })
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) updateRecords(ctx context.Context, bucket, kind, database string, fn func([]Record) ([]Record, error), credentials ...DatabaseCredential) error {
	if bucket != "databases" && bucket != "knowledge" {
		return fmt.Errorf("不支持的正式配置类型 %q", bucket)
	}
	return fileLock(ctx, s.Path+".config.lock", func() error {
		if err := s.recoverPendingLocked(ctx); err != nil {
			return err
		}
		if len(credentials) > 1 {
			return fmt.Errorf("一次只接受一个连接凭据")
		}
		for _, c := range credentials {
			if bucket != "databases" || !(kind == "database.create" && c.DatabaseID == database || kind == "database.update" && c.DatabaseID == WriteCredentialID(database)) {
				return fmt.Errorf("凭据只能随对应数据源创建或写连接更新保存")
			}
		}
		before, e := s.records(ctx, bucket)
		if e != nil {
			return e
		}
		updated, e := fn(before)
		if e != nil {
			return e
		}
		if bucket == "databases" {
			old := map[string]string{}
			for _, r := range before {
				var d map[string]any
				if e = json.Unmarshal([]byte(r.Data), &d); e != nil {
					return e
				}
				if e = normalizeDatabase(d); e != nil {
					return e
				}
				canonical, _ := json.Marshal(d)
				old[r.Key] = string(canonical)
			}
			routes := map[string]bool{}
			docs := map[string]map[string]any{}
			found := false
			for _, r := range updated {
				found = found || r.Key == database
				if !sourceID.MatchString(r.Key) {
					return fmt.Errorf("非法数据源 ID")
				}
				if docs[r.Key] != nil {
					return fmt.Errorf("重复数据源")
				}
				var d map[string]any
				if e = json.Unmarshal([]byte(r.Data), &d); e != nil {
					return e
				}
				if d == nil {
					return errors.New("数据库配置必须是对象")
				}
				if id, ok := d["id"].(string); ok && id != r.Key {
					return errors.New("数据库 ID 不匹配")
				}
				route, _ := d["mcp_path"].(string)
				if route == "" {
					route = r.Key
				}
				if routes[route] {
					return errors.New("MCP 路径重复")
				}
				routes[route] = true
				if e = normalizeDatabase(d); e != nil {
					return e
				}
				docs[r.Key] = d
			}
			if len(credentials) > 0 && !found {
				return errors.New("缺少凭据所属的数据源配置")
			}
			// A normal operation changes one source. Imports may add several independent
			// sources; every source is published as one complete recoverable generation.
			change := configurationChange{Sources: map[string]*sourceState{}}
			for _, r := range updated {
				canonical, _ := json.Marshal(docs[r.Key])
				if old[r.Key] == string(canonical) && !(len(credentials) > 0 && r.Key == database) {
					continue
				}
				x, e := s.source(ctx, r.Key, true)
				if e != nil {
					return e
				}
				d := docs[r.Key]
				cred := []DatabaseCredential{}
				if r.Key == database {
					cred = credentials
				}
				if e = x.withState(ctx, false, func(st *sourceState) error {
					change.Sources[r.Key] = st
					st.Database = d
					for _, c := range cred {
						purpose := "READ"
						if c.DatabaseID == WriteCredentialID(r.Key) {
							purpose = "WRITE"
						}
						st.Credentials[purpose] = base64.StdEncoding.EncodeToString(c.Ciphertext)
					}
					if kind == "database.create" {
						actor := audit.From(ctx).ActorID
						if actor > 0 {
							exists := false
							for _, g := range st.Grants {
								exists = exists || g.UserID == actor
							}
							if !exists {
								st.Grants = append(st.Grants, Grant{UserID: actor, DatabaseID: r.Key, Permissions: []string{"config.read", "config.write", "policy.write", "mcp.write", "knowledge.write", "audit.read", "grant"}})
							}
						}
					}
					return nil
				}); e != nil {
					return e
				}
				name, _ := d["name"].(string)
				if name == "" {
					name = r.Key
				}
				route, _ := d["mcp_path"].(string)
				if route == "" {
					route = r.Key
				}
				change.Registry = append(change.Registry, registryChange{r.Key, name, route})
			}
			if len(change.Sources) > 0 {
				if e = s.commitChange(ctx, change); e != nil {
					return e
				}
			}
		} else {
			if database == "" {
				return errors.New("逐库记录必须指定数据库")
			}
			x, e := s.Source(ctx, database)
			if e != nil {
				return e
			}
			if e = x.withState(ctx, true, func(st *sourceState) error {
				// Callbacks return upsert deltas. Preserve all other local records.
				local := append([]Record{}, st.Records[bucket]...)
				for _, r := range updated {
					var d map[string]any
					_ = json.Unmarshal([]byte(r.Data), &d)
					belongs := r.Key == database || len(r.Key) > len(database) && r.Key[:len(database)+1] == database+"/" || d["database_id"] == database
					if !belongs {
						continue
					}
					if bucket == "knowledge" {
						r.Data, e = canonicalKnowledgeJSON(r.Data)
						if e != nil {
							return e
						}
					}
					var normalized any
					if e = json.Unmarshal([]byte(r.Data), &normalized); e != nil {
						return e
					}
					b, _ := json.Marshal(normalized)
					r.Data = string(b)
					replaced := false
					for i, old := range local {
						if old.Key == r.Key {
							local[i] = r
							replaced = true
							break
						}
					}
					if !replaced {
						local = append(local, r)
					}
				}
				sort.Slice(local, func(i, j int) bool { return local[i].Key < local[j].Key })
				st.Records[bucket] = local
				return nil
			}); e != nil {
				return e
			}
		}
		s.auditChange(ctx, s.Audit, database, kind, map[string]any{"bucket": bucket})
		return nil
	})
}
func cleanDerived(v map[string]any) {
	if discovery, ok := v["discovery"].(map[string]any); ok {
		for _, field := range []string{"pending", "checked_at", "attempted_at", "error", "reports"} {
			delete(discovery, field)
		}
	}
	cfg, _ := v["config"].(map[string]any)
	policies, _ := cfg["tables"].([]any)
	for _, v := range policies {
		if p, ok := v.(map[string]any); ok {
			delete(p, "compiled_sql")
			delete(p, "compiled_deny_unless")
			delete(p, "params")
			delete(p, "deny_unless_params")
		}
	}
}
func (s *Store) internalPaths() []string {
	root := filepath.Dir(s.Path)
	paths := []string{s.Path, s.AuditPath(), s.Path + ".credentials.key", filepath.Join(root, "management.db"), filepath.Join(root, "management.db.credentials.key"), filepath.Join(root, "management.sqlite"), filepath.Join(root, "management.sqlite.credentials.key"), filepath.Join(root, "audit.sqlite")}
	// Retained backups contain the same private data as live metadata. Include
	// their file identities too, so a hard link cannot bypass path exclusion.
	for _, dir := range []string{"databases", "legacy", "oauth"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				paths = append(paths, path)
			}
			return nil
		})
	}
	for _, path := range append([]string(nil), paths...) {
		paths = append(paths, path+"-wal", path+"-shm")
	}
	return paths
}
