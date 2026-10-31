// Package service wires the rewriter to the database: resolve the trusted
// user, rewrite the statement, check configured guards, execute with binds.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"datascope-server/internal/audit"
	"datascope-server/internal/management"
	"datascope-server/internal/queryparams"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/store"
)

// QueryService executes AI-generated SQL only after permission rewriting.
type QueryService struct {
	Management    *management.Store
	Audit         *audit.Store
	DatabaseID    string
	PolicyVersion int
	Store         *store.Store
	Rewriter      *rewrite.Rewriter
	// AsOf is the reference time for grant expiry, bound as ${asOf}. Fixed by
	// default so the permission fixtures are reproducible.
	AsOf time.Time
}

// New builds a QueryService. It fails if the rewrite config does not cover
// exactly the database's tables.
func New(ctx context.Context, st *store.Store, cfg *rewrite.Config, asOf time.Time) (*QueryService, error) {
	names, err := st.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	for _, pol := range cfg.Tables {
		if st.Driver() != "sqlite" {
			if pol.CompiledSQL != "" {
				if err := st.ValidatePortable(pol.CompiledSQL, pol.Params); err != nil {
					return nil, fmt.Errorf("表 %s 策略不兼容: %w", pol.Table, err)
				}
			}
			if pol.CompiledGuard != "" {
				if err := st.ValidatePortable(rewrite.GuardQuery(pol.CompiledGuard), pol.GuardParams); err != nil {
					return nil, fmt.Errorf("表 %s 前置检查不兼容: %w", pol.Table, err)
				}
			}
		}
		if len(pol.Columns) == 0 {
			continue
		}
		schema, err := st.Describe(ctx, pol.Table)
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, c := range schema.Columns {
			key := c.Name
			if st.Driver() == "sqlite" {
				key = strings.ToLower(key)
			}
			known[key] = true
		}
		for _, c := range pol.Columns {
			key := c
			if st.Driver() == "sqlite" {
				key = strings.ToLower(key)
			}
			if !known[key] {
				return nil, fmt.Errorf("表 %s 没有字段 %s", pol.Table, c)
			}
		}
	}
	rw, err := rewrite.New(cfg, names)
	if err != nil {
		return nil, err
	}
	return &QueryService{Store: st, Rewriter: rw, AsOf: asOf}, nil
}

// Config returns the rewrite configuration in use.
func (s *QueryService) Config() *rewrite.Config { return s.Rewriter.Config() }

// Bindings returns the trusted values for every template variable.
func (s *QueryService) Bindings(userID int64) map[string]any {
	var asOf any = s.AsOf.UTC().Format("2006-01-02T15:04:05Z")
	if s.Store.Driver() != "sqlite" {
		asOf = s.AsOf.UTC()
	}
	return map[string]any{
		rewrite.VarUserID: userID,
		rewrite.VarAsOf:   asOf,
	}
}

func namedArgs(values map[string]any, names []string) []any {
	args := make([]any, 0, len(names))
	for _, name := range names {
		args = append(args, sql.Named(name, values[name]))
	}
	return args
}

// Rewritten is a rewritten statement plus the bind values for one user.
type Rewritten struct {
	OriginalSQL    string `json:"-"`
	BusinessUserID int64  `json:"-"`
	*rewrite.Result
	Binds map[string]any `json:"binds"`
	args  []any
}

