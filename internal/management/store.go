// Package management owns global identities and per-source durable configuration.
package management

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"datascope-server/internal/audit"
	"datascope-server/internal/metaschema"
	"encoding/base64"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrDenied = errors.New("无权执行此操作")
var Permissions = []string{
	"config.read", "config.write", "policy.write", "mcp.write", "action.write", "knowledge.read", "knowledge.write", "view.write", "query", "raw_query", "audit.read", "grant", "impersonate"}
var SystemPermissions = []string{"users.manage", "databases.create"}

type Store struct {
	DB           *sql.DB
	Path         string
	Audit        *audit.Store
	AllowedRoots []string
	Actions      ActionState
	mu           sync.Mutex
	sources      map[string]*Source
	sessions     map[string]session
	closeOnce    sync.Once
}
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	SystemAdmin bool   `json:"system_admin"`
	AuthVersion int64  `json:"-"`
}
type Grant struct {
	UserID         int64    `json:"user_id"`
	DatabaseID     string   `json:"database_id"`
	Permissions    []string `json:"permissions"`
	BusinessUserID int64    `json:"business_user_id"`
}
type Record struct {
	Key  string
	Data string
}

func Open(path string, a *audit.Store) (*Store, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if a != nil {
		mainInfo, _ := os.Stat(abs)
		auditInfo, _ := os.Stat(a.Path)
		if abs == a.Path || (mainInfo != nil && auditInfo != nil && os.SameFile(mainInfo, auditInfo)) {
			return nil, errors.New("管理库与审计库必须是两个独立的 SQLite 文件")
		}
	}
	if e = os.MkdirAll(filepath.Dir(abs), 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(abs+".migration.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return nil, e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	f, e := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"}).String()
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	if e = metaschema.Migrate(db); e != nil {
		db.Close()
		return nil, e
	}
	_, e = db.Exec("CREATE TABLE IF NOT EXISTS PLATFORM_USER(ID_ INTEGER PRIMARY KEY,USERNAME_ TEXT NOT NULL UNIQUE,NAME_ TEXT NOT NULL,PASSWORD_HASH_ TEXT NOT NULL,ACTIVE_ INTEGER NOT NULL DEFAULT 1,SYSTEM_ADMIN_ INTEGER NOT NULL DEFAULT 0,AUTH_VERSION_ INTEGER NOT NULL DEFAULT 0);\n CREATE TABLE IF NOT EXISTS PLATFORM_PERMISSION(USER_ID_ INTEGER NOT NULL REFERENCES PLATFORM_USER(ID_),PERMISSION_ TEXT NOT NULL,PRIMARY KEY(USER_ID_,PERMISSION_));\n CREATE TABLE IF NOT EXISTS SCHEMA_VERSION(VERSION_ INTEGER PRIMARY KEY,APPLIED_AT_ TEXT NOT NULL);\n INSERT OR IGNORE INTO SCHEMA_VERSION VALUES(1,CURRENT_TIMESTAMP);")
	if e != nil {
		db.Close()
		return nil, e
	}
	m := &Store{DB: db, Path: abs, Audit: a, Actions: NewMemoryActions(), sources: map[string]*Source{}, sessions: map[string]session{}}
	if e = m.initSources(); e != nil {
		db.Close()
		return nil, e
	}

	if e = m.initAuth(); e != nil {
		m.Close()
		return nil, e
	}
	return m, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, x := range s.sources {
			x.Close()
		}
		s.DB.Close()
	})
	return nil
}
func passwordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	key, e := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(salt) + ":" + base64.RawStdEncoding.EncodeToString(key), nil
}
func verify(password, hash string) bool {
	p := strings.Split(hash, ":")
	if len(p) != 2 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(p[0])
	if e != nil {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(p[1])
	if e != nil {
		return false
	}
	got, e := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	return e == nil && subtle.ConstantTimeCompare(want, got) == 1
}
func tokenHash(token string) string {
	b := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (s *Store) Change(ctx context.Context, action, database string, detail any, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.auditChange(ctx, s.Audit, database, action, detail)
	return nil

}
func (s *Store) Flush(ctx context.Context) error {
	if s.Audit != nil {
		s.Audit.Flush(ctx)
	}
	return nil
}
func (s *Store) Bootstrap(ctx context.Context, username, password string) error {
	var n int
	if e := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM PLATFORM_USER").Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	if username == "" || len(password) < 6 {
		return errors.New("首次启动需设置 ADMIN_USERNAME 和至少 6 字符的 ADMIN_PASSWORD")
	}
	hash, e := passwordHash(password)
	if e != nil {
		return e
	}
	return s.Change(ctx, "user.bootstrap", "", map[string]any{"username": username}, func(tx *sql.Tx) error {
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM PLATFORM_USER").Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return nil
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO PLATFORM_USER(USERNAME_,NAME_,PASSWORD_HASH_,SYSTEM_ADMIN_) VALUES(?,?,?,1)", username, username, hash)
		return e
	})
}
func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	e := row.Scan(&u.ID, &u.Username, &u.Name, &u.Active, &u.SystemAdmin, &u.AuthVersion)
	return u, e
}

