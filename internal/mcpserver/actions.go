package mcpserver

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type describeActionInput struct {
	Name string `json:"name"`
}

func (h handlers) listActions(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	if h.opts.Application == nil {
		return nil, nil, fmt.Errorf("action 服务未配置")
	}
	out, e := h.opts.Application.ListActions(ctx, h.opts.DatabaseID)
	return nil, out, e
}

type invokeActionInput struct {
	Name       string         `json:"name"`
	Arguments  map[string]any `json:"arguments"`
	RequestID  string         `json:"request_id"`
	ResultOnly bool           `json:"result_only,omitempty" jsonschema:"只查询同一 request_id 的回执，绝不执行或重试写入；确认后或网络故障后使用此选项，MCP 重启清空回执"`
}

func (h handlers) describeAction(ctx context.Context, _ *mcp.CallToolRequest, in describeActionInput) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("需要 action 名称，请先调用 list_actions")
	}
	if h.opts.Application == nil {
		return nil, nil, fmt.Errorf("action 服务未配置")
	}
	out, e := h.opts.Application.DescribeAction(ctx, h.opts.DatabaseID, in.Name)
	return nil, out, e
}
func (h handlers) invokeAction(ctx context.Context, _ *mcp.CallToolRequest, in invokeActionInput) (*mcp.CallToolResult, any, error) {
	if h.opts.Application == nil {
		return nil, nil, fmt.Errorf("action 服务未配置")
	}
	if in.ResultOnly {
		out, e := h.opts.Application.ActionResult(ctx, h.opts.DatabaseID, in.Name, in.RequestID)
		return nil, out, e
	}
	out, e := h.opts.Application.InvokeAction(ctx, h.opts.DatabaseID, in.Name, in.RequestID, in.Arguments)
	return nil, out, e
}
