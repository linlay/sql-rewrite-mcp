// Package application exposes the same authorized domain actions to HTTP and MCP.
package application

import (
	"context"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type Service struct {
	Management       *management.Store
	Catalog          *catalog.Catalog
	WebURL           string
	ManagementMCPURL string
	MCPBase          string
	SecureCookies    bool
}
type Input struct {
	Clone          *catalog.CloneRequest          `json:"clone,omitempty"`
	AuthConfig     *management.AuthConfig         `json:"auth_config,omitempty"`
	Arguments      map[string]any                 `json:"arguments,omitempty"`
	WritePassword  string                         `json:"write_password,omitempty"`
	ActorID        *int64                         `json:"actor_id,omitempty"`
	PlatformOnly   bool                           `json:"platform_only,omitempty"`
	Kind           string                         `json:"kind,omitempty"`
	Source         string                         `json:"source,omitempty"`
	Status         string                         `json:"status,omitempty"`
	Tool           string                         `json:"tool,omitempty"`
	StartedFrom    string                         `json:"started_from,omitempty"`
	StartedTo      string                         `json:"started_to,omitempty"`
	Resolutions    []catalog.AnnotationResolution `json:"resolutions,omitempty"`
	Table          string                         `json:"table,omitempty"`
	Column         string                         `json:"column,omitempty"`
	Annotation     *store.Annotation              `json:"annotation,omitempty"`
	Account        *management.BusinessAccount    `json:"account,omitempty"`
	DatabaseID     string                         `json:"database_id,omitempty"`
	UserID         int64                          `json:"user_id,omitempty"`
	Username       string                         `json:"username,omitempty"`
	Name           string                         `json:"name,omitempty"`
	Password       string                         `json:"password,omitempty"`
	Database       *catalog.Database              `json:"database,omitempty"`
	Permissions    []string                       `json:"permissions,omitempty"`
	BusinessUserID int64                          `json:"business_user_id,omitempty"`
	SQL            string                         `json:"sql,omitempty"`
	Query          string                         `json:"query,omitempty"`
	ID             string                         `json:"id,omitempty"`
	Description    *string                        `json:"description,omitempty"`
	Summary        *string                        `json:"summary,omitempty"` // Legacy request alias.
	Content        string                         `json:"content,omitempty"`
	Tables         []string                       `json:"tables,omitempty"`
	Published      bool                           `json:"published,omitempty"`
	Revision       int                            `json:"revision,omitempty"`
	Limit          int                            `json:"limit,omitempty"`
	Offset         int                            `json:"offset,omitempty"`
	RequestID      string                         `json:"request_id,omitempty"`
	Cursor         string                         `json:"cursor,omitempty"`
}
type Knowledge struct {
	ID          string   `json:"id"`
	DatabaseID  string   `json:"database_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Content     string   `json:"content,omitempty"`
	Tables      []string `json:"tables"`
	Published   bool     `json:"published"`
	Revision    int      `json:"revision"`
}

// Stored records and old clients may still use summary; responses always use description.
func (k *Knowledge) UnmarshalJSON(raw []byte) error {
	type plain Knowledge
	var value struct {
		*plain
		Description *string `json:"description"`
		Summary     *string `json:"summary"`
	}
	*k = Knowledge{}
	value.plain = (*plain)(k)
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Description != nil {
		k.Description = *value.Description
	} else if value.Summary != nil {
		k.Description = *value.Summary
	}
	return nil
}

func (s *Service) Call(ctx context.Context, action string, in Input) (out any, err error) {
	m := audit.From(ctx)
	m.Tool = action
	m.DatabaseID = in.DatabaseID
	audit.UpdateRequest(ctx, in.DatabaseID, 0)
	ctx = audit.With(ctx, m)
	ev, e := s.Management.Audit.Start(ctx, "action."+action, "")
	if e != nil {
		return nil, e
	}
	defer func() { err = audit.Join(err, s.Management.Audit.Finish(ev, err)) }()
	if _, business := guard.Business(ctx); business {
		if action == "list_knowledge_prompts" {
			return s.ListKnowledgePrompts(ctx, in)
		}
		if action == "get_knowledge_prompt" {
			return s.GetKnowledgePrompt(ctx, in)
		}
		if action == "search_knowledge" || action == "read_knowledge" {
			return s.ReadKnowledge(ctx, in, action == "read_knowledge", false)
		}
		return nil, management.ErrDenied
	}
	u, ok := guard.User(ctx)
	if !ok {
		return nil, management.ErrDenied
	}
	require := func(p string) error { return guard.Require(ctx, s.Management, in.DatabaseID, p) }
	switch action {
	case "preview_clone_configuration", "clone_configuration":
		return s.CloneConfiguration(ctx, in, action == "clone_configuration")
	case "list_knowledge_prompts":
		return s.ListKnowledgePrompts(ctx, in)
	case "get_knowledge_prompt":
		return s.GetKnowledgePrompt(ctx, in)
	case "get_auth_config", "save_auth_config":
		if !u.SystemAdmin {
			return nil, management.ErrDenied
		}
		if action == "get_auth_config" {
			return s.Management.AuthConfig(ctx)
		}
		if in.AuthConfig == nil {
			return nil, errors.New("需要 auth_config")
		}
		if e := s.Management.SaveAuthConfig(ctx, *in.AuthConfig); e != nil {
			return nil, e
		}
		return s.Management.AuthConfig(ctx)
	case "list_actions":
		return s.InspectAction(ctx, in.DatabaseID, "")
	case "describe_action":
		if in.Name == "" {
			return nil, errors.New("需要 action 名称，请先调用 list_actions")
		}
		return s.InspectAction(ctx, in.DatabaseID, in.Name)
	case "try_action":
		if e := require("action.write"); e != nil {
			return nil, e
		}
		if e := require("impersonate"); e != nil {
			return nil, e
		}
		if in.BusinessUserID <= 0 {
			return nil, errors.New("需要正整数业务身份")
		}
		return s.invokeAction(ctx, in.DatabaseID, in.Name, in.RequestID, in.Arguments, fmt.Sprintf("platform:%d", u.ID), in.BusinessUserID, "", 0, true)
	case "configure_actions":
		if e := require("action.write"); e != nil {
			return nil, e
		}
		if e := require("config.read"); e != nil {
			return nil, e
		}
		if in.Database == nil || in.Database.ID != in.DatabaseID {
			return nil, errors.New("需要标识一致的 database")
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		if d.Revision != in.Database.Revision {
			return nil, errors.New("配置已变更，请刷新后重试")
		}
		d.Actions = in.Database.Actions
		d.WriteEnabled = in.Database.WriteEnabled
		d.WriteConnection = in.Database.WriteConnection
		if e = s.Catalog.SaveWithWritePassword(ctx, *d, in.WritePassword); e != nil {
			return nil, e
		}
		saved, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *saved), nil
	case "list_users", "create_user", "disable_user", "enable_user", "reset_password":
		if e := guard.Require(ctx, s.Management, "", "users.manage"); e != nil {
			return nil, e
		}
		switch action {
		case "list_users":
			return s.Management.Users(ctx)
		case "create_user":
			return s.Management.CreateUser(ctx, in.Username, in.Name, in.Password)
		case "disable_user":
			return nil, s.Management.DisableUser(ctx, in.UserID)
		case "enable_user":
			return nil, s.Management.EnableUser(ctx, in.UserID)
		default:
			return nil, s.Management.ResetPassword(ctx, in.UserID, in.Password)
		}
	case "list_business_accounts", "save_business_account":
		if e := require("grant"); e != nil {
			return nil, e
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		if !d.LocalAuthentication() {
			return nil, errors.New("请先为数据库配置 local 业务认证来源")
		}
		if action == "list_business_accounts" {
			return s.Management.BusinessAccounts(ctx, in.DatabaseID)
		}
		// Binding identities and credentials can impersonate any row scope. Restrict
		// account administration to the platform recovery administrator.
		if !u.SystemAdmin {
			return nil, management.ErrDenied
		}
		if in.Account == nil || in.Account.DatabaseID != in.DatabaseID {
			return nil, errors.New("业务账号的 database_id 必须一致")
		}
		if d.Authentication != nil && d.Authentication.EmployeeDirectory {
			svc, e := s.Catalog.Open(ctx, d)
			if e != nil {
				return nil, e
			}
			defer svc.Store.Close()
			users, e := svc.Store.Users(ctx)
			if e != nil {
				return nil, e
			}
			matched := false
			for _, employee := range users {
				if employee.EmpNo == in.Account.Username && employee.ID == in.Account.BusinessUserID {
					matched = true
				}
			}
			if !matched {
				return nil, errors.New("工号与业务身份不匹配员工目录")
			}
		}
		return s.Management.SaveBusinessAccount(ctx, *in.Account, in.Password, int64(in.Revision))
	case "list_databases":
		ds, e := s.Catalog.List()
		if e != nil {
			return nil, e
		}
		out := []DatabaseView{}
		for _, d := range ds {
			v := s.DatabaseView(ctx, d)
			v.Discovery = nil // Full snapshots are fetched only for the selected database.
			if len(v.Permissions) > 0 {
				out = append(out, v)
			}
		}
		return out, nil
	case "create_database":
		if e := guard.Require(ctx, s.Management, "", "databases.create"); e != nil {
			return nil, e
		}
		if in.Database == nil {
			return nil, errors.New("需要 database 参数")
		}
		d, e := s.Catalog.Create(ctx, *in.Database, in.Password)
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *d), nil
	case "get_database", "get_policy":
		if e := require("config.read"); e != nil {
			return nil, e
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *d), nil
	case "update_database", "save_policy", "validate_policy", "configure_mcp":
		p := "config.write"
		if action == "save_policy" || action == "validate_policy" {
			p = "policy.write"
		}
		if action == "configure_mcp" {
			p = "mcp.write"
		}
		if e := require(p); e != nil {
			return nil, e
		}
		if e := require("config.read"); e != nil {
			return nil, e
		}
		if in.Database == nil || in.Database.ID != in.DatabaseID {
			return nil, errors.New("需要标识一致的 database 参数")
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		if d.Revision != in.Database.Revision {
			return nil, errors.New("配置已变更，请刷新后重试")
		}
		switch action {
		case "update_database":
			d.Name = in.Database.Name
		case "save_policy", "validate_policy":
			d.Config = in.Database.Config
		case "configure_mcp":
			d.KnowledgeEnabled = in.Database.KnowledgeEnabled
			d.MCPEnabled = in.Database.MCPEnabled
			d.MCPPath = in.Database.MCPPath
			d.Tools = in.Database.Tools
		}
		if action == "validate_policy" {
			svc, e := s.Catalog.Open(ctx, d)
			if e != nil {
				return nil, e
			}
			svc.Store.Close()
			return map[string]bool{"valid": true}, nil
		}
		if e := s.Catalog.Save(ctx, *d); e != nil {
			return nil, e
		}
		saved, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *saved), nil
	case "get_discovery", "discover_database", "apply_discovery", "save_annotation":
		if e := require("config.read"); e != nil {
			return nil, e
		}
		if action != "get_discovery" {
			if e := require("config.write"); e != nil {
				return nil, e
			}
		}
		var d *catalog.Database
		var e error
		switch action {
		case "get_discovery":
			d, e = s.Catalog.Get(in.DatabaseID)
		case "discover_database":
			d, e = s.Catalog.Discover(ctx, in.DatabaseID)
		case "apply_discovery":
			if e := require("policy.write"); e != nil {
				return nil, e
			}
			d, e = s.Catalog.ApplyDiscovery(ctx, in.DatabaseID, in.ID, in.Revision, in.Resolutions)
		case "save_annotation":
			if in.Annotation == nil {
				return nil, errors.New("需要 annotation 参数")
			}
			d, e = s.Catalog.SaveAnnotation(ctx, in.DatabaseID, in.Table, in.Column, in.Revision, *in.Annotation)
		}
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *d), nil
	case "test_database_connection":
		if !u.SystemAdmin {
			return nil, management.ErrDenied
		}
		if in.Database == nil {
			return nil, errors.New("需要 database 参数")
		}
		d := *in.Database
		d.Connection = store.ConnectionDefaults(d.Driver, d.Connection)
		if in.Password != "" {
			if d.Driver == "sqlite" || d.Connection == nil {
				return nil, errors.New("仅网络数据库接受连接密码")
			}
			if d.Connection.PasswordEnv != "" || d.Connection.CredentialID != "" {
				return nil, errors.New("请只提供一种数据库密码来源")
			}
			d.Connection.CredentialID = "probe"
		}
		if d.Driver == "sqlite" {
			if e := s.Management.ValidatePath(d.Path); e != nil {
				return nil, e
			}
		}
		var st *store.Store
		var e error
		if in.Password != "" {
			st, e = store.OpenConnectionWithPassword(ctx, d.Driver, d.Path, d.Connection, in.Password, s.Catalog.Timeout)
		} else {
			st, e = store.OpenConnection(ctx, d.Driver, d.Path, d.Connection, s.Catalog.Timeout)
		}
		if e != nil {
			return nil, e
		}
		defer st.Close()
		names, e := st.TableNames(ctx)
		if e != nil {
			return nil, e
		}
		return map[string]any{"connected": true, "driver": d.Driver, "table_count": len(names)}, nil
	case "test_connection":
		if e := require("config.read"); e != nil {
			return nil, e
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		st, e := s.Catalog.OpenStore(ctx, d)
		if e != nil {
			return nil, e
		}
		st.Close()
		return map[string]bool{"connected": true}, nil
	case "save_database":

		if in.Database == nil || in.Database.ID != in.DatabaseID {
			return nil, errors.New("数据库标识不匹配")
		}
		old, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		if old.Driver != in.Database.Driver || old.Path != in.Database.Path || !reflect.DeepEqual(old.Connection, in.Database.Connection) {
			return nil, errors.New("连接信息登记后固定；更换数据库目标或凭据引用请新建数据源并重新配置权限")
		}
		if !reflect.DeepEqual(old.Actions, in.Database.Actions) || old.WriteEnabled != in.Database.WriteEnabled || !reflect.DeepEqual(old.WriteConnection, in.Database.WriteConnection) || in.WritePassword != "" {
			if e := require("action.write"); e != nil {
				return nil, e
			}
		}
		if !reflect.DeepEqual(old.Authentication, in.Database.Authentication) && !u.SystemAdmin {
			return nil, management.ErrDenied
		}
		if old.Name != in.Database.Name {
			if e := require("config.write"); e != nil {
				return nil, e
			}
		}
		if !reflect.DeepEqual(old.Config, in.Database.Config) {
			if e := require("policy.write"); e != nil {
				return nil, e
			}
		}
		if !reflect.DeepEqual(old.KnowledgeEnabled, in.Database.KnowledgeEnabled) || old.MCPEnabled != in.Database.MCPEnabled || old.MCPPath != in.Database.MCPPath || !reflect.DeepEqual(old.Tools, in.Database.Tools) {
			if e := require("mcp.write"); e != nil {
				return nil, e
			}
		}
		if e := require("config.read"); e != nil {
			return nil, e
		}
		if in.WritePassword == "" && reflect.DeepEqual(old.Actions, in.Database.Actions) && old.WriteEnabled == in.Database.WriteEnabled && reflect.DeepEqual(old.WriteConnection, in.Database.WriteConnection) && reflect.DeepEqual(old.Authentication, in.Database.Authentication) && old.Name == in.Database.Name && reflect.DeepEqual(old.Config, in.Database.Config) && reflect.DeepEqual(old.KnowledgeEnabled, in.Database.KnowledgeEnabled) && old.MCPEnabled == in.Database.MCPEnabled && old.MCPPath == in.Database.MCPPath && reflect.DeepEqual(old.Tools, in.Database.Tools) {
			return s.DatabaseView(ctx, *old), nil
		}
		if e := s.Catalog.SaveWithWritePassword(ctx, *in.Database, in.WritePassword); e != nil {
			return nil, e
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		return s.DatabaseView(ctx, *d), nil
	case "list_database_grants":
		if e := require("grant"); e != nil {
			return nil, e
		}
		return s.Management.Grants(ctx, in.DatabaseID)
	case "grant_database_permissions", "revoke_database_permissions":
		if _, e := s.Catalog.Get(in.DatabaseID); e != nil {
			return nil, e
		}
		if action == "revoke_database_permissions" {
			in.Permissions = []string{}
			g, _ := s.Management.Grant(ctx, in.UserID, in.DatabaseID)
			in.BusinessUserID = g.BusinessUserID
		}
		return nil, s.Management.SaveGrant(ctx, u.ID, management.Grant{UserID: in.UserID, DatabaseID: in.DatabaseID, Permissions: in.Permissions, BusinessUserID: in.BusinessUserID})
	case "preview_rewrite":
		if e := require("query"); e != nil {
			return nil, e
		}
		bid, e := s.Management.BusinessID(ctx, u.ID, in.DatabaseID)
		if in.BusinessUserID != 0 {
			if e := require("impersonate"); e != nil {
				return nil, e
			}
			bid = in.BusinessUserID
			e = nil
		}
		if e != nil {
			return nil, e
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		svc, e := s.Catalog.Open(ctx, d)
		if e != nil {
			return nil, e
		}
		defer svc.Store.Close()
		return svc.Rewrite(ctx, in.SQL, bid)
	case "search_audit_events", "get_audit_page":
		if (in.DatabaseID == "" || in.PlatformOnly) && !u.SystemAdmin {
			return nil, management.ErrDenied
		}
		if in.DatabaseID != "" {
			if e := require("audit.read"); e != nil {
				return nil, e
			}
		}
		page, e := s.Management.Audit.Search(ctx, audit.Filter{DatabaseID: in.DatabaseID, PlatformOnly: in.PlatformOnly, RequestID: in.RequestID, ActorID: in.ActorID, Kind: in.Kind, Source: in.Source, Status: in.Status, Tool: in.Tool, Query: in.Query, StartedFrom: in.StartedFrom, StartedTo: in.StartedTo, Limit: in.Limit, Offset: in.Offset})
		if e != nil {
			return nil, e
		}
		if action == "search_audit_events" {
			return page.Items, nil
		}
		for _, id := range page.Actors {
			if id > 0 {
				if actor, e := s.Management.User(ctx, id); e == nil {
					page.ActorNames[id] = actor.Name
					if page.ActorNames[id] == "" {
						page.ActorNames[id] = actor.Username
					}
				}
			}
		}
		return page, nil
	case "search_knowledge", "read_knowledge":
		return s.ReadKnowledge(ctx, in, action == "read_knowledge", false)
	case "list_knowledge", "get_knowledge":
		if e := require("knowledge.write"); e != nil {
			return nil, e
		}
		return s.ReadKnowledge(ctx, in, action == "get_knowledge", true)
	case "save_knowledge":
		if e := require("knowledge.write"); e != nil {
			return nil, e
		}
		if in.ID == "" || strings.Contains(in.ID, "/") || in.Name == "" || len(in.Content) > 200000 {
			return nil, errors.New("需要合法知识 ID、名称，正文最多 200000 字节")
		}
		d, e := s.Catalog.Get(in.DatabaseID)
		if e != nil {
			return nil, e
		}
		if d.Config == nil {
			return nil, errors.New("缺少数据库策略")
		}
		if e := d.Config.Validate(); e != nil {
			return nil, e
		}
		for _, t := range in.Tables {
			if _, ok := d.Config.Lookup(t); !ok {
				return nil, fmt.Errorf("未知表 %s", t)
			}
		}
		var saved Knowledge
		e = s.Management.UpdateRecords(ctx, "knowledge", "knowledge.save", in.DatabaseID, func(records []management.Record) ([]management.Record, error) {
			key := in.DatabaseID + "/" + in.ID
			old := Knowledge{}
			for _, r := range records {
				if r.Key == key {
					if e := json.Unmarshal([]byte(r.Data), &old); e != nil {
						return nil, e
					}
				}
			}
			if old.Revision != in.Revision {
				return nil, errors.New("知识已更新，请刷新后重试")
			}
			description := in.Description
			if description == nil {
				description = in.Summary
			}
			text := ""
			if description != nil {
				text = *description
			}
			saved = Knowledge{ID: in.ID, DatabaseID: in.DatabaseID, Name: in.Name, Description: text, Content: in.Content, Tables: in.Tables, Published: in.Published, Revision: old.Revision + 1}
			raw, e := json.Marshal(saved)
			return []management.Record{{Key: key, Data: string(raw)}}, e
		})
		return saved, e
	default:
		return nil, fmt.Errorf("未知管理动作 %s", action)
	}
}
func (s *Service) ReadKnowledge(ctx context.Context, in Input, full, drafts bool) (any, error) {
	return s.readKnowledge(ctx, in, full, drafts, 50)
}
func (s *Service) readKnowledge(ctx context.Context, in Input, full, drafts bool, limit int) (any, error) {
	if full && in.ID == "" {
		return nil, errors.New("需要知识 ID")
	}
	if !drafts {
		if e := guard.Require(ctx, s.Management, in.DatabaseID, "knowledge.read"); e != nil {
			return nil, e
		}
		if e := guard.Require(ctx, s.Management, in.DatabaseID, "query"); e != nil {
			return nil, e
		}
	}
	d, e := s.Catalog.Get(in.DatabaseID)
	if e != nil {
		return nil, e
	}
	if d.Config == nil {
		return nil, errors.New("缺少数据库策略")
	}
	if e := d.Config.Validate(); e != nil {
		return nil, e
	}
	matches, e := s.Management.SearchKnowledge(ctx, in.Query)
	if e != nil {
		return nil, e
	}
	records, e := s.Management.Records(ctx, "knowledge")
	if e != nil {
		return nil, e
	}
	out := []Knowledge{}
	for _, r := range records {
		var k Knowledge
		if e = json.Unmarshal([]byte(r.Data), &k); e != nil {
			return nil, e
		}
		if k.DatabaseID != in.DatabaseID || (!drafts && !k.Published) {
			continue
		}
		if in.ID != "" && k.ID != in.ID {
			continue
		}
		visible := true
		for _, t := range k.Tables {
			p, ok := d.Config.Lookup(t)
			if !ok || (p.Enabled != nil && !*p.Enabled) || p.Columns != nil {
				visible = false
				break
			}
		}
		if !drafts && !visible {
			continue
		}
		if in.Query != "" && !matches[r.Key] && !strings.Contains(strings.ToLower(k.Name+" "+k.Description+" "+k.Content), strings.ToLower(in.Query)) {
			continue
		}
		if !full {
			k.Content = ""
		}
		out = append(out, k)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if full {
		if len(out) == 0 {
			return nil, errors.New("知识不存在或不可访问")
		}
		return out[0], nil
	}
	return out, nil
}

type DatabaseView struct {
	ManagementPath string                   `json:"management_path,omitempty"`
	AuditPath      string                   `json:"audit_path,omitempty"`
	Access         *management.ResolvedAuth `json:"access,omitempty"`
	MCPEndpoint    string                   `json:"mcp_endpoint"`
	catalog.Database
	Permissions    []string `json:"permissions"`
	BusinessUserID int64    `json:"business_user_id"`
}

func (s *Service) DatabaseView(ctx context.Context, d catalog.Database) DatabaseView {
	u, _ := guard.User(ctx)
	p := []string{}
	for _, permission := range management.Permissions {
		if s.Management.Can(ctx, u.ID, d.ID, permission) {
			p = append(p, permission)
		}
	}
	g, _ := s.Management.Grant(ctx, u.ID, d.ID)
	if !s.Management.Can(ctx, u.ID, d.ID, "config.read") {
		d.Discovery = nil
		d.Authentication = nil
		d.Path = ""
		d.Connection = nil
		d.Config = nil
		d.Actions = nil
		d.WriteConnection = nil
		d.WriteEnabled = false
		d.Tools = nil
	}
	view := DatabaseView{MCPEndpoint: strings.TrimRight(s.MCPBase, "/") + "/" + d.MCPRoute(), Database: d, Permissions: p, BusinessUserID: g.BusinessUserID}
	if s.Management.Can(ctx, u.ID, d.ID, "config.read") {
		if source, e := s.Management.Source(ctx, d.ID); e == nil {
			view.ManagementPath = source.Path
			if source.Audit != nil {
				view.AuditPath = source.Audit.Path
			}
		}
		if access, e := s.Management.ResolveAuth(ctx, "DATABASE", d.ID); e == nil {
			view.Access = &access
		}
	}
	return view
}
