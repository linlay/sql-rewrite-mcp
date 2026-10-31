package management

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPasswordMinimumLength(t *testing.T) {
	ctx := context.Background()
	m, err := Open(filepath.Join(t.TempDir(), "management.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.Bootstrap(ctx, "admin", "12345"); err == nil {
		t.Fatal("bootstrap accepted five characters")
	}
	if err = m.Bootstrap(ctx, "admin", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateUser(ctx, "reader", "Reader", "12345"); err == nil {
		t.Fatal("user creation accepted five characters")
	}
	u, err := m.CreateUser(ctx, "reader", "Reader", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Authenticate(ctx, u.Username, "123456"); err != nil {
		t.Fatal(err)
	}
	if err = m.ResetPassword(ctx, u.ID, "abcde"); err == nil {
		t.Fatal("password reset accepted five characters")
	}
	if _, err = m.Authenticate(ctx, u.Username, "123456"); err != nil {
		t.Fatal("rejected reset changed the password:", err)
	}
	if err = m.ResetPassword(ctx, u.ID, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Authenticate(ctx, u.Username, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if err = m.UpdateRecords(ctx, "databases", "database.create", "test", func([]Record) ([]Record, error) { return []Record{{"test", `{"id":"test","revision":1}`}}, nil }); err != nil {
		t.Fatal(err)
	}
	a := BusinessAccount{DatabaseID: "test", Username: "reader", Name: "Reader", BusinessUserID: 1, Active: true, Permissions: []string{"query"}}
	if _, err = m.SaveBusinessAccount(ctx, a, "12345", 0); err == nil {
		t.Fatal("business account creation accepted five characters")
	}
	a, err = m.SaveBusinessAccount(ctx, a, "123456", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SaveBusinessAccount(ctx, a, "abcde", a.AuthVersion); err == nil {
		t.Fatal("business password reset accepted five characters")
	}
	a, err = m.SaveBusinessAccount(ctx, a, "abcdef", a.AuthVersion)
	if err != nil {
		t.Fatal(err)
	}
	// Editing an existing business account can still leave its password unchanged.
	if _, err = m.SaveBusinessAccount(ctx, a, "", a.AuthVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AuthenticateBusiness(ctx, a.DatabaseID, a.Username, "abcdef"); err != nil {
		t.Fatal(err)
	}
}
