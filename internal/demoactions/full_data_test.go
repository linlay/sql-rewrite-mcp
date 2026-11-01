package demoactions_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/demoactions"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
)

// This opt-in test always copies the supplied database before opening a writer.
// Cases requiring committed preparation are covered by the small isolated suite.
func TestFullSecuritiesDemoPreviews(t *testing.T) {
	source := os.Getenv("DATASCOPE_DEMO_ACTIONS_TEST_DB")
	if source == "" {
		t.Skip("set DATASCOPE_DEMO_ACTIONS_TEST_DB to verify a full demo snapshot")
	}
	if _, e := os.Stat(source + "-wal"); e == nil {
		t.Fatal("provide a consistent SQLite snapshot without a WAL")
	}
	src, e := os.Open(source)
	if e != nil {
		t.Fatal(e)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "full-securities.sqlite")
	dst, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(dst, src); e != nil {
		t.Fatal(e)
	}
	if e = dst.Close(); e != nil {
		t.Fatal(e)
	}
	digest := func() string {
		f, e := os.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			t.Fatal(e)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	before := digest()
	st, e := store.OpenWriter(context.Background(), path, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
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
	bundle, e := demoactions.Load()
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range examples(t) {
		if len(c.Prepare) > 0 {
			continue
		}
		c := c
		t.Run(c.ID, func(t *testing.T) {
			runner := action.Runner{Libraries: bundle.ActionLibraries, Store: st, Rewriter: rw, UserID: c.UserID, Bindings: map[string]any{"userId": c.UserID, "asOf": store.DefaultAsOf.UTC().Format(time.RFC3339)}, Now: time.Now().UTC()}
			out, e := runner.Run(context.Background(), def(t, bundle, c.Name), c.Arguments, true, nil)
			if c.Status == "denied" {
				if e == nil {
					t.Fatal("expected denial")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(out.Writes) != c.Writes {
				t.Fatal("unexpected write count", len(out.Writes), c.Writes)
			}
		})
	}
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	if after := digest(); after != before {
		t.Fatal("full database snapshot changed after previews")
	}
}
