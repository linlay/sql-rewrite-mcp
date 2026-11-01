package mcpserver

import (
	"context"
	"datascope-server/internal/view"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strconv"
	"strings"
)

func (h handlers) viewResources(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
	d, err := h.opts.Catalog.Get(h.opts.DatabaseID)
	if err != nil {
		return nil, err
	}
	if method == "resources/templates/list" {
		return &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{}}, nil
	}
	if method == "resources/list" {
		items := []*mcp.Resource{}
		if d.MCPEnabled {
			for _, v := range d.Views {
				if v.Published {
					items = append(items, &mcp.Resource{URI: view.URI(d.ID, v.ID), Name: v.ID, Title: v.Name, Description: v.Description, MIMEType: view.MIMEType, Size: int64(len(v.HTML)), Meta: mcp.Meta{"type": "view"}})
				}
			}
		}
		start := 0
		request := req.(*mcp.ListResourcesRequest)
		if request.Params != nil && request.Params.Cursor != "" {
			start, err = strconv.Atoi(request.Params.Cursor)
			if err != nil || start < 0 || start >= len(items) {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "无效视图分页游标"}
			}
		}
		end := min(start+50, len(items))
		out := &mcp.ListResourcesResult{Resources: items[start:end], Cacheable: mcp.Cacheable{CacheScope: "private"}}
		if end < len(items) {
			out.NextCursor = strconv.Itoa(end)
		}
		return out, nil
	}
	request := req.(*mcp.ReadResourceRequest)
	prefix := view.URI(d.ID, "")
	if request.Params == nil || !strings.HasPrefix(request.Params.URI, prefix) {
		return nil, &jsonrpc.Error{Code: -32002, Message: "视图资源不存在"}
	}
	v, err := h.opts.Application.ReadView(ctx, d.ID, strings.TrimPrefix(request.Params.URI, prefix))
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32002, Message: "视图资源不存在或不可访问"}
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: request.Params.URI, MIMEType: view.MIMEType, Text: v.HTML, Meta: mcp.Meta{"type": "view"}}}, Cacheable: mcp.Cacheable{CacheScope: "private"}}, nil
}

func viewResult(database, id string) *mcp.CallToolResult {
	if id == "" {
		return nil
	}
	return &mcp.CallToolResult{Meta: mcp.Meta{"type": "view", "datascope/viewUri": view.URI(database, id)}}
}
