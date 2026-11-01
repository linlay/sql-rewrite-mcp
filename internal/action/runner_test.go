package action_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
)

func setup(t *testing.T) (action.Runner, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "business.sqlite")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`CREATE TABLE records(id INTEGER PRIMARY KEY,owner INTEGER,value TEXT,amount BIGINT);INSERT INTO records VALUES(1,10,'old',9223372036854775807),(2,20,'other',3);`)
	if e != nil {
		t.Fatal(e)
	}
	st, e := store.OpenWriter(ctx, path, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	cfg, e := rewrite.ParseConfig([]byte("version: 1\nschemes: [{scheme: CUSTOM, name: Owner}]\ntables:\n - {table: records, scheme: CUSTOM, sql: 'SELECT * FROM main.records WHERE owner=${userId}'}\n"))
	if e != nil {
		t.Fatal(e)
	}
	rw, e := rewrite.New(cfg, []string{"records"})
	if e != nil {
		t.Fatal(e)
	}
	return action.Runner{Store: st, Rewriter: rw, Bindings: map[string]any{"userId": int64(10), "asOf": "2026-10-29"}, UserID: 10, Now: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}, db
}
func definition(script string) action.Definition {
	return action.Definition{Name: "edit", Title: "Edit", Description: "Edit", Parameters: map[string]action.Parameter{"id": {Type: "integer"}, "value": {Type: "string"}}, Script: script}
}

const updateScript = "function run(args,ctx){const row=my.one`SELECT id FROM records WHERE id=${args.id}`;if(!row)deny('invisible');const n=db.exec`UPDATE records SET value=${args.value} WHERE id=${args.id} AND owner=${ctx.userId}`;if(n!==1)deny('changed');return {id:args.id,value:args.value};}"

func TestActionTemplatesPermissionsAndPreview(t *testing.T) {
	r, db := setup(t)
	ctx := context.Background()
	args := map[string]any{"id": 1, "value": "F001' OR '1'='1"}
	out, e := r.Run(ctx, definition(updateScript), args, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Writes) != 1 || out.Writes[0].Rows != 1 {
		t.Fatal(out)
	}
	var v string
	db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
	if v != "old" {
		t.Fatal("preview committed")
	}
	if _, e = r.Run(ctx, definition(updateScript), args, false, out); e != nil {
		t.Fatal(e)
	}
	db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
	if v != args["value"] {
		t.Fatal("injection or missing write", v)
	}
	args["id"] = 2
	if _, e = r.Run(ctx, definition(updateScript), args, false, nil); e == nil || !strings.Contains(e.Error(), "invisible") {
		t.Fatal(e)
	}
	db.QueryRow("SELECT value FROM records WHERE id=2").Scan(&v)
	if v != "other" {
		t.Fatal("unauthorized write")
	}
}
func TestActionRollbackAndLimits(t *testing.T) {
	cases := map[string]string{
		"deny": "deny('no')", "caught deny": "try{deny('no')}catch(e){}", "throw": "throw new Error('no')", "sql": "db.exec`INSERT INTO absent VALUES(1)`", "caught SQL": "try{db.exec`INSERT INTO absent VALUES(1)`}catch(e){}", "concat": "db.one('SELECT '+args.value)", "forged": "db.one(Object.freeze(['SELECT * FROM records']))", "timeout": "while(true){}", "SQL timeout": "db.one`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT SUM(x) FROM n`", "SQL limit": "for(let i=0;i<50;i++) db.one`SELECT 1`", "serialization": "return {toJSON(){deny('serialization denied')}}", "context": "ctx.userId=20", "network": "fetch('https://example.com')", "filesystem": "require('fs')",
	}
	for name, tail := range cases {
		t.Run(name, func(t *testing.T) {
			r, db := setup(t)
			r.Timeout = 100 * time.Millisecond
			script := "function run(args,ctx){db.exec`UPDATE records SET value='changed' WHERE id=1`;" + tail + ";return {ok:true}}"
			_, e := r.Run(context.Background(), definition(script), map[string]any{"id": 1, "value": "v"}, false, nil)
			if e == nil {
				t.Fatal("expected denial")
			}
			var v string
			db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
			if v != "old" {
				t.Fatal("failed action committed", v)
			}
		})
	}
}
func TestActionRejectsSQLAndPrecision(t *testing.T) {
	for _, script := range []string{
		"function run(a){return db.one`SELECT '${a.id}'`}", "function run(a){return db.one`SELECT 1 -- ${a.id}`} ", "function run(){db.exec`DELETE FROM records`;return {}}", "function run(){db.exec`UPDATE records SET value='x'`;return {}}", "function run(){db.exec`DROP TABLE records`;return {}}", "function run(a){return db.one`SELECT :extra`} ", "function run(a){return db.one`SELECT 1;SELECT 2`} ",
	} {
		if e := definition(script).Validate(); e == nil {
			t.Fatal("accepted invalid SQL", script)
		}
	}
	r, _ := setup(t)
	d := definition("function run(){return db.one`SELECT amount FROM records WHERE id=1`}")
	out, e := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	if out.Result.(map[string]any)["amount"] != "9223372036854775807" {
		t.Fatal("BIGINT lost precision", out.Result)
	}
}
func TestActionParameterSchema(t *testing.T) {
	maxLen := 2
	min := float64(1)
	d := action.Definition{Parameters: map[string]action.Parameter{"name": {Type: "string", MaxLength: &maxLen, Pattern: "^张", Enum: []any{"张三", "张四"}}, "n": {Type: "integer", Min: &min}, "optional": {Type: "boolean", Optional: true}}}
	if _, e := d.Arguments(map[string]any{"name": "张三", "n": 1}); e != nil {
		t.Fatal(e)
	}
	for _, args := range []map[string]any{{"name": "李四", "n": 1}, {"name": "张三丰", "n": 1}, {"name": "张三", "n": 0}, {"name": "张三", "n": 1.5}, {"name": "张三", "n": 9007199254740992.0}, {"name": "张三"}, {"name": "张三", "n": 1, "extra": true}} {
		if _, e := d.Arguments(args); e == nil || !strings.Contains(e.Error(), "additionalProperties") {
			t.Fatal(args, e)
		}
	}
}
func TestActionConfirmationPlanChangeRollsBack(t *testing.T) {
	r, db := setup(t)
	args := map[string]any{"id": 1, "value": "v"}
	d := definition("function run(a){const n=db.exec`UPDATE records SET value=${a.value} WHERE owner=10`;return {n}}")
	out, e := r.Run(context.Background(), d, args, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO records VALUES(3,10,'new',0)"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Run(context.Background(), d, args, false, out); e == nil {
		t.Fatal("accepted changed plan")
	}
	var v string
	db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
	if v != "old" {
		t.Fatal("changed plan committed")
	}
}