// Rewrite rewrites sql for userID and evaluates the configured deny_unless
// guards of every referenced table. It does not execute the statement.
func (s *QueryService) Rewrite(ctx context.Context, query string, userID int64) (*Rewritten, error) {
	return s.RewriteWithParameters(ctx, query, userID, nil)
}
func (s *QueryService) RewriteWithParameters(ctx context.Context, query string, userID int64, parameters map[string]any) (out *Rewritten, err error) {
	if err := queryparams.Validate(parameters); err != nil {
		return nil, err
	}
	m := audit.From(ctx)
	m.DatabaseID = s.DatabaseID
	m.SubjectID = userID
	m.PolicyVersion = s.PolicyVersion
	ctx = audit.With(ctx, m)
	event, err := s.Audit.Start(ctx, "sql.rewrite", query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if event != nil && out != nil {
			event.RewrittenSQL = out.RewrittenSQL
			event.Binds = out.Binds
		}
		err = audit.Join(err, s.Audit.Finish(event, err))
	}()
	names := []string{}
	for name := range parameters {
		names = append(names, name)
	}
	res, err := s.Rewriter.RewriteWithParameters(query, names)
	if err != nil {
		return nil, err
	}
	if err := s.Store.ValidatePortable(res.RewrittenSQL, res.Params); err != nil {
		return nil, err
	}
	if event != nil {
		event.RewrittenSQL = res.RewrittenSQL
	}
	values := s.Bindings(userID)
	for name, value := range parameters {
		values[name] = value
	}
	for _, g := range res.Guards {
		ok, err := s.Store.QueryBool(ctx, rewrite.GuardQuery(g.SQL), namedArgs(values, g.Params)...)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &rewrite.DeniedError{Reason: "无权查询表 " + g.Table + "（" + g.Message + "）"}
		}
	}
	binds := make(map[string]any, len(res.Params))
	for _, name := range res.Params {
		binds[name] = values[name]
	}
	return &Rewritten{BusinessUserID: userID, OriginalSQL: query, Result: res, Binds: binds, args: namedArgs(values, res.Params)}, nil
}

// Result is a rewritten statement plus its rows.
type Result struct {
	*Rewritten
	*store.QueryResult
}

// Query rewrites sql for userID and runs the rewritten statement.
func (s *QueryService) Query(ctx context.Context, query string, userID int64, maxRows int) (*Result, error) {
	m := audit.From(ctx)
	m.SubjectID = userID
	m.DatabaseID = s.DatabaseID
	m.PolicyVersion = s.PolicyVersion
	ctx = audit.With(ctx, m)
	rewritten, err := s.Rewrite(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Execute(ctx, rewritten, maxRows)
	if err != nil {
		return nil, err
	}
	return &Result{Rewritten: rewritten, QueryResult: rows}, nil
}

// Execute runs an already rewritten statement with its binds.
func (s *QueryService) Execute(ctx context.Context, r *Rewritten, maxRows int) (*store.QueryResult, error) {
	if maxRows <= 0 {
		maxRows = store.DefaultMaxRows
	}
	for _, name := range r.TableNames() {
		if pol, ok := s.Config().Lookup(name); ok && pol.MaxRows > 0 {
			maxRows = min(maxRows, pol.MaxRows)
		}
	}
	m := audit.From(ctx)
	m.DatabaseID = s.DatabaseID
	m.SubjectID = r.BusinessUserID
	m.PolicyVersion = s.PolicyVersion
	ctx = audit.With(ctx, m)
	ctx = audit.WithSQL(ctx, r.OriginalSQL, r.RewrittenSQL)
	return s.Store.Query(ctx, r.RewrittenSQL, maxRows, r.args...)
}

// QueryWithParameters executes direct SELECT with explicit bound values.
func (s *QueryService) QueryWithParameters(ctx context.Context, query string, userID int64, maxRows int, parameters map[string]any) (*Result, error) {
	m := audit.From(ctx)
	m.SubjectID = userID
	m.DatabaseID = s.DatabaseID
	m.PolicyVersion = s.PolicyVersion
	ctx = audit.With(ctx, m)
	r, e := s.RewriteWithParameters(ctx, query, userID, parameters)
	if e != nil {
		return nil, e
	}
	rows, e := s.Execute(ctx, r, maxRows)
	if e != nil {
		return nil, e
	}
	return &Result{Rewritten: r, QueryResult: rows}, nil
}
