// Package adminapi is the backend JSON API for the super-admin console. It
// reads the demo database directly (rows are not filtered by business
// permissions), previews rewrites, and inspects the MCP server as an MCP
// client.
package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"datascope-server/internal/audit"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"datascope-server/internal/rewrite"
	"datascope-server/internal/service"
	"datascope-server/internal/store"
)

// Handler returns the JSON API under /api/ plus GET /healthz.
func Handler(svc *service.QueryService, inspector *Inspector) http.Handler {
	api := apiHandlers{svc: svc, store: svc.Store, inspector: inspector, management: svc.Management}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/tables", api.tables)
	mux.HandleFunc("GET /api/tables/{name}", api.describe)
	mux.HandleFunc("GET /api/tables/{name}/rows", api.rows)
	mux.HandleFunc("GET /api/users", api.users) // compatibility alias
	mux.HandleFunc("GET /api/employees", api.users)
	mux.HandleFunc("GET /api/policies", api.policies)
	mux.HandleFunc("POST /api/query", api.query)
	mux.HandleFunc("POST /api/rewrite", api.rewrite)
	mux.HandleFunc("GET /api/mcp/info", api.mcpInfo)
	mux.HandleFunc("POST /api/mcp/call", api.mcpCall)
	mux.HandleFunc("POST /api/mcp/verify", api.mcpVerify)
	return localhostOnlyHost(http.NewCrossOriginProtection().Handler(mux))
}

// localhostOnlyHost mirrors the MCP SDK's DNS rebinding protection: on a
// loopback listener, reject requests whose Host is not a loopback name.
func localhostOnlyHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && local != nil {
			if isLoopback(local.String()) && !isLoopback(r.Host) {
				http.Error(w, "Forbidden: invalid Host header", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type apiHandlers struct {
	svc        *service.QueryService
	management *management.Store
	store      *store.Store
	inspector  *Inspector
}

// tableInfo is a table plus its rewrite scheme.
type tableInfo struct {
	store.Table
	Scheme rewrite.Scheme `json:"scheme"`
}

func (a apiHandlers) tables(w http.ResponseWriter, r *http.Request) {
	tables, err := a.store.Tables(r.Context())
	out := make([]tableInfo, len(tables))
	for i, t := range tables {
		out[i] = tableInfo{Table: t}
		if pol, ok := a.svc.Config().Lookup(t.Name); ok {
			out[i].Scheme = pol.Scheme
		}
	}
	respond(w, out, err)
}

func (a apiHandlers) describe(w http.ResponseWriter, r *http.Request) {
	schema, err := a.store.Describe(r.Context(), r.PathValue("name"))
	respond(w, schema, err)
}

func (a apiHandlers) rows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	page, err := a.store.Browse(r.Context(), store.BrowseOptions{
		Table:        r.PathValue("name"),
		Limit:        limit,
		Offset:       offset,
		OrderBy:      q.Get("order"),
		Desc:         q.Get("desc") == "1",
		Search:       q.Get("q"),
		FilterColumn: q.Get("col"),
		FilterValue:  q.Get("val"),
	})
	respond(w, page, err)
}

func (a apiHandlers) users(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.Users(r.Context())
	respond(w, users, err)
}

type policyView struct {
	*rewrite.Policy
	Comment string `json:"comment"`
}

// policies returns the rewrite configuration as loaded and compiled.
func (a apiHandlers) policies(w http.ResponseWriter, r *http.Request) {
	tables, err := a.store.Tables(r.Context())
	if err != nil {
		respond(w, nil, err)
		return
	}
	comments := map[string]string{}
	for _, t := range tables {
		comments[t.Name] = t.Comment
	}
	cfg := a.svc.Config()
	views := make([]policyView, len(cfg.Tables))
	for i, pol := range cfg.Tables {
		views[i] = policyView{Policy: pol, Comment: comments[pol.Table]}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      cfg.Path,
		"schemes":   cfg.Schemes,
		"variables": rewrite.Variables,
		"policies":  views,
		"as_of":     a.svc.AsOf,
	})
}

func (a apiHandlers) query(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parameters map[string]any `json:"parameters,omitempty"`
		SQL        string         `json:"sql"`
		MaxRows    int            `json:"max_rows"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := a.store.RawQueryWithParameters(r.Context(), in.SQL, in.MaxRows, in.Parameters)
	respond(w, result, err)
}

// rewrite previews the rewrite for a user and, unless rejected, executes it.
func (a apiHandlers) rewrite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parameters  map[string]any `json:"parameters,omitempty"`
		SQL         string         `json:"sql"`
		UserID      int64          `json:"user_id"`
		MaxRows     int            `json:"max_rows"`
		PreviewOnly bool           `json:"preview_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	if a.management != nil {
		u, ok := guard.User(ctx)
		if !ok {
			platformError(w, management.ErrDenied)
			return
		}
		bid, e := a.management.BusinessID(ctx, u.ID, a.svc.DatabaseID)
		if e != nil && !a.management.Can(ctx, u.ID, a.svc.DatabaseID, "impersonate") {
			platformError(w, e)
			return
		}
		if in.UserID != 0 && in.UserID != bid {
			if e := guard.Require(ctx, a.management, a.svc.DatabaseID, "impersonate"); e != nil {
				platformError(w, e)
				return
			}
		} else {
			in.UserID = bid
		}
		if in.UserID <= 0 {
			platformError(w, fmt.Errorf("请选择或绑定业务身份"))
			return
		}
	}
	meta := audit.From(ctx)
	meta.SubjectID = in.UserID
	ctx = audit.With(ctx, meta)
	res, err := a.svc.RewriteWithParameters(ctx, in.SQL, in.UserID, in.Parameters)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "denied": rewrite.IsDenied(err)})
		return
	}
	if in.PreviewOnly {
		writeJSON(w, http.StatusOK, res)
		return
	}
	rows, err := a.svc.Execute(ctx, res, in.MaxRows)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "rewrite": res})
		return
	}
	writeJSON(w, http.StatusOK, service.Result{Rewritten: res, QueryResult: rows})
}

func (a apiHandlers) mcpInfo(w http.ResponseWriter, r *http.Request) {
	info, err := a.inspector.Info(r.Context())
	respondUpstream(w, info, err)
}

func (a apiHandlers) mcpCall(w http.ResponseWriter, r *http.Request) {
	if a.management != nil && a.inspector.AuthMode == "none" {
		platformError(w, fmt.Errorf("none 模式固定使用本地默认身份，请使用 OAuth 或可信网关模式进行控制台 MCP 测试"))
		return
	}
	var in struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		UserID    int64          `json:"user_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if a.management != nil {
		u, ok := guard.User(r.Context())
		if !ok {
			platformError(w, management.ErrDenied)
			return
		}
		if in.UserID != 0 && in.UserID != u.ID {
			platformError(w, fmt.Errorf("MCP 测试使用当前平台用户，请勿传入业务身份"))
			return
		}
		in.UserID = u.ID
	}
	result, err := a.inspector.Call(r.Context(), in.UserID, in.Name, in.Arguments)
	respondUpstream(w, result, err)
}

func (a apiHandlers) mcpVerify(w http.ResponseWriter, r *http.Request) {
	report, err := a.inspector.Verify(r.Context())
	respondUpstream(w, report, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return false
	}
	return true
}

// respondUpstream reports MCP server failures as 502 so the console can tell
// "MCP server unreachable" apart from a bad request.
func respondUpstream(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.Is(err, store.ErrTableNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
