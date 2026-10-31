package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"datascope-server/internal/action"
	"datascope-server/internal/audit"
	"datascope-server/internal/catalog"
	"datascope-server/internal/guard"
	"datascope-server/internal/management"
)

type ActionResponse struct {
	Status            string          `json:"status"`
	Result            any             `json:"result,omitempty"`
	Message           string          `json:"message,omitempty"`
	Preview           *action.Preview `json:"preview,omitempty"`
	SQL               []audit.Event   `json:"sql,omitempty"`
	ConfirmationToken string          `json:"confirmation_token,omitempty"`
	ConfirmationURL   string          `json:"confirmation_url,omitempty"`
}
type actionPayload struct {
	Name                  string          `json:"name"`
	Arguments             map[string]any  `json:"arguments"`
	UserID                int64           `json:"user_id"`
	Username              string          `json:"username,omitempty"`
	AccountRevision       int64           `json:"account_revision,omitempty"`
	AuthenticationVersion string          `json:"authentication_version,omitempty"`
	Now                   time.Time       `json:"now"`
	Preview               *action.Preview `json:"preview,omitempty"`
	Response              *ActionResponse `json:"response,omitempty"`
}

func (s *Service) ListActions(ctx context.Context, database string) (any, error) {
	return s.DescribeAction(ctx, database, "")
}

func (s *Service) DescribeAction(ctx context.Context, database, name string) (any, error) {
	if e := guard.Require(ctx, s.Management, database, "action"); e != nil {
		return nil, e
	}
	d, e := s.Catalog.Get(database)
	if e != nil {
		return nil, e
	}
	if !d.MCPEnabled || !d.WriteEnabled {
		return nil, management.ErrDenied
	}
	return describeActions(d.Actions, name)
}

// InspectAction exposes the same public metadata to platform action editors,
// including before MCP or writes are enabled. It never exposes JavaScript.
func (s *Service) InspectAction(ctx context.Context, database, name string) (any, error) {
	if e := guard.Require(ctx, s.Management, database, "action.write"); e != nil {
		return nil, e
	}
	d, e := s.Catalog.Get(database)
	if e != nil {
		return nil, e
	}
	return describeActions(d.Actions, name)
}

