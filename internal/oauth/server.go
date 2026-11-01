// Package oauth implements demo OAuth and verification of demo MCP tokens.
package oauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/store"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/time/rate"
	"html/template"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var b64 = base64.RawURLEncoding

func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return b64.EncodeToString(b)
}
func hash(s string) string {
	v := sha256.Sum256([]byte(s))
	return b64.EncodeToString(v[:])
}

type Config struct {
	Catalog                                        *catalog.Catalog
	AccountDatabase                                string
	Management                                     *management.Store
	DatabaseForResource                            func(string) (string, error)
	Issuer, Resource, KeyPath, StatePath, Password string
	DebugLogin                                     bool
	ResourceAllowed                                func(string) bool
}
type Server struct {
	cfg                       Config
	key                       *rsa.PrivateKey
	db                        *sql.DB
	store                     *store.Store
	loginLimit, registerLimit *rate.Limiter
}
type grant struct {
	Realm                                                         string
	AuthConfig                                                    string
	Version                                                       int64
	Client, Redirect, Resource, Challenge, Subject, Scope, Family string
	Expires                                                       int64
	Used                                                          bool
}
type client struct {
	ID        string   `json:"client_id"`
	Redirects []string `json:"redirect_uris"`
	Name      string   `json:"client_name"`
	Method    string   `json:"token_endpoint_auth_method"`
}

