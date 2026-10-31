// Package mcpserver exposes permission-scoped business databases as MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/service"
	"datascope-server/internal/store"
)

const (
	Name    = "datascope-server"
	Version = "0.3.0"

	DefaultUserHeader = "X-User-Id"
)

// Options configures identity and debug tools.
type Options struct {
	Catalog     *catalog.Catalog
	Application *application.Service
	Management  *management.Store
	DatabaseID  string
	AuthMode    string
	Managed     bool
	Tools       []string
	// IdentityStore is the shared trusted employee directory for all data sources.
	IdentityStore *store.Store
	// UserHeader carries the trusted user id, set by the gateway in front of
	// this server. The AI never supplies identity.
	UserHeader string
	// DefaultUserID is used when the header is absent (e.g. stdio). Zero
	// means no default: requests without identity are rejected.
	DefaultUserID int64
	// Debug returns rewritten SQL from query and enables debug tools on legacy servers.
	// Managed servers use the database tool selection to enable debug tools.
	Debug bool
}

const instructions = `证券经纪业务样例库（SQLite 方言，全部为合成数据，金额单位为整数分，日期为 YYYY-MM-DD，交易时间为中国时区 RFC3339）。
建议流程：先 list_tables 了解表、中文注释和访问方案，再 describe_table 查看字段和外键，最后用 query 执行 SELECT。
query 会按当前用户的权限自动改写 SQL：FROM / JOIN 中的每张表都会被替换为该用户可见数据的子查询，因此直接写标准 SQL 即可，无需自行添加权限过滤。
访问方案为 RESTRICTED 的表（管理范围）仅超级管理员可查询。`

