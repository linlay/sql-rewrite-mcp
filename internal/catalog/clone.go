package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"datascope-server/internal/management"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
	parser "github.com/rqlite/sql"
)

type CloneScope struct {
	Tables         bool `json:"tables"`
	Policies       bool `json:"policies"`
	Actions        bool `json:"actions"`
	Knowledge      bool `json:"knowledge"`
	Authentication bool `json:"authentication"`
}

type CloneRequest struct {
	SourceDatabaseID string      `json:"source_database_id"`
	Scope            *CloneScope `json:"scope,omitempty"`
	SourceRevision   int         `json:"source_revision,omitempty"`
	TargetRevision   int         `json:"target_revision,omitempty"`
	SourceToken      string      `json:"source_token,omitempty"`
	TargetToken      string      `json:"target_token,omitempty"`
	Confirmation     string      `json:"confirmation,omitempty"`
	Confirmed        bool        `json:"confirmed,omitempty"`
}

type CloneItem struct {
	Scope       string   `json:"scope"`
	SourceCount int      `json:"source_count"`
	TargetCount int      `json:"target_count"`
	Names       []string `json:"names"`
}

type ClonePreview struct {
	SourceDatabaseID string      `json:"source_database_id"`
	SourceName       string      `json:"source_name"`
	TargetDatabaseID string      `json:"target_database_id"`
	TargetName       string      `json:"target_name"`
	SourceRevision   int         `json:"source_revision"`
	TargetRevision   int         `json:"target_revision"`
	SourceToken      string      `json:"source_token"`
	TargetToken      string      `json:"target_token"`
	Scope            CloneScope  `json:"scope"`
	Items            []CloneItem `json:"items"`
	Issues           []string    `json:"issues"`
	Warnings         []string    `json:"warnings"`
	Confirmation     string      `json:"confirmation"`
	Applied          bool        `json:"applied"`
}

