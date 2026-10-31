package platform_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"datascope-server/internal/management"
)

func TestActionConfirmationSharesMemoryAcrossServices(t *testing.T) {
	f, business := setupAction(t, true)
	dir, err := os.MkdirTemp("/tmp", "ds-confirm-")
	must(t, err)
	defer os.RemoveAll(dir)
	closeOwner, err := management.ServeActionMemory(context.Background(), filepath.Join(dir, "actions.sock"), f.m.Actions)
	must(t, err)
	defer closeOwner()
	apiStore, err := management.Open(f.m.Path, f.a)
	must(t, err)
	defer apiStore.Close()
	apiStore.Actions = management.RemoteActionMemory(filepath.Join(dir, "actions.sock"))
	apiApp := *f.app
	apiApp.Management = apiStore
	args := map[string]any{"id": 1, "name": "confirmed over private IPC"}
	preview, err := f.app.InvokeAction(business, "default", "rename_item", "ipc-confirm", args)
	must(t, err)
	if preview.Status != "confirm_required" {
		t.Fatal(preview)
	}
	info, username, err := apiApp.ConfirmationInfo(context.Background(), preview.ConfirmationToken)
	must(t, err)
	if info.Status != "confirm_required" || username != "E1" {
		t.Fatal(info, username)
	}
	if _, err = apiApp.ConfirmAction(context.Background(), preview.ConfirmationToken, "incorrect"); err == nil {
		t.Fatal("IPC bypassed business authentication")
	}
	confirmed, err := apiApp.ConfirmAction(context.Background(), preview.ConfirmationToken, "business-password")
	must(t, err)
	if confirmed.Status != "done" {
		t.Fatal(confirmed)
	}
	again, err := f.app.InvokeAction(business, "default", "rename_item", "ipc-confirm", args)
	must(t, err)
	if again.Status != "done" {
		t.Fatal("MCP did not observe API confirmation", again)
	}
	assertNoBusinessSQLite(t, f, "default")
}