// New builds an MCP server backed by svc.
func New(svc *service.QueryService, opts Options) *mcp.Server {
	if opts.UserHeader == "" {
		opts.UserHeader = DefaultUserHeader
	}
	title, guidance := "证券业务 SQL 查询（权限改写）", instructions
	dialectLabel := "SQLite 方言"
	if svc != nil && svc.Store.Driver() != "sqlite" {
		dialectLabel = "受限 SELECT 子集，后端 " + svc.Store.Driver()
	}
	if opts.Managed {
		title = "SQL 数据库代理"
		guidance = "使用" + dialectLabel + "。先通过 list_tables 查看可访问表，再通过 describe_table 查看可访问字段，最后通过 query 执行只读 SQL。查询按当前身份应用表访问、字段白名单、行权限改写和每表返回行数上限。接口是否提供由当前数据库配置决定。"
		if svc != nil && svc.Store.Driver() != "sqlite" {
			guidance += " 支持常用聚合、子查询、非递归 CTE、整数 LIMIT/OFFSET；不支持窗口函数、CAST、自定义函数、原生专有语法或跨 schema 查询。使用结构返回的字段名与显式表别名。"
		}
	}
	if opts.Management != nil {
		guidance += " 业务知识通过 prompts/list 查看名称与 description，再通过 prompts/get(name) 按需读取正文。SELECT 直接使用 query；DML 使用 Action。知识和示例不会扩大访问权限。"
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    Name,
		Title:   title,
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: guidance,
		// Expose tools and knowledge Prompts, without the SDK's historical logging capability.
		Capabilities: &mcp.ServerCapabilities{Prompts: func() *mcp.PromptCapabilities {
			if opts.Management != nil {
				return &mcp.PromptCapabilities{}
			}
			return nil
		}()},
	})
	h := handlers{svc: svc, opts: opts}
	businessMiddleware(server, h)
	if opts.Management != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "list_actions", Description: "列出可用业务 action 的名称、说明和人工确认要求；再用 describe_action(name) 获取完整参数格式。", Annotations: readOnly("列出业务操作")}, h.listActions)
		mcp.AddTool(server, &mcp.Tool{Name: "describe_action", Description: "按 name 查看单个业务 action 的说明和完整参数 JSON Schema。先用 list_actions 获取名称。", Annotations: readOnly("查看业务操作")}, h.describeAction)
		mcp.AddTool(server, &mcp.Tool{Name: "invoke_action", Description: "执行业务 action。request_id 必须唯一，同一服务运行期间重试使用同一值。回执仅保存在内存，服务重启后清空，不应自动重试重启前结果未知的写入。confirm_required 时向人展示 preview 和 confirmation_url；由本人打开页面登录确认后，使用相同 request_id 查询结果。模型不能代替人确认。", Annotations: &mcp.ToolAnnotations{Title: "执行业务操作", IdempotentHint: false}}, h.invokeAction)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tables",
		Title:       "列出数据表",
		Description: "列出当前数据库允许访问的数据表、表注释和访问方案。",
		Annotations: readOnly("列出数据表"),
	}, h.listTables)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "describe_table",
		Title:       "查看表结构",
		Description: "返回指定表允许访问的字段、类型与访问方案；配置字段白名单时隐藏完整建表语句、索引及外键信息。",
		Annotations: readOnly("查看表结构"),
	}, h.describeTable)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "query",
		Title: "执行 SQL（按权限改写）",
		Description: fmt.Sprintf("执行单条 SELECT（%s）。服务端会按当前用户权限把 FROM / JOIN 中的每张表改写为带权限的子查询，"+
			"结果只包含该用户可见的数据。不支持写操作、多语句、表值函数、schema 限定表名和 “IN 表名” 写法。默认最多返回 %d 行，上限 %d 行。",
			dialectLabel, store.DefaultMaxRows, store.HardMaxRows),
		Annotations: readOnly("执行 SQL（按权限改写）"),
	}, h.query)

	if opts.Debug || opts.Managed {
		mcp.AddTool(server, &mcp.Tool{
			Name:  "raw_query",
			Title: "原始 SQL（调试）",
			Description: "全量只读查询：不做权限改写，仅允许单条查询或结构查看 PRAGMA。" +
				"需要全量查询授权；旧员工模式仅管理员可调用，OAuth 还需 sql:debug。",
			Annotations: readOnly("原始 SQL（调试）"),
		}, h.rawQuery)

		mcp.AddTool(server, &mcp.Tool{
			Name:  "customer_scope",
			Title: "计算客户可见范围（调试）",
			Description: "调试专用：按参考权限规则计算任意用户可见的客户编号，用于核对 query 的改写结果。" +
				"规则：在职员工的名下客户，加上生效管理范围内客户；管理员可见全部。",
			Annotations: readOnly("计算客户可见范围（调试）"),
		}, h.customerScope)
	}

	if opts.Managed {
		for _, name := range []string{"list_tables", "describe_table", "query", "raw_query", "customer_scope", "search_knowledge", "read_knowledge", "list_actions", "describe_action", "invoke_action"} {
			if !slices.Contains(opts.Tools, name) {
				server.RemoveTools(name)
			}
		}
	}
	return server
}

func readOnly(title string) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed}
}

type handlers struct {
	svc  *service.QueryService
	opts Options
}

// userID resolves the trusted identity: the gateway header, else the
// configured default. Tool arguments are never consulted.
func (h handlers) userID(req *mcp.CallToolRequest) (int64, error) {
	if h.opts.AuthMode == "oauth" {
		if req.Extra == nil || req.Extra.TokenInfo == nil {
			return 0, fmt.Errorf("缺少 OAuth 身份")
		}
		id, err := strconv.ParseInt(req.Extra.TokenInfo.UserID, 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("无效 OAuth 身份")
		}
		return id, nil
	}
	if h.opts.AuthMode != "none" && req.Extra != nil && req.Extra.Header != nil {
		if v := req.Extra.Header.Get(h.opts.UserHeader); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				return 0, fmt.Errorf("请求头 %s 不是合法的用户编号", h.opts.UserHeader)
			}
			return id, nil
		}
	}
	if h.opts.DefaultUserID > 0 {
		return h.opts.DefaultUserID, nil
	}
	return 0, fmt.Errorf("缺少可信用户身份：需由网关在请求头 %s 中传入用户编号", h.opts.UserHeader)
}

// Access is a table's rewrite scheme as shown to the model.
type Access struct {
	Scheme rewrite.Scheme `json:"scheme"`
	Name   string         `json:"name"`
}

