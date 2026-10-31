package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneSQLChecksNeverExecuteBusinessStatements(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"mysql", "postgresql", "oracle", "dameng"} {
		t.Run(name, func(t *testing.T) {
			conn := &probeConn{}
			st := probeStore(name, conn)
			defer st.Close()
			st.engine.Tables = map[string]string{"records": "records"}
			for _, query := range []string{"SELECT id FROM main.records WHERE id=:arg_action0", "INSERT INTO records(id) VALUES(:arg_action0)", "UPDATE records SET id=:arg_action0 WHERE id=:arg_action1", "DELETE FROM records WHERE id=:arg_action0"} {
				if err := st.CheckCloneSQL(ctx, query); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
			}
			if conn.query != "" || conn.setup != "" {
				t.Fatal("static validation executed SQL")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "business.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE records(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO records VALUES(1,'unchanged')"); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.CheckCloneSQL(ctx, "UPDATE records SET value='changed' WHERE id=:arg_action0"); err != nil {
		t.Fatal(err)
	}
	if err = st.CheckCloneSQL(ctx, "SELECT missing FROM records"); err == nil {
		t.Fatal("missing column accepted")
	}
	var value string
	if err = db.QueryRow("SELECT value FROM records WHERE id=1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "unchanged" {
		t.Fatal("EXPLAIN executed update")
	}
}