// CloneConfiguration uses the management transaction boundary for both source
// reads and the whole target write. authorize is repeated under the writer lock.
func (c *Catalog) CloneConfiguration(ctx context.Context, targetID string, req CloneRequest, commit bool, authorize func(CloneScope) error) (*ClonePreview, error) {
	if c.Management == nil {
		return nil, errors.New("克隆需要 YAML 管理存储")
	}
	scope := CloneScope{Tables: true, Policies: true, Actions: true, Knowledge: true}
	if req.Scope != nil {
		scope = *req.Scope
	}
	if scope == (CloneScope{}) {
		return nil, errors.New("请至少选择一个复制范围")
	}
	if err := authorize(scope); err != nil {
		return nil, err
	}
	if commit && (!req.Confirmed || req.SourceRevision <= 0 || req.TargetRevision <= 0 || req.Confirmation == "") {
		return nil, errors.New("请先预览覆盖摘要，再明确确认克隆")
	}
	var preview *ClonePreview
	err := c.Management.CloneConfiguration(ctx, req.SourceDatabaseID, targetID, req.SourceToken, req.TargetToken, commit, func(src, dst management.CloneSnapshot) (management.CloneReplacement, error) {
		if err := authorize(scope); err != nil {
			return management.CloneReplacement{}, err
		}
		var source, target Database
		if err := json.Unmarshal(src.Database, &source); err != nil {
			return management.CloneReplacement{}, err
		}
		if err := json.Unmarshal(dst.Database, &target); err != nil {
			return management.CloneReplacement{}, err
		}
		if commit && (source.Revision != req.SourceRevision || target.Revision != req.TargetRevision) {
			return management.CloneReplacement{}, errors.New("来源库或目标库 revision 已变化，请重新预览")
		}
		st, err := c.OpenStore(ctx, &target)
		if err != nil {
			return management.CloneReplacement{}, err
		}
		defer st.Close()
		actual, err := captureSchema(ctx, st)
		if err != nil {
			return management.CloneReplacement{}, err
		}
		preview = &ClonePreview{SourceDatabaseID: source.ID, SourceName: source.Name, TargetDatabaseID: target.ID, TargetName: target.Name, SourceRevision: source.Revision, TargetRevision: target.Revision, SourceToken: src.Token, TargetToken: dst.Token, Scope: scope, Items: []CloneItem{}, Issues: []string{}, Warnings: []string{"仅检查可确定的结构与静态引用，不能证明任意 JavaScript / SQL 的业务兼容性；请在目标库验证 Action 行为。"}}
		next, records := buildClone(&source, &target, src.Knowledge, dst.Knowledge, actual, scope, preview)
		if scope.Authentication {
			if source.Authentication == nil {
				source.Authentication = &Authentication{Type: "local"}
			}
			directory := false
			if target.Authentication != nil {
				directory = target.Authentication.EmployeeDirectory
			}
			raw, _ := json.Marshal(source.Authentication)
			next.Authentication = &Authentication{}
			_ = json.Unmarshal(raw, next.Authentication)
			// Resolve inherited global policies, but keep target grants and default identity.
			policy := src.Authentication.Policy
			policy.Permissions = dst.Authentication.Policy.Permissions
			policy.BusinessUserID = dst.Authentication.Policy.BusinessUserID
			next.Authentication.Access = &policy
			next.Authentication.EmployeeDirectory = directory
			if err := next.ValidateAuthentication(); err != nil {
				preview.Issues = append(preview.Issues, err.Error())
			}
			if err := c.Management.ValidateDatabaseAuth(ctx, next.Authentication.Connection()); err != nil {
				preview.Issues = append(preview.Issues, err.Error())
			}
			next.AuthenticationRevision = target.AuthenticationRevision + 1
			preview.Items = append(preview.Items, CloneItem{"authentication", 1, 1, []string{src.Authentication.Policy.Mode, next.Authentication.Source, src.Authentication.Provider.Name}})
			preview.Warnings = append(preview.Warnings, "复制认证方式与提供方连接，保留目标库业务能力、默认身份、账号和人员授权；旧业务令牌及待确认预览失效。密钥环境变量仅复制引用。")
		}
		if next.Config != nil {
			if err := next.Config.Validate(); err != nil {
				preview.Issues = append(preview.Issues, err.Error())
			}
		}
		if err := validateActions(*next); err != nil {
			preview.Issues = append(preview.Issues, err.Error())
		}
		if len(preview.Issues) == 0 {
			// Check all resulting static dependencies, including retained knowledge
			// and Actions that can be affected by a partial policy/table clone.
			for _, p := range next.Config.Tables {
				for _, query := range []string{p.CompiledSQL, guardSQL(p.CompiledGuard)} {
					if query != "" {
						checkCloneSQL(ctx, st, query, actual, "查询权限 "+p.Table, preview)
					}
				}
			}
			for _, a := range next.Actions {
				queries, err := a.StaticSQL(next.ActionLibraries...)
				if err != nil {
					preview.Issues = append(preview.Issues, "Action "+a.Name+": "+err.Error())
					continue
				}
				for _, query := range queries {
					checkCloneSQL(ctx, st, query, actual, "Action "+a.Name, preview)
				}
			}
		}
		sort.Strings(preview.Issues)
		preview.Issues = slices.Compact(preview.Issues)
		// Bind confirmation to both complete configurations, selected scopes,
		// overwrite summary and the freshly inspected physical target schema.
		raw, _ := json.Marshal(struct {
			Preview *ClonePreview
			Schema  []store.TableSchema
		}{preview, actual})
		preview.Confirmation = fmt.Sprintf("%x", sha256.Sum256(raw))
		if commit {
			if len(preview.Issues) > 0 {
				return management.CloneReplacement{}, fmt.Errorf("克隆检查失败：%s", strings.Join(preview.Issues, "；"))
			}
			if req.Confirmation != preview.Confirmation {
				return management.CloneReplacement{}, errors.New("复制范围或目标结构已变化，请重新预览后确认")
			}
			preview.Applied = true
		}
		next.Revision++
		raw, err = json.Marshal(next)
		return management.CloneReplacement{Database: raw, Knowledge: records, Authentication: scope.Authentication}, err
	})
	return preview, err
}

func guardSQL(s string) string {
	if s == "" {
		return ""
	}
	return rewrite.GuardQuery(s)
}