const userColumns = "ID_,USERNAME_,NAME_,ACTIVE_,SYSTEM_ADMIN_,AUTH_VERSION_"

func (s *Store) User(ctx context.Context, id int64) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userColumns+" FROM PLATFORM_USER WHERE ID_=?", id))
}
func (s *Store) ByUsername(ctx context.Context, name string) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userColumns+" FROM PLATFORM_USER WHERE USERNAME_=?", name))
}
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT "+userColumns+" FROM PLATFORM_USER ORDER BY ID_")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, e := scanUser(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) Authenticate(ctx context.Context, name, password string) (User, error) {
	var hash string
	u, e := s.ByUsername(ctx, name)
	if e == nil {
		e = s.DB.QueryRowContext(ctx, "SELECT PASSWORD_HASH_ FROM PLATFORM_USER WHERE ID_=?", u.ID).Scan(&hash)
	}
	if e != nil || !u.Active {
		hash = "c29tZS1kdW1teS1zYWx0LTEyMw:qGxj+NkGIzvZOMz9ogEY5MWeSVvFY3krDkZ93HnTyf0"
	}
	ok := verify(password, hash)
	if e != nil || !u.Active || !ok {
		return User{}, errors.New("用户名或密码错误")
	}
	return u, nil
}
func (s *Store) CreateUser(ctx context.Context, name, display, password string) (User, error) {
	if len(name) == 0 || len(name) > 80 || strings.TrimSpace(name) != name || len(password) < 6 {
		return User{}, errors.New("需要用户名和至少 6 字符的密码")
	}
	hash, e := passwordHash(password)
	if e != nil {
		return User{}, e
	}
	var id int64
	e = s.Change(ctx, "user.create", "", map[string]any{"username": name}, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "INSERT INTO PLATFORM_USER(USERNAME_,NAME_,PASSWORD_HASH_) VALUES(?,?,?)", name, display, hash)
		if e == nil {
			id, e = r.LastInsertId()
		}
		return e
	})
	if e != nil {
		return User{}, e
	}
	return s.User(ctx, id)
}
func (s *Store) DisableUser(ctx context.Context, id int64) error {
	return s.Change(ctx, "user.disable", "", map[string]any{"user_id": id}, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "UPDATE PLATFORM_USER SET ACTIVE_=0,AUTH_VERSION_=AUTH_VERSION_+1 WHERE ID_=? AND SYSTEM_ADMIN_=0", id)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("用户不存在，或不能禁用初始化管理员")
		}
		return nil
	})
}
func (s *Store) ResetPassword(ctx context.Context, id int64, password string) error {
	if len(password) < 6 {
		return errors.New("密码至少 6 字符")
	}
	h, e := passwordHash(password)
	if e != nil {
		return e
	}
	return s.Change(ctx, "user.password.reset", "", map[string]any{"user_id": id}, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "UPDATE PLATFORM_USER SET PASSWORD_HASH_=?,AUTH_VERSION_=AUTH_VERSION_+1 WHERE ID_=?", h, id)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return errors.New("用户不存在")
		}
		return nil
	})
}

type session struct {
	UserID   int64
	Version  int64
	Resource string
	Expires  int64
	Policy   string
}

