package rewrite

import (
	"path/filepath"
	"strings"
	"testing"
)

func loadConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadConfig(filepath.Join("..", "..", "config", "rewrite.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func tableNames(cfg *Config) []string {
	var names []string
	for _, pol := range cfg.Tables {
		names = append(names, pol.Table)
	}
	return names
}

func TestEveryConfiguredTableRewrites(t *testing.T) {
	cfg := loadConfig(t)
	r, err := New(cfg, tableNames(cfg))
	if err != nil {
		t.Fatal(err)
	}
	for _, pol := range cfg.Tables {
		res, err := r.Rewrite("SELECT * FROM " + pol.Table)
		if err != nil {
			t.Errorf("%s: %v", pol.Table, err)
			continue
		}
		if len(res.Tables) != 1 || res.Tables[0].Scheme != pol.Scheme {
			t.Errorf("%s: refs %+v", pol.Table, res.Tables)
		}
		// User context must only ever enter as bind parameters.
		if !pol.Passthrough && (!strings.Contains(res.RewrittenSQL, ":userId") || len(res.Params) == 0) {
			t.Errorf("%s: no bind parameters in %s", pol.Table, res.RewrittenSQL)
		}
		if (pol.DenyUnless != "") != (len(res.Guards) == 1) {
			t.Errorf("%s: guards %+v", pol.Table, res.Guards)
		}
	}
}

func TestNewRequiresExactTableCoverage(t *testing.T) {
	cfg := loadConfig(t)
	if _, err := New(cfg, append(tableNames(cfg), "brand_new_table")); err == nil || !strings.Contains(err.Error(), "brand_new_table") {
		t.Errorf("table without rule: %v", err)
	}
	if _, err := New(cfg, tableNames(cfg)[1:]); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("rule for missing table: %v", err)
	}
}

const minimalConfig = `
version: 1
schemes:
  - {scheme: S, name: s, description: d}
tables:
`

func TestConfigValidation(t *testing.T) {
	for name, c := range map[string]struct{ tables, want string }{
		"missing sql":                {"  - {table: a, scheme: S, reason: r}", "passthrough: true"},
		"both":                       {"  - {table: a, scheme: S, reason: r, passthrough: true, sql: 'SELECT 1'}", "二选一"},
		"unknown scheme":             {"  - {table: a, scheme: X, reason: r, passthrough: true}", "未定义的方案"},
		"unknown variable":           {"  - {table: a, scheme: S, reason: r, sql: 'SELECT t.* FROM main.a AS t WHERE t.id = ${tenantId}'}", "未知变量"},
		"unqualified table":          {"  - {table: a, scheme: S, reason: r, sql: 'SELECT t.* FROM a AS t'}", "main.a"},
		"retired fragment reference": {"  - {table: a, scheme: S, reason: r, sql: 'SELECT t.* FROM main.a AS t WHERE {{nope}}'}", "无法解析"},
		"raw bind parameter":         {"  - {table: a, scheme: S, reason: r, sql: 'SELECT t.* FROM main.a AS t WHERE t.id = :userId OR t.id = ?'}", "绑定参数"},
		"not select":                 {"  - {table: a, scheme: S, reason: r, sql: 'DELETE FROM main.a'}", "SELECT"},
		"bad guard":                  {"  - {table: a, scheme: S, reason: r, passthrough: true, denyUnless: 'EXISTS (SELECT 1 FROM b)'}", "main.b"},
		"duplicate table":            {"  - {table: a, scheme: S, reason: r, passthrough: true}\n  - {table: A, scheme: S, reason: r, passthrough: true}", "重复"},
		"misspelled key":             {"  - {table: a, scheme: S, reason: r, passthru: true}", "passthru"},
	} {
		_, err := ParseConfig([]byte(minimalConfig + c.tables))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}

	if _, err := ParseConfig([]byte("fragments: {}\n" + minimalConfig)); err == nil {
		t.Fatal("retired fragments field accepted")
	}

	ok, err := ParseConfig([]byte(minimalConfig + "  - {table: a, scheme: S, reason: r, sql: 'SELECT t.* FROM main.a AS t WHERE u.id = ${userId}'}"))
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := ok.Lookup("A")
	if pol.CompiledSQL != "SELECT t.* FROM main.a AS t WHERE u.id = :userId" || len(pol.Params) != 1 {
		t.Errorf("compiled = %q params %v", pol.CompiledSQL, pol.Params)
	}
}

