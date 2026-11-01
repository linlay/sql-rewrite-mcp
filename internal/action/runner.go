package action

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
	"github.com/dop251/goja"
)

type Write struct {
	SQL   string `json:"sql"`
	Rows  int64  `json:"rows"`
	Binds []any  `json:"binds"`
}
type Preview struct {
	Result any     `json:"result"`
	Writes []Write `json:"writes"`
}

var ErrOutcomeUnknown = errors.New("事务结果未知，请核对审计，禁止自动重试")

type Runner struct {
	Libraries    []Library
	DatabaseID   string
	BeforeCommit func(*Preview) error
	Store        *store.Store
	Rewriter     *rewrite.Rewriter
	Bindings     map[string]any
	UserID       int64
	Now          time.Time
	// Timeout can shorten the fixed production limit for tests.
	Timeout time.Duration
}

// Run owns one transaction. Preview executes the same script and always rolls
// back. Any SQL error/deny is sticky, even if JavaScript catches the exception.
func (r Runner) Run(ctx context.Context, d Definition, args map[string]any, preview bool, expected *Preview) (out *Preview, err error) {
	if r.UserID <= 0 || r.UserID > 9007199254740991 {
		return nil, errors.New("业务身份超出 JavaScript 安全整数范围")
	}
	args, e := d.Arguments(args)
	if e != nil {
		return nil, e
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	program, sites, e := compileWithLibraries(d.Script, r.Libraries, r.DatabaseID)
	if e != nil {
		return nil, e
	}
	for _, s := range sites {
		if e = store.ValidateActionTemplate(s.sql, s.count, s.method == "exec"); e != nil {
			return nil, e
		}
	}
	tx, e := r.Store.BeginAction(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	vm := goja.New()
	vm.SetMaxCallStackSize(128)
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-stopped:
		}
	}()
	var fatal error
	initializing := len(r.Libraries) > 0
	abort := func(e error) goja.Value {
		if fatal == nil {
			fatal = e
		}
		panic(vm.NewGoError(e))
	}
	vm.Set("deny", func(c goja.FunctionCall) goja.Value { return abort(fmt.Errorf("%s", c.Argument(0).String())) })
	out = &Preview{Writes: []Write{}}
	for _, s := range sites {
		site := s
		if e = vm.GlobalObject().DefineDataProperty(site.name, vm.ToValue(func(c goja.FunctionCall) goja.Value {
			if initializing {
				return abort(errors.New("模块初始化阶段不能执行 SQL，请在函数中执行"))
			}
			if fatal != nil {
				return abort(fatal)
			}
			if ctx.Err() != nil {
				return abort(ctx.Err())
			}
			if len(c.Arguments) != site.count+1 {
				return abort(errors.New("SQL 只接受模板调用"))
			}
			binds := map[string]any{}
			args := []any{}
			for i := 0; i < site.count; i++ {
				if _, object := c.Arguments[i+1].(*goja.Object); object {
					return abort(errors.New("SQL 插值不接受对象或数组"))
				}
				v := c.Arguments[i+1].Export()
				switch x := v.(type) {
				case nil, string, bool:
				case int64:
					if x > 9007199254740991 || x < -9007199254740991 {
						return abort(errors.New("SQL 整数超出 JS 安全范围，请用字符串"))
					}
				case float64:
					if !finite(x) || math.Abs(x) > 9007199254740991 {
						return abort(errors.New("SQL 数值无效或超出安全范围"))
					}
				default:
					return abort(errors.New("SQL 插值只接受字符串、有限数值、布尔或 null"))
				}
				n := "arg_action" + strconv.Itoa(i)
				binds[n] = v
				args = append(args, sql.Named(n, v))
			}
			if site.method == "exec" {
				n, e := tx.Exec(ctx, site.sql, args...)
				if e != nil {
					return abort(e)
				}
				out.Writes = append(out.Writes, Write{SQL: site.sql, Rows: n, Binds: args})
				return vm.ToValue(n)
			}
			query := site.sql
			limit := 1000
			if site.scope == "my" {
				if r.Rewriter == nil {
					return abort(errors.New("缺少查询权限配置"))
				}
				names := []string{}
				for n := range binds {
					names = append(names, n)
				}
				res, e := r.Rewriter.RewriteWithParameters(query, names)
				if e != nil {
					return abort(e)
				}
				values := map[string]any{}
				for n, v := range r.Bindings {
					values[n] = v
				}
				for n, v := range binds {
					values[n] = v
				}
				named := func(names []string) []any {
					a := []any{}
					for _, n := range names {
						a = append(a, sql.Named(n, values[n]))
					}
					return a
				}
				for _, g := range res.Guards {
					rows, e := tx.Query(ctx, rewrite.GuardQuery(g.SQL), 1, named(g.Params)...)
					if e != nil {
						return abort(e)
					}
					ok := false
					if len(rows.Rows) == 1 {
						for _, v := range rows.Rows[0] {
							switch x := v.(type) {
							case int64:
								ok = x != 0
							case bool:
								ok = x
							case float64:
								ok = finite(x) && x != 0
							case string:
								n, err := strconv.ParseFloat(x, 64)
								ok = err == nil && finite(n) && n != 0
							}
						}
					}
					if !ok {
						return abort(fmt.Errorf("无权查询表 %s: %s", g.Table, g.Message))
					}
				}
				for _, n := range res.TableNames() {
					if p, ok := r.Rewriter.Config().Lookup(n); ok && p.MaxRows > 0 {
						limit = min(limit, p.MaxRows)
					}
				}
				query = res.RewrittenSQL
				args = named(res.Params)
			}
			rows, e := tx.Query(ctx, query, limit, args...)
			if e != nil {
				return abort(e)
			}
			if site.method == "one" {
				if len(rows.Rows) == 0 {
					return goja.Null()
				}
				return vm.ToValue(rows.Rows[0])
			}
			return vm.ToValue(rows.Rows)
		}), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_FALSE); e != nil {
			return nil, e
		}
	}
	// Capture JSON intrinsics before untrusted script can replace them.
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	raw, _ := json.Marshal(map[string]any{"args": args, "ctx": map[string]any{"userId": r.UserID, "now": r.Now.UTC().Format(time.RFC3339Nano)}})
	setup := `(function(){const x=JSON.parse(` + strconv.Quote(string(raw)) + `);function freeze(v){if(v&&typeof v==='object'){Object.keys(v).forEach(k=>freeze(v[k]));Object.freeze(v)}return v}Object.defineProperty(globalThis,'__input',{value:freeze(x),writable:false,configurable:false});})();`
	if _, e = vm.RunString(setup); e != nil {
		return nil, e
	}
	if _, e = vm.RunProgram(program); e != nil {
		return nil, e
	}
	initializing = false
	run, ok := goja.AssertFunction(vm.Get("run"))
	if !ok {
		return nil, errors.New("脚本必须定义 function run(args, ctx)")
	}
	input := vm.Get("__input").ToObject(vm)
	value, e := run(goja.Undefined(), input.Get("args"), input.Get("ctx"))
	if fatal != nil {
		return nil, fatal
	}
	if e != nil {
		return nil, e
	}
	if value.ExportType() == reflect.TypeFor[*goja.Promise]() {
		return nil, errors.New("run 必须同步返回 JSON 对象")
	}
	serialized, e := stringify(goja.Undefined(), value)
	if fatal != nil {
		return nil, fatal
	}
	if e != nil {
		return nil, e
	}
	text := serialized.String()
	if len(text) > 1048576 {
		return nil, errors.New("action 返回值超过 1 MiB")
	}
	if e = json.Unmarshal([]byte(text), &out.Result); e != nil {
		return nil, errors.New("action 返回值必须是 JSON")
	}
	if expected != nil {
		a, _ := json.Marshal(out)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			return nil, errors.New("执行结果或写入计划已变化，请使用新的 request_id 重新预览")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !preview && r.BeforeCommit != nil {
		if e = r.BeforeCommit(out); e != nil {
			return nil, e
		}
	}
	if preview {
		if e = tx.Rollback(); e != nil {
			return nil, e
		}
	} else {
		if e = tx.Commit(); e != nil {
			return nil, fmt.Errorf("%w: %v", ErrOutcomeUnknown, e)
		}
	}
	return out, nil
}
