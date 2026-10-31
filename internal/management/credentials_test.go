package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDatabaseCredentialsAreSealedAndShareable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "management.sqlite")
	m, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	password := "数据库 p@ss:/?# 空格"
	sealed, err := m.SealDatabaseCredential("source", password)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte(password)) {
		t.Fatal("password was not encrypted")
	}
	if err = m.UpdateRecords(ctx, "databases", "database.create", "source", func([]Record) ([]Record, error) {
		return []Record{{Key: "source", Data: `{"id":"source","connection":{"credential_id":"source"}}`}}, nil
	}, sealed); err != nil {
		t.Fatal(err)
	}
	// A separate server process uses the shared management DB and key file.
	other, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	got, err := other.DatabasePassword(ctx, "source")
	if err != nil || got != password {
		t.Fatalf("shared credential could not be read: %v", err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(path), "databases", "source", "credentials.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions are not private")
	}
	x, err := m.Source(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(x.Path, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if bytes.Contains(b, []byte(password)) {
				t.Fatal("password leaked into YAML")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sealed)
	if err != nil || bytes.Contains(raw, sealed.Ciphertext) || strings.Contains(string(raw), password) {
		t.Fatal("credential was serialized")
	}
	// Moving ciphertext to another source cannot reveal the original password.
	if _, err = m.SealDatabaseCredential("other", "other-password"); err != nil {
		t.Fatal(err)
	}
	if err = m.UpdateRecords(ctx, "databases", "database.create", "other", func(records []Record) ([]Record, error) {
		return append(records, Record{Key: "other", Data: `{"id":"other","connection":{"credential_id":"other"}}`}), nil
	}); err != nil {
		t.Fatal(err)
	}
	// Use the same key to isolate AAD binding from key mismatch.
	originalKey, err := os.ReadFile(filepath.Join(filepath.Dir(x.Path), "credentials.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(path), "databases", "other", "credentials.key"), originalKey, 0600); err != nil {
		t.Fatal(err)
	}
	otherSource, err := m.source(ctx, "other", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = otherSource.withState(ctx, true, func(st *sourceState) error {
		st.Credentials["READ"] = base64.StdEncoding.EncodeToString(sealed.Ciphertext)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = other.DatabasePassword(ctx, "other"); err == nil {
		t.Fatal("ciphertext was not bound to its source")
	}
	broken := append([]byte(nil), sealed.Ciphertext...)
	broken[len(broken)-1] ^= 1
	if err = x.withState(ctx, true, func(st *sourceState) error {
		st.Credentials["READ"] = base64.StdEncoding.EncodeToString(broken)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = other.DatabasePassword(ctx, "source"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if err = os.Rename(filepath.Join(filepath.Dir(path), "databases", "source", "credentials.key"), path+".key-backup"); err != nil {
		t.Fatal(err)
	}
	if _, err = other.SealDatabaseCredential("source", "new-password"); err == nil {
		t.Fatal("missing key was silently replaced")
	}
}

func TestCredentialCreationIsAtomicAndKeyPublicationIsConcurrent(t *testing.T) {
	ctx := context.Background()
	m, err := Open(filepath.Join(t.TempDir(), "management.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.SealDatabaseCredential("source", "test-password")
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := m.SealDatabaseCredential("source", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	err = m.UpdateRecords(ctx, "databases", "database.create", "source", func([]Record) ([]Record, error) { return nil, errors.New("registration failed") }, sealed)
	if err == nil {
		t.Fatal("failed registration succeeded")
	}
	x, err := m.source(ctx, "source", true)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err = x.withState(ctx, false, func(st *sourceState) error { count = len(st.Credentials); return nil }); err != nil || count != 0 {
		t.Fatal("failed registration left an orphan credential")
	}
	// Fail after credential insertion to check transaction rollback too.
	err = m.UpdateRecords(ctx, "databases", "database.create", "source", func([]Record) ([]Record, error) { return []Record{{Key: "source", Data: "invalid JSON"}}, nil }, sealed)
	if err == nil {
		t.Fatal("invalid record committed")
	}
	if err = x.withState(ctx, false, func(st *sourceState) error { count = len(st.Credentials); return nil }); err != nil || count != 0 {
		t.Fatal("credential survived a rolled-back record write")
	}
}

func TestWriteCredentialsRotateWithoutChangingReadCredentials(t *testing.T) {
	ctx := context.Background()
	m, e := Open(filepath.Join(t.TempDir(), "management.sqlite"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	save := func(action string, c DatabaseCredential) error {
		return m.UpdateRecords(ctx, "databases", action, "source", func([]Record) ([]Record, error) {
			return []Record{{Key: "source", Data: `{"id":"source","connection":{"credential_id":"source"}}`}}, nil
		}, c)
	}
	read, e := m.SealDatabaseCredential("source", "read-password")
	if e != nil {
		t.Fatal(e)
	}
	if e = save("database.create", read); e != nil {
		t.Fatal(e)
	}
	key := WriteCredentialID("source")
	for _, password := range []string{"write-password", "rotated-password"} {
		sealed, e := m.SealDatabaseCredential(key, password)
		if e != nil {
			t.Fatal(e)
		}
		if e = save("database.update", sealed); e != nil {
			t.Fatal(e)
		}
		got, e := m.DatabasePassword(ctx, key)
		if e != nil || got != password {
			t.Fatal(got, e)
		}
	}
	got, e := m.DatabasePassword(ctx, "source")
	if e != nil || got != "read-password" {
		t.Fatal("query credential changed", e)
	}
	_, e = m.SealDatabaseCredential("foreign", "foreign-read")
	if e != nil {
		t.Fatal(e)
	}
	// A staged foreign write reference is still rejected by registration.
	foreign, e := m.SealDatabaseCredential("foreign", "foreign-password")
	foreign.DatabaseID = WriteCredentialID("foreign")
	if e != nil {
		t.Fatal(e)
	}
	if e = save("database.update", foreign); e == nil {
		t.Fatal("cross-database write credential accepted")
	}
}
