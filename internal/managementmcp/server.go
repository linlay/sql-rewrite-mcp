// Package managementmcp exposes domain actions, never arbitrary internal SQL.
package managementmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"datascope-server/internal/application"
	"datascope-server/internal/audit"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type input struct {
	Action string                     `json:"action"`
	Args   map[string]json.RawMessage `json:"args"`
}

type operation struct {
	resource, action, domain string
	required, optional       string
}

// The matrix is both a routing and argument allowlist. Grouping never changes
// the application's per-action authorization, revision checks or audit.
var operations = map[string][]operation{
	"datascope_query": {
		{"authentication", "get", "get_auth_config", "", ""},
		{"discovery", "get", "get_discovery", "database_id", ""},
		{"database", "list", "list_databases", "", ""},
		{"database", "get", "get_database", "database_id", ""},
		{"policy", "get", "get_policy", "database_id", ""},
		{"grant", "list", "list_database_grants", "database_id", ""},
		{"user", "list", "list_users", "", ""},
		{"business_account", "list", "list_business_accounts", "database_id", ""},
		{"knowledge", "list", "list_knowledge", "database_id", "query"},
		{"knowledge", "get", "get_knowledge", "database_id id", ""},
		{"knowledge", "search", "search_knowledge", "database_id query", ""},
		{"knowledge", "read", "read_knowledge", "database_id id", ""},
		{"knowledge_prompt", "list", "list_knowledge_prompts", "database_id", "cursor"},
		{"knowledge_prompt", "get", "get_knowledge_prompt", "database_id name", ""},
		{"action", "list", "list_actions", "database_id", ""},
		{"action", "describe", "describe_action", "database_id name", ""},
		{"audit", "search", "search_audit_events", "", "database_id request_id actor_id platform_only kind source status tool query started_from started_to limit offset"},
		{"audit", "page", "get_audit_page", "", "database_id request_id actor_id platform_only kind source status tool query started_from started_to limit offset"},
	},
	"datascope_manage": {
		{"database", "clone_configuration", "clone_configuration", "database_id clone", ""},
		{"authentication", "save", "save_auth_config", "auth_config", ""},
		{"action", "save", "configure_actions", "database_id database", "write_password"},
		{"discovery", "refresh", "discover_database", "database_id", ""},
		{"discovery", "apply", "apply_discovery", "database_id id revision", "resolutions"},
		{"annotation", "save", "save_annotation", "database_id table revision annotation", "column"},
		{"database", "create", "create_database", "database", "password"},
		{"database", "save", "save_database", "database_id database", ""},
		{"database", "update", "update_database", "database_id database", ""},
		{"policy", "save", "save_policy", "database_id database", ""},
		{"mcp", "save", "configure_mcp", "database_id database", ""},
		{"grant", "grant", "grant_database_permissions", "database_id user_id permissions business_user_id", ""},
		{"grant", "revoke", "revoke_database_permissions", "database_id user_id", ""},
		{"user", "create", "create_user", "username name password", ""},
		{"business_account", "save", "save_business_account", "database_id account revision", "password"},
		{"user", "enable", "enable_user", "user_id", ""},
		{"user", "disable", "disable_user", "user_id", ""},
		{"user", "reset_password", "reset_password", "user_id password", ""},
		{"knowledge", "save", "save_knowledge", "database_id id name description content published revision", "summary"},
	},
	"datascope_inspect": {
		{"database", "preview_clone_configuration", "preview_clone_configuration", "database_id clone", ""},
		{"action", "try", "try_action", "database_id name arguments request_id business_user_id", ""},
		{"connection", "test", "test_database_connection", "database", "password"},
		{"database", "test_connection", "test_connection", "database_id", ""},
		{"policy", "validate", "validate_policy", "database_id database", ""},
		{"policy", "preview_rewrite", "preview_rewrite", "database_id sql", "business_user_id parameters"},
	},
}

