package catalog

import (
	"context"
	"crypto/sha256"
	"datascope-server/internal/management"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Authentication keeps local login options and an optional database-owned
// access policy. An absent access policy preserves the global binding/default.
type Authentication struct {
	Type              string                   `json:"type" yaml:"type"`
	Label             string                   `json:"label,omitempty" yaml:"label,omitempty"`
	EmployeeDirectory bool                     `json:"employee_directory,omitempty" yaml:"employeeDirectory,omitempty"`
	Source            string                   `json:"source,omitempty" yaml:"source,omitempty"`
	Provider          *management.AuthProvider `json:"provider,omitempty" yaml:"provider,omitempty"`
	Access            *management.AuthPolicy   `json:"access,omitempty" yaml:"access,omitempty"`
}

func (a Authentication) Connection() management.DatabaseAuthConnection {
	return management.DatabaseAuthConnection{Source: a.Source, Access: a.Access, Provider: a.Provider}
}
func (d Database) LocalAuthentication() bool {
	return d.Authentication == nil || d.Authentication.Type == "local"
}
func (d Database) AuthenticationVersion() string {
	raw, _ := json.Marshal(struct {
		Config   *Authentication
		Revision int64
		Policy   string
	}{d.Authentication, d.AuthenticationRevision, d.AuthPolicyFingerprint})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}
func (d Database) AuthorizationIssuer(base string) string {
	return strings.TrimRight(base, "/") + "/auth/databases/" + url.PathEscape(d.ID)
}
func (d Database) ValidateAuthentication() error {
	if d.Authentication == nil {
		return nil
	}
	if d.Authentication.Type != "local" {
		return fmt.Errorf("业务 MCP 仅支持当前数据库的独立业务账号（local）")
	}
	if len(d.Authentication.Label) > 120 {
		return fmt.Errorf("认证来源名称不能超过 120 字节")
	}
	if d.Authentication.EmployeeDirectory && d.Driver != "sqlite" {
		return fmt.Errorf("员工目录校验仅适用于 SQLite 证券演示库")
	}
	return nil
}
func (c *Catalog) BusinessAccount(ctx context.Context, d *Database, username string) (management.BusinessAccount, error) {
	if !d.LocalAuthentication() {
		return management.BusinessAccount{}, fmt.Errorf("未配置独立业务认证")
	}
	a, err := c.Management.BusinessAccount(ctx, d.ID, username)
	if err != nil || !a.Active {
		return a, management.ErrDenied
	}
	if d.Authentication != nil && d.Authentication.EmployeeDirectory {
		svc, e := c.Open(ctx, d)
		if e != nil {
			return a, e
		}
		defer svc.Store.Close()
		users, e := svc.Store.Users(ctx)
		if e != nil {
			return a, e
		}
		for _, u := range users {
			if u.ID == a.BusinessUserID && u.EmpNo == a.Username && u.Status == "active" {
				return a, nil
			}
		}
		return a, management.ErrDenied
	}
	return a, nil
}
