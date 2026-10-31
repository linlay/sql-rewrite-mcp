package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"datascope-server/internal/application"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This client uses only a human-supplied business bearer, never platform
// impersonation headers. Tokens live in the request and are never audited.
type businessBearer struct{ token string }

func (b businessBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func businessWorkbench(app *application.Service, w http.ResponseWriter, r *http.Request, id string) {
	if err := guard.Require(r.Context(), app.Management, id, "action.write"); err != nil {
		platformError(w, err)
		return
	}
	d, err := app.Catalog.Get(id)
	if err != nil {
		platformError(w, err)
		return
	}
	p, err := app.Management.ResolveAuth(r.Context(), "DATABASE", id)
	if err != nil {
		platformError(w, err)
		return
	}
	resource := strings.TrimRight(app.MCPBase, "/") + "/" + d.MCPRoute()
	base := strings.TrimSuffix(app.ManagementMCPURL, "/mcp/admin")
	issuer := d.AuthorizationIssuer(base)
	redirect := strings.TrimRight(app.WebURL, "/") + "/api/action-oauth/callback"
	if r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"mode": p.Policy.Mode, "provider_type": p.Provider.Type, "authorization_url": issuer + "/oauth/authorize", "redirect_uri": redirect, "resource": resource})
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Operation string         `json:"operation"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Username  string         `json:"username"`
		Password  string         `json:"password"`
		Code      string         `json:"code"`
		ClientID  string         `json:"client_id"`
		Verifier  string         `json:"verifier"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if in.Operation == "call" {
		if !slices.Contains([]string{"list_actions", "describe_action", "invoke_action"}, in.Name) {
			platformError(w, management.ErrDenied)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if p.Policy.Mode != "NONE" && (token == "" || token == r.Header.Get("Authorization")) {
			platformError(w, fmt.Errorf("请先通过业务身份认证"))
			return
		}
		if p.Policy.Mode == "NONE" {
			token = ""
		}
		client := &http.Client{Transport: businessBearer{token}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		session, e := mcp.NewClient(&mcp.Implementation{Name: "datascope-business-workbench", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: resource, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if e != nil {
			platformError(w, fmt.Errorf("业务 MCP 连接失败，请检查认证和服务状态"))
			return
		}
		defer session.Close()
		out, e := session.CallTool(ctx, &mcp.CallToolParams{Name: in.Name, Arguments: in.Arguments})
		if e != nil {
			platformError(w, fmt.Errorf("业务 MCP 调用失败；写入结果可能未知，请仅查询回执并核对审计"))
			return
		}
		writeJSON(w, 200, out)
		return
	}
	// Pinned existing authentication endpoints, no caller-controlled upstream URL.
	endpoint, contentType := "", "application/json"
	var body []byte
	switch in.Operation {
	case "account_token":
		if p.Policy.Mode != "ACCOUNT" {
			platformError(w, management.ErrDenied)
			return
		}
		endpoint = base + "/auth/databases/" + url.PathEscape(id) + "/token"
		body, _ = json.Marshal(map[string]string{"username": in.Username, "password": in.Password, "resource": resource})
	case "oauth_register":
		if p.Provider.Type != "DEMO_OAUTH" {
			platformError(w, management.ErrDenied)
			return
		}
		endpoint = issuer + "/oauth/register"
		body, _ = json.Marshal(map[string]any{"client_name": "DataScope Action workbench", "redirect_uris": []string{redirect}, "token_endpoint_auth_method": "none"})
	case "oauth_token":
		if p.Provider.Type != "DEMO_OAUTH" {
			platformError(w, management.ErrDenied)
			return
		}
		endpoint = issuer + "/oauth/token"
		contentType = "application/x-www-form-urlencoded"
		body = []byte(url.Values{"grant_type": {"authorization_code"}, "code": {in.Code}, "client_id": {in.ClientID}, "code_verifier": {in.Verifier}, "redirect_uri": {redirect}, "resource": {resource}}.Encode())
	default:
		platformError(w, management.ErrDenied)
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		platformError(w, fmt.Errorf("认证端点配置无效"))
		return
	}
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		platformError(w, fmt.Errorf("业务认证服务暂不可用"))
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		platformError(w, fmt.Errorf("业务认证失败，请检查账号或重新授权"))
		return
	}
	// Only forward documented credentials; upstream error bodies are never logged.
	var result map[string]any
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		platformError(w, fmt.Errorf("业务认证响应无效"))
		return
	}
	out := map[string]any{}
	for _, key := range []string{"client_id", "access_token", "token_type", "expires_in"} {
		if v, ok := result[key]; ok {
			out[key] = v
		}
	}
	writeJSON(w, 200, out)
}

func businessOAuthCallback(app *application.Service, w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(app.WebURL)
	if err != nil || u.Host == "" {
		http.Error(w, "invalid web URL", 500)
		return
	}
	origin, _ := json.Marshal(u.Scheme + "://" + u.Host)
	data, _ := json.Marshal(map[string]string{"type": "datascope-business-oauth", "code": r.URL.Query().Get("code"), "state": r.URL.Query().Get("state"), "error": r.URL.Query().Get("error")})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-business-oauth'; frame-ancestors 'none'")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><script nonce="business-oauth">window.opener?.postMessage(%s,%s);window.close()</script>`, data, origin)
}
