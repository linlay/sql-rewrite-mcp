package action_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"datascope-server/internal/action"
)

func TestImportsShareTransactionAndPermissions(t *testing.T) {
	r, db := setup(t)
	r.Libraries = []action.Library{
		{Name: "employee", Title: "Employee", Script: `export function owner(ctx) { return ctx.userId; } export function label() { return "updated"; }`},
		{Name: "records", Title: "Records", Script: "import {owner} from 'employee'; export function update(args,ctx) { const before=my.one`SELECT id FROM records WHERE id=${args.id}`; if(!before)deny('invisible'); const n=db.exec`UPDATE records SET value=${args.value} WHERE id=${args.id} AND owner=${owner(ctx)}`;if(n!==1)deny('changed');return db.one`SELECT value FROM records WHERE id=${args.id}`; }"},
	}
	d := definition(`import {update as save} from "records"; import {owner, label} from "employee"; function run(args,ctx){const row=save(args,ctx);return {value:row.value,label:label(),owner:owner(ctx)};}`)
	if err := action.ValidateLibraries(r.Libraries); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(r.Libraries...); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": 1, "value": "new' OR '1'='1"}
	preview, err := r.Run(context.Background(), d, args, true, nil)
	if err != nil || len(preview.Writes) != 1 {
		t.Fatal(preview, err)
	}
	var value string
	if err = db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&value); err != nil || value != "old" {
		t.Fatal("preview committed", value, err)
	}
	if _, err = r.Run(context.Background(), d, args, false, preview); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&value); err != nil || value != args["value"] {
		t.Fatal("import lost parameter binding", value, err)
	}
	args["id"] = 2
	if _, err = r.Run(context.Background(), d, args, false, nil); err == nil || !strings.Contains(err.Error(), "invisible") {
		t.Fatal("import bypassed query permissions", err)
	}
	queries, err := d.StaticSQL(r.Libraries...)
	if err != nil || len(queries) != 3 {
		t.Fatal("imported SQL missing from clone inspection", queries, err)
	}
}

func TestImportsAreFreshAndOnlyLoadReferencedModules(t *testing.T) {
	r, _ := setup(t)
	r.Libraries = []action.Library{
		{Name: "counter", Title: "Counter", Script: `let count=0; export function next(){return ++count;}`},
		{Name: "unused", Title: "Unused", Script: `this is intentionally invalid JavaScript`},
	}
	d := definition(`import {next} from "counter"; function run(){return next();}`)
	for i := 0; i < 2; i++ {
		out, err := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
		if err != nil || out.Result != float64(1) {
			t.Fatal("module state leaked or unused library loaded", out, err)
		}
	}
	r.Libraries[0].Script = `export function next(){return 2;}`
	out, err := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
	if err != nil || out.Result != float64(2) {
		t.Fatal("compiled cache retained old library", out, err)
	}
}

func TestInvalidModuleDependenciesAndSQL(t *testing.T) {
	for name, script := range map[string]string{
		"missing module":    `import {a} from "missing"; function run(){return a();}`,
		"missing export":    `import {missing} from "lib"; function run(){return missing();}`,
		"duplicate binding": `import {a} from "lib"; const a=2; function run(){return a;}`,
		"absolute path":     `import {a} from "/tmp/lib"; function run(){return a();}`,
		"relative path":     `import {a} from "../lib"; function run(){return a();}`,
		"remote module":     `import {a} from "https://example.com/lib.js"; function run(){return a();}`,
		"dynamic import":    `function run(){return import("lib");}`,
		"dynamic variable":  `function run(args){return import(args.value);}`,
		"require":           `function run(){return require("lib");}`,
		"require variable":  `function run(args){return require(args.value);}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := definition(script).Validate(action.Library{Name: "lib", Title: "Lib", Script: `export function a(){return 1;}`})
			if err == nil {
				t.Fatal("accepted invalid module dependency")
			}
		})
	}
	if err := action.ValidateLibraries([]action.Library{
		{Name: "a", Title: "A", Script: `import {b} from "b"; export function a(){return b();}`},
		{Name: "b", Title: "B", Script: `import {a} from "a"; export function b(){return a();}`},
	}); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatal("cycle accepted", err)
	}
	err := definition(`import {erase} from "lib"; function run(){return erase();}`).Validate(action.Library{Name: "lib", Title: "Lib", Script: "export function erase(){return db.exec`DELETE FROM records`; }"})
	if err == nil {
		t.Fatal("imported unsafe SQL bypassed validation")
	}
}

func TestModuleInitializationCannotExecuteSQL(t *testing.T) {
	r, db := setup(t)
	r.Libraries = []action.Library{{Name: "lib", Title: "Lib", Script: "try { db.exec`UPDATE records SET value='bad' WHERE id=1`; } catch(e) {} export function value(){return 1;}"}}
	d := definition(`import {value} from "lib"; function run(){return value();}`)
	if _, err := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, false, nil); err == nil {
		t.Fatal("initialization SQL accepted")
	}
	var value string
	if err := db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&value); err != nil || value != "old" {
		t.Fatal("module initialization changed data", value, err)
	}
}

func TestImportsStandardFormsAndConcurrentIsolation(t *testing.T) {
	r, _ := setup(t)
	r.Libraries = []action.Library{
		{Name: "math", Title: "Math", Script: `export default function increment(x){return x+1;} export function twice(x){return x*2;}`},
		{Name: "forward", Title: "Forward", Script: `export { default as increment, twice } from "math"; let count=0; export function next(){return ++count;}`},
	}
	d := definition(`import increment, {twice} from "math"; import * as helpers from "forward"; function run(){return increment(1)+twice(2)+helpers.increment(2)+helpers.twice(3)+helpers.next();}`)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := r.Run(context.Background(), d, map[string]any{"id": 1, "value": "v"}, true, nil)
			if err == nil && out.Result != float64(16) {
				err = fmt.Errorf("module state crossed invocations: %v", out.Result)
			}
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := definition(`import {twice} from "math"; twice=()=>0; function run(){return twice(1);}`).Validate(r.Libraries...); err == nil {
		t.Fatal("imported binding could be reassigned")
	}
}
