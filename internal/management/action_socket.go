package management

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The socket is private local IPC. It contains no receipts; the MCP process
// owns all state and the API confirmation page accesses that same memory.
type actionRPC struct {
	Operation string        `json:"operation"`
	Record    ActionRequest `json:"record"`
	From      string        `json:"from,omitempty"`
	State     string        `json:"state,omitempty"`
	Data      string        `json:"data,omitempty"`
	Token     string        `json:"token,omitempty"`
	Expires   int64         `json:"expires,omitempty"`
}
type actionReply struct {
	Record  ActionRequest `json:"record"`
	Fresh   bool          `json:"fresh"`
	Token   string        `json:"token,omitempty"`
	Expires int64         `json:"expires,omitempty"`
	Error   string        `json:"error,omitempty"`
	Code    string        `json:"code,omitempty"`
}

func ServeActionMemory(ctx context.Context, path string, state ActionState) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("Action IPC 路径不是 Unix socket")
		}
		live, e := net.DialTimeout("unix", path, 200*time.Millisecond)
		if e == nil {
			live.Close()
			return nil, errors.New("Action 内存服务已有实例运行")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /action-state", func(w http.ResponseWriter, r *http.Request) {
		var in actionRPC
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			http.Error(w, "invalid action-state request", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			http.Error(w, "invalid trailing data", 400)
			return
		}
		out := actionReply{}
		var err error
		switch in.Operation {
		case "lookup":
			out.Record, err = state.Lookup(r.Context(), in.Record)
		case "reserve":
			out.Record, out.Fresh, err = state.Reserve(r.Context(), in.Record)
		case "token":
			out.Record, err = state.Token(r.Context(), in.Token)
		case "transition":
			err = state.Transition(r.Context(), in.Record, in.From, in.State, in.Data, in.Token, in.Expires)
		default:
			http.Error(w, "unsupported action-state operation", 400)
			return
		}
		if err != nil {
			out.Error = err.Error()
			if errors.Is(err, sql.ErrNoRows) {
				out.Code = "not_found"
			}
			if errors.Is(err, ErrDenied) {
				out.Code = "denied"
			}
		}
		out.Token, out.Expires = out.Record.Token, out.Record.Expires
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go srv.Serve(listener)
	return func() { _ = srv.Close(); _ = listener.Close() }, nil
}

type remoteActions struct{ client *http.Client }

func RemoteActionMemory(path string) ActionState {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, MaxConnsPerHost: 8}
	return &remoteActions{client: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
}
func (r *remoteActions) call(ctx context.Context, in actionRPC) (actionReply, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return actionReply{}, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://action-memory/action-state", bytes.NewReader(raw))
	if err != nil {
		return actionReply{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return actionReply{}, fmt.Errorf("MCP Action 内存服务不可用: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return actionReply{}, fmt.Errorf("Action 内存服务返回 %d", response.StatusCode)
	}
	var out actionReply
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&out); err != nil {
		return out, err
	}
	if out.Error != "" {
		switch out.Code {
		case "not_found":
			return out, sql.ErrNoRows
		case "denied":
			return out, ErrDenied
		}
		return out, errors.New(out.Error)
	}
	out.Record.Token, out.Record.Expires = out.Token, out.Expires
	return out, nil
}
func (r *remoteActions) Reserve(ctx context.Context, in ActionRequest) (ActionRequest, bool, error) {
	out, err := r.call(ctx, actionRPC{Operation: "reserve", Record: in})
	return out.Record, out.Fresh, err
}
func (r *remoteActions) Token(ctx context.Context, token string) (ActionRequest, error) {
	out, err := r.call(ctx, actionRPC{Operation: "token", Token: token})
	return out.Record, err
}
func (r *remoteActions) Transition(ctx context.Context, in ActionRequest, from, state, data, token string, expires int64) error {
	_, err := r.call(ctx, actionRPC{Operation: "transition", Record: in, From: from, State: state, Data: data, Token: token, Expires: expires})
	return err
}

func (r *remoteActions) Lookup(ctx context.Context, in ActionRequest) (ActionRequest, error) {
	out, err := r.call(ctx, actionRPC{Operation: "lookup", Record: in})
	return out.Record, err
}