func ValidURL(raw string, callback bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || strings.ContainsAny(u.Host, ";,\"' \t\r\n") || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "[::1]" || u.Hostname() == "::1")
}
func New(cfg Config, st *store.Store) (*Server, error) {
	if !ValidURL(cfg.Issuer, false) || !ValidURL(cfg.Resource, false) {
		return nil, errors.New("OAuth issuer/resource must be HTTPS or loopback HTTP")
	}
	if cfg.Management == nil && len(cfg.Password) < 6 {
		return nil, errors.New("OAUTH_DEMO_PASSWORD must contain at least 6 characters")
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	key, e := loadKey(cfg.KeyPath)
	if e != nil {
		return nil, e
	}

	db, e := sql.Open("sqlite", fmt.Sprintf("file:oauth-%s?mode=memory&cache=shared", random()))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec("CREATE TABLE IF NOT EXISTS OAUTH_CLIENT(ID_ TEXT PRIMARY KEY,DATA_ TEXT NOT NULL);CREATE TABLE IF NOT EXISTS OAUTH_GRANT(ID_ TEXT PRIMARY KEY,KIND_ TEXT NOT NULL,DATA_ TEXT NOT NULL);")
	if e != nil {
		db.Close()
		return nil, e
	}
	if cfg.Management != nil {
		rows, err := cfg.Management.DB.Query("SELECT ID_,DATA_ FROM OAUTH_CLIENT")
		if err == nil {
			for rows.Next() {
				var id, raw string
				rows.Scan(&id, &raw)
				db.Exec("INSERT OR REPLACE INTO OAUTH_CLIENT VALUES(?,?)", id, raw)
			}
			rows.Close()
		}
	}
	return &Server{cfg: cfg, key: key, db: db, store: st, loginLimit: rate.NewLimiter(2, 10), registerLimit: rate.NewLimiter(1, 10)}, nil
}
func (s *Server) Close() error {
	return s.db.Close()
}
func loadKey(path string) (*rsa.PrivateKey, error) {
	raw, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			return nil, e
		}
		raw = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(raw)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		return key, nil
	}
	if e != nil {
		return nil, e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid RSA PEM")
	}
	key, e := x509.ParsePKCS1PrivateKey(block.Bytes)
	if e != nil {
		return nil, e
	}
	return key, key.Validate()
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func oauthError(w http.ResponseWriter, code string) {
	jsonOut(w, 400, map[string]string{"error": code})
}
func (s *Server) Handler(next http.Handler) http.Handler {
	m := http.NewServeMux()
	if s.cfg.AccountDatabase == "" && s.cfg.Catalog != nil {
		m.HandleFunc("POST /auth/databases/{database}/token", s.accountToken)
		m.HandleFunc("/auth/databases/{database}/", s.realmHandler)
		m.HandleFunc("GET /.well-known/oauth-authorization-server/auth/databases/{database}", s.realmMetadata)
	}
	m.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	m.HandleFunc("GET /oauth/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, s.JWKS())
	})
	m.HandleFunc("POST /oauth/register", s.register)
	m.HandleFunc("GET /oauth/authorize", s.authorize)
	m.HandleFunc("POST /oauth/authorize", s.login)
	m.HandleFunc("POST /oauth/token", s.token)
	m.Handle("/", next)
	return m
}
func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	i := s.cfg.Issuer
	scopes := []string{"sql:query", "sql:debug"}
	if s.cfg.AccountDatabase != "" {
		scopes = []string{"sql:query"}
	}
	jsonOut(w, 200, map[string]any{"issuer": i, "authorization_endpoint": i + "/oauth/authorize", "token_endpoint": i + "/oauth/token", "registration_endpoint": i + "/oauth/register", "jwks_uri": i + "/oauth/jwks.json", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_methods_supported": []string{"none"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": scopes, "authorization_response_iss_parameter_supported": true})
}
func (s *Server) JWKS() any {
	return map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "demo-rsa-1", "n": b64.EncodeToString(s.key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(s.key.E)).Bytes())}}}
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.registerLimit.Allow() {
		http.Error(w, "rate limited", 429)
		return
	}
	var c client
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	if d.Decode(&c) != nil || len(c.Redirects) == 0 || len(c.Redirects) > 10 || (c.Method != "" && c.Method != "none") {
		oauthError(w, "invalid_client_metadata")
		return
	}
	for _, u := range c.Redirects {
		if !ValidURL(u, true) {
			oauthError(w, "invalid_redirect_uri")
			return
		}
	}
	c.ID = random()
	c.Method = "none"
	raw, _ := json.Marshal(c)
	if _, e := s.db.Exec("INSERT INTO OAUTH_CLIENT VALUES (?,?)", c.ID, string(raw)); e != nil {
		http.Error(w, "registration unavailable", 500)
		return
	}
	if s.cfg.Management != nil {
		if _, e := s.cfg.Management.DB.ExecContext(r.Context(), "INSERT INTO OAUTH_CLIENT VALUES(?,?)", c.ID, string(raw)); e != nil {
			http.Error(w, "registration unavailable", 500)
			return
		}
	}
	jsonOut(w, 201, c)
}
func (s *Server) getClient(id string) (client, error) {
	var c client
	var raw string
	e := s.db.QueryRow("SELECT DATA_ FROM OAUTH_CLIENT WHERE ID_=?", id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &c)
	}
	return c, e
}
func (s *Server) putGrant(id, kind string, g grant) error {
	if _, e := s.db.Exec("DELETE FROM OAUTH_GRANT WHERE CAST(json_extract(DATA_,'$.Expires') AS INTEGER) < ?", time.Now().Unix()); e != nil {
		return e
	}
	raw, _ := json.Marshal(g)
	_, e := s.db.Exec("INSERT INTO OAUTH_GRANT VALUES (?,?,?)", hash(id), kind, string(raw))
	return e
}

