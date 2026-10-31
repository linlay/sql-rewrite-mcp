package management

import (
	"context"
	"database/sql"
	"datascope-server/internal/audit"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (s *Store) initSources() error {
	if _, e := s.DB.Exec(`CREATE TABLE IF NOT EXISTS OAUTH_CLIENT(ID_ TEXT PRIMARY KEY,DATA_ TEXT NOT NULL);CREATE TABLE IF NOT EXISTS SOURCE_REGISTRY(DATABASE_ID_ TEXT PRIMARY KEY,NAME_ TEXT NOT NULL,MCP_PATH_ TEXT NOT NULL UNIQUE,STATE_ TEXT NOT NULL DEFAULT 'ACTIVE');`); e != nil {
		return e
	}
	var n int
	if e := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='CONFIG_RECORD'").Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		if e := s.DB.QueryRow("SELECT COUNT(*) FROM CONFIG_RECORD WHERE BUCKET_='databases'").Scan(&n); e != nil {
			return e
		}
	}
	if n > 0 {
		return fmt.Errorf("请先使用上一版 migrate-state 将旧全局业务配置拆为逐库 management.db，再执行本版 migrate-state")
	}
	ids, e := s.sourceIDs()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if _, e = s.Source(context.Background(), id); e != nil {
			return e
		}
	}
	for _, table := range []string{"PLATFORM_GRANT", "CONNECTION_SECRET", "BUSINESS_ACCOUNT", "ACTION_RECEIPT", "CONFIG_RECORD", "CONFIG_VERSION", "AUDIT_OUTBOX", "PLATFORM_SESSION", "OAUTH_GRANT"} {
		if _, e = s.DB.Exec("DROP TABLE IF EXISTS " + table); e != nil {
			return e
		}
	}
	if s.Audit != nil {
		s.Audit.Source = func(id string) *audit.Store {
			x, e := s.Source(context.Background(), id)
			if e != nil {
				return nil
			}
			return x.Audit
		}
		s.Audit.Sources = func() []*audit.Store {
			ids, _ := s.sourceIDs()
			out := []*audit.Store{}
			for _, id := range ids {
				if x, e := s.Source(context.Background(), id); e == nil {
					out = append(out, x.Audit)
				}
			}
			return out
		}
	}
	return nil
}

// CopyLegacyState never modifies the old files; publication uses a complete
// SQLite snapshot and keeps the original available for rollback.
func CopyLegacyState(old, new string) error {
	if e := os.MkdirAll(filepath.Dir(new), 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(new+".copy.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if _, e := os.Stat(new); e == nil {
		return nil
	}
	if _, e := os.Stat(old); os.IsNotExist(e) {
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(new), 0700); e != nil {
		return e
	}
	db, e := sql.Open("sqlite", "file:"+filepath.ToSlash(old)+"?mode=ro")
	if e != nil {
		return e
	}
	defer db.Close()
	staged := new + fmt.Sprintf(".migrate-%d", time.Now().UnixNano())
	if _, e = db.Exec("VACUUM main INTO ?", staged); e != nil {
		return e
	}
	if e = os.Chmod(staged, 0600); e != nil {
		return e
	}
	if b, e := os.ReadFile(old + ".credentials.key"); e == nil {
		if e = os.WriteFile(new+".credentials.key", b, 0600); e != nil {
			return e
		}
	}
	return os.Rename(staged, new)
}
