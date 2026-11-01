// Package demoactions contains explicit, opt-in operations for the synthetic
// securities dataset. Merely starting a server never installs or grants them.
package demoactions

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"datascope-server/internal/action"
	"datascope-server/internal/application"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"gopkg.in/yaml.v3"
)

//go:embed securities.yml
var source []byte

type Bundle struct {
	Version         int                 `yaml:"version"`
	Actions         []action.Definition `yaml:"actions"`
	ActionLibraries []action.Library    `yaml:"actionLibraries"`
}

func Load() (Bundle, error) {
	var b Bundle
	d := yaml.NewDecoder(bytes.NewReader(source))
	d.KnownFields(true)
	if e := d.Decode(&b); e != nil {
		return b, e
	}
	if b.Version != 1 || len(b.Actions) != 18 {
		return b, fmt.Errorf("证券案例包版本或数量无效")
	}
	seen := map[string]bool{}
	if err := action.ValidateLibraries(b.ActionLibraries); err != nil {
		return b, err
	}
	for _, a := range b.Actions {
		if seen[a.Name] || !a.Confirm {
			return b, fmt.Errorf("演示 action 名称重复或缺少人工确认")
		}
		seen[a.Name] = true
		if e := a.Validate(b.ActionLibraries...); e != nil {
			return b, fmt.Errorf("action %s: %w", a.Name, e)
		}
	}
	return b, nil
}

var Tables = []string{"branches", "cash_flows", "customers", "daily_balances", "daily_quotes", "departments", "employee_scopes", "employees", "fund_accounts", "orders", "positions", "roles", "schema_comments", "securities", "stock_accounts", "trades", "trading_calendar"}

// ValidateSource checks the complete schema, role semantics and initial data.
// The installer refuses unrelated SQLite sources and never changes their policy.
func ValidateSource(ctx context.Context, c *catalog.Catalog, d *catalog.Database) error {
	if d.Driver != "sqlite" {
		return fmt.Errorf("演示 Action 仅用于 SQLite 证券演示库")
	}
	st, e := c.OpenStore(ctx, d)
	if e != nil {
		return e
	}
	defer st.Close()
	names, e := st.TableNames(ctx)
	if e != nil {
		return e
	}
	for _, n := range Tables {
		if !slices.Contains(names, n) {
			return fmt.Errorf("不是完整证券演示库，缺少表 %s", n)
		}
	}
	r, e := st.Query(ctx, "SELECT COUNT(*) AS n FROM roles WHERE code IN ('MANAGER','DEPARTMENT_HEAD','BRANCH_HEAD','HQ_SUPERVISOR','ADMIN')", 1)
	if e != nil {
		return e
	}
	if r.Rows[0]["n"] != int64(5) {
		return fmt.Errorf("证券角色字典不匹配")
	}
	r, e = st.Query(ctx, "SELECT COUNT(*) AS n FROM fund_accounts WHERE currency='CNY'", 1)
	if e != nil {
		return e
	}
	if r.Rows[0]["n"] == int64(0) {
		return fmt.Errorf("缺少演示资金账户")
	}
	svc, e := c.Open(ctx, d)
	if e != nil {
		return e
	}
	return svc.Store.Close()
}

type InstallResult struct {
	DatabaseID      string   `json:"database_id"`
	ActionCount     int      `json:"action_count"`
	ConfigChanged   bool     `json:"config_changed"`
	GrantedAccounts []string `json:"granted_accounts"`
	Revision        int      `json:"revision"`
}

