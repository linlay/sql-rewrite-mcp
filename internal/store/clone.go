package store

import (
	"context"
	"database/sql"
	"strings"

	parser "github.com/rqlite/sql"
)

// CheckCloneSQL checks SQLite's resolvable table/column references with EXPLAIN,
// never executing the statement (including DML). Other engines retain their
// portable compiler checks; catalog also inspects known static dependencies.
func (s *Store) CheckCloneSQL(ctx context.Context, query string) error {
	stmt, err := parser.NewParser(strings.NewReader(query)).ParseStatement()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	_, err = parser.Walk(parser.VisitFunc(func(n parser.Node) (parser.Node, error) {
		if b, ok := n.(*parser.BindExpr); ok {
			names[strings.TrimLeft(b.Name, ":@$?")] = true
		}
		return n, nil
	}), stmt)
	if err != nil {
		return err
	}
	args := []any{}
	for name := range names {
		args = append(args, sql.Named(name, nil))
	}
	if s.Driver() != "sqlite" {
		if _, selectSQL := stmt.(*parser.SelectStatement); selectSQL {
			_, _, err = s.Prepare(query, args...)
		} else {
			_, _, err = s.engine.CompileWrite(query, args...)
		}
		return err
	}
	rows, err := s.db.QueryContext(ctx, "EXPLAIN "+query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return rows.Err()
}