var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (s *Server) allowsResource(resource string) bool {
	if !ValidURL(resource, false) {
		return false
	}
	if s.cfg.ResourceAllowed != nil {
		if !s.cfg.ResourceAllowed(resource) {
			return false
		}
		if s.cfg.AccountDatabase != "" {
			d, e := s.localDatabase()
			return e == nil && d.MCPEnabled && d.LocalAuthentication() && s.databaseForResource(resource) == d.ID
		}
		return true
	}
	return resource == s.cfg.Resource
}
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, e := s.getClient(q.Get("client_id"))
	if e != nil || !slices.Contains(c.Redirects, q.Get("redirect_uri")) {
		oauthError(w, "invalid_request")
		return
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = "sql:query"
	}
	for _, v := range strings.Fields(scope) {
		if v != "sql:query" && v != "sql:debug" {
			oauthError(w, "invalid_scope")
			return
		}
	}
	if len(q["resource"]) != 1 || !s.allowsResource(q.Get("resource")) {
		oauthError(w, "invalid_target")
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || !challengeRE.MatchString(q.Get("code_challenge")) {
		oauthError(w, "invalid_request")
		return
	}
	if s.redirectRealm(w, r) {
		return
	}
	nonce := random()
	g := grant{Realm: s.cfg.AccountDatabase, AuthConfig: s.authConfig(), Client: c.ID, Redirect: q.Get("redirect_uri"), Resource: q.Get("resource"), Challenge: q.Get("code_challenge"), Scope: scope, Family: q.Get("state"), Expires: time.Now().Add(10 * time.Minute).Unix()}
	if e = s.putGrant(nonce, "login", g); e != nil {
		http.Error(w, "login unavailable", 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCookieName(nonce), Value: nonce, Path: s.loginPath(), HttpOnly: true, Secure: strings.HasPrefix(s.cfg.Issuer, "https:"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Cache-Control", "no-store")
	callback, _ := url.Parse(g.Redirect)
	callbackOrigin := callback.Scheme + "://" + callback.Host
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+callbackOrigin+"; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	users := []store.UserSummary{}
	if s.cfg.DebugLogin && s.cfg.Management == nil {
		users, _ = s.store.Users(r.Context())
	}
	label := "DataScope 管理账号授权"
	if d, e := s.localDatabase(); e == nil {
		label = d.Name + " · 员工账号授权"
		if d.Authentication != nil && d.Authentication.Label != "" {
			label = d.Authentication.Label + " · 授权"
		}
	}
	loginPage.Execute(w, map[string]any{"Heading": label, "LoginPath": s.loginPath(), "Nonce": nonce, "Client": c.Name, "Scope": scope, "Resource": g.Resource, "Debug": s.cfg.DebugLogin && s.cfg.Management == nil, "Users": users})
}
func loginCookieName(nonce string) string {
	return "oauth_login_" + hash(nonce)[:16]
}

var loginProblemPage = template.Must(template.New("loginProblem").Parse("<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>请重新打开授权页</title><body><h1>请重新打开授权页</h1><p>{{.Message}}</p>{{if .Retry}}<p><a href=\"{{.Retry}}\">重新打开授权页</a></p>{{end}}<p>也可以回到发起连接的客户端，重新发起账号授权。</p></body></html>"))

func loginProblem(w http.ResponseWriter, r *http.Request, message string) {
	retry := ""
	if r.URL.Query().Get("client_id") != "" {
		retry = "/oauth/authorize?" + r.URL.Query().Encode()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusBadRequest)
	loginProblemPage.Execute(w, map[string]string{"Message": message, "Retry": retry})
}

var loginPage = template.Must(template.New("login").Parse("<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>DataScope · 账号授权</title><style>body{font:16px system-ui;background:#eef2f7;margin:8vh auto;max-width:460px;padding:24px}form{background:white;padding:28px;border-radius:14px}input,select,button{box-sizing:border-box;width:100%;padding:12px;margin:8px 0 18px}button{background:#1763ba;color:white;border:0;border-radius:6px}small{color:#526070;overflow-wrap:anywhere}</style><form method=\"post\" action=\"{{.LoginPath}}\"><h1>{{.Heading}}</h1><p>向 {{.Client}} 授权数据库代理访问</p><small>资源：{{.Resource}}<br>请求权限：{{.Scope}}</small><input type=\"hidden\" name=\"nonce\" value=\"{{.Nonce}}\"><label>用户名<input name=\"emp_no\" placeholder=\"用户名\" autocomplete=\"username\"></label><label>密码<input type=\"password\" name=\"password\" autocomplete=\"current-password\"></label>{{if .Debug}}<label>调试：模拟员工<select name=\"debug_employee\"><option value=\"\">使用工号密码登录</option>{{range .Users}}{{if eq .Status \"active\"}}<option value=\"{{.EmpNo}}\">{{.EmpNo}} {{.Name}} · {{.Roles}}</option>{{end}}{{end}}</select></label>{{end}}<button type=\"submit\">登录并同意授权</button><small>切换账号请重新发起授权。</small></form></html>"))

func (s *Server) employee(ctx context.Context, emp string) (store.UserSummary, error) {
	if s.cfg.AccountDatabase != "" {
		return s.businessEmployee(ctx, emp)
	}
	if s.cfg.Management != nil {
		u, e := s.cfg.Management.ByUsername(ctx, emp)
		if e != nil || !u.Active {
			return store.UserSummary{}, errors.New("inactive or unknown user")
		}
		role := ""
		if u.SystemAdmin {
			role = "ADMIN"
		}
		return store.UserSummary{ID: u.ID, EmpNo: u.Username, Name: u.Name, Status: "active", RoleCode: role, AuthVersion: u.AuthVersion}, nil
	}
	us, e := s.store.Users(ctx)
	if e != nil {
		return store.UserSummary{}, e
	}
	for _, u := range us {
		if u.EmpNo == emp && u.Status == "active" {
			return u, nil
		}
	}
	return store.UserSummary{}, errors.New("inactive or unknown employee")
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow() {
		http.Error(w, "rate limited", 429)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, "invalid_request")
		return
	}
	nonce := r.PostForm.Get("nonce")
	cookie, e := r.Cookie(loginCookieName(nonce))
	if e != nil {
		cookie, e = r.Cookie("oauth_login")
	}
	if e != nil || nonce == "" || subtle.ConstantTimeCompare([]byte(nonce), []byte(cookie.Value)) != 1 {
		loginProblem(w, r, "授权页面的登录会话已失效，可能重复打开过授权页，或浏览器未保存 Cookie。请重新打开授权页后登录。")
		return
	}
	var pendingRaw string
	var pending grant
	if s.db.QueryRowContext(r.Context(), "SELECT DATA_ FROM OAUTH_GRANT WHERE ID_=? AND KIND_='login'", hash(nonce)).Scan(&pendingRaw) != nil || json.Unmarshal([]byte(pendingRaw), &pending) != nil || pending.Expires <= time.Now().Unix() || !s.authConfigMatches(pending) || !s.allowsResource(pending.Resource) {
		loginProblem(w, r, "授权页面已失效，请重新发起授权。")
		return
	}
	emp := r.PostForm.Get("emp_no")
	debug := s.cfg.Management == nil && s.cfg.DebugLogin && r.PostForm.Get("debug_employee") != ""
	if debug {
		emp = r.PostForm.Get("debug_employee")
	}
	var u store.UserSummary
	if s.cfg.Management != nil {
		debug = false
		ev, ae := s.cfg.Management.Audit.Start(ctxWithOAuth(r.Context()), "auth.oauth.login", "")
		if ae != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		var account management.User
		var err error
		if s.cfg.AccountDatabase != "" {
			u, err = s.authenticateBusiness(r.Context(), emp, r.PostForm.Get("password"))
			if ev != nil {
				ev.DatabaseID = s.cfg.AccountDatabase
				if business, err := s.cfg.Management.BusinessAccount(r.Context(), s.cfg.AccountDatabase, emp); err == nil {
					ev.SubjectID = business.BusinessUserID
				}
			}
		} else {
			policy, pe := s.cfg.Management.ResolveAuth(r.Context(), "API", "platform")
			err = pe
			if err == nil {
				if policy.Provider.Type == "PLATFORM_ACCOUNT" {
					account, err = s.cfg.Management.Authenticate(r.Context(), emp, r.PostForm.Get("password"))
				} else {
					var a management.BusinessAccount
					a, err = s.cfg.Management.VerifyAccount(r.Context(), policy, "", emp, r.PostForm.Get("password"))
					if err == nil {
						account, err = s.cfg.Management.PlatformIdentity(r.Context(), policy, a)
					}
				}
			}
		}
		if ev != nil {
			ev.ActorID = account.ID
		}
		if ae = s.cfg.Management.Audit.Finish(ev, err); ae != nil {
			http.Error(w, "audit unavailable", 503)
			return
		}
		if err != nil {
			http.Error(w, "用户名或密码错误", 401)
			return
		}
		if s.cfg.AccountDatabase == "" {
			u, e = s.employee(r.Context(), emp)
		}
	} else {
		a := sha256.Sum256([]byte(r.PostForm.Get("password")))
		b := sha256.Sum256([]byte(s.cfg.Password))
		if !debug && subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
			http.Error(w, "工号或密码错误", 401)
			return
		}
		u, e = s.employee(r.Context(), emp)
	}
	if e != nil {
		http.Error(w, "用户名或密码错误", 401)
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	defer tx.Rollback()
	var raw string
	var g grant
	if tx.QueryRow("SELECT DATA_ FROM OAUTH_GRANT WHERE ID_=? AND KIND_='login'", hash(nonce)).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &g) != nil || g.Expires <= time.Now().Unix() || !s.authConfigMatches(g) || !s.allowsResource(g.Resource) {
		loginProblem(w, r, "授权页面已过期或已经提交过，请重新发起账号授权。")
		return
	}
	if !s.userResourceAllowed(r.Context(), u.ID, g.Resource) {
		http.Error(w, "无权访问此资源", 403)
		return
	}
	state := g.Family
	g.Subject = u.EmpNo
	g.Version = u.AuthVersion
	g.Family = ""
	g.Expires = time.Now().Add(2 * time.Minute).Unix()
	if u.RoleCode != "ADMIN" {
		g.Scope = "sql:query"
	}
	code := random()
	rawBytes, _ := json.Marshal(g)
	if _, e = tx.Exec("DELETE FROM OAUTH_GRANT WHERE ID_=?", hash(nonce)); e == nil {
		_, e = tx.Exec("INSERT INTO OAUTH_GRANT VALUES (?,'code',?)", hash(code), string(rawBytes))
	}
	if e != nil || tx.Commit() != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	dest, _ := url.Parse(g.Redirect)
	q := dest.Query()
	q.Set("code", code)
	q.Set("state", state)
	q.Set("iss", s.cfg.Issuer)
	dest.RawQuery = q.Encode()
	http.SetCookie(w, &http.Cookie{Name: loginCookieName(nonce), Value: "", Path: s.loginPath(), MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(s.cfg.Issuer, "https:"), SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "oauth_login", Value: "", Path: s.loginPath(), MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, dest.String(), http.StatusSeeOther)
}
func (s *Server) SignEmployee(ctx context.Context, id int64) (string, error) {
	return s.SignEmployeeForResource(ctx, id, s.cfg.Resource)
}
func (s *Server) SignEmployeeForResource(ctx context.Context, id int64, resource string) (string, error) {
	if !s.allowsResource(resource) {
		return "", errors.New("unknown or disabled OAuth resource")
	}
	if s.cfg.Management != nil {
		u, e := s.cfg.Management.User(ctx, id)
		if e != nil || !u.Active || !s.userResourceAllowed(ctx, id, resource) {
			return "", errors.New("无权访问此资源")
		}
		scope := "sql:query"
		if u.SystemAdmin {
			scope += " sql:debug"
		}
		return s.signVersion(u.Username, scope, resource, u.AuthVersion)
	}
	us, e := s.store.Users(ctx)
	if e != nil {
		return "", e
	}
	for _, u := range us {
		if u.ID == id && u.Status == "active" {
			scope := "sql:query"
			if u.RoleCode == "ADMIN" {
				scope += " sql:debug"
			}
			return s.sign(u.EmpNo, scope, resource)
		}
	}
	return "", errors.New("inactive or unknown employee")
}
func (s *Server) sign(sub, scope, resource string) (string, error) {
	return s.signVersion(sub, scope, resource, 0)
}
func (s *Server) signVersion(sub, scope, resource string, version int64) (string, error) {
	return s.signClaims(map[string]any{"iss": s.cfg.Issuer, "aud": resource, "sub": sub, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "scope": scope, "jti": random(), "version": version, "identity": s.identity(), "auth_config": s.authConfig()})
}
func (s *Server) signClaims(values map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "at+jwt", "kid": "demo-rsa-1"})
	claims, _ := json.Marshal(values)
	body := b64.EncodeToString(header) + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(body))
	sig, e := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, h[:])
	return body + "." + b64.EncodeToString(sig), e
}
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil {
		oauthError(w, "invalid_request")
		return
	}
	q := r.PostForm
	if len(q["resource"]) != 1 || !s.allowsResource(q.Get("resource")) {
		oauthError(w, "invalid_target")
		return
	}
	c, e := s.getClient(q.Get("client_id"))
	if e != nil {
		oauthError(w, "invalid_client")
		return
	}
	kind, id := "code", q.Get("code")
	switch q.Get("grant_type") {
	case "authorization_code":
	case "refresh_token":
		kind = "refresh"
		id = q.Get("refresh_token")
	default:
		oauthError(w, "unsupported_grant_type")
		return
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	defer tx.Rollback()
	var raw string
	var g grant
	if tx.QueryRow("SELECT DATA_ FROM OAUTH_GRANT WHERE ID_=? AND KIND_=?", hash(id), kind).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &g) != nil || g.Client != c.ID || g.Resource != q.Get("resource") || !s.authConfigMatches(g) || g.Expires <= time.Now().Unix() {
		oauthError(w, "invalid_grant")
		return
	}
	if kind == "code" && (!verifierRE.MatchString(q.Get("code_verifier")) || g.Challenge != hash(q.Get("code_verifier")) || g.Redirect != q.Get("redirect_uri")) {
		oauthError(w, "invalid_grant")
		return
	}
	if g.Used {
		if kind == "refresh" {
			tx.Exec("DELETE FROM OAUTH_GRANT WHERE KIND_='refresh' AND json_extract(DATA_,'$.Family')=?", g.Family)
			tx.Commit()
		}
		oauthError(w, "invalid_grant")
		return
	}
	u, e := s.employee(r.Context(), g.Subject)
	if e != nil {
		oauthError(w, "invalid_grant")
		return
	}
	if (s.cfg.Management != nil && g.Version != u.AuthVersion) || !s.userResourceAllowed(r.Context(), u.ID, g.Resource) {
		oauthError(w, "invalid_grant")
		return
	}
	if u.RoleCode != "ADMIN" {
		g.Scope = "sql:query"
	}
	if want := q.Get("scope"); want != "" {
		for _, sc := range strings.Fields(want) {
			if !slices.Contains(strings.Fields(g.Scope), sc) {
				oauthError(w, "invalid_scope")
				return
			}
		}
		g.Scope = want
	}
	access, e := s.signVersion(g.Subject, g.Scope, g.Resource, g.Version)
	if e != nil {
		http.Error(w, "signing failed", 500)
		return
	}
	g.Used = true
	rawBytes, _ := json.Marshal(g)
	if _, e = tx.Exec("UPDATE OAUTH_GRANT SET DATA_=? WHERE ID_=?", string(rawBytes), hash(id)); e != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	refresh := random()
	g.Used = false
	g.Expires = time.Now().Add(7 * 24 * time.Hour).Unix()
	if g.Family == "" {
		g.Family = random()
	}
	rawBytes, _ = json.Marshal(g)
	if _, e = tx.Exec("INSERT INTO OAUTH_GRANT VALUES (?,'refresh',?)", hash(refresh), string(rawBytes)); e != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	if e = tx.Commit(); e != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	jsonOut(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "refresh_token": refresh, "scope": g.Scope})
}

