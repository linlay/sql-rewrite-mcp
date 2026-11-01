package adminapi

import (
	"datascope-server/deploy/connectors"
	"datascope-server/internal/application"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"encoding/json"
	"errors"
	"golang.org/x/time/rate"
	"net/http"
	"strings"
	"time"
)

// PlatformHandler protects every legacy and new API. There is no unauthenticated API fallback.
func PlatformHandler(app *application.Service, next http.Handler) http.Handler {
	m := app.Management
	limiter := rate.NewLimiter(2, 10)
	external := newExternalAuth(app)
	confirmation := ActionConfirmationHandler(app)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if external.handle(w, r) {
			return
		}
		if r.URL.Path == "/api/action-oauth/callback" && r.Method == "GET" {
			businessOAuthCallback(app, w, r)
			return
		}
		if r.URL.Path == "/actions/confirm" {
			confirmation.ServeHTTP(w, external.withIdentity(r))
			return
		}
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		ctx := audit.With(r.Context(), audit.Metadata{RequestID: audit.From(r.Context()).RequestID, Source: "web", Tool: r.URL.Path})
		r = r.WithContext(ctx)
		if r.URL.Path == "/api/auth/login" && r.Method == "POST" {
			if !limiter.Allow() {
				writeJSON(w, 429, map[string]string{"error": "登录过于频繁，请稍后重试"})
				return
			}
			var in struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if !decode(w, r, &in) {
				return
			}
			ev, e := m.Audit.Start(ctx, "auth.login", "")
			if e != nil {
				platformError(w, e)
				return
			}
			policy, e := m.ResolveAuth(ctx, "API", "platform")
			var u management.User
			if e == nil {
				if policy.Provider.Type == "PLATFORM_ACCOUNT" {
					u, e = m.Authenticate(ctx, in.Username, in.Password)
				} else {
					var a management.BusinessAccount
					a, e = m.VerifyAccount(ctx, policy, "", in.Username, in.Password)
					if e == nil {
						u, e = m.PlatformIdentity(ctx, policy, a)
					}
				}
			}
			if ev != nil {
				ev.ActorID = u.ID
			}
			if done := m.Audit.Finish(ev, e); done != nil {
				platformError(w, done)
				return
			}
			if e != nil {
				writeJSON(w, 401, map[string]string{"error": e.Error()})
				return
			}
			token, e := m.NewSession(ctx, u, "web", 12*time.Hour)
			if e != nil {
				platformError(w, e)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "platform_session", Value: token, Path: "/", HttpOnly: true, Secure: app.SecureCookies || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
			writeJSON(w, 200, u)
			return
		}
		token := ""
		if c, e := r.Cookie("platform_session"); e == nil {
			token = c.Value
		}
		u, e := m.Session(ctx, token, "web")
		if e != nil {
			writeJSON(w, 401, map[string]string{"error": "请先登录"})
			return
		}
		meta := audit.From(ctx)
		meta.ActorID = u.ID
		audit.UpdateRequest(ctx, "", u.ID)
		ctx = guard.WithUser(audit.With(ctx, meta), u)
		r = r.WithContext(ctx)
		if r.URL.Path == "/api/auth/me" && r.Method == "GET" {
			writeJSON(w, 200, u)
			return
		}
		if r.URL.Path == "/api/auth/logout" && r.Method == "POST" {
			e := m.Logout(ctx, token)
			if e != nil {
				platformError(w, e)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "platform_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if r.URL.Path == "/api/connectors/datascope-admin.zip" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			archive, err := connectors.AdminArchive(app.ManagementMCPURL)
			if err != nil {
				platformError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="datascope-admin.zip"`)
			w.Write(archive)
			return
		}
		if r.URL.Path == "/api/management/call" && r.Method == "POST" {
			var in struct {
				Action    string          `json:"action"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "管理参数无效: " + err.Error()})
				return
			}
			arguments, e := application.DecodeInput(in.Arguments)
			if e != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "管理参数无效: " + e.Error()})
				return
			}
			out, e := app.Call(ctx, in.Action, arguments)
			if e != nil {
				platformError(w, e)
				return
			}
			writeJSON(w, 200, out)
			return
		}
		if r.URL.Path == "/api/databases" {
			action := "list_databases"
			in := application.Input{}
			if r.Method == "POST" {
				action = "create_database"
				var create struct {
					catalog.Database
					Password string `json:"password"`
				}
				if !decode(w, r, &create) {
					return
				}
				in.Database = &create.Database
				in.Password = create.Password
			} else if r.Method != "GET" {
				http.NotFound(w, r)
				return
			}
			out, e := app.Call(ctx, action, in)
			if e != nil {
				platformError(w, e)
				return
			}
			writeJSON(w, 200, out)
			return
		}
		id := "default"
		rest := strings.TrimPrefix(r.URL.Path, "/api/")
		if strings.HasPrefix(rest, "databases/") {
			parts := strings.SplitN(strings.TrimPrefix(rest, "databases/"), "/", 2)
			id = parts[0]
			rest = ""
			if len(parts) > 1 {
				rest = parts[1]
			}
		}
		meta = audit.From(ctx)
		meta.DatabaseID = id
		audit.UpdateRequest(ctx, id, 0)
		ctx = audit.With(ctx, meta)
		r = r.WithContext(ctx)
		if rest == "" {
			in := application.Input{DatabaseID: id}
			action := "get_database"
			if r.Method == "PUT" {
				in.Database = newDatabase()
				if !decode(w, r, in.Database) {
					return
				}
				in.Database.ID = id
				action = "save_database"
			} else if r.Method != "GET" {
				http.NotFound(w, r)
				return
			}
			out, e := app.Call(ctx, action, in)
			if e != nil {
				platformError(w, e)
				return
			}
			writeJSON(w, 200, out)
			return
		}
		if rest == "business-mcp" {
			businessWorkbench(app, w, r, id)
			return
		}
		permission := "config.read"
		switch {
		case rest == "query" || strings.HasSuffix(rest, "/rows"):
			permission = "raw_query"
		case rest == "rewrite":
			permission = "query"
		case rest == "mcp/call":
			permission = "query"
		case rest == "mcp/verify":
			permission = "impersonate"
		case rest == "employees" || rest == "users":
			permission = "query"
		}
		if e := guard.Require(ctx, m, id, permission); e != nil {
			original := ""
			if r.Method == "POST" && (rest == "query" || rest == "rewrite") {
				var in struct {
					SQL string `json:"sql"`
				}
				_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
				original = in.SQL
			}
			ev, ae := m.Audit.Start(ctx, "access.denied", original)
			if ae == nil {
				ae = m.Audit.Finish(ev, e)
			}
			platformError(w, audit.Join(e, ae))
			return
		}
		// Production verification relies on demo-specific direct reads and is not a public API.
		if rest == "mcp/verify" {
			writeJSON(w, 403, map[string]string{"error": "请通过独立测试运行演示验收"})
			return
		}
		if rest == "employees" || rest == "users" {
			d, e := app.Catalog.Get(id)
			if e != nil {
				platformError(w, e)
				return
			}
			if e = d.ValidateAuthentication(); e != nil {
				platformError(w, e)
				return
			}
			if e = guard.Require(ctx, m, id, "impersonate"); e != nil {
				platformError(w, e)
				return
			}
			accounts, e := m.BusinessAccounts(ctx, id)
			if e != nil {
				platformError(w, e)
				return
			}
			out := []map[string]any{}
			for _, a := range accounts {
				if _, e := app.Catalog.BusinessAccount(ctx, d, a.Username); e == nil {
					out = append(out, map[string]any{"id": a.BusinessUserID, "emp_no": a.Username, "name": a.Name, "status": "active", "role_code": "", "roles": "业务账号"})
				}
			}
			writeJSON(w, 200, out)
			return
		}
		next.ServeHTTP(w, r)
	})
	return localhostOnlyHost(http.NewCrossOriginProtection().Handler(audit.HTTPHandler(m.Audit, "web", h)))
}
func platformError(w http.ResponseWriter, e error) {
	code := 400
	if errors.Is(e, management.ErrDenied) {
		code = 403
	}
	writeJSON(w, code, map[string]string{"error": e.Error()})
}
func newDatabase() *catalog.Database { return &catalog.Database{} }
