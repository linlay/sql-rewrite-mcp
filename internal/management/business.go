package management

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"strings"
)

// BusinessAccount is a source-local demo identity, separate from platform users.
type BusinessAccount struct {
	ID             int64    `json:"id"`
	DatabaseID     string   `json:"database_id"`
	Username       string   `json:"username"`
	Name           string   `json:"name"`
	BusinessUserID int64    `json:"business_user_id"`
	Permissions    []string `json:"permissions"`

	Active      bool  `json:"active"`
	AuthVersion int64 `json:"revision"`
}

var BusinessPermissions = []string{"query", "knowledge.read", "raw_query", "action"}

func (s *Store) businessStates(ctx context.Context, database string) ([]accountState, error) {
	x, e := s.Source(ctx, database)
	if e != nil {
		return nil, e
	}
	var accounts []accountState
	e = x.withState(ctx, false, func(st *sourceState) error { accounts = st.Accounts; return nil })
	return accounts, e
}
func (s *Store) BusinessAccount(ctx context.Context, database, username string) (BusinessAccount, error) {
	accounts, e := s.businessStates(ctx, database)
	if e != nil {
		return BusinessAccount{}, e
	}
	for _, a := range accounts {
		if a.Username == username {
			return a.BusinessAccount, nil
		}
	}
	return BusinessAccount{}, sql.ErrNoRows
}
func (s *Store) BusinessAccounts(ctx context.Context, database string) ([]BusinessAccount, error) {
	accounts, e := s.businessStates(ctx, database)
	if e != nil {
		return nil, e
	}
	out := []BusinessAccount{}
	for _, a := range accounts {
		out = append(out, a.BusinessAccount)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}
func (s *Store) AuthenticateBusiness(ctx context.Context, database, username, password string) (BusinessAccount, error) {
	accounts, e := s.businessStates(ctx, database)
	if e == nil {
		for _, a := range accounts {
			if a.Username == username && a.Active && verify(password, a.PasswordHash) {
				return a.BusinessAccount, nil
			}
		}
	}
	return BusinessAccount{}, errors.New("工号或密码错误")
}

func (s *Store) SaveBusinessAccount(ctx context.Context, a BusinessAccount, password string, version int64) (BusinessAccount, error) {
	if a.DatabaseID == "" || strings.TrimSpace(a.Username) == "" || len(a.Username) > 128 || a.Name == "" || a.BusinessUserID <= 0 {
		return a, errors.New("需要数据库、账号、姓名和正整数业务身份")
	}
	for _, p := range a.Permissions {
		if !slices.Contains(BusinessPermissions, p) {
			return a, errors.New("业务账号只能获得 query、knowledge.read、raw_query 或 action 能力")
		}
	}
	if password != "" && len(password) < 6 {
		return a, errors.New("密码至少 6 字符")
	}
	if version == 0 && password == "" {
		return a, errors.New("新建业务账号需要初始密码")
	}
	encoded := ""
	var err error
	if password != "" {
		encoded, err = passwordHash(password)
		if err != nil {
			return a, err
		}
	}
	err = s.sourceChange(ctx, a.DatabaseID, "business_account.save", map[string]any{"username": a.Username, "business_user_id": a.BusinessUserID, "permissions": a.Permissions, "active": a.Active}, func(st *sourceState) error {
		var maxID int64
		for i, old := range st.Accounts {
			if old.ID > maxID {
				maxID = old.ID
			}
			if old.Username != a.Username {
				continue
			}
			if version == 0 || old.AuthVersion != version {
				return errors.New("业务账号已变更，请刷新后重试")
			}
			a.ID = old.ID
			a.AuthVersion = old.AuthVersion + 1
			if encoded == "" {
				encoded = old.PasswordHash
			}
			st.Accounts[i] = accountState{a, encoded}
			return nil
		}
		if version != 0 {
			return errors.New("业务账号已变更，请刷新后重试")
		}
		a.ID = maxID + 1
		a.AuthVersion = 1
		st.Accounts = append(st.Accounts, accountState{a, encoded})
		return nil
	})
	if err != nil {
		return a, err
	}
	return s.BusinessAccount(ctx, a.DatabaseID, a.Username)
}

func (s *Store) BusinessAccountID(ctx context.Context, database string, id int64) (BusinessAccount, error) {
	accounts, e := s.businessStates(ctx, database)
	if e != nil {
		return BusinessAccount{}, e
	}
	for _, a := range accounts {
		if a.ID == id {
			return a.BusinessAccount, nil
		}
	}
	return BusinessAccount{}, sql.ErrNoRows
}