type Verifier struct {
	Catalog          *catalog.Catalog
	Management       *management.Store
	DatabaseID       string
	Issuer, Resource string
	Store            *store.Store
	Client           *http.Client
	mu               sync.Mutex
	keys             map[string]*rsa.PublicKey
	until            time.Time
}

func (v *Verifier) Verify(ctx context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
	bad := auth.ErrInvalidToken
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, bad
	}
	var head struct{ Alg, Kid, Typ string }
	h, e := b64.DecodeString(parts[0])
	if e != nil || json.Unmarshal(h, &head) != nil || head.Alg != "RS256" || head.Typ != "at+jwt" {
		return nil, bad
	}
	key, e := v.key(ctx, head.Kid)
	if e != nil {
		return nil, bad
	}
	sig, e := b64.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if e != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return nil, bad
	}
	var c struct {
		Iss, Aud, Sub, Scope string
		Exp, Iat             int64
		Version              int64
		Identity             string
		AuthConfig           string                      `json:"auth_config"`
		Business             *management.BusinessAccount `json:"business,omitempty"`
	}
	raw, e := b64.DecodeString(parts[1])
	if e != nil || json.Unmarshal(raw, &c) != nil || c.Iss != v.Issuer || c.Aud != v.Resource || c.Exp <= time.Now().Unix() || c.Iat > time.Now().Add(30*time.Second).Unix() {
		return nil, bad
	}
	if v.DatabaseID != "" && (v.Management != nil || v.Catalog != nil && v.Catalog.Management != nil) {
		scopes := strings.Fields(c.Scope)
		var a management.BusinessAccount
		var err error
		if c.Business != nil {
			a, err = v.accountIdentity(ctx, *c.Business, c.Identity, c.AuthConfig)
		} else {
			a, err = v.businessToken(ctx, c.Sub, c.Identity, c.AuthConfig, c.Version, scopes)
		}
		if err != nil {
			return nil, bad
		}
		audit.UpdateRequest(ctx, v.DatabaseID, 0)
		return &auth.TokenInfo{UserID: strconv.FormatInt(a.ID, 10), Expiration: time.Unix(c.Exp, 0), Scopes: scopes, Extra: map[string]any{"identity": "database", "database_id": a.DatabaseID, "username": a.Username, "business_user_id": a.BusinessUserID, "business_account": a}}, nil
	}
	if databaseIdentity(c.Identity) {
		return nil, bad
	}
	if v.Management != nil {
		policy, pe := v.Management.ResolveAuth(ctx, "API", "platform")
		if pe != nil || c.AuthConfig != policy.Fingerprint() {
			return nil, bad
		}
		u, e := v.Management.ByUsername(ctx, c.Sub)
		if e != nil || !u.Active || u.AuthVersion != c.Version || c.Identity != "platform" {
			return nil, bad
		}
		audit.UpdateRequest(ctx, v.DatabaseID, u.ID)
		scopes := strings.Fields(c.Scope)
		if slices.Contains(scopes, "sql:debug") && !u.SystemAdmin {
			return nil, bad
		}
		return &auth.TokenInfo{UserID: strconv.FormatInt(u.ID, 10), Expiration: time.Unix(c.Exp, 0), Scopes: scopes}, nil
	}
	us, e := v.Store.Users(ctx)
	if e != nil {
		return nil, bad
	}
	for _, u := range us {
		if u.EmpNo == c.Sub && u.Status == "active" {
			scopes := strings.Fields(c.Scope)
			if slices.Contains(scopes, "sql:debug") && u.RoleCode != "ADMIN" {
				return nil, bad
			}
			return &auth.TokenInfo{UserID: strconv.FormatInt(u.ID, 10), Expiration: time.Unix(c.Exp, 0), Scopes: scopes, Extra: map[string]any{"emp_no": c.Sub}}, nil
		}
	}
	return nil, bad
}
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Now().Before(v.until) {
		if k := v.keys[kid]; k != nil {
			return k, nil
		}
		return nil, errors.New("unknown kid")
	}
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	req, e := http.NewRequestWithContext(ctx, "GET", v.Issuer+"/oauth/jwks.json", nil)
	if e != nil {
		return nil, e
	}
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("JWKS status %d", res.StatusCode)
	}
	var doc struct {
		Keys []struct{ Kty, Kid, Alg, Use, N, E string }
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&doc); e != nil {
		return nil, e
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range doc.Keys {
		n, ne := b64.DecodeString(j.N)
		eb, ee := b64.DecodeString(j.E)
		if ne != nil || ee != nil || j.Kty != "RSA" || j.Alg != "RS256" || j.Use != "sig" || len(n) < 256 || len(eb) > 4 {
			continue
		}
		ex := new(big.Int).SetBytes(eb).Int64()
		if ex < 3 || ex > 2147483647 {
			continue
		}
		keys[j.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ex)}
	}
	v.keys = keys
	v.until = time.Now().Add(5 * time.Minute)
	if k := keys[kid]; k != nil {
		return k, nil
	}
	return nil, errors.New("unknown kid")
}
func ctxWithOAuth(ctx context.Context) context.Context {
	return audit.With(ctx, audit.Metadata{Source: "oauth", Tool: "login"})
}
func (s *Server) userResourceAllowed(ctx context.Context, id int64, resource string) bool {
	if s.cfg.AccountDatabase != "" {
		d, e := s.localDatabase()
		if e != nil || s.databaseForResource(resource) != d.ID {
			return false
		}
		account, err := s.cfg.Management.BusinessAccountID(ctx, d.ID, id)
		if err != nil {
			return false
		}
		a, e := s.cfg.Catalog.BusinessAccount(ctx, d, account.Username)
		return e == nil && (slices.Contains(a.Permissions, "query") || slices.Contains(a.Permissions, "action") || slices.Contains(a.Permissions, "knowledge.read"))
	}
	if s.cfg.Management == nil {
		return true
	}
	if s.cfg.DatabaseForResource == nil {
		return false
	}
	db, e := s.cfg.DatabaseForResource(resource)
	if e != nil {
		return false
	}
	if db == "" {
		u, e := s.cfg.Management.User(ctx, id)
		return e == nil && u.Active
	}
	return false
}