func TestRejectsBindParametersInInput(t *testing.T) {
	cfg := loadConfig(t)
	r, _ := New(cfg, tableNames(cfg))
	for _, q := range []string{"SELECT * FROM customers WHERE id = :userId", "SELECT ?", "SELECT * FROM customers WHERE id = $1"} {
		if _, err := r.Rewrite(q); !IsDenied(err) {
			t.Errorf("%q: %v", q, err)
		}
	}
}

// The token-level check must catch any table reference the AST rewrite missed.
func TestCheckAllTablesRewritten(t *testing.T) {
	r := &Rewriter{tables: map[string]bool{"customers": true, "roles": true}}
	for sql, ok := range map[string]bool{
		`SELECT * FROM "main"."customers" AS "customers"`:                     true,
		`SELECT "customers"."id" FROM (SELECT 1 AS id) AS "customers"`:        true,
		`SELECT * FROM "customers"`:                                           false,
		`SELECT * FROM (SELECT 1) AS x JOIN customers ON 1`:                   false,
		`SELECT 1 WHERE 1 IN (SELECT id FROM "main"."roles" AS r, customers)`: false,
	} {
		if err := r.checkAllTablesRewritten(sql); (err == nil) != ok {
			t.Errorf("checkAllTablesRewritten(%q) = %v, want ok=%v", sql, err, ok)
		}
	}
}

func TestRewriteKeepsInputText(t *testing.T) {
	cfg := loadConfig(t)
	r, err := New(cfg, tableNames(cfg))
	if err != nil {
		t.Fatal(err)
	}
	sub := func(table string) string { return "(" + r.subSQL[table] + ")" }
	for _, c := range []struct{ in, want string }{
		{
			"SELECT c.customer_no,c.name,b.closing_balance\nFROM customers c\nJOIN daily_balances b ON b.customer_no=c.customer_no\nWHERE b.trade_date='2026-10-20'\nORDER BY b.closing_balance DESC",
			"SELECT c.customer_no,c.name,b.closing_balance\nFROM " + sub("customers") + " AS c\nJOIN " + sub("daily_balances") + " AS b ON b.customer_no=c.customer_no\nWHERE b.trade_date='2026-10-20'\nORDER BY b.closing_balance DESC",
		},
		{"select * from customers -- 全部\nwhere name = '张三'", "select * from " + sub("customers") + " AS customers -- 全部\nwhere name = '张三'"},
		{`SELECT "order".x FROM Customers AS "order" INDEXED BY idx, roles r`, `SELECT "order".x FROM ` + sub("customers") + ` AS "order", main.roles AS r`},
		{
			"WITH v AS (SELECT customer_no FROM customers)\nSELECT * FROM v WHERE customer_no IN (SELECT customer_no FROM fund_accounts NOT INDEXED)",
			"WITH v AS (SELECT customer_no FROM " + sub("customers") + " AS customers)\nSELECT * FROM v WHERE customer_no IN (SELECT customer_no FROM " + sub("fund_accounts") + " AS fund_accounts)",
		},
	} {
		res, err := r.Rewrite(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
		} else if res.RewrittenSQL != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.in, res.RewrittenSQL, c.want)
		}
	}
	if strings.ContainsAny(r.subSQL["customers"], "\n\"") {
		t.Errorf("subquery not one unquoted line: %s", r.subSQL["customers"])
	}
}

func TestSplicedRejectsMismatch(t *testing.T) {
	if _, ok := spliced([]rune("SELECT 1"), []splice{{start: 7, end: 8, text: "2"}}, `SELECT 1`); ok {
		t.Error("splice changing the statement was accepted")
	}
}