// AccessFor returns the access scheme reported for a table.
func AccessFor(cfg *rewrite.Config, table string) Access {
	pol, ok := cfg.Lookup(table)
	if !ok {
		return Access{}
	}
	return Access{Scheme: pol.Scheme, Name: cfg.SchemeName(pol.Scheme)}
}

type listTablesInput struct{}

type tableEntry struct {
	DisplayName string `json:"display_name,omitempty"`
	Name        string `json:"name"`
	Comment     string `json:"comment"`
	Access      Access `json:"access"`
}

type listTablesOutput struct {
	Tables []tableEntry `json:"tables"`
}

func (h handlers) listTables(ctx context.Context, _ *mcp.CallToolRequest, _ listTablesInput) (*mcp.CallToolResult, listTablesOutput, error) {
	h = h.current(ctx)
	tables, err := h.svc.Store.Tables(ctx)
	if err != nil {
		return nil, listTablesOutput{}, err
	}
	out := listTablesOutput{Tables: []tableEntry{}}
	for _, t := range tables {
		if pol, ok := h.svc.Config().Lookup(t.Name); ok && pol.Enabled != nil && !*pol.Enabled {
			continue
		}
		// Row counts are global, not per user, so they are not exposed here.
		out.Tables = append(out.Tables, tableEntry{Name: t.Name, DisplayName: t.DisplayName, Comment: t.Comment, Access: AccessFor(h.svc.Config(), t.Name)})
	}
	return nil, out, nil
}

type describeTableInput struct {
	Table string `json:"table" jsonschema:"表名，例如 customers；可先调用 list_tables 获取"`
}

type describeTableOutput struct {
	*store.TableSchema
	Access Access `json:"access"`
}

func (h handlers) describeTable(ctx context.Context, _ *mcp.CallToolRequest, in describeTableInput) (*mcp.CallToolResult, *describeTableOutput, error) {
	h = h.current(ctx)
	pol, ok := h.svc.Config().Lookup(in.Table)
	if !ok || (pol.Enabled != nil && !*pol.Enabled) {
		return nil, nil, fmt.Errorf("表不可访问")
	}
	schema, err := h.svc.Store.Describe(ctx, in.Table)
	if err != nil {
		return nil, nil, err
	}
	if pol.Columns != nil {
		allowed := []store.Column{}
		for _, col := range schema.Columns {
			if slices.ContainsFunc(pol.Columns, func(name string) bool { return strings.EqualFold(name, col.Name) }) {
				allowed = append(allowed, col)
			}
		}
		schema.Columns = allowed
		schema.DDL = ""
		schema.Indexes = []string{}
		for i := range schema.Columns {
			schema.Columns[i].References = ""
		}
	}
	return nil, &describeTableOutput{TableSchema: schema, Access: AccessFor(h.svc.Config(), schema.Name)}, nil
}

type queryInput struct {
	SQL        string         `json:"sql" jsonschema:"单条 SELECT，使用服务器 Instructions 声明的方言，无需添加权限过滤"`
	MaxRows    int            `json:"max_rows,omitempty" jsonschema:"最多返回的行数，默认 200，上限 1000"`
	Parameters map[string]any `json:"parameters,omitempty" jsonschema:"可选绑定参数，键使用 arg_ 前缀，例如 SQL 中 :arg_name 对应 parameters.arg_name；身份参数由服务端提供"`
}

type queryOutput struct {
	*store.QueryResult
	Tables []rewrite.TableRef `json:"tables"`
	// Only in debug mode.
	UserID       int64          `json:"user_id,omitempty"`
	RewrittenSQL string         `json:"rewritten_sql,omitempty"`
	Binds        map[string]any `json:"binds,omitempty"`
}

