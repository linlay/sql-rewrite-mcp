package demoactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/application"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/demoactions"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
)

func platformFixture(t *testing.T) (*application.Service, context.Context) {
	t.Helper()
	path, _, _, _ := fixture(t)
	dir := t.TempDir()
	a, e := audit.Open(filepath.Join(dir, "audit.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	m, e := management.Open(filepath.Join(dir, "management.sqlite"), a)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	m.AllowedRoots = []string{filepath.Dir(path)}
	ctx := context.Background()
	if e = m.Bootstrap(ctx, "admin", "installer-test-password"); e != nil {
		t.Fatal(e)
	}
	admin, e := m.ByUsername(ctx, "admin")
	if e != nil {
		t.Fatal(e)
	}
	ctx = guard.WithUser(audit.With(ctx, audit.Metadata{ActorID: admin.ID, Source: "test"}), admin)
	st, e := store.Open(path, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	c, e := catalog.BootstrapManaged(ctx, m, filepath.Join(dir, "config"), st, "../../config/rewrite.yml", store.DefaultAsOf, 5*time.Second, filepath.Join(dir, "missing.json"))
	if e != nil {
		t.Fatal(e)
	}
	d, e := c.Get("default")
	if e != nil {
		t.Fatal(e)
	}
	d.Authentication = &catalog.Authentication{Type: "local", EmployeeDirectory: true}
	if e = c.Save(ctx, *d); e != nil {
		t.Fatal(e)
	}
	for _, id := range []int64{1, 121, 175} {
		_, e = m.SaveBusinessAccount(ctx, management.BusinessAccount{DatabaseID: "default", Username: fmt.Sprintf("E%04d", id), Name: "Demo", BusinessUserID: id, Permissions: []string{"query", "knowledge.read"}, Active: true}, "business-test-password", 0)
		if e != nil {
			t.Fatal(e)
		}
	}
	return &application.Service{Management: m, Catalog: c}, ctx
}
func TestInstallIsExplicitIdempotentAndPreservesAccounts(t *testing.T) {
	app, ctx := platformFixture(t)
	accounts := []string{"E0001", "E0121", "E0175"}
	before := map[string]management.BusinessAccount{}
	for _, name := range accounts {
		a, e := app.Management.BusinessAccount(ctx, "default", name)
		if e != nil {
			t.Fatal(e)
		}
		before[name] = a
	}
	result, e := demoactions.Install(ctx, app, "default", accounts, false)
	if e != nil {
		t.Fatal(e)
	}
	if result.ActionCount != 18 || !result.ConfigChanged || len(result.GrantedAccounts) != 3 {
		t.Fatal(result)
	}
	for _, name := range accounts {
		a, e := app.Management.AuthenticateBusiness(ctx, "default", name, "business-test-password")
		if e != nil {
			t.Fatal("password changed", e)
		}
		if !slices.Contains(a.Permissions, "action") || a.AuthVersion != before[name].AuthVersion+1 || a.BusinessUserID != before[name].BusinessUserID {
			t.Fatal(a)
		}
	}
	second, e := demoactions.Install(ctx, app, "default", accounts, false)
	if e != nil {
		t.Fatal(e)
	}
	if second.ConfigChanged || len(second.GrantedAccounts) != 0 || second.Revision != result.Revision {
		t.Fatal("reinstall changed configuration or accounts", second)
	}
	d, e := app.Catalog.Get("default")
	if e != nil {
		t.Fatal(e)
	}
	d.Actions[0].Description = "User customized definition"
	if e = app.Catalog.Save(ctx, *d); e != nil {
		t.Fatal(e)
	}
	if _, e = demoactions.Install(ctx, app, "default", accounts, false); e == nil {
		t.Fatal("silently overwrote customized action")
	}
	if _, e = demoactions.Install(ctx, app, "default", accounts, true); e != nil {
		t.Fatal(e)
	}
	records, e := app.Management.Records(ctx, "knowledge")
	if e != nil {
		t.Fatal(e)
	}
	if len(records) != 1 {
		t.Fatal("case guide missing", records)
	}
	var guide application.Knowledge
	if e = json.Unmarshal([]byte(records[0].Data), &guide); e != nil || !guide.Published {
		t.Fatal("guide not published", e)
	}
	if _, e = demoactions.Install(context.Background(), app, "default", accounts, false); !errors.Is(e, management.ErrDenied) {
		t.Fatal(e)
	}
	// A typo is detected before enabling writes or changing account versions.
	if _, e = demoactions.Install(ctx, app, "default", []string{"missing"}, false); e == nil {
		t.Fatal("missing account silently accepted")
	}
}
func TestInstalledSecuritiesActionConfirmationAndReplay(t *testing.T) {
	app, ctx := platformFixture(t)
	if _, e := demoactions.Install(ctx, app, "default", []string{"E0001"}, false); e != nil {
		t.Fatal(e)
	}
	a, e := app.Management.BusinessAccount(ctx, "default", "E0001")
	if e != nil {
		t.Fatal(e)
	}
	business := guard.WithBusiness(context.Background(), a)
	args := map[string]any{"customer_no": "C00001359", "mobile": "13900001359", "reason": "test"}
	out, e := app.InvokeAction(business, "default", "update_customer_mobile", "confirm-demo", args)
	if e != nil || out.Status != "confirm_required" {
		t.Fatal(out, e)
	}
	done, e := app.ConfirmAction(context.Background(), out.ConfirmationToken, "business-test-password")
	if e != nil || done.Status != "done" {
		t.Fatal(done, e)
	}
	again, e := app.InvokeAction(business, "default", "update_customer_mobile", "confirm-demo", args)
	if e != nil || again.Status != "done" {
		t.Fatal(again, e)
	}
	if !demoactions.EqualJSON(done.Result, again.Result) {
		t.Fatal("replay result changed")
	}
}
func TestCashTransferFailureAfterDebitRollsBack(t *testing.T) {
	_, db, st, rw := fixture(t)
	bundle, e := demoactions.Load()
	if e != nil {
		t.Fatal(e)
	}
	// Force the credit side to exceed the JS-safe range after a valid debit.
	if _, e = db.Exec("DELETE FROM cash_flows WHERE fund_account_no='880000000079'; UPDATE daily_balances SET opening_balance=9007199254740990,transfer_in=0,transfer_out=0,buy_amount=0,sell_amount=0,fees=0,closing_balance=9007199254740990,available_balance=9007199254740990,withdrawable_balance=9007199254740990 WHERE fund_account_no='880000000079'"); e != nil {
		t.Fatal(e)
	}
	runner := runnerFor(st, rw, 175)
	before := snapshot(t, db)
	_, e = runner.Run(context.Background(), def(t, bundle, "transfer_demo_cash"), map[string]any{"source_fund_account_no": "880000000080", "target_fund_account_no": "880000000079", "amount_cents": 100, "reason": "credit failure"}, false, nil)
	if e == nil {
		t.Fatal("credit failure accepted")
	}
	if snapshot(t, db) != before {
		t.Fatal("debit or debit flow survived failed credit")
	}
	invariants(t, db)
}
func TestConcurrentDemoDepositsDoNotLoseUpdates(t *testing.T) {
	_, db, st, rw := fixture(t)
	bundle, e := demoactions.Load()
	if e != nil {
		t.Fatal(e)
	}
	runner := runnerFor(st, rw, 1)
	d := def(t, bundle, "deposit_demo_cash")
	var before int64
	if e = db.QueryRow("SELECT closing_balance FROM daily_balances WHERE fund_account_no='880000000927'").Scan(&before); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Run(context.Background(), d, map[string]any{"fund_account_no": "880000000927", "amount_cents": 100, "reason": "concurrent test"}, false, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var after int64
	if e = db.QueryRow("SELECT closing_balance FROM daily_balances WHERE fund_account_no='880000000927'").Scan(&after); e != nil {
		t.Fatal(e)
	}
	if after != before+400 {
		t.Fatal(before, after)
	}
	invariants(t, db)
}

func runnerFor(st *store.Store, rw *rewrite.Rewriter, uid int64) action.Runner {
	bundle, err := demoactions.Load()
	if err != nil {
		panic(err)
	}
	return action.Runner{Libraries: bundle.ActionLibraries, Store: st, Rewriter: rw, Bindings: map[string]any{"userId": uid, "asOf": "2026-10-20T00:00:00Z"}, UserID: uid, Now: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
}

func TestSecuritiesActionParameterErrorsNeverWrite(t *testing.T) {
	bundle, e := demoactions.Load()
	if e != nil {
		t.Fatal(e)
	}
	_, db, st, rw := fixture(t)
	runner := runnerFor(st, rw, 1)
	before := snapshot(t, db)
	tests := []step{
		{Name: "update_customer_risk_level", Arguments: map[string]any{"customer_no": "C00001359", "risk_level": "C9", "reason": "test"}},
		{Name: "update_customer_mobile", Arguments: map[string]any{"customer_no": "C00001359", "mobile": "123", "reason": "test"}},
		{Name: "withdraw_demo_cash", Arguments: map[string]any{"fund_account_no": "880000000927", "amount_cents": -1, "reason": "test"}},
		{Name: "deposit_demo_cash", Arguments: map[string]any{"fund_account_no": "880000000927", "amount_cents": 1.5, "reason": "test"}},
		{Name: "deposit_demo_cash", Arguments: map[string]any{"fund_account_no": "880000000927", "amount_cents": 100000001, "reason": "test"}},
		{Name: "freeze_fund_account", Arguments: map[string]any{"fund_account_no": "880000000079", "reason": "   "}},
		{Name: "freeze_fund_account", Arguments: map[string]any{"fund_account_no": "880000000079", "reason": "test", "confirmed": true}},
	}
	for _, test := range tests {
		if _, e = runner.Run(context.Background(), def(t, bundle, test.Name), test.Arguments, false, nil); e == nil {
			t.Fatal("invalid parameters accepted", test)
		}
	}
	if after := snapshot(t, db); after != before {
		t.Fatal("parameter rejection changed data")
	}
}