func (s *Store) NewSession(ctx context.Context, u User, resource string, duration time.Duration) (string, error) {
	current, e := s.User(ctx, u.ID)
	if e != nil || !current.Active || current.AuthVersion != u.AuthVersion {
		return "", ErrDenied
	}
	p, e := s.ResolveAuth(ctx, "API", "platform")
	if e != nil {
		return "", e
	}
	token := audit.ID() + audit.ID()
	s.mu.Lock()
	for key, v := range s.sessions {
		if v.Expires <= time.Now().Unix() {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 10000 {
		s.mu.Unlock()
		return "", errors.New("会话数量已达到上限，请稍后重试")
	}
	s.sessions[tokenHash(token)] = session{u.ID, u.AuthVersion, resource, time.Now().Add(duration).Unix(), p.Fingerprint()}
	s.mu.Unlock()
	return token, nil
}
func (s *Store) Session(ctx context.Context, token, resource string) (User, error) {
	s.mu.Lock()
	r, ok := s.sessions[tokenHash(token)]
	if !ok || r.Expires <= time.Now().Unix() {
		delete(s.sessions, tokenHash(token))
	}
	s.mu.Unlock()
	if !ok || r.Resource != resource || r.Expires <= time.Now().Unix() {
		return User{}, ErrDenied
	}
	p, e := s.ResolveAuth(ctx, "API", "platform")
	if e != nil || p.Fingerprint() != r.Policy {
		return User{}, ErrDenied
	}
	u, e := s.User(ctx, r.UserID)
	if e != nil || !u.Active || u.AuthVersion != r.Version {
		return User{}, ErrDenied
	}
	return u, nil
}
func (s *Store) Logout(ctx context.Context, token string) error {
	s.mu.Lock()
	delete(s.sessions, tokenHash(token))
	s.mu.Unlock()
	return nil
}
func (s *Store) Can(ctx context.Context, id int64, database, permission string) bool {
	u, e := s.User(ctx, id)
	if e != nil || !u.Active {
		return false
	}
	if u.SystemAdmin {
		return true
	}
	if database == "" {
		var n int
		return s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM PLATFORM_PERMISSION WHERE USER_ID_=? AND PERMISSION_=?", id, permission).Scan(&n) == nil && n > 0
	}
	g, e := s.Grant(ctx, id, database)
	return e == nil && slices.Contains(g.Permissions, permission)
}
func (s *Store) Grant(ctx context.Context, id int64, database string) (Grant, error) {
	grants, e := s.Grants(ctx, database)
	if e != nil {
		return Grant{}, e
	}
	for _, g := range grants {
		if g.UserID == id {
			return g, nil
		}
	}
	return Grant{UserID: id, DatabaseID: database}, sql.ErrNoRows
}
func (s *Store) Grants(ctx context.Context, database string) ([]Grant, error) {
	x, e := s.Source(ctx, database)
	if e != nil {
		return nil, e
	}
	var grants []Grant
	e = x.withState(ctx, false, func(st *sourceState) error { grants = st.Grants; return nil })
	return grants, e
}
func (s *Store) SaveGrant(ctx context.Context, actor int64, g Grant) error {
	if !s.Can(ctx, actor, g.DatabaseID, "grant") {
		return ErrDenied
	}
	u, e := s.User(ctx, actor)
	if e != nil {
		return e
	}
	target, e := s.User(ctx, g.UserID)
	if e != nil || !target.Active {
		return errors.New("目标用户不存在或已禁用")
	}
	for _, p := range g.Permissions {
		if !slices.Contains(Permissions, p) {
			return fmt.Errorf("未知权限 %s", p)
		}
		if !u.SystemAdmin && !s.Can(ctx, actor, g.DatabaseID, p) {
			return ErrDenied
		}
	}
	old, _ := s.Grant(ctx, g.UserID, g.DatabaseID)
	if !u.SystemAdmin {
		if old.BusinessUserID != g.BusinessUserID {
			return errors.New("业务身份绑定仅系统管理员可修改")
		}
		for _, p := range old.Permissions {
			if !s.Can(ctx, actor, g.DatabaseID, p) {
				return ErrDenied
			}
		}
	}
	if g.BusinessUserID < 0 {
		return errors.New("业务身份必须为非负编号")
	}
	return s.sourceChange(ctx, g.DatabaseID, "database.grant", map[string]any{"before": old, "after": g}, func(st *sourceState) error {
		for i, v := range st.Grants {
			if v.UserID == g.UserID {
				st.Grants[i] = g
				return nil
			}
		}
		st.Grants = append(st.Grants, g)
		return nil
	})
}
func (s *Store) BusinessID(ctx context.Context, id int64, database string) (int64, error) {
	g, e := s.Grant(ctx, id, database)
	if e != nil {
		return 0, errors.New("尚未绑定此数据库的业务身份")
	}
	if g.BusinessUserID <= 0 {
		return 0, errors.New("尚未绑定此数据库的业务身份")
	}
	return g.BusinessUserID, nil
}
func (s *Store) Records(ctx context.Context, bucket string) ([]Record, error) {
	return s.records(ctx, bucket)
}

func (s *Store) UpdateRecords(ctx context.Context, bucket, action, database string, fn func([]Record) ([]Record, error), credentials ...DatabaseCredential) error {
	return s.updateRecords(ctx, bucket, action, database, fn, credentials...)
}
func (s *Store) ValidatePath(path string) error {
	real, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	abs, e := filepath.Abs(real)
	if e != nil {
		return e
	}
	for _, p := range s.internalPaths() {
		if p == "" {
			continue
		}
		rp, _ := filepath.EvalSymlinks(p)
		a, _ := os.Stat(abs)
		b, _ := os.Stat(rp)
		if abs == rp || (a != nil && b != nil && os.SameFile(a, b)) {
			return errors.New("内部管理库和审计库不能注册为业务数据库")
		}
	}
	for _, root := range s.AllowedRoots {
		r, e := filepath.EvalSymlinks(root)
		if e != nil {
			continue
		}
		r, _ = filepath.Abs(r)
		rel, e := filepath.Rel(r, abs)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return errors.New("数据库文件不在 DATABASE_ALLOWED_ROOTS 允许的目录内")
}
func (s *Store) AuditPath() string {
	if s.Audit != nil {
		return s.Audit.Path
	}
	return ""
}
func (s *Store) EnableUser(ctx context.Context, id int64) error {
	return s.Change(ctx, "user.enable", "", map[string]any{"user_id": id}, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "UPDATE PLATFORM_USER SET ACTIVE_=1,AUTH_VERSION_=AUTH_VERSION_+1 WHERE ID_=?", id)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errors.New("用户不存在")
		}
		return nil
	})
}