func (h handlers) query(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *queryOutput, error) {
	h = h.current(ctx)
	uid, err := h.businessID(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Parameters) > 100 {
		return nil, nil, fmt.Errorf("绑定参数最多 100 个")
	}
	for name, value := range in.Parameters {
		if !queryParameterName.MatchString(name) {
			return nil, nil, fmt.Errorf("业务参数必须使用 arg_ 前缀，不能覆盖身份参数")
		}
		switch v := value.(type) {
		case nil, string, bool:
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, nil, fmt.Errorf("参数 %s 必须是有限数字", name)
			}
		default:
			return nil, nil, fmt.Errorf("参数 %s 必须是标量", name)
		}
	}
	res, err := h.svc.QueryWithParameters(ctx, in.SQL, uid, in.MaxRows, in.Parameters)
	if err != nil {
		return nil, nil, err
	}
	out := &queryOutput{QueryResult: res.QueryResult, Tables: res.Tables}
	if h.opts.Debug {
		out.UserID, out.RewrittenSQL, out.Binds = uid, res.RewrittenSQL, res.Binds
	}
	return nil, out, nil
}

var queryParameterName = regexp.MustCompile(`^arg_[A-Za-z][A-Za-z0-9_]{0,59}$`)

type rawQueryInput struct {
	SQL     string `json:"sql" jsonschema:"单条只读 SELECT / WITH / VALUES 或允许的结构查看 PRAGMA，按原样执行，不做权限改写"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"最多返回的行数，默认 200，上限 1000"`
}

func (h handlers) rawQuery(ctx context.Context, req *mcp.CallToolRequest, in rawQueryInput) (*mcp.CallToolResult, *store.QueryResult, error) {
	h = h.current(ctx)
	if err := h.debugAllowed(ctx, req); err != nil {
		return nil, nil, err
	}
	result, err := h.svc.Store.RawQuery(ctx, in.SQL, in.MaxRows)
	if err != nil {
		return nil, nil, err
	}
	return nil, result, nil
}

type customerScopeInput struct {
	UserID int64  `json:"user_id" jsonschema:"系统用户编号，例如 1（客户经理）、121（营业部主管）、175（管理员）"`
	AsOf   string `json:"as_of,omitempty" jsonschema:"参考时间，RFC 3339 格式，默认与改写使用的参考时间相同"`
}

type customerScopeOutput struct {
	UserID      int64    `json:"user_id"`
	AsOf        string   `json:"as_of"`
	CustomerIDs []string `json:"customer_ids"`
}

func (h handlers) customerScope(ctx context.Context, req *mcp.CallToolRequest, in customerScopeInput) (*mcp.CallToolResult, customerScopeOutput, error) {
	h = h.current(ctx)
	if err := h.debugAllowed(ctx, req); err != nil {
		return nil, customerScopeOutput{}, err
	}
	asOf := h.svc.AsOf
	if in.AsOf != "" {
		parsed, err := time.Parse(time.RFC3339, in.AsOf)
		if err != nil {
			return nil, customerScopeOutput{}, fmt.Errorf("as_of 不是合法的 RFC 3339 时间: %w", err)
		}
		asOf = parsed
	}
	ids, err := h.svc.Store.VisibleCustomerIDs(ctx, in.UserID, asOf)
	if err != nil {
		return nil, customerScopeOutput{}, err
	}
	return nil, customerScopeOutput{UserID: in.UserID, AsOf: asOf.UTC().Format(time.RFC3339), CustomerIDs: ids}, nil
}

func (h handlers) debugAllowed(ctx context.Context, req *mcp.CallToolRequest) error {
	if h.opts.Management != nil {
		return guard.Require(ctx, h.opts.Management, h.opts.DatabaseID, "raw_query")
	}
	if h.opts.AuthMode == "oauth" {
		if req.Extra == nil || req.Extra.TokenInfo == nil || !slices.Contains(req.Extra.TokenInfo.Scopes, "sql:debug") {
			return fmt.Errorf("需要 sql:debug 权限")
		}
		return nil
	}
	uid, err := h.userID(req)
	if err != nil {
		return err
	}
	identity := h.opts.IdentityStore
	if identity == nil {
		identity = h.svc.Store
	}
	users, err := identity.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.ID == uid && u.Status == "active" && u.RoleCode == "ADMIN" {
			return nil
		}
	}
	return fmt.Errorf("仅在职管理员可使用调试工具")
}