func buildClone(source, target *Database, sourceRecords, targetRecords []management.Record, actual []store.TableSchema, scope CloneScope, out *ClonePreview) (*Database, []management.Record) {
	next := target
	known := map[string]store.TableSchema{}
	for _, table := range actual {
		known[strings.ToLower(table.Name)] = table
	}
	check := func(table, column string) {
		t, ok := known[strings.ToLower(table)]
		if !ok {
			out.Issues = append(out.Issues, "目标库缺少表 "+table)
			return
		}
		if column != "" {
			for _, c := range t.Columns {
				if strings.EqualFold(c.Name, column) {
					return
				}
			}
			out.Issues = append(out.Issues, "目标库缺少字段 "+table+"."+column)
		}
	}
	if next.Config == nil {
		out.Issues = append(out.Issues, "目标库缺少查询权限配置")
		return next, nil
	}
	if scope.Tables || scope.Policies {
		if source.Config == nil {
			out.Issues = append(out.Issues, "来源库缺少查询权限配置")
			return next, nil
		}
		policies := map[string]*rewrite.Policy{}
		for _, p := range next.Config.Tables {
			policies[strings.ToLower(p.Table)] = p
		}
		if scope.Policies {
			schemes := source.Config.Schemes
			for _, s := range next.Config.Schemes {
				found := false
				for _, x := range schemes {
					found = found || x.Scheme == s.Scheme
				}
				if !found {
					schemes = append(schemes, s)
				}
			}
			next.Config.Schemes = schemes
		}
		names := []string{}
		for _, p := range source.Config.Tables {
			if p == nil {
				out.Issues = append(out.Issues, "来源库有无效表配置")
				continue
			}
			check(p.Table, "")
			old := policies[strings.ToLower(p.Table)]
			if old == nil {
				out.Issues = append(out.Issues, "目标库尚未登记表 "+p.Table+"，请先同步结构")
				continue
			}
			names = append(names, p.Table)
			if scope.Policies {
				enabled, columns := old.Enabled, old.Columns
				*old = *p
				old.Enabled, old.Columns = enabled, columns
			}
			if scope.Tables {
				old.Enabled, old.Columns = p.Enabled, p.Columns
			}
			for _, col := range old.Columns {
				check(old.Table, col)
			}
		}
		if scope.Tables {
			out.Items = append(out.Items, CloneItem{"tables", len(names), len(names), names})
			if next.Discovery == nil {
				next.Discovery = &Discovery{Annotations: map[string]store.TableAnnotation{}}
			}
			if next.Discovery.Annotations == nil {
				next.Discovery.Annotations = map[string]store.TableAnnotation{}
			}
			if source.Discovery != nil {
				for _, table := range source.Discovery.Tables {
					check(table.Name, "")
					a := source.Discovery.Annotations[table.Name]
					if a.Comment == nil {
						text := table.Comment
						a.Comment = &text
					}
					if a.Columns == nil {
						a.Columns = map[string]store.Annotation{}
					}
					for _, col := range table.Columns {
						check(table.Name, col.Name)
						ca := a.Columns[col.Name]
						if ca.Comment == nil {
							text := col.Comment
							ca.Comment = &text
						}
						a.Columns[col.Name] = ca
					}
					for col := range a.Columns {
						check(table.Name, col)
					}
					next.Discovery.Annotations[table.Name] = a
				}
				for table, a := range source.Discovery.Annotations {
					check(table, "")
					for col := range a.Columns {
						check(table, col)
					}
				}
			}
			// The snapshot comes exclusively from the target's live connection.
			if len(out.Issues) == 0 {
				d := next.Discovery
				changes := schemaDiff(d.Tables, actual, d.Annotations)
				d.CheckedAt = time.Now().UTC()
				d.AttemptedAt, d.Error = d.CheckedAt, ""
				if d.Version == 0 || len(changes) > 0 {
					d.Version++
					d.Reports = append(d.Reports, SchemaReport{Version: d.Version, At: d.CheckedAt, Changes: changes})
					if len(d.Reports) > 20 {
						d.Reports = d.Reports[len(d.Reports)-20:]
					}
				}
				d.Tables = actual
			}
		}
		if scope.Policies {
			out.Items = append(out.Items, CloneItem{"policies", len(names), len(names), names})
		}
	}
	if scope.Actions {
		names := []string{}
		for _, a := range source.Actions {
			names = append(names, a.Name)
		}
		out.Items = append(out.Items, CloneItem{"actions", len(source.Actions), len(next.Actions), names})
		libraryNames := []string{}
		for _, library := range source.ActionLibraries {
			libraryNames = append(libraryNames, library.Name)
		}
		if len(source.ActionLibraries) > 0 || len(next.ActionLibraries) > 0 {
			out.Items = append(out.Items, CloneItem{"action_libraries", len(source.ActionLibraries), len(next.ActionLibraries), libraryNames})
		}
		next.Actions = source.Actions
		next.ActionLibraries = source.ActionLibraries
	}
	result := targetRecords
	if scope.Knowledge {
		result = []management.Record{}
		oldRevisions := map[string]int{}
		for _, r := range targetRecords {
			var k cloneKnowledge
			if json.Unmarshal([]byte(r.Data), &k) == nil {
				oldRevisions[k.ID] = k.Revision
			}
		}
		names := []string{}
		for _, r := range sourceRecords {
			var k cloneKnowledge
			if err := json.Unmarshal([]byte(r.Data), &k); err != nil {
				out.Issues = append(out.Issues, "来源知识无效")
				continue
			}
			names = append(names, k.ID)
			k.DatabaseID, k.Revision = target.ID, oldRevisions[k.ID]+1
			raw, _ := json.Marshal(k)
			result = append(result, management.Record{Key: target.ID + "/" + k.ID, Data: string(raw)})
		}
		out.Items = append(out.Items, CloneItem{"knowledge", len(sourceRecords), len(targetRecords), names})
	}
	// Keep target-only policies exactly as configured. Stale structure requires
	// the existing review/sync flow; cloning must not silently trim access rules.
	registered := map[string]bool{}
	for _, p := range next.Config.Tables {
		if p == nil {
			continue
		}
		registered[strings.ToLower(p.Table)] = true
		check(p.Table, "")
		for _, column := range p.Columns {
			check(p.Table, column)
		}
	}
	for _, table := range actual {
		if !registered[strings.ToLower(table.Name)] {
			out.Issues = append(out.Issues, "目标库尚未登记表 "+table.Name+"，请先同步结构")
		}
	}
	for _, r := range result {
		var k cloneKnowledge
		if err := json.Unmarshal([]byte(r.Data), &k); err != nil {
			out.Issues = append(out.Issues, "知识记录无效")
		}
	}
	if !scope.Knowledge {
		result = nil
	}
	return next, result
}

