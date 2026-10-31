package management

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

type ActionRequest struct {
	DatabaseID  string `json:"database_id"`
	Actor       string `json:"actor"`
	RequestID   string `json:"request_id"`
	Fingerprint string `json:"fingerprint"`
	Revision    int    `json:"revision"`
	State       string `json:"state"`
	Data        string `json:"data"`
	Token       string `json:"-"`
	Expires     int64  `json:"-"`
}

type ActionState interface {
	Lookup(context.Context, ActionRequest) (ActionRequest, error)
	Reserve(context.Context, ActionRequest) (ActionRequest, bool, error)
	Token(context.Context, string) (ActionRequest, error)
	Transition(context.Context, ActionRequest, string, string, string, string, int64) error
}

func (s *Store) ReserveAction(ctx context.Context, r ActionRequest) (ActionRequest, bool, error) {
	return s.Actions.Reserve(ctx, r)
}
func (s *Store) ActionToken(ctx context.Context, token string) (ActionRequest, error) {
	return s.Actions.Token(ctx, token)
}
func (s *Store) TransitionAction(ctx context.Context, r ActionRequest, from, state, data, token string, expires int64) error {
	return s.Actions.Transition(ctx, r, from, state, data, token, expires)
}

// FinishAction uses an independent context. Receipts live only until the owning
// MCP process restarts; unresolved requests are never reclaimed in that lifetime.
func (s *Store) FinishAction(r ActionRequest, from, state, data string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.TransitionAction(ctx, r, from, state, data, "", 0)
}

type actionKey struct{ database, actor, request string }

func keyOf(r ActionRequest) actionKey { return actionKey{r.DatabaseID, r.Actor, r.RequestID} }

type MemoryActions struct {
	mu      sync.Mutex
	records map[actionKey]ActionRequest
	tokens  map[string]actionKey
	bytes   int
}

func NewMemoryActions() *MemoryActions {
	return &MemoryActions{records: map[actionKey]ActionRequest{}, tokens: map[string]actionKey{}}
}
func actionSize(r ActionRequest) int {
	return len(r.DatabaseID) + len(r.Actor) + len(r.RequestID) + len(r.Fingerprint) + len(r.State) + len(r.Data) + len(r.Token) + 256
}
func (m *MemoryActions) Reserve(ctx context.Context, r ActionRequest) (ActionRequest, bool, error) {
	if err := ctx.Err(); err != nil {
		return ActionRequest{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := keyOf(r)
	if old, ok := m.records[key]; ok {
		return old, false, nil
	}
	if r.DatabaseID == "" || r.Actor == "" || r.RequestID == "" || r.State != "running" || r.Token != "" {
		return ActionRequest{}, false, errors.New("非法 action 内存登记")
	}
	if len(m.records) >= 10000 || m.bytes+actionSize(r) > 64<<20 {
		return ActionRequest{}, false, errors.New("Action 内存回执已达容量上限")
	}
	m.records[key] = r
	m.bytes += actionSize(r)
	return r, true, nil
}
func (m *MemoryActions) Token(ctx context.Context, token string) (ActionRequest, error) {
	if err := ctx.Err(); err != nil {
		return ActionRequest{}, err
	}
	if len(token) != 64 {
		return ActionRequest{}, ErrDenied
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key, ok := m.tokens[token]
	if !ok {
		return ActionRequest{}, sql.ErrNoRows
	}
	return m.records[key], nil
}
func (m *MemoryActions) Transition(ctx context.Context, r ActionRequest, from, state, data, token string, expires int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := keyOf(r)
	old, ok := m.records[key]
	if !ok || old.State != from {
		return errors.New("action 请求已被处理或正在执行")
	}
	if token != "" {
		if len(token) != 64 {
			return ErrDenied
		}
		if other, exists := m.tokens[token]; exists && other != key {
			return errors.New("确认令牌重复")
		}
	}
	next := old
	next.State = state
	next.Data = data
	next.Token = token
	next.Expires = expires
	if m.bytes-actionSize(old)+actionSize(next) > 64<<20 {
		return errors.New("Action 内存回执已达容量上限")
	}
	if old.Token != "" {
		delete(m.tokens, old.Token)
	}
	if token != "" {
		m.tokens[token] = key
	}
	m.bytes += actionSize(next) - actionSize(old)
	m.records[key] = next
	return nil
}

// Lookup never creates a reservation, including after the owning process restarts.
func (m *MemoryActions) Lookup(ctx context.Context, r ActionRequest) (ActionRequest, error) {
	if err := ctx.Err(); err != nil {
		return ActionRequest{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[keyOf(r)]
	if !ok {
		return ActionRequest{}, sql.ErrNoRows
	}
	return record, nil
}
