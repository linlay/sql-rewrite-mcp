package mcpserver

import (
	"context"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/management"
	"datascope-server/internal/service"
	"datascope-server/internal/store"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"slices"
	"strings"
)

// ManagedHTTPHandler resolves current configuration on every request. Stateless
// sessions ensure a previously connected client cannot retain revoked tools.
// Identity verification follows each database's configured account realm.
func ManagedHTTPHandler(c *catalog.Catalog, identity *store.Store, opts Options, mode, issuer, resource, path string) (http.Handler, error) {
	// Validate authentication configuration at startup using the existing handler.
	if _, err := HTTPHandler(nil, identity, mode, issuer, resource, path, true); err != nil {
		return nil, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		segment := ""
		prefix := strings.TrimRight(path, "/") + "/"
		metaPrefix := "/.well-known/oauth-protected-resource" + prefix
		switch {
		case r.URL.Path == path:
		case strings.HasPrefix(r.URL.Path, prefix):
			segment = strings.TrimPrefix(r.URL.Path, prefix)
		case strings.HasPrefix(r.URL.Path, metaPrefix):
			segment = strings.TrimPrefix(r.URL.Path, metaPrefix)
		case r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource"+path:
		default:
			http.NotFound(w, r)
			return
		}
		var d *catalog.Database
		var err error
		if segment == "" {
			d, err = c.Get("default")
		} else {
			d, err = c.ByMCPPath(segment)
		}
		if err != nil {
			http.Error(w, "数据库不存在", 404)
			return
		}
		audit.UpdateRequest(r.Context(), d.ID, 0)
		if !d.MCPEnabled {
			http.Error(w, "该数据库未开启 MCP", 403)
			return
		}
		selected := opts
		selected.Catalog = c
		selected.IdentityStore = identity
		selected.Tools = d.Tools
		selected.DatabaseID = d.ID
		selected.DatabaseDriver = d.Driver
		selected.Managed = true
		endpoint := path
		if segment != "" {
			endpoint = prefix + segment
		}
		// Discovery, authorization resource and token audience identify the exact
		// endpoint. Build from the pinned public URL, never the request Host.
		endpointResource := resource
		if segment != "" {
			endpointResource = strings.TrimRight(resource, "/") + "/" + segment
		}
		endpointIssuer := issuer
		authMode := mode
		var selectedAuth *management.ResolvedAuth
		if c.Management != nil {
			resolved, e := c.Management.ResolveAuth(r.Context(), "DATABASE", d.ID)
			if e != nil {
				http.Error(w, "认证配置不可用", 503)
				return
			}
			selectedAuth = &resolved
			if resolved.Provider.Type == "DEMO_OAUTH" {
				endpointIssuer = d.AuthorizationIssuer(issuer)
				authMode = "oauth"
			} else {
				authMode = "none"
			}
		}
		selected.AuthMode = authMode
		// Open a query service only for tool calls. Knowledge Prompts and protocol
		// discovery do not depend on query policies or a live data connection.
		h, err := HTTPHandlerWithManagement(ManagedServer(c, d.ID, nil, selected), identity, authMode, endpointIssuer, endpointResource, endpoint, true, opts.Management, d.ID, c)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if selectedAuth != nil && selectedAuth.Provider.Type != "DEMO_OAUTH" {
			h = policyAuthentication(h, c, d, *selectedAuth, endpointResource, issuer, endpoint)
		}

		h.ServeHTTP(w, r)
	})
	return audit.HTTPHandler(c.Audit, "database-mcp", handler), nil
}

// ManagedServer applies the same live catalog rules to long-lived transports
// such as stdio. Request-local services avoid races between concurrent calls.
func ManagedServer(c *catalog.Catalog, id string, svc *service.QueryService, opts Options) *mcp.Server {
	opts.Managed = true
	opts.Catalog = c
	opts.DatabaseID = id
	opts.Tools = catalog.Tools
	server := New(svc, opts)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			d, err := c.Get(id)
			if err != nil {
				return nil, err
			}
			if !d.MCPEnabled {
				return nil, fmt.Errorf("该数据库未开启 MCP")
			}
			enabled := func(name string) bool {
				return name != "search_knowledge" && name != "read_knowledge" && slices.Contains(d.Tools, name)
			}
			switch method {
			case "tools/list":
				res, err := next(ctx, method, req)
				if err != nil {
					return nil, err
				}
				list := res.(*mcp.ListToolsResult)
				allowed := []*mcp.Tool{}
				for _, tool := range list.Tools {
					if enabled(tool.Name) {
						allowed = append(allowed, tool)
					}
				}
				list.Tools = allowed
				return list, nil
			case "tools/call":
				call := req.(*mcp.CallToolRequest)
				if !enabled(call.Params.Name) {
					return nil, fmt.Errorf("该数据库未开启接口 %s", call.Params.Name)
				}
				current, err := c.Open(ctx, d)
				if err != nil {
					return nil, err
				}
				defer current.Store.Close()
				ctx = context.WithValue(ctx, serviceContextKey{}, current)
			}
			return next(ctx, method, req)
		}
	})
	return server
}

type serviceContextKey struct{}
type businessContextKey struct{}

func (h handlers) current(ctx context.Context) handlers {
	if svc, ok := ctx.Value(serviceContextKey{}).(*service.QueryService); ok {
		h.svc = svc
	}
	return h
}