type cloneKnowledge struct {
	ID          string `json:"id"`
	DatabaseID  string `json:"database_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Published   bool   `json:"published"`
	Revision    int    `json:"revision"`
}

func checkCloneSQL(ctx context.Context, st *store.Store, query string, actual []store.TableSchema, label string, out *ClonePreview) {
	stmt, err := parser.NewParser(strings.NewReader(query)).ParseStatement()
	if err != nil {
		out.Issues = append(out.Issues, label+": "+err.Error())
		return
	}
	known := map[string]store.TableSchema{}
	for _, t := range actual {
		known[strings.ToLower(t.Name)] = t
	}
	ctes := map[string]bool{}
	aliases := map[string]string{}
	addAlias := func(alias, table string) {
		key := strings.ToLower(alias)
		if old, exists := aliases[key]; exists && old != table {
			aliases[key] = ""
		} else if !exists {
			aliases[key] = table
		}
	}
	_, _ = parser.Walk(parser.VisitFunc(func(n parser.Node) (parser.Node, error) {
		if c, ok := n.(*parser.CTE); ok {
			ctes[strings.ToLower(parser.IdentName(c.TableName))] = true
		}
		return n, nil
	}), stmt)
	_, _ = parser.Walk(parser.VisitFunc(func(n parser.Node) (parser.Node, error) {
		table := ""
		switch x := n.(type) {
		case *parser.QualifiedTableName:
			if x.Schema != nil || !ctes[strings.ToLower(parser.IdentName(x.Name))] {
				table = parser.IdentName(x.Name)
				addAlias(x.TableName(), table)
			}
		case *parser.InsertStatement:
			table = parser.IdentName(x.Table)
		}
		if table != "" {
			if _, ok := known[strings.ToLower(table)]; !ok {
				out.Issues = append(out.Issues, label+" 引用目标库缺少的表 "+table)
			}
		}
		return n, nil
	}), stmt)
	checkColumn := func(table, column string) {
		t, exists := known[strings.ToLower(table)]
		if !exists || column == "" {
			return
		}
		for _, c := range t.Columns {
			if strings.EqualFold(c.Name, column) {
				return
			}
		}
		out.Issues = append(out.Issues, label+" 引用目标库缺少的字段 "+table+"."+column)
	}
	_, _ = parser.Walk(parser.VisitFunc(func(n parser.Node) (parser.Node, error) {
		switch x := n.(type) {
		case *parser.QualifiedRef:
			// Reused aliases across nested queries are ambiguous here; leave
			// those to the engine instead of asserting a false dependency.
			if table := aliases[strings.ToLower(parser.IdentName(x.Table))]; table != "" {
				checkColumn(table, parser.IdentName(x.Column))
			}
		case *parser.InsertStatement:
			for _, col := range x.Columns {
				checkColumn(parser.IdentName(x.Table), parser.IdentName(col))
			}
		case *parser.UpdateStatement:
			for _, a := range x.Assignments {
				for _, col := range a.Columns {
					checkColumn(parser.IdentName(x.Table.Name), parser.IdentName(col))
				}
			}
		}
		return n, nil
	}), stmt)
	if err := st.CheckCloneSQL(ctx, query); err != nil {
		out.Issues = append(out.Issues, label+": "+err.Error())
	}
}
