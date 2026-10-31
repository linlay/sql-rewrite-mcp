package management

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type MigrationReport struct {
	ID         string `json:"id"`
	Management string `json:"management"`
	Backup     string `json:"backup,omitempty"`
	Policies   int    `json:"query_policies"`
	Actions    int    `json:"actions"`
	Accounts   int    `json:"business_accounts"`
	Grants     int    `json:"grants"`
	Knowledge  int    `json:"knowledge"`
	Revision   any    `json:"revision"`
}

func readonlySQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}).String())
}

// MigrateToYAML is an explicit offline upgrade. The original database is never
// modified. VACUUM INTO includes committed WAL state in a consistent snapshot.
// Only verified YAML generations are published; retries never reimport old DBs.
func MigrateToYAML(ctx context.Context, global string) ([]MigrationReport, error) {
	global, e := filepath.Abs(global)
	if e != nil {
		return nil, e
	}
	if _, e = os.Stat(global); e != nil {
		return nil, e
	}
	reports := []MigrationReport{}
	e = fileLock(ctx, global+".migration.lock", func() error {
		return fileLock(ctx, global+".config.lock", func() error {
			db, e := readonlySQLite(global)
			if e != nil {
				return e
			}
			defer db.Close()
			var n int
			if e = db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='SOURCE_REGISTRY'").Scan(&n); e != nil {
				return e
			}
			if n == 0 {
				return errors.New("需要已拆分逐库 management.db 的上一版数据布局")
			}
			rows, e := db.QueryContext(ctx, "SELECT DATABASE_ID_ FROM SOURCE_REGISTRY ORDER BY DATABASE_ID_")
			if e != nil {
				return e
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				if !sourceID.MatchString(id) {
					rows.Close()
					return errors.New("非法数据源 ID")
				}
				ids = append(ids, id)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			backupRoot := filepath.Join(filepath.Dir(global), "legacy", "yaml-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
			backupCreated := false
			for _, id := range ids {
				parent := filepath.Join(filepath.Dir(global), "databases", id)
				path := filepath.Join(parent, "management")
				report := MigrationReport{ID: id, Management: path}
				if e = fileLock(ctx, filepath.Join(parent, ".management.lock"), func() error {
					if e := recoverPublication(path); e != nil {
						return e
					}
					if _, e := os.Stat(path); e == nil {
						st, e := readState(path)
						if e != nil {
							return e
						}
						dest := filepath.Join(backupRoot, id)
						compacted, e := compactDerivedYAML(path, dest, st)
						if e != nil {
							return e
						}
						if compacted {
							report.Backup = dest
						}
						fillMigrationReport(&report, st)
						return writeMigrationMarker(parent)
					} else if !os.IsNotExist(e) {
						return e
					}
					if _, err := os.Stat(filepath.Join(parent, ".management-yaml-migrated")); err == nil {
						return errors.New("该库已完成 YAML 迁移，当前配置缺失，请恢复 YAML 备份；禁止重新导入历史 SQLite")
					} else if !os.IsNotExist(err) {
						return err
					}
					old := filepath.Join(parent, "management.db")
					if _, e := os.Stat(old); e != nil {
						return e
					}
					if !backupCreated {
						if e := os.MkdirAll(backupRoot, 0700); e != nil {
							return e
						}
						if _, e := db.ExecContext(ctx, "VACUUM main INTO ?", filepath.Join(backupRoot, "global-management.db")); e != nil {
							return e
						}
						if e := os.Chmod(filepath.Join(backupRoot, "global-management.db"), 0600); e != nil {
							return e
						}
						backupCreated = true
					}
					dest := filepath.Join(backupRoot, id)
					if e := os.MkdirAll(dest, 0700); e != nil {
						return e
					}
					legacy, e := readonlySQLite(old)
					if e != nil {
						return e
					}
					snapshot := filepath.Join(dest, "management.db")
					_, e = legacy.ExecContext(ctx, "VACUUM main INTO ?", snapshot)
					legacy.Close()
					if e != nil {
						return e
					}
					if e = os.Chmod(snapshot, 0600); e != nil {
						return e
					}
					input, e := readonlySQLite(snapshot)
					if e != nil {
						return e
					}
					defer input.Close()
					var check string
					if e = input.QueryRow("PRAGMA integrity_check").Scan(&check); e != nil || check != "ok" {
						return errors.New("业务管理库一致性校验失败")
					}
					st, e := legacyState(ctx, input)
					if e != nil {
						return fmt.Errorf("%s: %w", id, e)
					}
					if st.Database["id"] != id {
						return errors.New("源目录与数据库配置 ID 不匹配")
					}
					for _, a := range st.Accounts {
						if a.DatabaseID != id {
							return errors.New("业务账号数据库归属无效")
						}
					}
					for _, g := range st.Grants {
						if g.DatabaseID != id {
							return errors.New("授权数据库归属无效")
						}
					}
					var binding string
					e = db.QueryRowContext(ctx, "SELECT POLICY_ID_ FROM AUTH_BINDING WHERE TARGET_TYPE_='DATABASE' AND TARGET_ID_=?", id).Scan(&binding)
					if e != nil && e != sql.ErrNoRows {
						return e
					}
					st.BindingPolicyID = binding
					keyPath := filepath.Join(parent, "credentials.key")
					key, keyErr := os.ReadFile(keyPath)
					if keyErr == nil {
						info, e := os.Lstat(keyPath)
						if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(key) != 32 {
							return errors.New("连接密钥格式或权限无效")
						}
						if e = os.WriteFile(filepath.Join(dest, "credentials.key"), key, 0600); e != nil {
							return e
						}
					} else if len(st.Credentials) > 0 || !os.IsNotExist(keyErr) {
						return errors.New("连接密钥缺失或损坏，迁移已停止")
					}
					for purpose, ciphertext := range st.Credentials {
						keyID := id
						if purpose == "WRITE" {
							keyID = WriteCredentialID(id)
						}
						if e = verifyCiphertext(key, keyID, ciphertext); e != nil {
							return e
						}
					}

					if e = publishState(path, st); e != nil {
						return e
					}
					if e = writeMigrationMarker(parent); e != nil {
						return e
					}
					report.Backup = snapshot
					fillMigrationReport(&report, st)
					// Backup and original are kept as historical recovery inputs.
					b, _ := json.MarshalIndent(report, "", "  ")
					if e = os.WriteFile(filepath.Join(dest, "migration.json"), b, 0600); e != nil {
						return e
					}
					return syncDir(dest)
				}); e != nil {
					return fmt.Errorf("迁移 %s 失败: %w", id, e)
				}
				reports = append(reports, report)
			}
			writable, e := sql.Open("sqlite", global)
			if e != nil {
				return e
			}
			defer writable.Close()
			_, e = writable.ExecContext(ctx, "DELETE FROM AUTH_BINDING WHERE TARGET_TYPE_='DATABASE'")
			return e
		})
	})
	return reports, e
}
func fillMigrationReport(r *MigrationReport, st *sourceState) {
	r.Accounts = len(st.Accounts)
	r.Grants = len(st.Grants)
	r.Knowledge = len(st.Records["knowledge"])
	r.Revision = st.Database["revision"]
	if cfg, ok := st.Database["config"].(map[string]any); ok {
		if tables, ok := cfg["tables"].([]any); ok {
			r.Policies = len(tables)
		}
	}
	if actions, ok := st.Database["actions"].([]any); ok {
		r.Actions = len(actions)
	}
}
func legacyState(ctx context.Context, db *sql.DB) (*sourceState, error) {
	d, e := (&legacySource{db}).load(ctx)
	if e != nil {
		return nil, e
	}
	if e = migrateLegacyDatabase(d); e != nil {
		return nil, e
	}
	st := emptyState()
	st.Database = d
	rows, e := db.QueryContext(ctx, "SELECT PURPOSE_,PASSWORD_CIPHERTEXT_ FROM CONNECTION_CONFIG WHERE PASSWORD_CIPHERTEXT_ IS NOT NULL")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var purpose string
		var sealed []byte
		if e = rows.Scan(&purpose, &sealed); e != nil {
			rows.Close()
			return nil, e
		}
		st.Credentials[purpose] = base64.StdEncoding.EncodeToString(sealed)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.QueryContext(ctx, "SELECT ID_,DATABASE_ID_,USERNAME_,NAME_,BUSINESS_USER_ID_,PERMISSIONS_,ACTIVE_,AUTH_VERSION_,PASSWORD_HASH_ FROM BUSINESS_ACCOUNT ORDER BY ID_")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var a accountState
		var p string
		if e = rows.Scan(&a.ID, &a.DatabaseID, &a.Username, &a.Name, &a.BusinessUserID, &p, &a.Active, &a.AuthVersion, &a.PasswordHash); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &a.Permissions); e != nil {
			rows.Close()
			return nil, e
		}
		st.Accounts = append(st.Accounts, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.QueryContext(ctx, "SELECT USER_ID_,DATABASE_ID_,PERMISSIONS_,BUSINESS_USER_ID_ FROM PLATFORM_GRANT ORDER BY USER_ID_")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var g Grant
		var p string
		if e = rows.Scan(&g.UserID, &g.DatabaseID, &p, &g.BusinessUserID); e != nil {
			rows.Close()
			return nil, e
		}
		if e = json.Unmarshal([]byte(p), &g.Permissions); e != nil {
			rows.Close()
			return nil, e
		}
		st.Grants = append(st.Grants, g)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.QueryContext(ctx, "SELECT BUCKET_,KEY_,DATA_ FROM CONFIG_RECORD ORDER BY BUCKET_,KEY_")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var bucket string
		var r Record
		if e = rows.Scan(&bucket, &r.Key, &r.Data); e != nil {
			rows.Close()
			return nil, e
		}
		// Operational scan state is intentionally discarded; it can be scanned again.
		if bucket != "discovery_checks" && bucket != "discovery_pending" {
			st.Records[bucket] = append(st.Records[bucket], r)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = db.QueryContext(ctx, "SELECT KEY_,DATA_ FROM KNOWLEDGE ORDER BY KEY_")
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var r Record
		if e = rows.Scan(&r.Key, &r.Data); e != nil {
			rows.Close()
			return nil, e
		}
		r.Data, e = canonicalKnowledgeJSON(r.Data)
		if e != nil {
			rows.Close()
			return nil, e
		}
		st.Records["knowledge"] = append(st.Records["knowledge"], r)
	}
	e = rows.Err()
	rows.Close()
	return st, e
}

// The marker prevents an explicit retry from resurrecting stale SQLite after a
// completed YAML directory has subsequently been lost. Runtime never reads it.
func writeMigrationMarker(parent string) error {
	path := filepath.Join(parent, ".management-yaml-migrated")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(parent, ".yaml-migration-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString("1\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(parent)
}