func decode(tool string, in input) (string, application.Input, error) {
	var out application.Input
	var resource string
	if err := json.Unmarshal(in.Args["resourceType"], &resource); err != nil || resource == "" {
		return "", out, fmt.Errorf("args.resourceType 必须是非空字符串")
	}
	for _, op := range operations[tool] {
		if resource != op.resource || in.Action != op.action {
			continue
		}
		allowed := strings.Fields(op.required + " " + op.optional)
		args := make(map[string]json.RawMessage, len(in.Args))
		for key, value := range in.Args {
			if key == "resourceType" {
				continue
			}
			if !slices.Contains(allowed, key) {
				return "", out, fmt.Errorf("%s %s.%s 不接受参数 %s", tool, resource, in.Action, key)
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return "", out, fmt.Errorf("参数 %s 不能为 null", key)
			}
			args[key] = value
		}
		if op.domain == "save_knowledge" {
			if _, ok := args["description"]; !ok {
				if legacy, exists := args["summary"]; exists {
					args["description"] = legacy
				}
			}
			delete(args, "summary")
		}
		for _, key := range strings.Fields(op.required) {
			value, exists := args[key]
			if !exists || (slices.Contains([]string{"database_id", "id", "username", "name", "password", "sql", "query"}, key) && bytes.Equal(bytes.TrimSpace(value), []byte(`""`))) {
				return "", out, fmt.Errorf("缺少参数 %s", key)
			}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return "", out, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&out); err != nil {
			return "", out, fmt.Errorf("管理参数无效: %w", err)
		}
		if (out.ActorID != nil && *out.ActorID < 0) || out.UserID < 0 || (slices.Contains(strings.Fields(op.required), "user_id") && out.UserID == 0) || out.BusinessUserID < 0 || out.Revision < 0 || out.Limit < 0 || out.Offset < 0 {
			return "", out, fmt.Errorf("用户 ID 必须为正整数，业务身份、版本和分页参数不能为负数")
		}
		if out.Database != nil && op.domain != "create_database" && op.domain != "test_database_connection" {
			if out.Database.ID != out.DatabaseID || out.Database.Revision <= 0 {
				return "", out, fmt.Errorf("database.id 必须与 database_id 一致，且 database.revision 必须为最新正整数版本")
			}
		}
		if out.Database != nil {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(args["database"], &fields); err != nil {
				return "", out, err
			}
			required := "id revision"
			switch op.domain {
			case "create_database":
				required = "name driver"
				if out.Database.Driver == "sqlite" {
					required += " path"
				} else {
					required += " connection"
				}
			case "test_database_connection":
				required = "driver"
				if out.Database.Driver == "sqlite" {
					required += " path"
				} else {
					required += " connection"
				}
			case "save_database":
				required += " name driver path config mcp_enabled mcp_path tools"
			case "update_database":
				required += " name"
			case "save_policy", "validate_policy":
				required += " config"
			case "configure_actions":
				required += " actions write_enabled"
			case "configure_mcp":
				required += " mcp_enabled mcp_path tools"
			}
			for _, key := range strings.Fields(required) {
				value, exists := fields[key]
				if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return "", out, fmt.Errorf("缺少 database.%s 或值为 null", key)
				}
			}
		}
		return op.domain, out, nil
	}
	return "", out, fmt.Errorf("%s 不支持 %s.%s；请阅读 datascope-admin skill 的操作矩阵", tool, resource, in.Action)
}

func New(app *application.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "datascope-management", Version: "2.0.0", Title: "DataScope 管理平台"}, &mcp.ServerOptions{
		Instructions: "使用 datascope-admin 连接器及配套 skill 管理用户、数据库、权限、策略和知识。工具使用 {action,args}，args.resourceType 指定资源。先读取当前配置与 revision，校验后保存并回读。业务查询使用独立数据库 MCP。",
		Capabilities: &mcp.ServerCapabilities{},
	})
	for _, name := range []string{"datascope_query", "datascope_manage", "datascope_inspect"} {
		description := map[string]string{
			"datascope_query":   "查询数据库、策略、用户、授权、知识和审计。使用前阅读 datascope-admin skill。",
			"datascope_manage":  "创建或修改数据库、策略、MCP 配置、授权、用户和知识。使用前阅读 datascope-admin skill。",
			"datascope_inspect": "测试数据库连接、校验策略或预览 SQL 改写，不保存配置或执行业务查询。使用前阅读 datascope-admin skill。",
		}[name]
		actions, resources := []string{}, []string{}
		for _, op := range operations[name] {
			if !slices.Contains(actions, op.action) {
				actions = append(actions, op.action)
			}
			if !slices.Contains(resources, op.resource) {
				resources = append(resources, op.resource)
			}
		}
		mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: name != "datascope_manage"}, InputSchema: map[string]any{
			"type": "object", "required": []string{"action", "args"}, "additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": actions},
				"args": map[string]any{"type": "object", "required": []string{"resourceType"}, "properties": map[string]any{
					"resourceType": map[string]any{"type": "string", "enum": resources},
				}, "description": "资源参数、版本与操作矩阵见 datascope-admin skill。"},
			},
		}}, func(ctx context.Context, req *mcp.CallToolRequest, in input) (*mcp.CallToolResult, any, error) {
			if req.Extra == nil || req.Extra.TokenInfo == nil {
				return nil, nil, management.ErrDenied
			}
			uid, err := strconv.ParseInt(req.Extra.TokenInfo.UserID, 10, 64)
			if err != nil {
				return nil, nil, err
			}
			u, err := app.Management.User(ctx, uid)
			if err != nil || !u.Active {
				return nil, nil, management.ErrDenied
			}
			action, args, err := decode(name, in)
			if err != nil {
				return nil, nil, err
			}
			ctx = guard.WithUser(audit.With(ctx, audit.Metadata{RequestID: audit.From(ctx).RequestID, ActorID: u.ID, DatabaseID: args.DatabaseID, Source: "management-mcp", Tool: action}), u)
			out, err := app.Call(ctx, action, args)
			return nil, out, err
		})
	}
	return server
}
