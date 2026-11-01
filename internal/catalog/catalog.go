// Package catalog persists database connections and their independent proxy policies.
package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"datascope-server/internal/action"
	"datascope-server/internal/audit"
	"datascope-server/internal/management"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/service"
	"datascope-server/internal/store"
	"datascope-server/internal/view"
)

var DefaultTools = []string{"list_tables", "describe_table", "query"}
var Tools = []string{"list_tables", "describe_table", "query", "raw_query", "customer_scope", "search_knowledge", "read_knowledge", "list_actions", "describe_action", "invoke_action"}
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Database struct {
	Views                  []view.Definition   `json:"views,omitempty" yaml:"views,omitempty"`
	QueryViewID            string              `json:"query_view_id,omitempty" yaml:"queryViewId,omitempty"`
	AuthPolicyFingerprint  string              `json:"-" yaml:"-"`
	Actions                []action.Definition `json:"actions,omitempty" yaml:"actions,omitempty"`
	ActionLibraries        []action.Library    `json:"action_libraries,omitempty" yaml:"actionLibraries,omitempty"`
	WriteEnabled           bool                `json:"write_enabled,omitempty" yaml:"writeEnabled,omitempty"`
	WriteConnection        *store.Connection   `json:"write_connection,omitempty" yaml:"writeConnection,omitempty"`
	Discovery              *Discovery          `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	AuthenticationRevision int64               `json:"authentication_revision,omitempty" yaml:"authenticationRevision,omitempty"`
	Authentication         *Authentication     `json:"authentication,omitempty" yaml:"authentication,omitempty"`
	ID                     string              `json:"id" yaml:"id"`
	Name                   string              `json:"name" yaml:"name"`
	Driver                 string              `json:"driver" yaml:"driver"`
	Path                   string              `json:"path" yaml:"path"`
	Connection             *store.Connection   `json:"connection,omitempty" yaml:"connection,omitempty"`
	MCPEnabled             bool                `json:"mcp_enabled" yaml:"mcpEnabled"`
	KnowledgeEnabled       *bool               `json:"knowledge_enabled,omitempty" yaml:"knowledgeEnabled,omitempty"`
	MCPPath                string              `json:"mcp_path" yaml:"mcpPath,omitempty"`
	Tools                  []string            `json:"tools" yaml:"tools"`
	Config                 *rewrite.Config     `json:"config" yaml:"config"`
	Revision               int                 `json:"revision" yaml:"revision"`
	ETag                   string              `json:"etag,omitempty" yaml:"-"`
}
type Catalog struct {
	Management     *management.Store
	Audit          *audit.Store
	Path           string
	Timeout        time.Duration
	AsOf           time.Time
	scanMu         sync.Mutex
	runtimeMu      sync.Mutex
	discoveryState map[string]*discoveryRuntime
	mu             sync.Mutex
}

// Bootstrap imports legacy JSON or the initial rewrite rules only once.
func Bootstrap(path string, st *store.Store, configPath string, asOf time.Time, timeout time.Duration, legacyPaths ...string) (*Catalog, error) {
	c := &Catalog{Path: path, AsOf: asOf, Timeout: timeout}
	legacyPath := "data/databases.json"
	if len(legacyPaths) > 0 {
		legacyPath = legacyPaths[0]
	}
	err := c.update(func(items *[]Database) error {
		if len(*items) > 0 {
			return migrateDatabaseNames(items)
		}
		data, err := os.ReadFile(legacyPath)
		if err == nil {
			if err = json.Unmarshal(data, items); err != nil {
				return fmt.Errorf("迁移旧数据库配置: %w", err)
			}
			if len(*items) == 0 {
				return fmt.Errorf("旧数据库配置为空")
			}
			return migrateDatabaseNames(items)
		}
		if !os.IsNotExist(err) {
			return err
		}
		if st == nil {
			return nil
		}
		cfg, err := rewrite.LoadConfig(configPath)
		if err != nil {
			return err
		}
		*items = []Database{{ID: "default", Name: databaseName(st.Path()), Driver: "sqlite", Path: st.Path(), MCPEnabled: true, MCPPath: "default", Tools: DefaultTools, Config: cfg, Revision: 1}}
		return nil
	})
	return c, err
}

func New(path string, st *store.Store, cfg *rewrite.Config, asOf time.Time, timeout time.Duration) (*Catalog, error) {
	c := &Catalog{Path: path, AsOf: asOf, Timeout: timeout}
	err := c.update(func(items *[]Database) error {
		if len(*items) == 0 {
			*items = []Database{{ID: "default", Name: databaseName(st.Path()), Driver: "sqlite", Path: st.Path(), MCPEnabled: true, MCPPath: "default", Tools: DefaultTools, Config: cfg, Revision: 1}}
		}
		return migrateDatabaseNames(items)
	})
	return c, err
}

func databaseName(path string) string {
	name := filepath.Base(path)
	if stem := strings.TrimSuffix(name, filepath.Ext(name)); stem != "" {
		return stem
	}
	return name
}

func migrateDatabaseNames(items *[]Database) error {
	for i := range *items {
		changed := false
		if (*items)[i].Name == "默认数据库" {
			(*items)[i].Name = databaseName((*items)[i].Path)
			changed = true
		}
		if (*items)[i].MCPPath == "" {
			(*items)[i].MCPPath = (*items)[i].ID
			changed = true
		}
		if changed {
			(*items)[i].Revision++
		}
	}
	return nil
}

// Each database is stored in its own YAML file. Invalid files fail closed.
func (c *Catalog) List() ([]Database, error) {
	if c.Management != nil {
		return c.listManaged(context.Background())
	}
	entries, err := os.ReadDir(c.Path)
	if err != nil {
		return nil, err
	}
	items := []Database{}
	seen := map[string]bool{}
	routes := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		path := filepath.Join(c.Path, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var item Database
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err = dec.Decode(&item); err != nil {
			return nil, fmt.Errorf("配置 %s: %w", path, err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("配置 %s 只能包含一个 YAML 文档", path)
		}
		item.ETag = fmt.Sprintf("%x", sha256.Sum256(data))
		if !validID.MatchString(item.ID) || seen[item.ID] {
			return nil, fmt.Errorf("配置 %s 的数据库 ID 无效或重复", path)
		}
		if entry.Name() != configFilename(item) {
			return nil, fmt.Errorf("配置 %s 的文件名应为 %s", path, configFilename(item))
		}
		if !validID.MatchString(item.MCPRoute()) || routes[item.MCPRoute()] {
			return nil, fmt.Errorf("配置 %s 的 MCP 路径无效或重复", path)
		}
		routes[item.MCPRoute()] = true
		seen[item.ID] = true
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if (items[i].ID == "default") != (items[j].ID == "default") {
			return items[i].ID == "default"
		}
		return items[i].ID < items[j].ID
	})
	c.overlayDiscovery(items)
	return items, nil
}

// MCPRoute is the path segment beneath the shared MCP prefix. Legacy files
// without mcp_path retain their existing database-ID endpoint.
func (d Database) MCPRoute() string {
	if d.MCPPath != "" {
		return d.MCPPath
	}
	return d.ID
}

// AllowsMCPResource accepts only configured, enabled endpoints on the pinned
// public resource URL. An arbitrary URL or a renamed/disabled route is rejected.
func (c *Catalog) AllowsMCPResource(base, resource string) bool {
	items, err := c.List()
	if err != nil {
		return false
	}
	for _, item := range items {
		if !item.MCPEnabled {
			continue
		}
		if resource == strings.TrimRight(base, "/")+"/"+item.MCPRoute() || (item.ID == "default" && resource == base) {
			return true
		}
	}
	return false
}

func (c *Catalog) ByMCPPath(segment string) (*Database, error) {
	items, err := c.List()
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.MCPRoute() == segment {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("MCP 路径不存在: %s", segment)
}

func configFilename(d Database) string {
	if d.ID == "default" && d.Driver == "sqlite" {
		return databaseName(d.Path) + ".yml"
	}
	return d.ID + ".yml"
}

func (c *Catalog) Get(id string) (*Database, error) {
	if c.Management != nil {
		return c.getManaged(context.Background(), id)
	}
	items, err := c.List()
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("数据库不存在: %s", id)
}
func (c *Catalog) Open(ctx context.Context, d *Database) (*service.QueryService, error) {
	if c.Management != nil && d.Driver == "sqlite" {
		if err := c.Management.ValidatePath(d.Path); err != nil {
			return nil, err
		}
	}
	if err := store.ValidateConnection(d.Driver, d.Connection); err != nil {
		return nil, err
	}
	if d.Config == nil {
		return nil, fmt.Errorf("缺少规则配置")
	}
	data, err := json.Marshal(d.Config)
	if err != nil {
		return nil, err
	}
	var cfg rewrite.Config
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	st, err := c.OpenStore(ctx, d)
	if err != nil {
		return nil, err
	}
	svc, err := service.New(ctx, st, &cfg, c.AsOf)
	if err != nil {
		st.Close()
		return nil, err
	}
	if err := validateActions(*d); err != nil {
		st.Close()
		return nil, err
	}
	svc.Management = c.Management
	svc.Audit = c.Audit
	if c.Management != nil {
		svc.Audit = c.Management.AuditFor(d.ID)
	}
	svc.DatabaseID = d.ID
	svc.PolicyVersion = d.Revision
	st.Audit = svc.Audit
	return svc, nil
}
func (c *Catalog) Add(ctx context.Context, d Database) error {
	_, err := c.Create(ctx, d, "")
	return err
}

// Create generates a stable internal identity; callers only need a display name.
func (c *Catalog) Create(ctx context.Context, d Database, password string) (*Database, error) {
	if d.ID == "" {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		d.ID = fmt.Sprintf("db_%x", raw)
	}
	d.Connection = store.ConnectionDefaults(d.Driver, d.Connection)
	if err := c.addDatabase(ctx, d, password); err != nil {
		return nil, err
	}
	created, err := c.Get(d.ID)
	if err != nil {
		return nil, err
	}
	if created.Discovery != nil {
		c.recordDiscoveryReport(d.ID, SchemaReport{Version: created.Discovery.Version, At: time.Now().UTC(), Changes: schemaDiff(nil, created.Discovery.Tables, nil)})
		c.recordDiscoveryCheck(d.ID, time.Now().UTC(), nil)
	}
	return c.Get(d.ID)
}

func (c *Catalog) addDatabase(ctx context.Context, d Database, password string) error {
	if d.Authentication != nil {
		if c.Management == nil {
			if d.Authentication.Access != nil || d.Authentication.Source != "" || d.Authentication.Provider != nil {
				return fmt.Errorf("逐库认证需要管理存储")
			}
		} else if err := c.Management.ValidateDatabaseAuth(ctx, d.Authentication.Connection()); err != nil {
			return err
		}
	}
	var credentials []management.DatabaseCredential
	if password != "" {
		if c.Management == nil {
			return fmt.Errorf("直接输入密码需要使用管理数据库")
		}
		if d.Driver == "sqlite" || d.Connection == nil {
			return fmt.Errorf("仅网络数据库接受连接密码")
		}
		if d.Connection.PasswordEnv != "" || d.Connection.CredentialID != "" {
			return fmt.Errorf("请只提供一种数据库密码来源")
		}
		d.Connection.CredentialID = d.ID
	}

	if d.Authentication == nil && c.Management != nil {
		d.Authentication = &Authentication{Type: "local", Label: d.Name + "业务账号"}
	}
	if err := d.ValidateAuthentication(); err != nil {
		return err
	}
	if c.Management != nil && d.Driver == "sqlite" {
		if err := c.Management.ValidatePath(d.Path); err != nil {
			return err
		}
	}
	if !validID.MatchString(d.ID) || d.Name == "" {
		return fmt.Errorf("需要名称和合法 ID（字母、数字、下划线或连字符，最多 64 位）")
	}
	if err := store.ValidateConnection(d.Driver, d.Connection); err != nil {
		return err
	}
	if d.MCPPath == "" {
		d.MCPPath = d.ID
	}
	if d.Driver == "sqlite" {
		path, err := filepath.Abs(d.Path)
		if err != nil || d.Path == "" {
			return fmt.Errorf("需要数据库文件路径")
		}
		d.Path = path
	} else if d.Path != "" {
		return fmt.Errorf("网络数据库不能设置 path")
	}
	var st *store.Store
	var err error
	if password != "" {
		st, err = store.OpenConnectionWithPassword(ctx, d.Driver, d.Path, d.Connection, password, c.Timeout)
	} else {
		st, err = store.OpenConnection(ctx, d.Driver, d.Path, d.Connection, c.Timeout)
	}
	if err != nil {
		return err
	}
	defer st.Close()
	tables, err := captureSchema(ctx, st)
	if err != nil {
		return err
	}
	d.Config = &rewrite.Config{Version: 1, Schemes: []rewrite.SchemeInfo{{Scheme: "NONE", Name: "公共访问", Description: "不进行行权限改写"}, {Scheme: "CUSTOM", Name: "自定义改写", Description: "使用完整 SQL 过滤数据"}}, Tables: []*rewrite.Policy{}}
	d.Discovery = nil
	applyDiscovery(&d, tables)
	d.MCPEnabled = false
	d.Actions = nil
	d.ActionLibraries = nil
	d.WriteEnabled = false
	d.WriteConnection = nil
	d.Tools = append([]string(nil), DefaultTools...)
	d.Revision = 1
	d.AuthenticationRevision = 1
	if password != "" {
		sealed, err := c.Management.SealDatabaseCredential(d.ID, password)
		if err != nil {
			return err
		}
		credentials = append(credentials, sealed)
	}
	return c.updateContext(ctx, "database.create", d.ID, func(items *[]Database) error {
		for _, item := range *items {
			if item.ID == d.ID {
				return fmt.Errorf("数据库 ID 已存在")
			}
		}
		*items = append(*items, d)
		return nil
	}, credentials...)
}
func (c *Catalog) Save(ctx context.Context, d Database) error {
	return c.SaveWithWritePassword(ctx, d, "")
}

func (c *Catalog) SaveWithWritePassword(ctx context.Context, d Database, password string) error {
	if d.Authentication != nil {
		if c.Management == nil {
			if d.Authentication.Access != nil || d.Authentication.Source != "" || d.Authentication.Provider != nil {
				return fmt.Errorf("逐库认证需要管理存储")
			}
		} else if err := c.Management.ValidateDatabaseAuth(ctx, d.Authentication.Connection()); err != nil {
			return err
		}
	}
	var credentials []management.DatabaseCredential
	if password != "" {
		if c.Management == nil || d.Driver == "sqlite" || d.WriteConnection == nil {
			return fmt.Errorf("写入密码仅用于网络数据库")
		}
		if d.WriteConnection.PasswordEnv != "" {
			return fmt.Errorf("请只提供一种写入密码来源")
		}
		d.WriteConnection.CredentialID = management.WriteCredentialID(d.ID)
		sealed, e := c.Management.SealDatabaseCredential(d.WriteConnection.CredentialID, password)
		if e != nil {
			return e
		}
		credentials = append(credentials, sealed)
	}
	if err := d.ValidateAuthentication(); err != nil {
		return err
	}
	if d.Tools == nil {
		d.Tools = []string{}
	}
	if d.Name == "" {
		return fmt.Errorf("数据库名称不能为空")
	}
	for _, tool := range d.Tools {
		found := false
		for _, allowed := range Tools {
			if allowed == tool {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("不支持的 MCP 接口 %s", tool)
		}
	}
	return c.updateContext(ctx, "database.update", d.ID, func(items *[]Database) error {
		for i, old := range *items {
			if old.ID == d.ID {
				if old.Revision != d.Revision || old.ETag != d.ETag {
					return fmt.Errorf("配置已被其他窗口修改，请刷新后重试")
				}
				d.Discovery = old.Discovery
				d.AuthenticationRevision = old.AuthenticationRevision
				if !reflect.DeepEqual(d.Authentication, old.Authentication) {
					d.AuthenticationRevision++
				}
				if d.Driver != old.Driver || d.Path != old.Path || !reflect.DeepEqual(d.Connection, old.Connection) {
					return fmt.Errorf("连接信息登记后固定，请新建数据源")
				}
				svc, err := c.Open(ctx, &d)
				if err != nil {
					return err
				}
				svc.Store.Close()
				d.Revision++
				(*items)[i] = d
				return nil
			}
		}
		return fmt.Errorf("数据库不存在")
	}, credentials...)
}
func (c *Catalog) update(fn func(*[]Database) error) error {
	return c.updateContext(context.Background(), "database.import", "", fn)
}
func (c *Catalog) updateContext(ctx context.Context, action, database string, fn func(*[]Database) error, credentials ...management.DatabaseCredential) error {
	if c.Management != nil {
		return c.updateManaged(ctx, action, database, fn, credentials...)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.Path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(c.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	items, err := c.List()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	before := map[string][]byte{}
	for _, item := range items {
		data, err := yaml.Marshal(item)
		if err != nil {
			return err
		}
		before[item.ID] = data
	}
	if err = fn(&items); err != nil {
		return err
	}
	files := map[string][]byte{}
	routes := map[string]bool{}
	for _, item := range items {
		if !validID.MatchString(item.ID) {
			return fmt.Errorf("无效数据库 ID %q", item.ID)
		}
		if !validID.MatchString(item.MCPRoute()) {
			return fmt.Errorf("MCP 路径只允许字母、数字、下划线或连字符，最多 64 位")
		}
		if routes[item.MCPRoute()] {
			return fmt.Errorf("MCP 路径 %s 已被其他数据库使用", item.MCPRoute())
		}
		routes[item.MCPRoute()] = true
		name := configFilename(item)
		if _, ok := files[name]; ok {
			return fmt.Errorf("数据库配置文件名冲突: %s", name)
		}
		data, err := yaml.Marshal(item)
		if err != nil {
			return err
		}
		files[name] = data
	}
	if _, err = os.Stat(c.Path); os.IsNotExist(err) {
		// Publish initial imports as one complete directory, including after a crash.
		staged, err := os.MkdirTemp(filepath.Dir(c.Path), ".databases-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staged)
		for name, data := range files {
			if err = atomicWrite(filepath.Join(staged, name), data); err != nil {
				return err
			}
		}
		return os.Rename(staged, c.Path)
	} else if err != nil {
		return err
	}
	for _, item := range items {
		name := configFilename(item)
		data := files[name]
		if bytes.Equal(before[item.ID], data) {
			continue
		}
		if err = atomicWrite(filepath.Join(c.Path, name), data); err != nil {
			return err
		}
	}
	return nil
}
func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".database-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
