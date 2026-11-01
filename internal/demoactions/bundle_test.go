package demoactions_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/demoactions"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
)

type step struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type example struct {
	step
	ID      string `json:"id"`
	Title   string `json:"title"`
	UserID  int64  `json:"business_user_id"`
	Prepare []step `json:"prepare"`
	Status  string `json:"expected_status"`
	Writes  int    `json:"expected_writes"`
}

func examples(t *testing.T) []example {
	t.Helper()
	raw, e := os.ReadFile("../../fixtures/actions/securities.json")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Cases []example `json:"cases"`
	}
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	return v.Cases
}
func fixture(t *testing.T) (string, *sql.DB, *store.Store, *rewrite.Rewriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "securities.sqlite")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, p := range []string{"../../sql/schema.sqlite.sql", "../../fixtures/actions/securities-seed.sql"} {
		raw, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(p, e)
		}
	}
	st, e := store.OpenWriter(context.Background(), path, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	cfg, e := rewrite.LoadConfig("../../config/rewrite.yml")
	if e != nil {
		t.Fatal(e)
	}
	names, e := st.TableNames(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	rw, e := rewrite.New(cfg, names)
	if e != nil {
		t.Fatal(e)
	}
	return path, db, st, rw
}
func def(t *testing.T, b demoactions.Bundle, n string) action.Definition {
	t.Helper()
	for _, a := range b.Actions {
		if a.Name == n {
			return a
		}
	}
	t.Fatal("missing action", n)
	return action.Definition{}
}
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range demoactions.Tables {
		rows, e := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if e != nil {
			t.Fatal(e)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if e = rows.Scan(p...); e != nil {
				t.Fatal(e)
			}
			b, _ := json.Marshal(v)
			out.WriteString(table)
			out.Write(b)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
	}
	return out.String()
}
func invariants(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"SELECT COUNT(*) FROM pragma_foreign_key_check",
		"SELECT COUNT(*) FROM daily_balances WHERE closing_balance<>opening_balance+transfer_in-transfer_out-buy_amount+sell_amount-fees OR closing_balance<0 OR available_balance<>closing_balance OR withdrawable_balance<>MAX(0,closing_balance-sell_amount)",
		"SELECT COUNT(*) FROM positions WHERE quantity<>opening_quantity+buy_quantity-sell_quantity OR available_quantity<>quantity-buy_quantity OR market_value_cents<>quantity*close_price",
		"SELECT COUNT(*) FROM trades WHERE amount_cents<>price*quantity OR fee_cents<>commission_cents+stamp_tax_cents+transfer_fee_cents",
		"SELECT COUNT(*) FROM orders o WHERE filled_quantity<>COALESCE((SELECT SUM(quantity) FROM trades WHERE order_id=o.id),0)",
		"SELECT COUNT(*) FROM daily_quotes WHERE low>open OR low>close OR high<open OR high<close",
		"SELECT COUNT(*) FROM positions p JOIN daily_quotes q ON q.security_code=p.security_code AND q.trade_date=p.trade_date WHERE p.close_price<>q.close",
		"SELECT COUNT(*) FROM daily_balances b WHERE transfer_in<>COALESCE((SELECT SUM(amount_cents) FROM cash_flows f WHERE f.fund_account_no=b.fund_account_no AND f.trade_date=b.trade_date AND f.flow_type='DEPOSIT'),0) OR transfer_out<>COALESCE((SELECT SUM(-amount_cents) FROM cash_flows f WHERE f.fund_account_no=b.fund_account_no AND f.trade_date=b.trade_date AND f.flow_type='WITHDRAWAL'),0)",
	} {
		var n int
		if e := db.QueryRow(q).Scan(&n); e != nil {
			t.Fatal(q, e)
		}
		if n != 0 {
			t.Fatalf("invariant failed (%d rows): %s", n, q)
		}
	}
}
func TestEverySecuritiesActionAndRejectionCase(t *testing.T) {
	bundle, e := demoactions.Load()
	if e != nil {
		t.Fatal(e)
	}
	covered := map[string]bool{}
	for _, c := range examples(t) {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			_, db, st, rw := fixture(t)
			invariants(t, db)
			runner := action.Runner{Libraries: bundle.ActionLibraries, Store: st, Rewriter: rw, Bindings: map[string]any{"userId": c.UserID, "asOf": "2026-10-20T00:00:00Z"}, UserID: c.UserID, Now: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
			for _, p := range c.Prepare {
				if _, e = runner.Run(context.Background(), def(t, bundle, p.Name), p.Arguments, false, nil); e != nil {
					t.Fatalf("prepare %s: %v", p.Name, e)
				}
			}
			before := snapshot(t, db)
			d := def(t, bundle, c.Name)
			preview, err := runner.Run(context.Background(), d, c.Arguments, true, nil)
			if after := snapshot(t, db); after != before {
				t.Fatal("preview or denial modified data")
			}
			if c.Status == "denied" {
				if err == nil {
					t.Fatal("expected denial", preview)
				}
				invariants(t, db)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Writes) != c.Writes {
				t.Fatalf("writes: got %d want %d", len(preview.Writes), c.Writes)
			}
			done, err := runner.Run(context.Background(), d, c.Arguments, false, preview)
			if err != nil {
				t.Fatal(err)
			}
			if !demoactions.EqualJSON(done, preview) {
				t.Fatal("confirmation changed plan")
			}
			if snapshot(t, db) == before {
				t.Fatal("successful action did not change data")
			}
			invariants(t, db)
			covered[c.Name] = true
		})
	}
	for _, a := range bundle.Actions {
		if !covered[a.Name] {
			t.Error("missing successful case", a.Name)
		}
	}
}