func describeActions(definitions []action.Definition, name string) (any, error) {
	actions := []map[string]any{}
	for _, a := range definitions {
		v := map[string]any{"name": a.Name, "title": a.Title, "description": a.Description, "confirm": a.Confirm}
		if name != "" {
			if name != a.Name {
				continue
			}
			v["parameters"] = a.Schema()
			return v, nil
		}
		actions = append(actions, v)
	}
	if name != "" {
		return nil, fmt.Errorf("action 不存在: %s", name)
	}
	return map[string]any{"actions": actions}, nil
}
func (s *Service) actionDefinition(ctx context.Context, database, name string, liveMCP bool) (*catalog.Database, action.Definition, error) {
	d, e := s.Catalog.Get(database)
	if e != nil {
		return nil, action.Definition{}, e
	}
	if !d.WriteEnabled || liveMCP && (!d.MCPEnabled || !slices.Contains(d.Tools, "invoke_action")) {
		return nil, action.Definition{}, management.ErrDenied
	}
	for _, a := range d.Actions {
		if a.Name == name {
			return d, a, nil
		}
	}
	return nil, action.Definition{}, fmt.Errorf("action 不存在: %s", name)
}
func (s *Service) InvokeAction(ctx context.Context, database, name, requestID string, args map[string]any) (out *ActionResponse, err error) {
	meta := audit.From(ctx)
	meta.DatabaseID = database
	meta.Tool = name
	if requestID != "" {
		meta.RequestID = requestID
	}
	ctx = audit.With(ctx, meta)
	event, e := s.Management.Audit.Start(ctx, "action.request", "")
	if e != nil {
		return nil, e
	}
	defer func() {
		if event != nil {
			status := ""
			if out != nil {
				status = out.Status
			}
			event.Detail = map[string]any{"name": name, "status": status}
		}
		err = errors.Join(err, s.Management.Audit.Finish(event, err))
	}()
	if e := guard.Require(ctx, s.Management, database, "action"); e != nil {
		return nil, e
	}
	a, ok := guard.Business(ctx)
	if !ok || a.DatabaseID != database {
		return nil, management.ErrDenied
	}
	return s.invokeAction(ctx, database, name, requestID, args, fmt.Sprintf("business:%d", a.ID), a.BusinessUserID, a.Username, a.AuthVersion, false)
}
func (s *Service) invokeAction(ctx context.Context, database, name, requestID string, args map[string]any, actor string, userID int64, username string, accountRevision int64, dryRun bool) (*ActionResponse, error) {
	if len(requestID) < 1 || len(requestID) > 128 || strings.TrimSpace(requestID) != requestID {
		return nil, errors.New("request_id 必填且最多 128 字符")
	}
	d, def, e := s.actionDefinition(ctx, database, name, !dryRun)
	if e != nil {
		return nil, e
	}
	args, e = def.Arguments(args)
	if e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args, "user_id": userID, "dry_run": dryRun})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(raw))
	p := actionPayload{Name: name, Arguments: args, UserID: userID, Username: username, AccountRevision: accountRevision, AuthenticationVersion: d.AuthenticationVersion(), Now: time.Now().UTC()}
	data, _ := json.Marshal(p)
	rec, fresh, e := s.Management.ReserveAction(ctx, management.ActionRequest{DatabaseID: database, Actor: actor, RequestID: requestID, Fingerprint: fingerprint, Revision: d.Revision, State: "running", Data: string(data)})
	if e != nil {
		return nil, e
	}
	if rec.Fingerprint != fingerprint {
		return nil, errors.New("request_id 已用于其他 action、参数或业务身份")
	}
	if !fresh {
		if e = json.Unmarshal([]byte(rec.Data), &p); e != nil {
			return nil, e
		}
		if p.Response != nil {
			return p.Response, nil
		}
		if rec.State == "pending" {
			if rec.Revision != d.Revision || rec.Expires <= time.Now().Unix() {
				return nil, errors.New("确认预览已过期，请使用新的 request_id")
			}
			return s.confirmResponse(rec, &p), nil
		}
		return nil, errors.New("请求正在执行或结果未知；为避免重复写入不会再次执行，请核对审计")
	}
	meta := audit.From(ctx)
	meta.RequestID = requestID
	ctx = audit.With(ctx, meta)
	preview := dryRun || def.Confirm
	out, e := s.runAction(ctx, d, def, p, preview, nil)
	if e != nil {
		if errors.Is(e, action.ErrOutcomeUnknown) {
			return nil, e
		}
		res := &ActionResponse{Status: "denied", Message: e.Error()}
		p.Response = res
		data, _ = json.Marshal(p)
		if err := s.Management.FinishAction(rec, "running", "denied", string(data)); err != nil {
			return nil, err
		}
		return res, nil
	}
	p.Preview = out
	if preview && !dryRun {
		data, _ = json.Marshal(p)
		rec.Token = audit.ID() + audit.ID()
		rec.Expires = time.Now().Add(10 * time.Minute).Unix()
		rec.State = "pending"
		rec.Data = string(data)
		if e = s.Management.TransitionAction(ctx, rec, "running", "pending", rec.Data, rec.Token, rec.Expires); e != nil {
			return nil, e
		}
		return s.confirmResponse(rec, &p), nil
	}
	res := &ActionResponse{Status: "done", Result: out.Result}
	if dryRun {
		res.Status = "preview"
		res.Preview = out
		if s.Management.Audit != nil {
			res.SQL, e = s.Management.Audit.List(ctx, database, requestID, 1000, 0)
		}
		if e != nil {
			return nil, e
		}
	}
	p.Response = res
	data, _ = json.Marshal(p)
	if e = s.Management.FinishAction(rec, "running", "done", string(data)); e != nil {
		return nil, e
	}
	return res, nil
}
func (s *Service) confirmResponse(rec management.ActionRequest, p *actionPayload) *ActionResponse {
	base := strings.TrimSuffix(s.ManagementMCPURL, "/mcp/admin")
	return &ActionResponse{Status: "confirm_required", Preview: p.Preview, ConfirmationToken: rec.Token, ConfirmationURL: base + "/actions/confirm?token=" + rec.Token}
}
func (s *Service) runAction(ctx context.Context, d *catalog.Database, def action.Definition, p actionPayload, preview bool, expected *action.Preview) (out *action.Preview, err error) {
	meta := audit.From(ctx)
	meta.DatabaseID = d.ID
	meta.SubjectID = p.UserID
	meta.PolicyVersion = d.Revision
	meta.Tool = def.Name
	ctx = audit.With(ctx, meta)
	ev, e := s.Management.Audit.Start(ctx, "action.invoke", "")
	if e != nil {
		return nil, e
	}
	defer func() {
		if ev != nil {
			ev.Detail = map[string]any{"preview": preview, "arguments": p.Arguments, "output": out}
		}
		finishErr := s.Management.Audit.Finish(ev, err)
		if finishErr != nil && err == nil && !preview {
			err = action.ErrOutcomeUnknown
		}
		err = errors.Join(err, finishErr)
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	svc, e := s.Catalog.Open(ctx, d)
	if e != nil {
		return nil, e
	}
	defer svc.Store.Close()
	writer, e := s.Catalog.OpenWriter(ctx, d)
	if e != nil {
		return nil, e
	}
	defer writer.Close()
	bindings := svc.Bindings(p.UserID)
	ready := func(result *action.Preview) error {
		if ev == nil {
			return nil
		}
		ev.Detail = map[string]any{"preview": false, "arguments": p.Arguments, "output": result}
		return s.Management.Audit.Put(ctx, *ev)
	}
	return (action.Runner{BeforeCommit: ready, Store: writer, Rewriter: svc.Rewriter, Bindings: bindings, UserID: p.UserID, Now: p.Now}).Run(ctx, def, p.Arguments, preview, expected)
}

// ConfirmationInfo reveals only the preview for an unguessable, short-lived
// capability. Executing it additionally requires the originating account password.
func (s *Service) ConfirmationInfo(ctx context.Context, token string) (*ActionResponse, string, error) {
	rec, e := s.Management.ActionToken(ctx, token)
	if e != nil {
		return nil, "", e
	}
	if rec.State != "pending" || rec.Expires <= time.Now().Unix() {
		return nil, "", errors.New("确认已失效或已被处理")
	}
	var p actionPayload
	if e = json.Unmarshal([]byte(rec.Data), &p); e != nil {
		return nil, "", e
	}
	return s.confirmResponse(rec, &p), p.Username, nil
}
func (s *Service) ConfirmAction(ctx context.Context, token, password string) (*ActionResponse, error) {
	rec, e := s.Management.ActionToken(ctx, token)
	if e != nil {
		return nil, e
	}
	if rec.State != "pending" || rec.Expires <= time.Now().Unix() {
		return nil, errors.New("确认已失效或已被处理")
	}
	var p actionPayload
	if e = json.Unmarshal([]byte(rec.Data), &p); e != nil {
		return nil, e
	}
	policy, e := s.Management.ResolveAuth(ctx, "DATABASE", rec.DatabaseID)
	if e != nil {
		return nil, e
	}
	a, trusted := guard.Business(ctx)
	if !trusted || policy.Provider.Type != "ENTERPRISE_OAUTH" {
		if policy.Provider.Type == "DEMO_OAUTH" {
			a, e = s.Management.AuthenticateBusiness(ctx, rec.DatabaseID, p.Username, password)
		} else {
			a, e = s.Management.VerifyAccount(ctx, policy, rec.DatabaseID, p.Username, password)
		}
		if e != nil {
			return nil, e
		}
	}
	if a.DatabaseID != rec.DatabaseID {
		return nil, management.ErrDenied
	}

	if fmt.Sprintf("business:%d", a.ID) != rec.Actor || a.AuthVersion != p.AccountRevision || a.BusinessUserID != p.UserID {
		return nil, management.ErrDenied
	}
	ctx = guard.WithBusiness(ctx, a)
	if e = guard.Require(ctx, s.Management, rec.DatabaseID, "action"); e != nil {
		return nil, e
	}
	d, def, e := s.actionDefinition(ctx, rec.DatabaseID, p.Name, true)
	if e != nil {
		return nil, e
	}
	if rec.Revision != d.Revision || p.AuthenticationVersion != d.AuthenticationVersion() {
		return nil, errors.New("数据库配置已变化，请重新预览")
	}
	if policy.Local() {
		if _, e = s.Catalog.BusinessAccount(ctx, d, a.Username); e != nil {
			return nil, e
		}
	}
	if e = s.Management.TransitionAction(ctx, rec, "pending", "running", rec.Data, "", 0); e != nil {
		return nil, e
	}
	meta := audit.From(ctx)
	meta.RequestID = rec.RequestID
	meta.Source = "action-confirmation"
	ctx = audit.With(ctx, meta)
	out, e := s.runAction(ctx, d, def, p, false, p.Preview)
	if errors.Is(e, action.ErrOutcomeUnknown) {
		return nil, e
	}
	res := &ActionResponse{Status: "done"}
	if e != nil {
		res.Status = "denied"
		res.Message = e.Error()
	} else {
		res.Result = out.Result
	}
	p.Response = res
	raw, _ := json.Marshal(p)
	if e = s.Management.FinishAction(rec, "running", res.Status, string(raw)); e != nil {
		return nil, e
	}
	return res, nil
}

// ActionResult is a read-only receipt lookup under the authenticated business
// identity. Missing receipts (including after restart) must never invoke writes.
func (s *Service) ActionResult(ctx context.Context, database, name, requestID string) (*ActionResponse, error) {
	if err := guard.Require(ctx, s.Management, database, "action"); err != nil {
		return nil, err
	}
	a, ok := guard.Business(ctx)
	if !ok || a.DatabaseID != database {
		return nil, management.ErrDenied
	}
	if len(requestID) < 1 || len(requestID) > 128 || strings.TrimSpace(requestID) != requestID {
		return nil, errors.New("request_id 必填且最多 128 字符")
	}
	d, _, err := s.actionDefinition(ctx, database, name, true)
	if err != nil {
		return nil, err
	}
	rec, err := s.Management.Actions.Lookup(ctx, management.ActionRequest{DatabaseID: database, Actor: fmt.Sprintf("business:%d", a.ID), RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return &ActionResponse{Status: "unknown", Message: "回执不存在或 MCP 已重启；请核对审计和业务数据，不要重试未知结果的写入。"}, nil
	}
	if err != nil {
		return nil, err
	}
	var payload actionPayload
	if err = json.Unmarshal([]byte(rec.Data), &payload); err != nil {
		return nil, err
	}
	if payload.Name != name || payload.AccountRevision != a.AuthVersion || payload.UserID != a.BusinessUserID || payload.AuthenticationVersion != d.AuthenticationVersion() {
		return nil, management.ErrDenied
	}
	if payload.Response != nil {
		return payload.Response, nil
	}
	if rec.State == "pending" {
		if rec.Revision != d.Revision || rec.Expires <= time.Now().Unix() {
			return &ActionResponse{Status: "denied", Message: "确认预览已失效"}, nil
		}
		return s.confirmResponse(rec, &payload), nil
	}
	return &ActionResponse{Status: "unknown", Message: "请求正在执行或结果未知，请核对审计。"}, nil
}