// Install preserves custom actions, query policies, existing capabilities and
// passwords. Different definitions with an existing name require explicit replace.
func Install(ctx context.Context, app *application.Service, id string, accounts []string, replace bool) (InstallResult, error) {
	result := InstallResult{DatabaseID: id, GrantedAccounts: []string{}}
	u, ok := guard.User(ctx)
	if !ok || !u.SystemAdmin {
		return result, management.ErrDenied
	}
	d, e := app.Catalog.Get(id)
	if e != nil {
		return result, e
	}
	if e = ValidateSource(ctx, app.Catalog, d); e != nil {
		return result, e
	}
	bundle, e := Load()
	if e != nil {
		return result, e
	}
	// Validate every grant before making any configuration change.
	pending := []management.BusinessAccount{}
	for _, name := range accounts {
		a, e := app.Catalog.BusinessAccount(ctx, d, name)
		if e != nil {
			return result, fmt.Errorf("业务账号 %s 不可用: %w", name, e)
		}
		pending = append(pending, a)
	}
	merged := append([]action.Definition{}, d.Actions...)
	for _, a := range bundle.Actions {
		index := slices.IndexFunc(merged, func(old action.Definition) bool { return old.Name == a.Name })
		if index < 0 {
			merged = append(merged, a)
			continue
		}
		if !reflect.DeepEqual(merged[index], a) {
			if !replace {
				return result, fmt.Errorf("action %s 已存在且内容不同，保留现有定义；如需替换请显式使用 -replace", a.Name)
			}
			merged[index] = a
		}
	}
	d.Actions = merged
	for _, library := range bundle.ActionLibraries {
		index := slices.IndexFunc(d.ActionLibraries, func(old action.Library) bool { return old.Name == library.Name })
		if index < 0 {
			d.ActionLibraries = append(d.ActionLibraries, library)
		} else if !reflect.DeepEqual(d.ActionLibraries[index], library) {
			if !replace {
				return result, fmt.Errorf("公共函数库 %s 已存在且内容不同；如需替换请显式使用 -replace", library.Name)
			}
			d.ActionLibraries[index] = library
		}
	}
	d.WriteEnabled = true
	d.MCPEnabled = true
	for _, tool := range []string{"list_actions", "describe_action", "invoke_action"} {
		if !slices.Contains(d.Tools, tool) {
			d.Tools = append(d.Tools, tool)
		}
	}
	before, e := app.Catalog.Get(id)
	if e != nil {
		return result, e
	}
	if !reflect.DeepEqual(before.Actions, d.Actions) || !reflect.DeepEqual(before.ActionLibraries, d.ActionLibraries) || !before.WriteEnabled || !before.MCPEnabled || !reflect.DeepEqual(before.Tools, d.Tools) {
		if _, e = app.Call(ctx, "save_database", application.Input{DatabaseID: id, Database: d}); e != nil {
			return result, e
		}
		result.ConfigChanged = true
	}
	for _, a := range pending {
		if slices.Contains(a.Permissions, "action") {
			continue
		}
		a.Permissions = append(a.Permissions, "action")
		if _, e = app.Call(ctx, "save_business_account", application.Input{DatabaseID: id, Account: &a, Revision: int(a.AuthVersion)}); e != nil {
			return result, e
		}
		result.GrantedAccounts = append(result.GrantedAccounts, a.Username)
	}
	saved, e := app.Catalog.Get(id)
	if e != nil {
		return result, e
	}
	result.ActionCount = len(saved.Actions)
	result.Revision = saved.Revision
	if e = publishGuide(ctx, app, id, bundle); e != nil {
		return result, e
	}
	return result, nil
}

// EqualJSON is useful for verifying that an installed definition retains its
// parameter schema after YAML-to-JSON persistence has normalized enum numbers.
func EqualJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// publishGuide creates an actionable discovery entry without overwriting an
// existing guide. Concrete record examples are documented in the repository.
func publishGuide(ctx context.Context, app *application.Service, id string, b Bundle) error {
	const key = "securities-actions-guide"
	records, e := app.Management.Records(ctx, "knowledge")
	if e != nil {
		return e
	}
	for _, r := range records {
		if r.Key == id+"/"+key {
			return nil
		}
	}
	var text strings.Builder
	text.WriteString("证券演示库操作案例。先 list_actions 列出动作，describe_action(name) 查看参数，用 query 查询本人可见记录，再 invoke_action。所有操作 confirm:true，预览会回滚，只有本人打开确认页登录后才正式执行。金额单位为分，入金、出金、划转只修改合成数据，不连接银行。\n\nE0001 是客户经理，只能维护本人账户；客户共享资料要求全部账户归本人。E0121 主管可见范围更广，但查询权限不等于写权限。E0175 是证券业务管理员，可维护目录、调整经理并重估最新持仓。新增 action 能力后须重新 OAuth 授权。\n\n")
	for _, a := range b.Actions {
		schema, _ := json.Marshal(a.Schema())
		fmt.Fprintf(&text, "## %s — %s\n%s\n参数：%s\n\n", a.Name, a.Title, a.Description, schema)
	}
	text.WriteString("最新演示账期以查询结果为准，初始数据为 2026-10-20。不要修改历史成交、角色、管理范围、交易日历；结构说明通过管理元数据维护。测试证券账户新增时先选择没有历史委托的账户删除，再新增，不能删除已被引用的账户。客户资料、冻结解冻、订单元数据不修改账务；资金操作同步维护流水与日结；价格更正同步重估该证券最新日全部持仓。")
	knowledgeDescription1 := "18 种可修改操作、角色范围、参数格式、最新账期记账和人工确认流程"
	_, e = app.Call(ctx, "save_knowledge", application.Input{DatabaseID: id, ID: key, Name: "证券演示 Action 操作案例", Description: &knowledgeDescription1, Content: text.String(), Published: true})
	return e
}
