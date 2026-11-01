// Package rewrite turns AI-generated SELECT statements into permission-scoped
// SQL. The only transformation is structural: every table in FROM / JOIN is
// replaced by the subquery configured for that table (config/rewrite.yml).
package rewrite

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	sql "github.com/rqlite/sql"
	"gopkg.in/yaml.v3"
)

// Bind variables a permission policy may use. Values come from the trusted server
// context and are passed as named parameters, never spliced into SQL text.
const (
	VarUserID = "userId"
	VarAsOf   = "asOf"
)

// Variables documents every bind variable available to permission policies.
var Variables = []VariableInfo{
	{Name: VarUserID, Description: "当前员工内部编号，来自 OAuth 工号映射或可信网关"},
	{Name: VarAsOf, Description: "授权过期判断的参考时间（RFC 3339 字符串）"},
}

type VariableInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Scheme is a rewrite category label defined in the config.
type Scheme string

type SchemeInfo struct {
	Scheme      Scheme `yaml:"scheme" json:"scheme"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

// Policy is the configured rewrite rule for one table.
type Policy struct {
	Table       string   `yaml:"table" json:"table"`
	Enabled     *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Columns     []string `yaml:"columns" json:"columns"`
	MaxRows     int      `yaml:"maxRows,omitempty" json:"max_rows"`
	Scheme      Scheme   `yaml:"scheme" json:"scheme"`
	Reason      string   `yaml:"reason" json:"reason"`
	Alternative string   `yaml:"alternative" json:"alternative,omitempty"`
	// Divergent marks an alternative that can return different rows from the
	// chosen path (a real policy decision), as opposed to an equivalent path.
	Divergent bool `yaml:"divergent" json:"divergent"`
	// Passthrough leaves the table unfiltered. It must be explicit so a
	// forgotten sql never silently means "no filtering".
	Passthrough bool   `yaml:"passthrough" json:"passthrough"`
	SQL         string `yaml:"sql" json:"sql,omitempty"`
	// DenyUnless is a boolean SQL expression evaluated before execution; if it
	// is false the whole statement is rejected with DenyMessage.
	DenyUnless  string `yaml:"denyUnless" json:"deny_unless,omitempty"`
	DenyMessage string `yaml:"denyMessage" json:"deny_message,omitempty"`

	// Compiled forms, with ${var} turned into :var; held only in memory.
	CompiledSQL   string   `yaml:"-" json:"compiled_sql,omitempty"`
	CompiledGuard string   `yaml:"-" json:"compiled_deny_unless,omitempty"`
	Params        []string `yaml:"-" json:"params"`
	GuardParams   []string `yaml:"-" json:"deny_unless_params,omitempty"`
	subquery      *sql.SelectStatement
}

// Config is a loaded and validated rewrite configuration.
type Config struct {
	Version int          `yaml:"version" json:"version"`
	Schemes []SchemeInfo `yaml:"schemes" json:"schemes"`
	Tables  []*Policy    `yaml:"tables" json:"tables"`
	Path    string       `yaml:"-" json:"path"`

	byTable map[string]*Policy
}

// LoadConfig reads and validates a rewrite config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取改写配置: %w", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("改写配置 %s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// ParseConfig parses and validates config YAML.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled key must not be silently ignored
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.compile(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Lookup returns the policy for a table (case-insensitive).
func (c *Config) Lookup(table string) (*Policy, bool) {
	pol, ok := c.byTable[strings.ToLower(table)]
	return pol, ok
}

// SchemeName returns the display name of a scheme.
func (c *Config) SchemeName(s Scheme) string {
	for _, info := range c.Schemes {
		if info.Scheme == s {
			return info.Name
		}
	}
	return string(s)
}

var (
	variableRef = regexp.MustCompile(`\$\{\s*([A-Za-z0-9_]+)\s*\}`)
)

func (c *Config) compile() error {
	if c.Version != 1 {
		return fmt.Errorf("不支持的配置版本 %d", c.Version)
	}
	schemes := map[Scheme]bool{}
	for _, s := range c.Schemes {
		if s.Scheme == "" || schemes[s.Scheme] {
			return fmt.Errorf("方案 %q 为空或重复", s.Scheme)
		}
		schemes[s.Scheme] = true
	}

	c.byTable = map[string]*Policy{}
	var errs []error
	for i, pol := range c.Tables {
		if pol == nil || pol.Table == "" {
			errs = append(errs, fmt.Errorf("tables[%d] 缺少 table", i))
			continue
		}
		name := strings.ToLower(pol.Table)
		if c.byTable[name] != nil {
			errs = append(errs, fmt.Errorf("表 %s 重复登记", name))
			continue
		}
		pol.Table = name
		c.byTable[name] = pol
		if err := c.compilePolicy(pol, schemes); err != nil {
			errs = append(errs, fmt.Errorf("表 %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) compilePolicy(pol *Policy, schemes map[Scheme]bool) error {
	pol.CompiledSQL = ""
	pol.CompiledGuard = ""
	pol.Params = nil
	pol.GuardParams = nil
	pol.subquery = nil
	if pol.MaxRows < 0 || pol.MaxRows > 1000 {
		return errors.New("max_rows 必须为 0 到 1000")
	}
	if !schemes[pol.Scheme] {
		return fmt.Errorf("未定义的方案 %q", pol.Scheme)
	}
	switch {
	case pol.Passthrough && strings.TrimSpace(pol.SQL) != "":
		return errors.New("passthrough 与 sql 只能二选一")
	case !pol.Passthrough && strings.TrimSpace(pol.SQL) == "":
		return errors.New("缺少 sql；若确实不需要改写，请显式设置 passthrough: true")
	}
	if pol.Columns != nil && len(pol.Columns) == 0 && (pol.Enabled == nil || *pol.Enabled) {
		return errors.New("可访问表至少选择一个字段")
	}
	if pol.Passthrough {
		pol.Params = []string{}
	} else {
		text, params, err := bindVariables(pol.SQL)
		if err != nil {
			return fmt.Errorf("sql: %w", err)
		}
		stmt, err := parseTemplate(text)
		if err != nil {
			return fmt.Errorf("sql: %w", err)
		}
		pol.CompiledSQL, pol.Params, pol.subquery = text, params, stmt
	}
	if len(pol.Columns) > 0 {
		quote := func(v string) string { return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\"" }
		cols := make([]string, len(pol.Columns))
		for i, col := range pol.Columns {
			cols[i] = "__access." + quote(col)
		}
		source := "main." + quote(pol.Table)
		if !pol.Passthrough {
			source = "(" + pol.CompiledSQL + ")"
		}
		text := "SELECT " + strings.Join(cols, ", ") + " FROM " + source + " AS __access"
		stmt, err := parseTemplate(text)
		if err != nil {
			return err
		}
		pol.CompiledSQL, pol.subquery = text, stmt
	}
	if strings.TrimSpace(pol.DenyUnless) != "" {
		text, params, err := bindVariables(pol.DenyUnless)
		if err != nil {
			return fmt.Errorf("deny_unless: %w", err)
		}
		if _, err := parseTemplate(GuardQuery(text)); err != nil {
			return fmt.Errorf("deny_unless: %w", err)
		}
		pol.CompiledGuard, pol.GuardParams = text, params
		if pol.DenyMessage == "" {
			pol.DenyMessage = "无权查询表 " + pol.Table
		}
	}
	return nil
}

// GuardQuery wraps a deny_unless expression into a statement returning 0 or 1.
func GuardQuery(expr string) string {
	return "SELECT CASE WHEN (" + expr + ") THEN 1 ELSE 0 END"
}

// bindVariables turns ${var} into :var bind parameters.
func bindVariables(text string) (string, []string, error) {
	var params []string
	var unknown []string
	out := variableRef.ReplaceAllStringFunc(text, func(m string) string {
		name := variableRef.FindStringSubmatch(m)[1]
		if !slices.ContainsFunc(Variables, func(v VariableInfo) bool { return v.Name == name }) {
			unknown = append(unknown, name)
		} else if !slices.Contains(params, name) {
			params = append(params, name)
		}
		return ":" + name
	})
	if len(unknown) > 0 {
		return "", nil, fmt.Errorf("未知变量 ${%s}", strings.Join(unknown, "}、${"))
	}
	if strings.Contains(out, "${") || strings.Contains(out, "{{") {
		return "", nil, errors.New("存在无法解析的 ${...} 或 {{...}}")
	}
	slices.Sort(params)
	return strings.TrimSpace(out), params, nil
}

// parseTemplate parses a configured SELECT and checks the invariants the
// rewriter relies on: every table is written main.<table>, and the only bind
// parameters are known variables.
func parseTemplate(text string) (*sql.SelectStatement, error) {
	parser := sql.NewParser(strings.NewReader(text))
	stmt, err := parser.ParseStatement()
	if err != nil {
		return nil, fmt.Errorf("SQL 解析失败：%w", err)
	}
	if next, err := parser.ParseStatement(); next != nil || err == nil {
		return nil, errors.New("只能是单条语句")
	}
	sel, ok := stmt.(*sql.SelectStatement)
	if !ok {
		return nil, errors.New("必须是 SELECT 语句")
	}
	_, err = sql.Walk(sql.VisitFunc(func(n sql.Node) (sql.Node, error) {
		switch x := n.(type) {
		case *sql.QualifiedTableName:
			if !strings.EqualFold(sql.IdentName(x.Schema), "main") {
				return nil, fmt.Errorf("表 %s 必须写成 main.%s", sql.IdentName(x.Name), sql.IdentName(x.Name))
			}
		case *sql.QualifiedTableFunctionName:
			return nil, fmt.Errorf("不支持表值函数 %s", sql.IdentName(x.Name))
		case *sql.BindExpr:
			name := strings.TrimLeft(x.Name, ":@$?")
			if !slices.ContainsFunc(Variables, func(v VariableInfo) bool { return v.Name == name }) || !strings.HasPrefix(x.Name, ":") {
				return nil, fmt.Errorf("不允许的绑定参数 %s，请使用 ${变量}", x.Name)
			}
		}
		return n, nil
	}), sel)
	if err != nil {
		return nil, err
	}
	return sel, nil
}

// Validate recompiles a configuration edited through the management API.
func (c *Config) Validate() error { return c.compile() }

// MarshalYAML preserves null (all columns) versus [] (no columns).
func (p Policy) MarshalYAML() (any, error) {
	type plain Policy
	var node yaml.Node
	if err := node.Encode(plain(p)); err != nil {
		return nil, err
	}
	if p.Columns == nil {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == "columns" {
				node.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
				break
			}
		}
	}
	return &node, nil
}
