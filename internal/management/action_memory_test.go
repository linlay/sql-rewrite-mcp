package management

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryActionsConcurrentReservationAndIsolation(t *testing.T) {
	state := NewMemoryActions()
	ctx := context.Background()
	r := ActionRequest{DatabaseID: "one", Actor: "business:1", RequestID: "same", State: "running", Fingerprint: "fingerprint"}
	var fresh atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, isNew, err := state.Reserve(ctx, r)
			if err != nil {
				t.Error(err)
				return
			}
			if got != r {
				t.Error(got)
			}
			if isNew {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("multiple reservations", fresh.Load())
	}
	for _, key := range []ActionRequest{{DatabaseID: "two", Actor: r.Actor, RequestID: r.RequestID, State: "running"}, {DatabaseID: r.DatabaseID, Actor: "business:2", RequestID: r.RequestID, State: "running"}} {
		if _, new, err := state.Reserve(ctx, key); err != nil || !new {
			t.Fatal("isolation failure", err)
		}
	}
	token := strings.Repeat("a", 64)
	if err := state.Transition(ctx, r, "running", "pending", "preview", token, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	got, err := state.Token(ctx, token)
	if err != nil || got.Data != "preview" {
		t.Fatal(got, err)
	}
	if err = state.Transition(ctx, r, "pending", "running", "preview", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = state.Token(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("consumed token retained", err)
	}
	if err = state.Transition(ctx, r, "pending", "running", "preview", "", 0); err == nil {
		t.Fatal("duplicate transition accepted")
	}
	if _, new, err := NewMemoryActions().Reserve(ctx, r); err != nil || !new {
		t.Fatal("state survived new runtime", err)
	}
}

func TestActionMemorySocketPreservesConfirmationState(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ds-action-memory-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "action.sock")
	ctx := context.Background()
	owner := NewMemoryActions()
	closeServer, err := ServeActionMemory(ctx, path, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	remote := RemoteActionMemory(path)
	r := ActionRequest{DatabaseID: "default", Actor: "business:7", RequestID: "request", State: "running", Data: "arguments"}
	if _, new, err := remote.Reserve(ctx, r); err != nil || !new {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Minute).Unix()
	token := strings.Repeat("b", 64)
	if err = remote.Transition(ctx, r, "running", "pending", "preview", token, expires); err != nil {
		t.Fatal(err)
	}
	got, err := remote.Token(ctx, token)
	if err != nil || got.Token != token || got.Expires != expires || got.Data != "preview" {
		t.Fatalf("lost wire fields: %+v %v", got, err)
	}
	if err = owner.Transition(ctx, r, "pending", "done", "result", "", 0); err != nil {
		t.Fatal(err)
	}
	lookup, err := remote.Lookup(ctx, r)
	if err != nil || lookup.State != "done" || lookup.Data != "result" {
		t.Fatal(lookup, err)
	}
	missing := r
	missing.RequestID = "missing"
	if _, err := remote.Lookup(ctx, missing); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("lookup created missing receipt", err)
	}
	got, new, err := remote.Reserve(ctx, r)
	if err != nil || new || got.State != "done" || got.Data != "result" {
		t.Fatal(got, new, err)
	}
	if _, err = remote.Token(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket permissions", err)
	}
	closeServer()
	if _, _, err = remote.Reserve(ctx, r); err == nil {
		t.Fatal("unavailable owner silently created independent state")
	}
	closeNew, err := ServeActionMemory(ctx, path, NewMemoryActions())
	if err != nil {
		t.Fatal(err)
	}
	defer closeNew()
	if _, new, err = RemoteActionMemory(path).Reserve(ctx, r); err != nil || !new {
		t.Fatal("owner restart reused state", err)
	}
}