func TestActionAuditFailureBeforeCommitRollsBack(t *testing.T) {
	r, db := setup(t)
	r.BeforeCommit = func(*action.Preview) error { return fmt.Errorf("audit unavailable") }
	_, e := r.Run(context.Background(), definition(updateScript), map[string]any{"id": 1, "value": "bad"}, false, nil)
	if e == nil {
		t.Fatal("audit failure ignored")
	}
	var v string
	db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
	if v != "old" {
		t.Fatal("audit failure committed")
	}
}
func TestActionPostSelectAndRowLimit(t *testing.T) {
	r, db := setup(t)
	_, e := db.Exec("WITH RECURSIVE n(x) AS (SELECT 3 UNION ALL SELECT x+1 FROM n WHERE x<1100) INSERT INTO records SELECT x,10,'many',0 FROM n")
	if e != nil {
		t.Fatal(e)
	}
	d := definition("function run(){const rows=my.all`SELECT id FROM records`;const n=db.exec`UPDATE records SET value='changed' WHERE id=1`;if(db.one`SELECT value FROM records WHERE id=1`.value!=='changed')deny('transaction cannot read own writes');return {length:rows.length,n};}")
	out, e := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
	if e != nil {
		t.Fatal(e)
	}
	if out.Result.(map[string]any)["length"] != float64(1000) {
		t.Fatal(out.Result)
	}
	d.Script = "function run(){db.exec`UPDATE records SET value='changed' WHERE id=1`;if(db.one`SELECT value FROM records WHERE id=1`.value==='changed')deny('postcheck');return {}}"
	if _, e = r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, false, nil); e == nil {
		t.Fatal("postcheck did not abort")
	}
	var v string
	db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
	if v != "old" {
		t.Fatal("postcheck committed")
	}
}

func TestActionMyQueriesUseGuardsColumnsAndLimits(t *testing.T) {
	for _, mode := range []string{"guard", "column", "disabled", "limit"} {
		t.Run(mode, func(t *testing.T) {
			r, db := setup(t)
			policy := "{table: records, scheme: CUSTOM, sql: 'SELECT * FROM main.records WHERE owner=${userId}'"
			switch mode {
			case "guard":
				policy += ", denyUnless: '${userId} = 20', denyMessage: 'blocked'"
			case "column":
				policy += ", columns: [id]"
			case "disabled":
				policy += ", enabled: false"
			case "limit":
				policy += ", maxRows: 1"
			}
			policy += "}"
			cfg, e := rewrite.ParseConfig([]byte("version: 1\nschemes: [{scheme: CUSTOM, name: Owner}]\ntables:\n - " + policy))
			if e != nil {
				t.Fatal(e)
			}
			r.Rewriter, e = rewrite.New(cfg, []string{"records"})
			if e != nil {
				t.Fatal(e)
			}
			d := definition("function run(){db.exec`UPDATE records SET value='changed' WHERE id=1`;const rows=my.all`SELECT value FROM records`;return {n:rows.length}}")
			out, e := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
			if mode == "limit" {
				if e != nil {
					t.Fatal(e)
				}
				if out.Result.(map[string]any)["n"] != float64(1) {
					t.Fatal(out)
				}
			} else if e == nil {
				t.Fatal("my query bypassed " + mode)
			}
			var v string
			db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&v)
			if v != "old" {
				t.Fatal("policy check failure wrote data")
			}
		})
	}
}
